// Package warmpool implements doc §5.5's warm-pool claim: "Warm pool
// policy... claiming is an atomic Redis CAS plus a K8s label patch," plus
// the background fill loop (Filler) that keeps the pool stocked ahead of
// demand.
package warmpool

import (
	"context"
	"log"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"

	"github.com/tanyasawarn/skillfyme-hands-on/orchestrator/internal/envstatus"
	"github.com/tanyasawarn/skillfyme-hands-on/orchestrator/internal/k8s"
	"github.com/tanyasawarn/skillfyme-hands-on/orchestrator/internal/loop"
	"github.com/tanyasawarn/skillfyme-hands-on/orchestrator/internal/metrics"
	"github.com/tanyasawarn/skillfyme-hands-on/orchestrator/internal/reaper"
	"github.com/tanyasawarn/skillfyme-hands-on/orchestrator/internal/ttl"
)

func newEnvID() string {
	return uuid.New().String()
}

// Manager tracks pre-provisioned, fixture-applied environments keyed by
// blueprint id, so a Provision() call can claim one instead of
// cold-provisioning. The claim/CAS mechanics are real (atomic Redis
// SPOP, so two concurrent claims never receive the same env_id); Filler
// (below) is the background loop that keeps the pool topped up. Doc
// §5.5's richer "predicted_demand" sizing (time-of-day/historical load
// curves) is not implemented -- Filler uses a fixed target depth per
// blueprint instead, the honest subset for Phase 1's guided-lab volume.
// Every claim miss still safely falls through to cold-provision in
// Server.Provision regardless of whether Filler is running.
type Manager struct {
	rdb *redis.Client
}

func NewManager(rdb *redis.Client) *Manager {
	return &Manager{rdb: rdb}
}

func poolKey(blueprintID string) string {
	return "warmpool:" + blueprintID
}

// Claim atomically pops one warm environment id for the given blueprint,
// if any are available. Doc §5.5: "atomic Redis CAS" -- SPOP against a
// Redis set is inherently atomic (single command, single key), which is
// what makes concurrent claims from two Provision() calls never race.
func (m *Manager) Claim(ctx context.Context, blueprintID string) (envID string, claimed bool) {
	val, err := m.rdb.SPop(ctx, poolKey(blueprintID)).Result()
	if err == redis.Nil {
		metrics.WarmPoolClaimTotal.WithLabelValues("miss").Inc()
		return "", false
	}
	if err != nil {
		log.Printf("[warmpool] claim error (falling through to cold-provision): %v", err)
		metrics.WarmPoolClaimTotal.WithLabelValues("miss").Inc()
		return "", false
	}
	metrics.WarmPoolClaimTotal.WithLabelValues("hit").Inc()
	return val, true
}

// Add registers a pre-provisioned, healthy environment as available for
// claiming. Doc §5.5: "Warm environments have their own shorter TTL
// (30 min) and are recycled" -- callers should also arm a TTL-based
// destroy for anything added here; Add itself just makes the id
// claimable, it doesn't own the environment's lifecycle.
func (m *Manager) Add(ctx context.Context, blueprintID, envID string) error {
	return m.rdb.SAdd(ctx, poolKey(blueprintID), envID).Err()
}

// Size reports current pool depth for a blueprint, used by Filler and by
// observability.
func (m *Manager) Size(ctx context.Context, blueprintID string) (int64, error) {
	return m.rdb.SCard(ctx, poolKey(blueprintID)).Result()
}

// UnclaimedAttemptID is the placeholder attempt_id a warm environment's
// env.environment row is written with before it's claimed -- a real
// attempt_id doesn't exist yet at fill time (no learner is attached),
// but the row must exist before Filler can register the environment
// with the reaper (env.environment_reaper.environment_id has a foreign
// key to env.environment.id, so Register() would fail its FK constraint
// otherwise -- same ordering requirement Server.Provision's own doc
// comment calls out for the cold-provision path). Server.Provision
// overwrites this with the real attempt_id via ON CONFLICT when the
// environment is later claimed.
const UnclaimedAttemptID = "00000000-0000-0000-0000-000000000000"

// warmPoolTTL bounds how long an unclaimed warm environment lives before
// the reaper recycles it. Doc §5.5: "Warm environments have their own
// shorter TTL (30 min) and are recycled" -- intentionally shorter than
// ttl.EnvironmentDefault (90min, now a compile-time-linked reference via
// internal/ttl rather than a value only related by a prose comment)
// since an unclaimed warm environment is pure standing cost with no
// learner attached, so it should turn over faster than a real attempt
// would.
const warmPoolTTL = ttl.WarmPool

// Target is one blueprint's desired warm-pool depth. Doc §5.5's fuller
// "predicted_demand" sizing (time-of-day/historical curves) is not
// implemented -- a fixed target per blueprint is the honest Phase 1
// subset (see the Manager doc comment above).
type Target struct {
	BlueprintID string
	Image       string
	Count       int
}

// Filler is the background loop that keeps each configured blueprint's
// warm pool topped up to its target depth by cold-provisioning
// environments ahead of demand and adding them to the pool once healthy.
// Every field it touches (Provisioner, reaper.Reaper, the DB pool) is
// exactly what Server.Provision's own cold-provision path already uses,
// so a warm-filled environment is indistinguishable from a freshly
// cold-provisioned one once claimed.
type Filler struct {
	pool        *Manager
	provisioner *k8s.Provisioner
	reaper      *reaper.Reaper
	db          *pgxpool.Pool
	targets     []Target
	interval    time.Duration

	// fillOneFn is the single "provision one warm env and add it to the
	// pool" step. It's a field, defaulting to (*Filler).fillOne, so
	// fillOnce's deficit/back-off orchestration -- how many to fill, and
	// the "stop hammering a failing blueprint this tick" rule -- is
	// unit-testable against a miniredis-backed Manager without a real
	// k8s.Provisioner. Production code never sets it.
	fillOneFn func(ctx context.Context, target Target) error
}

// NewFiller builds a Filler. interval controls how often the loop checks
// pool depth and tops up -- doesn't need to be fast, since every claim
// miss safely falls through to cold-provisioning regardless.
func NewFiller(pool *Manager, provisioner *k8s.Provisioner, rp *reaper.Reaper, db *pgxpool.Pool, targets []Target, interval time.Duration) *Filler {
	f := &Filler{pool: pool, provisioner: provisioner, reaper: rp, db: db, targets: targets, interval: interval}
	f.fillOneFn = f.fillOne
	return f
}

// Run blocks, filling the pool on a ticker until ctx is cancelled. Meant
// to be started as `go filler.Run(ctx)` from main, the same pattern
// idledetect.Detector.Run and reaper's sweep loop already use.
// runImmediately=true, unlike those two: a Claim() call must find a
// pre-provisioned environment ready, so the pool cannot sit empty for a
// full interval after startup before its first fill (see internal/loop's
// own doc comment for the full reasoning behind this being the one
// caller that needs the opposite of reaper/idledetect/costmeter).
func (f *Filler) Run(ctx context.Context) {
	if len(f.targets) == 0 {
		return
	}
	loop.RunTicker(ctx.Done(), f.interval, func() { f.fillOnce(ctx) }, true)
}

func (f *Filler) fillOnce(ctx context.Context) {
	for _, target := range f.targets {
		current, err := f.pool.Size(ctx, target.BlueprintID)
		if err != nil {
			log.Printf("[warmpool] filler: checking pool size for blueprint=%s: %v", target.BlueprintID, err)
			continue
		}
		metrics.WarmPoolDepth.WithLabelValues(target.BlueprintID).Set(float64(current))
		deficit := int64(target.Count) - current
		if deficit <= 0 {
			continue
		}
		log.Printf("[warmpool] filler: blueprint=%s pool depth=%d target=%d, filling %d", target.BlueprintID, current, target.Count, deficit)
		for i := int64(0); i < deficit; i++ {
			if err := f.fillOneFn(ctx, target); err != nil {
				log.Printf("[warmpool] filler: failed to provision warm env for blueprint=%s: %v", target.BlueprintID, err)
				break // don't hammer a failing blueprint the rest of this tick; retry next tick
			}
		}
	}
}

func (f *Filler) fillOne(ctx context.Context, target Target) error {
	envID := newEnvID()
	log.Printf("[warmpool] filler: cold-provisioning warm env=%s for blueprint=%s", envID, target.BlueprintID)

	if err := f.provisioner.Provision(ctx, k8s.ProvisionRequest{
		AttemptID: UnclaimedAttemptID,
		EnvID:     envID,
		Image:     target.Image,
		Resources: k8s.ResourceSpec{CPU: "2", Memory: "4Gi"},
	}); err != nil {
		return err
	}

	// env.environment row must exist before reaper.Register (FK), same
	// ordering Server.Provision's cold-provision path documents.
	if _, err := f.db.Exec(ctx, `
		INSERT INTO env.environment (id, attempt_id, tier, blueprint_id, status, namespace, ready_at)
		VALUES ($1, $2, 'TIER_T1_SHARED_CONTAINER', $3, $5, $4, now())
		ON CONFLICT (id) DO UPDATE SET status = $5, ready_at = now()
	`, envID, UnclaimedAttemptID, target.BlueprintID, "env-"+envID, envstatus.Ready); err != nil {
		return err
	}

	if err := f.reaper.Register(ctx, envID, "env-"+envID, warmPoolTTL); err != nil {
		log.Printf("[warmpool] filler: WARNING: failed to register warm env=%s with reaper: %v (environment may leak if not destroyed cleanly)", envID, err)
	}

	if err := f.provisioner.WaitForPodReady(ctx, envID); err != nil {
		return err
	}

	return f.pool.Add(ctx, target.BlueprintID, envID)
}
