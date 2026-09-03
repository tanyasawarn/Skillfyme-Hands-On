package ttl

import (
	"testing"
	"time"
)

// PLAN.md K18: every value here previously lived as an independent
// constant in a different package, kept "in sync" only via prose
// cross-references in comments (e.g. warmpool's "shorter than
// defaultTTL", wsgateway's "well under defaultTTL (90min)") -- this
// pins each real value so a future edit to any one can't silently drift
// without a test failure, matching this codebase's own established
// discipline (K14/K15's tests).
func TestTTLValues_MatchDocumentedDefaults(t *testing.T) {
	cases := []struct {
		name string
		got  time.Duration
		want time.Duration
	}{
		{"EnvironmentDefault (doc §5.5)", EnvironmentDefault, 90 * time.Minute},
		{"EnvironmentDefaultT2 (t2-cost-optimization.md §3.1)", EnvironmentDefaultT2, 45 * time.Minute},
		{"IdleTimeoutDefault (doc §5.6)", IdleTimeoutDefault, 15 * time.Minute},
		{"WarmPool (doc §5.5)", WarmPool, 30 * time.Minute},
		{"SessionToken", SessionToken, 30 * time.Minute},
		{"ValidatorCredential (doc §6.2)", ValidatorCredential, 5 * time.Minute},
		{"FixtureToken", FixtureToken, 4 * time.Hour},
	}
	for _, c := range cases {
		if c.got != c.want {
			t.Errorf("%s = %v, want %v", c.name, c.got, c.want)
		}
	}
}

func TestWarmPool_ShorterThanEnvironmentDefault(t *testing.T) {
	// Doc §5.5: "Warm environments have their own shorter TTL (30 min)
	// and are recycled" -- a real invariant, not just documentation.
	if WarmPool >= EnvironmentDefault {
		t.Errorf("WarmPool (%v) must be shorter than EnvironmentDefault (%v)", WarmPool, EnvironmentDefault)
	}
}

func TestSessionToken_ShorterThanEnvironmentDefault(t *testing.T) {
	if SessionToken >= EnvironmentDefault {
		t.Errorf("SessionToken (%v) must be shorter than EnvironmentDefault (%v)", SessionToken, EnvironmentDefault)
	}
}

func TestEnvironmentDefaultT2_ShorterThanT1Default(t *testing.T) {
	// t2-cost-optimization.md §3.1: at T2's $0.10-0.35/env-hr band, a
	// walked-away microVM on the 90-min T1 default burns ~2x its intended
	// per-attempt cost. The T2 default must stay strictly shorter.
	if EnvironmentDefaultT2 >= EnvironmentDefault {
		t.Errorf("EnvironmentDefaultT2 (%v) must be shorter than EnvironmentDefault (%v)", EnvironmentDefaultT2, EnvironmentDefault)
	}
}
