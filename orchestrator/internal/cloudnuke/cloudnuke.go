// Package cloudnuke implements Stage 2.2's nuke + mandatory verification:
// the containerised aws-nuke runner invoked by the release path, plus a
// standalone nightly sweeper that nukes every AVAILABLE + QUARANTINED
// account regardless of state (memory.md §5.3; PLAN_PHASE3_PROJECTS.md A8).
//
// The actual `aws-nuke` invocation + the post-nuke verification pass
// (AWS Config + Resource Explorer + a hardcoded nuke-blind-spot service
// list) live behind cloudaws.Client.RunNuke, so this package is the
// scheduling / bookkeeping half and is fully unit-tested with a
// FakeClient.
//
// The release-path nuke is driven by accountpool.Manager.Release
// (already calls cloudaws.RunNuke). This package adds the *sweeper* — a
// cron that walks the pool and re-nukes anything that should be empty,
// catching drift the release path missed (a killed Release, a resource
// created after a verify, an account that never went through Release).
package cloudnuke

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/tanyasawarn/skillfyme-hands-on/orchestrator/internal/cloudaws"
	"github.com/tanyasawarn/skillfyme-hands-on/orchestrator/internal/logging"
	"github.com/tanyasawarn/skillfyme-hands-on/orchestrator/internal/loop"
)

var log = logging.Component("cloudnuke")

// Pager is called when the sweeper finds a non-empty account that should
// be empty. The real impl hits a PagerDuty / Opsgenie webhook.
type Pager interface {
	Page(ctx context.Context, accountID, reason, detail string)
}

// Sweeper is the nightly re-nuke job.
type Sweeper struct {
	db    *pgxpool.Pool
	aws   cloudaws.Client
	pager Pager
}

func NewSweeper(db *pgxpool.Pool, aws cloudaws.Client, pager Pager) *Sweeper {
	return &Sweeper{db: db, aws: aws, pager: pager}
}

// Run blocks until stop is closed, sweeping once per interval (nightly in
// production; the caller passes 24h).
func (s *Sweeper) Run(stop <-chan struct{}, interval time.Duration) {
	loop.RunTicker(stop, interval, s.Sweep, false)
}

// SweepResult summarises one sweep.
type SweepResult struct {
	Checked          int
	CleanReconfirmed int
	Quarantined      int
	Errored          int
}

// Sweep nukes + verifies every AVAILABLE and QUARANTINED account. An
// AVAILABLE account that comes back non-empty is a real leak — it is
// moved to QUARANTINED and paged. A QUARANTINED account that now
// verifies clean is left QUARANTINED (a human clears it — the sweeper
// never auto-releases from quarantine), but the clean result is logged
// so the on-call can close it out.
func (s *Sweeper) Sweep() {
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Hour)
	defer cancel()
	res := s.sweepOnce(ctx)
	log.Info("nightly sweep complete",
		"checked", res.Checked, "clean_reconfirmed", res.CleanReconfirmed,
		"quarantined", res.Quarantined, "errored", res.Errored)
}

func (s *Sweeper) sweepOnce(ctx context.Context) SweepResult {
	rows, err := s.db.Query(ctx,
		`SELECT aws_account_id, state, attempt_id
		   FROM env.cloud_account
		  WHERE state IN ('AVAILABLE', 'QUARANTINED')
		  ORDER BY updated_at ASC`)
	if err != nil {
		log.Error("sweep query failed", "err", err)
		return SweepResult{Errored: 1}
	}
	type acct struct {
		id, state string
		attempt   *string
	}
	var accounts []acct
	for rows.Next() {
		var a acct
		if err := rows.Scan(&a.id, &a.state, &a.attempt); err != nil {
			log.Error("sweep scan failed", "err", err)
			continue
		}
		accounts = append(accounts, a)
	}
	rows.Close()

	var res SweepResult
	for _, a := range accounts {
		res.Checked++
		nr, err := s.aws.RunNuke(ctx, a.id)
		att := ""
		if a.attempt != nil {
			att = *a.attempt
		}
		if err != nil {
			res.Errored++
			s.markQuarantined(ctx, a.id, "sweeper", 0, "sweeper nuke error: "+err.Error())
			s.pager.Page(ctx, a.id, "sweeper_nuke_error", err.Error())
			continue
		}
		clean := nr.Verified && nr.ResourcesRemaining == 0 && len(nr.BlindSpotHits) == 0
		if clean {
			res.CleanReconfirmed++
			if a.state == "QUARANTINED" {
				log.Info("quarantined account now verifies clean — a human can clear it",
					"account_id", a.id, "attempt_id", att)
			}
			continue
		}
		// non-empty
		res.Quarantined++
		detail := nr.Detail
		if len(nr.BlindSpotHits) > 0 {
			detail += "; blind-spot hits: "
			for i, h := range nr.BlindSpotHits {
				if i > 0 {
					detail += ", "
				}
				detail += h
			}
		}
		if a.state == "AVAILABLE" {
			// a leak in the pool — this is the one the §2 hard gate exists for
			log.Error("SWEEPER FOUND A LEAK IN AN AVAILABLE ACCOUNT",
				"account_id", a.id, "resources_remaining", nr.ResourcesRemaining)
		}
		s.markQuarantined(ctx, a.id, "sweeper", nr.ResourcesRemaining, detail)
		s.pager.Page(ctx, a.id, "sweeper_found_resources", detail)
	}
	return res
}

func (s *Sweeper) markQuarantined(ctx context.Context, accountID, reason string, remaining int, detail string) {
	_, err := s.db.Exec(ctx, `
		UPDATE env.cloud_account
		   SET state = 'QUARANTINED',
		       quarantine_reason = $2,
		       quarantine_resources_remaining = $3,
		       quarantine_detail = $4,
		       updated_at = now()
		 WHERE aws_account_id = $1`,
		accountID, reason, remaining, truncate(detail, 2000))
	if err != nil {
		log.Error("sweeper failed to write QUARANTINED", "account_id", accountID, "err", err)
	}
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}
