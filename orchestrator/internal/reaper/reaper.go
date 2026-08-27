// Package reaper implements doc §5.6: "Every environment is registered
// in an environment_reaper table with a hard deadline. A reaper job runs
// every 60s and force-destroys anything past deadline regardless of what
// the orchestrator thinks... Assume the orchestrator will crash
// mid-provision -- the reaper is the thing that keeps your cloud bill
// finite." M1.7.
package reaper

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/tanyasawarn/skillfyme-hands-on/orchestrator/internal/destroyreason"
	"github.com/tanyasawarn/skillfyme-hands-on/orchestrator/internal/k8s"
	"github.com/tanyasawarn/skillfyme-hands-on/orchestrator/internal/logging"
	"github.com/tanyasawarn/skillfyme-hands-on/orchestrator/internal/loop"
	"github.com/tanyasawarn/skillfyme-hands-on/orchestrator/internal/metrics"
)

// log is this package's structured logger (PHASE1_MVP_COMPLETION.md
// §4.2). Every record carries component=reaper; per-call fields add
// env_id / namespace / reason / error / count.
var log = logging.Component("reaper")

// DestroyFunc lets the reaper's force-destroy go through the same
// teardown-plus-ENV_DESTROYED-publish logic every other destroy path
// uses (see internal/orchestrator/destroyer.go) instead of calling
// k8s.Provisioner.Destroy directly, which would silently skip meter
// stop / idle untrack / DB status update / the ENV_DESTROYED event --
// exactly the gap that left reaper-destroyed environments' attempts
// stuck occupying a learner's concurrent-environment slot forever.
// Optional: SetDestroyFunc must be called (from main.go, after the
// Destroyer exists) before Run/sweep/OrphanSweep are used with the rich
// path; falling back to the raw provisioner delete keeps this package
// usable standalone (e.g. in tests) without requiring a Destroyer.
type DestroyFunc func(ctx context.Context, envID, reason string) error

// Reaper runs independently of the request-response Provision/Destroy
// path -- it is the backstop that fires even if a Destroy RPC was never
// called (crashed caller, lost network, a bug in Practice Core's
// eligibility logic that never requests teardown). Doc's own framing:
// this is not best-effort, it is the mechanism that makes teardown
// reliable at all.
type Reaper struct {
	db          *pgxpool.Pool
	provisioner *k8s.Provisioner
	interval    time.Duration
	destroyFn   DestroyFunc
}

func New(db *pgxpool.Pool, provisioner *k8s.Provisioner) *Reaper {
	return &Reaper{db: db, provisioner: provisioner, interval: 60 * time.Second}
}

// SetDestroyFunc wires the reaper's force-destroy through the shared
// Destroyer. See DestroyFunc's doc comment for why this exists.
func (r *Reaper) SetDestroyFunc(fn DestroyFunc) {
	r.destroyFn = fn
}

func (r *Reaper) destroy(ctx context.Context, envID, reason string) error {
	if r.destroyFn != nil {
		return r.destroyFn(ctx, envID, reason)
	}
	return r.provisioner.Destroy(ctx, envID)
}

// Register records a hard deadline for an environment. Called by the
// orchestrator server immediately after a successful Provision, before
// returning to the caller -- so even if the process crashes on the very
// next line, the reaper still knows this environment must die by its
// deadline.
func (r *Reaper) Register(ctx context.Context, envID, namespace string, ttl time.Duration) error {
	deadline := time.Now().Add(ttl)
	_, err := r.db.Exec(ctx, `
		INSERT INTO env.environment_reaper (environment_id, namespace, hard_deadline)
		VALUES ($1, $2, $3)
		ON CONFLICT (environment_id) DO UPDATE SET hard_deadline = EXCLUDED.hard_deadline
	`, envID, namespace, deadline)
	return err
}

// Unregister removes an environment from the reaper's watch list on
// clean, intentional teardown (a real Destroy() call succeeded) --
// avoids the reaper re-destroying (harmlessly, but noisily) a namespace
// that's already gone.
func (r *Reaper) Unregister(ctx context.Context, envID string) error {
	_, err := r.db.Exec(ctx, `DELETE FROM env.environment_reaper WHERE environment_id = $1`, envID)
	return err
}

// Run blocks, sweeping every r.interval until ctx is cancelled. Doc §5.6:
// "A reaper job runs every 60s and force-destroys anything past deadline."
// runImmediately=false: nothing has had time to go overdue in the first
// instant of a fresh process, so waiting one interval before the first
// sweep is harmless (see internal/loop's own doc comment for the
// contrast with warmpool, which needs the opposite).
func (r *Reaper) Run(ctx context.Context) {
	log.Info("started", "interval", r.interval.String())
	loop.RunTicker(ctx.Done(), r.interval, func() { r.sweep(ctx) }, false)
	log.Info("stopped")
}

func (r *Reaper) sweep(ctx context.Context) {
	rows, err := r.db.Query(ctx, `
		SELECT environment_id, namespace FROM env.environment_reaper
		WHERE hard_deadline < now()
	`)
	if err != nil {
		log.Error("sweep query failed", logging.KeyError, err)
		return
	}
	defer rows.Close()

	type overdue struct{ envID, namespace string }
	var expired []overdue
	for rows.Next() {
		var o overdue
		if err := rows.Scan(&o.envID, &o.namespace); err != nil {
			log.Error("row scan failed", logging.KeyError, err)
			continue
		}
		expired = append(expired, o)
	}

	for _, o := range expired {
		log.Info("force-destroying overdue environment",
			logging.KeyEnvID, o.envID, logging.KeyNamespace, o.namespace, logging.KeyReason, destroyreason.Reaper)
		if err := r.destroy(ctx, o.envID, destroyreason.Reaper); err != nil {
			log.Error("force-destroy failed, will retry next sweep",
				logging.KeyEnvID, o.envID, logging.KeyReason, destroyreason.Reaper, logging.KeyError, err)
			continue // leave it registered; retry next tick rather than losing track of it
		}
		metrics.ReaperDestroyedTotal.WithLabelValues(destroyreason.Reaper).Inc()
		if _, err := r.db.Exec(ctx, `DELETE FROM env.environment_reaper WHERE environment_id = $1`, o.envID); err != nil {
			log.Error("failed to clear reaper record", logging.KeyEnvID, o.envID, logging.KeyError, err)
		}
	}

	if len(expired) > 0 {
		log.Info("sweep complete", logging.KeyCount, len(expired))
	}
}

// OrphanSweep implements doc §5.6's secondary check: "Orphan detection
// sweeps the cluster/cloud for resources tagged with unknown or
// completed attempt IDs, hourly." This walks live K8s namespaces
// directly (not the reaper table) to catch the case the table itself
// can't: a namespace that exists in the cluster but was never
// successfully registered (e.g. Provision crashed after
// createNamespace but before Register was called).
func (r *Reaper) OrphanSweep(ctx context.Context, listManagedNamespaces func(context.Context) ([]string, error)) {
	namespaces, err := listManagedNamespaces(ctx)
	if err != nil {
		log.Error("orphan sweep: failed to list namespaces", logging.KeyError, err)
		return
	}

	for _, ns := range namespaces {
		var exists bool
		err := r.db.QueryRow(ctx, `
			SELECT EXISTS(SELECT 1 FROM env.environment_reaper WHERE namespace = $1)
		`, ns).Scan(&exists)
		if err != nil {
			log.Error("orphan sweep: query failed", logging.KeyNamespace, ns, logging.KeyError, err)
			continue
		}
		if !exists {
			log.Warn("orphan namespace has no reaper record, force-destroying", logging.KeyNamespace, ns)
			metrics.ReaperOrphansFound.Inc()
			envID := envIDFromNamespace(ns)
			if err := r.destroy(ctx, envID, destroyreason.Reaper); err != nil {
				log.Error("orphan destroy failed",
					logging.KeyNamespace, ns, logging.KeyEnvID, envID, logging.KeyError, err)
				continue
			}
			metrics.ReaperDestroyedTotal.WithLabelValues(destroyreason.Reaper).Inc()
		}
	}
}

func envIDFromNamespace(ns string) string {
	const prefix = "env-"
	if len(ns) > len(prefix) && ns[:len(prefix)] == prefix {
		return ns[len(prefix):]
	}
	return ns
}
