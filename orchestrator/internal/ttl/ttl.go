// Package ttl centralizes every TTL/timeout duration that doc §5.5/§5.6/
// §6.2 define, previously kept "in sync" only via prose cross-references
// in comments across 5 files -- e.g. warmpool/manager.go's comment says
// "shorter than defaultTTL" and wsgateway/gateway.go's comment says
// "well under defaultTTL (90min)," both referencing server.go's
// defaultTTL by value in a comment, not a compile-time link. A leaf
// package (no dependency on any other internal/ package) so every
// consumer can import it without creating a cycle -- same shape as
// internal/config.
package ttl

import "time"

const (
	// EnvironmentDefault is doc §5.5's "Wall-clock since READY exceeds
	// ttl_minutes" default -- how long a provisioned environment lives
	// before forced teardown, absent a per-request override
	// (ProvisionRequest.TtlMinutes).
	EnvironmentDefault = 90 * time.Minute

	// IdleTimeoutDefault is doc §5.6's "no stdin, no file write, no
	// validation... for idle_timeout (default 15 min)" -- absent a
	// per-activity override (environment.idle_timeout_minutes).
	IdleTimeoutDefault = 15 * time.Minute

	// WarmPool is doc §5.5's "warm environments have their own shorter
	// TTL (30 min) and are recycled" -- intentionally shorter than
	// EnvironmentDefault, not a copy of it.
	WarmPool = 30 * time.Minute

	// SessionToken bounds how long a minted terminal session token
	// stays valid -- kept well under EnvironmentDefault so a token
	// can't outlive a still-working session, but is its own,
	// independently-tunable value (session tokens are re-minted on
	// reconnect; the environment itself is not).
	SessionToken = 30 * time.Minute

	// ValidatorCredential is doc §6.2's "minted per run with a
	// 5-minute lifetime" -- the default TTL for a validator's scoped
	// K8s credentials when MintValidatorCredentials's caller doesn't
	// specify one.
	ValidatorCredential = 5 * time.Minute

	// FixtureToken is fx.k3s-ready.v1's learner-kubectl TokenRequest
	// TTL -- doc's own note: kept comfortably longer than
	// EnvironmentDefault so a long guided lab doesn't fail with
	// Unauthorized near the attempt's own TTL boundary.
	FixtureToken = 4 * time.Hour
)
