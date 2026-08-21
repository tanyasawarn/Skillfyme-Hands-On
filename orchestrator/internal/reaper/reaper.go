// Package reaper implements doc §5.6: "Every environment is registered
// in an environment_reaper table with a hard deadline. A reaper job runs
// every 60s and force-destroys anything past deadline regardless of what
// the orchestrator thinks... Assume the orchestrator will crash
// mid-provision -- the reaper is the thing that keeps your cloud bill
// finite." M1.7.
package reaper

import (
	"context"
	"log"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/tanyasawarn/skillfyme-hands-on/orchestrator/internal/k8s"
)

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
}

func New(db *pgxpool.Pool, provisioner *k8s.Provisioner) *Reaper {
	return &Reaper{db: db, provisioner: provisioner, interval: 60 * time.Second}
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
func (r *Reaper) Run(ctx context.Context) {
	ticker := time.NewTicker(r.interval)
	defer ticker.Stop()
	log.Printf("[reaper] started, sweeping every %s", r.interval)

	for {
		select {
		case <-ctx.Done():
			log.Println("[reaper] stopped")
			return
		case <-ticker.C:
			r.sweep(ctx)
		}
	}
}

func (r *Reaper) sweep(ctx context.Context) {
	rows, err := r.db.Query(ctx, `
		SELECT environment_id, namespace FROM env.environment_reaper
		WHERE hard_deadline < now()
	`)
	if err != nil {
		log.Printf("[reaper] sweep query failed: %v", err)
		return
	}
	defer rows.Close()

	type overdue struct{ envID, namespace string }
	var expired []overdue
	for rows.Next() {
		var o overdue
		if err := rows.Scan(&o.envID, &o.namespace); err != nil {
			log.Printf("[reaper] row scan failed: %v", err)
			continue
		}
		expired = append(expired, o)
	}

	for _, o := range expired {
		log.Printf("[reaper] force-destroying overdue environment %s (namespace=%s)", o.envID, o.namespace)
		if err := r.provisioner.Destroy(ctx, o.envID); err != nil {
			log.Printf("[reaper] force-destroy failed for %s: %v (will retry next sweep)", o.envID, err)
			continue // leave it registered; retry next tick rather than losing track of it
		}
		if _, err := r.db.Exec(ctx, `DELETE FROM env.environment_reaper WHERE environment_id = $1`, o.envID); err != nil {
			log.Printf("[reaper] failed to clear reaper record for %s: %v", o.envID, err)
		}
	}

	if len(expired) > 0 {
		log.Printf("[reaper] swept %d overdue environment(s)", len(expired))
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
		log.Printf("[reaper] orphan sweep: failed to list namespaces: %v", err)
		return
	}

	for _, ns := range namespaces {
		var exists bool
		err := r.db.QueryRow(ctx, `
			SELECT EXISTS(SELECT 1 FROM env.environment_reaper WHERE namespace = $1)
		`, ns).Scan(&exists)
		if err != nil {
			log.Printf("[reaper] orphan sweep: query failed for %s: %v", ns, err)
			continue
		}
		if !exists {
			log.Printf("[reaper] orphan namespace %s has no reaper record -- force-destroying", ns)
			envID := envIDFromNamespace(ns)
			if err := r.provisioner.Destroy(ctx, envID); err != nil {
				log.Printf("[reaper] orphan destroy failed for %s: %v", ns, err)
			}
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
