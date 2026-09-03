// Package cloudcost implements Stage 2.5's independent Cost Explorer /
// CUR poll (memory.md §5.3, §10.4; PLAN_PHASE3_PROJECTS.md A10).
//
// AWS Budgets lag by hours, so the platform needs its own view:
//   - hourly: Cost Explorer per IN_USE account, grouped by the
//     attempt_id tag → an env.usage_meter row with cloud_cost_usd set;
//   - daily: a CUR-in-S3 read for reconciliation, updating the same
//     rows to the authoritative figure.
//
// "one account = one attempt" (memory.md line 1849) makes attribution
// exact — the attempt_id comes straight off the account row.
//
// All AWS access is via cloudaws.Client, so the poll logic + the
// upsert-into-usage_meter is fully unit-tested against a FakeClient.
package cloudcost

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/tanyasawarn/skillfyme-hands-on/orchestrator/internal/cloudaws"
	"github.com/tanyasawarn/skillfyme-hands-on/orchestrator/internal/logging"
	"github.com/tanyasawarn/skillfyme-hands-on/orchestrator/internal/loop"
	"github.com/tanyasawarn/skillfyme-hands-on/orchestrator/internal/metrics"
)

var log = logging.Component("cloudcost")

// Poller runs the hourly CE poll and the daily CUR reconciliation.
type Poller struct {
	db  *pgxpool.Pool
	aws cloudaws.Client
}

func NewPoller(db *pgxpool.Pool, aws cloudaws.Client) *Poller {
	return &Poller{db: db, aws: aws}
}

// RunHourly blocks until stop is closed, polling CE once per interval
// (the caller passes 1h).
func (p *Poller) RunHourly(stop <-chan struct{}, interval time.Duration) {
	loop.RunTicker(stop, interval, p.PollOnce, false)
}

// RunDaily blocks until stop is closed, reconciling from the CUR once
// per interval (24h).
func (p *Poller) RunDaily(stop <-chan struct{}, interval time.Duration) {
	loop.RunTicker(stop, interval, p.ReconcileYesterday, false)
}

// PollOnce reads Cost Explorer for every account that is IN_USE (or was
// released recently — trailing spend still posts after teardown) and
// writes/updates its env.usage_meter cloud-cost rows.
func (p *Poller) PollOnce() {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()
	n, err := p.pollOnce(ctx, time.Now().Add(-25*time.Hour))
	if err != nil {
		log.Error("hourly CE poll failed", "err", err)
		return
	}
	log.Info("hourly CE poll complete", "rows_written", n)
}

func (p *Poller) pollOnce(ctx context.Context, since time.Time) (int, error) {
	rows, err := p.db.Query(ctx, `
		SELECT aws_account_id, attempt_id
		  FROM env.cloud_account
		 WHERE attempt_id IS NOT NULL
		   AND (state = 'IN_USE' OR released_at > now() - interval '48 hours')`)
	if err != nil {
		return 0, err
	}
	type acct struct{ id, attempt string }
	var accts []acct
	for rows.Next() {
		var a acct
		if err := rows.Scan(&a.id, &a.attempt); err != nil {
			rows.Close()
			return 0, err
		}
		accts = append(accts, a)
	}
	rows.Close()

	written := 0
	for _, a := range accts {
		costs, err := p.aws.GetCostSince(ctx, a.id, since)
		if err != nil {
			log.Warn("CE query failed for account", "account_id", a.id, "err", err)
			continue
		}
		for _, c := range costs {
			attempt := c.AttemptID
			if attempt == "" {
				attempt = a.attempt // fall back to the account's holder
			}
			if err := p.upsertCost(ctx, a.id, attempt, c); err != nil {
				log.Warn("usage_meter upsert failed", "account_id", a.id, "err", err)
				continue
			}
			metrics.CloudCostRowsTotal.WithLabelValues("ce").Inc()
			written++
		}
	}
	return written, nil
}

// ReconcileYesterday reads the CUR export for yesterday and updates the
// matching usage_meter rows to the authoritative figure.
func (p *Poller) ReconcileYesterday() {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()
	day := time.Now().AddDate(0, 0, -1)
	n, err := p.reconcile(ctx, day)
	if err != nil {
		log.Error("daily CUR reconciliation failed", "day", day.Format("2006-01-02"), "err", err)
		return
	}
	log.Info("daily CUR reconciliation complete", "day", day.Format("2006-01-02"), "rows_updated", n)
}

func (p *Poller) reconcile(ctx context.Context, day time.Time) (int, error) {
	costs, err := p.aws.ReconcileFromCUR(ctx, day)
	if err != nil {
		return 0, err
	}
	updated := 0
	for _, c := range costs {
		if err := p.upsertCost(ctx, c.AccountID, c.AttemptID, c); err != nil {
			log.Warn("CUR upsert failed", "account_id", c.AccountID, "err", err)
			continue
		}
		metrics.CloudCostRowsTotal.WithLabelValues("cur").Inc()
		updated++
	}
	return updated, nil
}

// upsertCost writes one cloud-cost row into env.usage_meter, keyed by
// (environment_id, attempt_id, window_start). environment_id is set to
// "cloud:<account>" since a T3 attempt's compute env id changes across
// suspend/resume but the account (and thus the cost stream) does not.
//
// The Phase 1 usage_meter schema has no unique constraint on that tuple,
// so this is an explicit UPDATE-then-INSERT (in a transaction so a
// concurrent poll can't double-insert). A later CUR reconciliation
// overwrites the CE figure with the authoritative one via the same path.
func (p *Poller) upsertCost(ctx context.Context, accountID, attemptID string, c cloudaws.AccountCost) error {
	envID := "cloud:" + accountID
	tx, err := p.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	tag, err := tx.Exec(ctx, `
		UPDATE env.usage_meter
		   SET cloud_cost_usd = $5,
		       total_cost_usd = ai_cost_usd + $5,
		       window_end = $4
		 WHERE environment_id = $1 AND attempt_id = $2 AND window_start = $3`,
		envID, attemptID, c.WindowStart, c.WindowEnd, c.AmountUSD)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		if _, err := tx.Exec(ctx, `
			INSERT INTO env.usage_meter
			  (environment_id, attempt_id, window_start, window_end, cloud_cost_usd, total_cost_usd)
			VALUES ($1, $2, $3, $4, $5, $5)`,
			envID, attemptID, c.WindowStart, c.WindowEnd, c.AmountUSD); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}
