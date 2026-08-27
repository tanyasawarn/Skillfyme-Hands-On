// Package audit implements PLAN.md M1.14's "security baseline audit"
// line item -- doc's own security-baseline checklist names "audit log"
// alongside gVisor/NetworkPolicy/egress-proxy/quotas/reaper. Records
// every security-relevant RPC this orchestrator serves (Provision,
// Destroy, InjectFault, MintValidatorCredentials, ExecShell) to a
// durable, queryable table (env.audit_log, db/migrations/0004_audit_log.sql)
// -- not stdout log lines, which are ephemeral, unsearchable, and (in a
// real deployment) subject to whatever log-rotation policy the hosting
// platform applies, none of which is an acceptable property for an
// audit trail.
package audit

import (
	"context"
	"encoding/json"
	"log"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Outcome is intentionally a closed, small set of values (not an
// arbitrary string) so a query against env.audit_log.outcome is
// reliable -- callers can't accidentally introduce a typo'd third
// outcome value that a WHERE outcome = 'SUCCESS' query silently misses.
type Outcome string

const (
	Success Outcome = "SUCCESS"
	Failure Outcome = "FAILURE"
)

// Action names the security-relevant operation being recorded. A closed
// set (not free-form) for the same reliability reason as Outcome --
// every actual call site in this codebase uses one of these constants,
// never a raw string literal.
type Action string

const (
	ActionProvision       Action = "PROVISION"
	ActionDestroy         Action = "DESTROY"
	ActionInjectFault     Action = "INJECT_FAULT"
	ActionMintCredentials Action = "MINT_CREDENTIALS"
	ActionExecShell       Action = "EXEC_SHELL"
	// PLAN.md Phase 2 closure item: Connect, CaptureBaseline,
	// CheckRegression, and ExecValidator previously wrote zero audit
	// entries despite being security-sensitive (Connect mints a session
	// token; ExecValidator runs arbitrary content-authored assertions
	// inside a learner's environment).
	ActionConnect         Action = "CONNECT"
	ActionCaptureBaseline Action = "CAPTURE_BASELINE"
	ActionCheckRegression Action = "CHECK_REGRESSION"
	ActionExecValidator   Action = "EXEC_VALIDATOR"
)

// Logger writes audit entries to env.audit_log. A thin wrapper over
// *pgxpool.Pool (not its own connection pool) -- audit writes share the
// same database every other orchestrator write already uses; this isn't
// a separate audit-log-specific datastore (doc's own §8.4 schema-per-
// bounded-context puts this in the env schema alongside everything else
// Dev A owns).
type Logger struct {
	db *pgxpool.Pool
}

func NewLogger(db *pgxpool.Pool) *Logger {
	return &Logger{db: db}
}

// Entry is what a caller builds and hands to Record. Detail must never
// contain a raw secret (a minted token, a command's stdout/stderr that
// could contain credentials, etc) -- doc §9.3's own framing ("any secret
// that appears in a learner's environment must be assumed compromised")
// argues for minimizing where secrets are ever written, and an audit
// log that's meant to be queried/exported for review is exactly the
// kind of place a leaked secret would have outsized blast radius. Every
// real call site in this package only ever puts non-secret identifiers
// (fault ids, scopes, exit codes, byte counts) into Detail -- enforced
// by convention and code review at each call site, not by this type
// itself (Detail is intentionally a generic map so different actions can
// carry different shaped detail without a combinatorial explosion of
// per-action Entry types).
type Entry struct {
	EnvironmentID string
	AttemptID     string
	Action        Action
	Outcome       Outcome
	Detail        map[string]any
	ErrorMessage  string
}

// Record writes one audit entry. Best-effort: a failed audit-log write
// is logged (to stdout, the one place this package deliberately still
// uses ephemeral logging, since the whole point of this package is to
// have a durable fallback record when the durable record itself can't
// be written) but never returned as an error to the caller -- an RPC
// that successfully provisioned an environment must not fail the whole
// request because the audit-log INSERT hit a transient DB blip. This
// mirrors every other post-action bookkeeping step in this codebase
// (reaper.Unregister, meter.StopMetering, etc), which are all
// best-effort for the identical reason.
func (l *Logger) Record(ctx context.Context, e Entry) {
	detailJSON, err := json.Marshal(e.Detail)
	if err != nil {
		log.Printf("[audit] failed to marshal detail for action=%s env=%s: %v", e.Action, e.EnvironmentID, err)
		detailJSON = []byte(`{}`)
	}

	_, err = l.db.Exec(ctx, `
		INSERT INTO env.audit_log (environment_id, attempt_id, action, outcome, detail, error_message)
		VALUES ($1, $2, $3, $4, $5, $6)
	`, nullIfEmpty(e.EnvironmentID), nullIfEmpty(e.AttemptID), string(e.Action), string(e.Outcome), detailJSON, nullIfEmpty(e.ErrorMessage))
	if err != nil {
		log.Printf("[audit] WARNING: failed to write audit log entry action=%s env=%s outcome=%s: %v", e.Action, e.EnvironmentID, e.Outcome, err)
	}
}

// nullIfEmpty lets an empty string bind as SQL NULL instead of an empty
// string literal -- environment_id/attempt_id/error_message are all
// legitimately absent for some entries (e.g. a Provision call that
// failed before an environment_id was even assigned), and NULL is the
// honest representation of "not applicable" versus "" which would read
// as a real, if empty, value.
func nullIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}
