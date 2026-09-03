// Package accountpool implements Stage 2.4's Account Pool Manager: the
// AVAILABLE -> IN_USE -> NUKING -> (AVAILABLE | QUARANTINED) state machine
// for vended AWS sandbox accounts (memory.md §5.3, PLAN_PHASE3_PROJECTS.md
// A4).
//
// Split, mirroring internal/warmpool: the durable state + the quarantine
// queue live in env.cloud_account (migration 0005); the fast claim path
// is an atomic Redis CAS so two concurrent Provision(tier=T3) calls never
// receive the same account; Filler is the background warm-fill loop
// (reuses internal/loop.RunTicker) keeping N clean AVAILABLE accounts per
// region.
//
// Claim path: pick AVAILABLE -> set budget alarm (2.3) -> apply baseline
// TF (1.3) -> set SCP exception tag (1.2) -> IN_USE -> emit ACCOUNT_CLAIMED.
//
// Release path: (STS broker stop is 2.1, done by the caller before this)
// -> NUKING -> nuke + verify (2.2) -> AVAILABLE (emit ACCOUNT_NUKED) or
// QUARANTINED (emit ACCOUNT_QUARANTINED, page).
//
// All AWS work goes through cloudaws.Client, so the whole package has
// real unit tests against a FakeClient with no credentials.
package accountpool

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"

	"github.com/tanyasawarn/skillfyme-hands-on/orchestrator/internal/cloudaws"
	"github.com/tanyasawarn/skillfyme-hands-on/orchestrator/internal/logging"
	"github.com/tanyasawarn/skillfyme-hands-on/orchestrator/internal/loop"
	"github.com/tanyasawarn/skillfyme-hands-on/orchestrator/internal/metrics"
)

var log = logging.Component("accountpool")

// ErrNoAccountAvailable is returned by Claim when the pool has no
// AVAILABLE account in the requested region. Server.Provision maps this
// to gRPC ResourceExhausted (the same response path as the 2.3 launch
// cap).
var ErrNoAccountAvailable = errors.New("accountpool: no AVAILABLE account in region")

// EventPublisher emits the ACCOUNT_* taxonomy events (contracts/events.md,
// added in 0.8). Injected so this package doesn't import a NATS client
// directly — same narrow-interface pattern as orchestrator.RecordingForgetter.
type EventPublisher interface {
	PublishAccountClaimed(ctx context.Context, attemptID, accountID, region string)
	PublishAccountNuked(ctx context.Context, attemptID, accountID string, verified bool, resourcesRemaining int)
	PublishAccountQuarantined(ctx context.Context, attemptID, accountID, reason string, resourcesRemaining int)
}

// ClaimInput is everything needed to move an account AVAILABLE → IN_USE.
type ClaimInput struct {
	AttemptID     string
	TenantID      string
	Region        string
	BudgetUSD     float64
	SkuExceptions []string
}

// ClaimResult is the claimed account + the role the learner's broker
// assumes into it.
type ClaimResult struct {
	AccountID string
	RoleName  string
}

// Manager is the pool state machine.
type Manager struct {
	db     *pgxpool.Pool
	rdb    *redis.Client
	aws    cloudaws.Client
	events EventPublisher

	// budgetThresholds are the alarm levels PutAccountBudget arms (2.3:
	// 50/80/100%). Fixed here; a real deployment could make it config.
	budgetThresholds []cloudaws.BudgetThreshold
}

func NewManager(db *pgxpool.Pool, rdb *redis.Client, aws cloudaws.Client, events EventPublisher) *Manager {
	return &Manager{
		db:     db,
		rdb:    rdb,
		aws:    aws,
		events: events,
		budgetThresholds: []cloudaws.BudgetThreshold{
			{Percent: 50}, {Percent: 80}, {Percent: 100},
		},
	}
}

func availKey(region string) string { return "accountpool:available:" + region }

// RegisterAvailableAccount inserts (or resets) a vended account row as
// AVAILABLE in a region and adds it to the Redis fast-path set. This is
// how an operator (or a test) adds accounts to the pool after
// `aws organizations create-account` — the Pool Manager never vends
// accounts itself (that is a slow, quota-bound Organizations call done
// ahead of a cohort, §7.4). Idempotent.
func (m *Manager) RegisterAvailableAccount(ctx context.Context, accountID, region string) error {
	_, err := m.db.Exec(ctx, `
		INSERT INTO env.cloud_account (aws_account_id, state, region)
		VALUES ($1, 'AVAILABLE', $2)
		ON CONFLICT (aws_account_id) DO UPDATE
		   SET state = 'AVAILABLE', region = $2,
		       attempt_id = NULL, budget_usd = NULL,
		       quarantine_reason = NULL, quarantine_resources_remaining = NULL,
		       quarantine_detail = NULL, updated_at = now()`,
		accountID, region)
	if err != nil {
		return fmt.Errorf("accountpool: register account: %w", err)
	}
	return m.rdb.SAdd(ctx, availKey(region), accountID).Err()
}

// StateOf returns the current lifecycle state of an account (for
// observability / ops tooling).
func (m *Manager) StateOf(ctx context.Context, accountID string) (string, error) {
	var s string
	err := m.db.QueryRow(ctx,
		`SELECT state FROM env.cloud_account WHERE aws_account_id = $1`, accountID).Scan(&s)
	return s, err
}

// SyncRedisFromDB rebuilds the Redis AVAILABLE set from the authoritative
// table. Called at startup and by Filler so a restart doesn't lose the
// fast-path index. Idempotent.
func (m *Manager) SyncRedisFromDB(ctx context.Context) error {
	rows, err := m.db.Query(ctx,
		`SELECT aws_account_id, region FROM env.cloud_account WHERE state = 'AVAILABLE'`)
	if err != nil {
		return fmt.Errorf("accountpool: sync query: %w", err)
	}
	defer rows.Close()

	byRegion := map[string][]any{}
	for rows.Next() {
		var id, region string
		if err := rows.Scan(&id, &region); err != nil {
			return err
		}
		byRegion[region] = append(byRegion[region], id)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	pipe := m.rdb.Pipeline()
	for region, ids := range byRegion {
		pipe.Del(ctx, availKey(region))
		if len(ids) > 0 {
			pipe.SAdd(ctx, availKey(region), ids...)
		}
	}
	_, err = pipe.Exec(ctx)
	return err
}

// Claim moves an AVAILABLE account to IN_USE for an attempt. The Redis
// SPop is the CAS that makes concurrent claims safe; everything after it
// is done under the row's own state guard so a crashed claim can be
// recovered by Filler / the sweeper.
func (m *Manager) Claim(ctx context.Context, in ClaimInput) (ClaimResult, error) {
	if in.SkuExceptions == nil {
		in.SkuExceptions = []string{}
	}
	accountID, err := m.rdb.SPop(ctx, availKey(in.Region)).Result()
	if err == redis.Nil {
		metrics.AccountPoolClaimTotal.WithLabelValues(in.Region, "miss").Inc()
		return ClaimResult{}, ErrNoAccountAvailable
	}
	if err != nil {
		metrics.AccountPoolClaimTotal.WithLabelValues(in.Region, "error").Inc()
		return ClaimResult{}, fmt.Errorf("accountpool: SPop: %w", err)
	}

	// Guarded transition AVAILABLE → IN_USE. If the row isn't AVAILABLE
	// (a sweeper grabbed it, or a stale Redis entry), abort and let the
	// caller retry — the account is not put back in Redis here because
	// its true state is whatever the row says.
	tag, err := m.db.Exec(ctx, `
		UPDATE env.cloud_account
		   SET state = 'IN_USE',
		       attempt_id = $2,
		       budget_usd = $3,
		       sku_exceptions = $4,
		       claimed_at = now(),
		       released_at = NULL,
		       quarantine_reason = NULL,
		       quarantine_resources_remaining = NULL,
		       quarantine_detail = NULL,
		       updated_at = now()
		 WHERE aws_account_id = $1 AND state = 'AVAILABLE'`,
		accountID, in.AttemptID, in.BudgetUSD, in.SkuExceptions)
	if err != nil {
		return ClaimResult{}, fmt.Errorf("accountpool: claim update: %w", err)
	}
	if tag.RowsAffected() == 0 {
		metrics.AccountPoolClaimTotal.WithLabelValues(in.Region, "stale").Inc()
		return ClaimResult{}, ErrNoAccountAvailable
	}

	// 2.3 budget alarm, then 1.3 baseline, then 1.2 SCP exception tag.
	if err := m.aws.PutAccountBudget(ctx, accountID, in.BudgetUSD, m.budgetThresholds); err != nil {
		m.rollbackClaim(ctx, accountID, "PutAccountBudget", err)
		return ClaimResult{}, err
	}
	roleName, err := m.aws.ApplyBaseline(ctx, accountID, in.AttemptID, in.TenantID)
	if err != nil {
		m.rollbackClaim(ctx, accountID, "ApplyBaseline", err)
		return ClaimResult{}, err
	}
	if err := m.aws.SetSkuExceptionTag(ctx, accountID, in.SkuExceptions); err != nil {
		m.rollbackClaim(ctx, accountID, "SetSkuExceptionTag", err)
		return ClaimResult{}, err
	}

	metrics.AccountPoolClaimTotal.WithLabelValues(in.Region, "hit").Inc()
	m.events.PublishAccountClaimed(ctx, in.AttemptID, accountID, in.Region)
	log.Info("account claimed", "account_id", accountID, "attempt_id", in.AttemptID, "region", in.Region)
	return ClaimResult{AccountID: accountID, RoleName: roleName}, nil
}

// rollbackClaim sends a partially-claimed account straight to NUKING so
// the release path cleans whatever the failed step left behind — never
// back to AVAILABLE, because we can't be sure it's clean.
func (m *Manager) rollbackClaim(ctx context.Context, accountID, step string, cause error) {
	log.Error("claim step failed, sending account to NUKING", "account_id", accountID, "step", step, "err", cause)
	_, _ = m.db.Exec(ctx,
		`UPDATE env.cloud_account SET state = 'NUKING', updated_at = now() WHERE aws_account_id = $1`,
		accountID)
	// Best-effort async release; the sweeper is the backstop.
	go func() {
		relCtx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
		defer cancel()
		_ = m.Release(relCtx, accountID)
	}()
}

// Release runs the teardown path for an account currently IN_USE or
// NUKING: NUKING → nuke + verify → AVAILABLE or QUARANTINED.
//
// The caller (T3 driver Destroy) is responsible for stopping the STS
// broker (2.1) BEFORE calling Release.
func (m *Manager) Release(ctx context.Context, accountID string) error {
	// Grab the row + move to NUKING (idempotent: NUKING → NUKING is fine).
	var attemptID *string
	err := m.db.QueryRow(ctx, `
		UPDATE env.cloud_account
		   SET state = 'NUKING', released_at = COALESCE(released_at, now()), updated_at = now()
		 WHERE aws_account_id = $1 AND state IN ('IN_USE', 'NUKING')
		RETURNING attempt_id`,
		accountID).Scan(&attemptID)
	if errors.Is(err, pgx.ErrNoRows) {
		// Already AVAILABLE or QUARANTINED — nothing to do (idempotent).
		return nil
	}
	if err != nil {
		return fmt.Errorf("accountpool: release → NUKING: %w", err)
	}
	att := ""
	if attemptID != nil {
		att = *attemptID
	}

	// 2.3 budget teardown (best-effort — a leftover budget is harmless).
	_ = m.aws.DeleteAccountBudget(ctx, accountID)

	// 2.2 nuke + mandatory verification.
	res, nukeErr := m.aws.RunNuke(ctx, accountID)
	if nukeErr != nil {
		m.quarantine(ctx, accountID, att, "nuke_error", 0, nukeErr.Error())
		return nil
	}
	if !res.Verified || res.ResourcesRemaining > 0 || len(res.BlindSpotHits) > 0 {
		detail := res.Detail
		if len(res.BlindSpotHits) > 0 {
			detail = fmt.Sprintf("%s; blind-spot hits: %v", detail, res.BlindSpotHits)
		}
		m.quarantine(ctx, accountID, att, "verification_nonempty", res.ResourcesRemaining, detail)
		m.events.PublishAccountNuked(ctx, att, accountID, false, res.ResourcesRemaining)
		return nil
	}

	// Clean → back to AVAILABLE.
	if err := m.aws.ClearAccountTags(ctx, accountID); err != nil {
		log.Warn("ClearAccountTags failed (non-fatal)", "account_id", accountID, "err", err)
	}
	_, err = m.db.Exec(ctx, `
		UPDATE env.cloud_account
		   SET state = 'AVAILABLE',
		       attempt_id = NULL,
		       budget_usd = NULL,
		       sku_exceptions = '{}',
		       last_nuked_at = now(),
		       quarantine_reason = NULL,
		       quarantine_resources_remaining = NULL,
		       quarantine_detail = NULL,
		       updated_at = now()
		 WHERE aws_account_id = $1 AND state = 'NUKING'`,
		accountID)
	if err != nil {
		return fmt.Errorf("accountpool: release → AVAILABLE: %w", err)
	}

	// region for the Redis re-add
	var region string
	_ = m.db.QueryRow(ctx, `SELECT region FROM env.cloud_account WHERE aws_account_id = $1`, accountID).Scan(&region)
	if region != "" {
		_ = m.rdb.SAdd(ctx, availKey(region), accountID).Err()
	}

	metrics.AccountPoolReleaseTotal.WithLabelValues("available").Inc()
	m.events.PublishAccountNuked(ctx, att, accountID, true, 0)
	log.Info("account released to pool", "account_id", accountID)
	return nil
}

func (m *Manager) quarantine(ctx context.Context, accountID, attemptID, reason string, remaining int, detail string) {
	_, err := m.db.Exec(ctx, `
		UPDATE env.cloud_account
		   SET state = 'QUARANTINED',
		       quarantine_reason = $2,
		       quarantine_resources_remaining = $3,
		       quarantine_detail = $4,
		       updated_at = now()
		 WHERE aws_account_id = $1`,
		accountID, reason, remaining, truncate(detail, 2000))
	if err != nil {
		log.Error("failed to write QUARANTINED state", "account_id", accountID, "err", err)
		return
	}
	metrics.AccountPoolReleaseTotal.WithLabelValues("quarantined").Inc()
	m.events.PublishAccountQuarantined(ctx, attemptID, accountID, reason, remaining)
	log.Error("ACCOUNT QUARANTINED — human review required",
		"account_id", accountID, "reason", reason, "resources_remaining", remaining, "detail", detail)
}

// --- warm-fill loop --------------------------------------------------

// FillTarget is one region's desired warm-pool depth.
type FillTarget struct {
	Region string
	Count  int
}

// Filler keeps each region's AVAILABLE pool topped up. It does NOT vend
// new AWS accounts (that's a slow, quota-bound Organizations call an
// operator does ahead of a cohort — §7.4); it re-syncs Redis from the
// table and re-drives Release for any account stuck in NUKING (crash
// recovery), so the pool self-heals.
type Filler struct {
	mgr     *Manager
	targets []FillTarget
}

func NewFiller(mgr *Manager, targets []FillTarget) *Filler {
	return &Filler{mgr: mgr, targets: targets}
}

// Run blocks until stop is closed, ticking every interval.
func (f *Filler) Run(stop <-chan struct{}, interval time.Duration) {
	loop.RunTicker(stop, interval, f.tick, true)
}

func (f *Filler) tick() {
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Minute)
	defer cancel()

	if err := f.mgr.SyncRedisFromDB(ctx); err != nil {
		log.Warn("filler: redis sync failed", "err", err)
	}

	// Crash recovery: any account left in NUKING (a claim rollback or a
	// killed Release) gets its release re-driven.
	rows, err := f.mgr.db.Query(ctx,
		`SELECT aws_account_id FROM env.cloud_account
		  WHERE state = 'NUKING' AND updated_at < now() - interval '10 minutes'`)
	if err != nil {
		log.Warn("filler: stuck-NUKING query failed", "err", err)
		return
	}
	var stuck []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err == nil {
			stuck = append(stuck, id)
		}
	}
	rows.Close()
	for _, id := range stuck {
		log.Info("filler: re-driving stuck NUKING account", "account_id", id)
		if err := f.mgr.Release(ctx, id); err != nil {
			log.Warn("filler: re-drive Release failed", "account_id", id, "err", err)
		}
	}

	// Depth observability against the targets.
	for _, t := range f.targets {
		n, err := f.mgr.rdb.SCard(ctx, availKey(t.Region)).Result()
		if err != nil {
			continue
		}
		metrics.AccountPoolDepth.WithLabelValues(t.Region).Set(float64(n))
		if int(n) < t.Count {
			log.Warn("account pool below target — vend more accounts ahead of the next cohort",
				"region", t.Region, "have", n, "want", t.Count)
		}
	}
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}
