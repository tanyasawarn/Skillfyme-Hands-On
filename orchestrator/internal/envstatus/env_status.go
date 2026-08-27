// Package envstatus centralizes the env.environment.status column's
// real values -- PLAN.md K20. The column itself is plain `text NOT
// NULL DEFAULT 'PROVISIONING'` with no CHECK constraint or DB-level
// enum (db/migrations/0001_env_and_billing.sql), so these Go consts are
// the only enforcement that exists anywhere for these values; previously
// each was a bare SQL string literal embedded independently in
// server.go's Provision UPSERT and warmpool's Filler UPSERT ('READY'),
// and destroyer.go's teardown UPDATE ('DESTROYED'). A leaf package
// (imports nothing internal) so both internal/orchestrator (which
// imports internal/warmpool) and internal/warmpool can use it without
// creating a cycle.
package envstatus

const (
	Ready     = "READY"
	Destroyed = "DESTROYED"
)
