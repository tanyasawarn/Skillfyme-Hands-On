package orchestrator

import (
	"testing"
	"time"

	"github.com/tanyasawarn/skillfyme-hands-on/orchestrator/internal/k8s"
	"github.com/tanyasawarn/skillfyme-hands-on/orchestrator/internal/ttl"
	pb "github.com/tanyasawarn/skillfyme-hands-on/orchestrator/pkg/pb"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// resolveTier is deliberately the one function in this package with unit
// tests -- see its own doc comment for why the rest of Server (needing
// pgxpool.Pool, *k8s.Provisioner, *warmpool.Manager, etc.) doesn't have
// test infrastructure in this pass, and why this specific decision was
// worth carving out anyway.

func TestResolveTier_T1RequestAlwaysAllowed(t *testing.T) {
	for _, t2Enabled := range []bool{true, false} {
		tier, err := resolveTier(pb.Tier_TIER_T1_SHARED_CONTAINER, t2Enabled)
		if err != nil {
			t.Errorf("t2Enabled=%v: unexpected error for a T1 request: %v", t2Enabled, err)
		}
		if tier != k8s.TierT1SharedContainer {
			t.Errorf("t2Enabled=%v: expected TierT1SharedContainer, got %v", t2Enabled, tier)
		}
	}
}

func TestResolveTier_UnspecifiedTierDefaultsToT1(t *testing.T) {
	// TIER_UNSPECIFIED (the proto's zero value) must resolve to T1, not
	// be treated as an error or, worse, silently matched against the T2
	// branch -- a caller that forgets to set tier at all must get T1
	// (this package's long-standing default), never accidentally get
	// rejected or accidentally get T2.
	tier, err := resolveTier(pb.Tier_TIER_UNSPECIFIED, false)
	if err != nil {
		t.Fatalf("unexpected error for unspecified tier: %v", err)
	}
	if tier != k8s.TierT1SharedContainer {
		t.Errorf("expected TierT1SharedContainer for unspecified tier, got %v", tier)
	}
}

func TestResolveTier_T2RequestRejectedWhenDisabled(t *testing.T) {
	_, err := resolveTier(pb.Tier_TIER_T2_ISOLATED_MICROVM, false)
	if err == nil {
		t.Fatal("expected an error when T2 is requested but not enabled")
	}
	if status.Code(err) != codes.FailedPrecondition {
		t.Errorf("expected codes.FailedPrecondition, got %v", status.Code(err))
	}
}

// TestResolveTier_T2NeverSilentlyDowngradesToT1 is the security-relevant
// assertion: a disabled-T2 request must return an ERROR, never silently
// resolve to T1. A caller that asked for microVM isolation and got a
// shared gVisor sandbox instead without being told would be a real
// security discrepancy (weaker isolation than requested/expected), not
// an acceptable graceful degradation.
func TestResolveTier_T2NeverSilentlyDowngradesToT1(t *testing.T) {
	tier, err := resolveTier(pb.Tier_TIER_T2_ISOLATED_MICROVM, false)
	if err == nil && tier == k8s.TierT1SharedContainer {
		t.Fatal("SECURITY REGRESSION: a disabled T2 request silently resolved to T1 with no error -- must reject explicitly")
	}
}

func TestResolveTier_T2RequestAllowedWhenEnabled(t *testing.T) {
	tier, err := resolveTier(pb.Tier_TIER_T2_ISOLATED_MICROVM, true)
	if err != nil {
		t.Fatalf("unexpected error when T2 is enabled: %v", err)
	}
	if tier != k8s.TierT2IsolatedMicroVM {
		t.Errorf("expected TierT2IsolatedMicroVM, got %v", tier)
	}
}

// resolveEnvTTL: the cost-relevant TTL-selection decision. T2's default
// must be shorter than T1's so a walked-away microVM can't burn ~2x its
// intended per-attempt cost (docs/t2-cost-optimization.md §3.1), and a
// caller-supplied ttl_minutes must still override either default.

func TestResolveEnvTTL_T2DefaultsShorterThanT1(t *testing.T) {
	t1Default := ttl.EnvironmentDefault // 90m, the Server's configured default

	t1 := resolveEnvTTL(k8s.TierT1SharedContainer, 0, t1Default)
	if t1 != t1Default {
		t.Errorf("T1 with no override: expected %v, got %v", t1Default, t1)
	}

	t2 := resolveEnvTTL(k8s.TierT2IsolatedMicroVM, 0, t1Default)
	if t2 != ttl.EnvironmentDefaultT2 {
		t.Errorf("T2 with no override: expected %v, got %v", ttl.EnvironmentDefaultT2, t2)
	}
	if t2 >= t1 {
		t.Errorf("cost regression: T2 default TTL (%v) must be strictly shorter than T1's (%v)", t2, t1)
	}
}

func TestResolveEnvTTL_CallerOverrideWinsForEitherTier(t *testing.T) {
	const override int32 = 20
	want := 20 * time.Minute
	for _, tier := range []k8s.Tier{k8s.TierT1SharedContainer, k8s.TierT2IsolatedMicroVM} {
		if got := resolveEnvTTL(tier, override, ttl.EnvironmentDefault); got != want {
			t.Errorf("tier=%v: caller ttl_minutes=%d should win, expected %v, got %v", tier, override, want, got)
		}
	}
}

func TestResolveEnvTTL_ZeroOrNegativeOverrideIgnored(t *testing.T) {
	for _, bad := range []int32{0, -1} {
		if got := resolveEnvTTL(k8s.TierT2IsolatedMicroVM, bad, ttl.EnvironmentDefault); got != ttl.EnvironmentDefaultT2 {
			t.Errorf("ttl_minutes=%d must be ignored, expected T2 default %v, got %v", bad, ttl.EnvironmentDefaultT2, got)
		}
	}
}

// checkEnvironmentOwnership closes the access-control gap
// PHASE2_CLOSEOUT.md flagged: InjectFault previously had no way to
// verify the caller's attempt actually owns the target environment. The
// same gap existed on Connect, Destroy, MintValidatorCredentials,
// ExecValidator, and ExecShell, all now closed by this same shared
// helper (originally named checkFaultInjectionOwnership when it only
// backed InjectFault; generalized and renamed once the other 5 RPCs
// started using it too). These tests are the pure-function half of that
// fix's coverage -- each RPC handler does its own real DB lookup
// (untestable without a *pgxpool.Pool, same constraint as everything
// else in this file) and hands both attempt IDs to this function.

func TestCheckEnvironmentOwnership_MatchingAttemptAllowed(t *testing.T) {
	if err := checkEnvironmentOwnership("bb851a5d-c766-4071-b7fd-8291867d901a", "bb851a5d-c766-4071-b7fd-8291867d901a"); err != nil {
		t.Errorf("expected no error when the caller's attempt matches the environment's owner, got: %v", err)
	}
}

// TestCheckEnvironmentOwnership_MismatchedAttemptDenied is the
// security-relevant assertion: a caller claiming a DIFFERENT attempt_id
// than the one that actually provisioned the target environment must be
// rejected -- this is precisely the cross-attempt attack (fault
// injection, environment takeover, credential theft, etc.) the
// access-control gap allowed before this check existed.
func TestCheckEnvironmentOwnership_MismatchedAttemptDenied(t *testing.T) {
	err := checkEnvironmentOwnership("bb851a5d-c766-4071-b7fd-8291867d901a", "11111111-1111-1111-1111-111111111111")
	if err == nil {
		t.Fatal("SECURITY REGRESSION: expected an error when the caller's attempt does not own the environment")
	}
	if status.Code(err) != codes.PermissionDenied {
		t.Errorf("expected codes.PermissionDenied, got %v", status.Code(err))
	}
}

// TestCheckEnvironmentOwnership_CaseInsensitiveMatch is a regression
// test for a real bug caught live during this session's own end-to-end
// verification of this exact check: Postgres's uuid column normalizes
// to lowercase on read, so a legitimate caller passing its own
// attempt_id in a different casing than what's stored (e.g. from
// uuid.New().String() vs a client library that uppercases, or just a
// differently-cased literal) was incorrectly rejected with
// PermissionDenied even though it WAS the real owning attempt --
// confirmed by grpcurl against the live orchestrator before this
// specific fix (uuid.Parse-based comparison) was added.
func TestCheckEnvironmentOwnership_CaseInsensitiveMatch(t *testing.T) {
	lower := "bb851a5d-c766-4071-b7fd-8291867d901a"
	upper := "BB851A5D-C766-4071-B7FD-8291867D901A"
	if err := checkEnvironmentOwnership(lower, upper); err != nil {
		t.Errorf("expected a case-insensitive match (owner from DB is always lowercase, caller casing must not matter): %v", err)
	}
	if err := checkEnvironmentOwnership(upper, lower); err != nil {
		t.Errorf("expected a case-insensitive match in the other direction too: %v", err)
	}
}

func TestCheckEnvironmentOwnership_MalformedUUIDDeniedNotPanics(t *testing.T) {
	// Fails closed: a malformed attempt_id must be denied, never treated
	// as matching anything (including another equally-malformed string)
	// via an accidental raw-string fallback, and must never panic.
	if err := checkEnvironmentOwnership("not-a-uuid", "not-a-uuid"); err == nil {
		t.Error("SECURITY REGRESSION: two identical malformed strings must not be treated as a match")
	}
	if status.Code(checkEnvironmentOwnership("not-a-uuid", "also-not-a-uuid")) != codes.PermissionDenied {
		t.Error("expected PermissionDenied for malformed input, not a different error class")
	}
}

func TestCheckEnvironmentOwnership_EmptyOwnerDenied(t *testing.T) {
	// An environment row with no attempt_id recorded (shouldn't happen in
	// practice -- Provision always writes one -- but a caller passing an
	// empty attempt_id must never match an empty/unset owner by
	// coincidence; that would be a second way to bypass the check).
	err := checkEnvironmentOwnership("", "")
	if err == nil {
		t.Fatal("SECURITY REGRESSION: two empty attempt IDs must not be treated as a match")
	}
}

func TestResolveTier_T3RequestNotHonoredAsT2(t *testing.T) {
	// T3 (cloud account, Phase 3 scope) has no K8s-side driver at all
	// (k8s.Tier only names T1/T2, see its own doc comment). Confirms a
	// T3 request doesn't fall through to the T2 branch by accident (e.g.
	// via a future careless != check) and doesn't silently become T1
	// either -- it should resolve to T1 today only because there's
	// nothing else this function can return, not because T3 is
	// considered equivalent to T1. This test exists so a future T3
	// driver addition is forced to revisit this function rather than
	// inheriting an untested assumption.
	tier, err := resolveTier(pb.Tier_TIER_T3_CLOUD_ACCOUNT, false)
	if err != nil {
		t.Fatalf("unexpected error for a T3 request (T3 has no driver yet, should fall back to T1, not error): %v", err)
	}
	if tier != k8s.TierT1SharedContainer {
		t.Errorf("expected T1 fallback for a T3 request (no T3 driver exists), got %v", tier)
	}
}
