package k8s

import (
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
)

// PLAN.md Phase 4's K15: DefaultT1Resources/DefaultT2Resources are now
// the single source of truth resourceQuotaFor, limitRangeMaxFor, and
// the workspace pod's own container Limits fallback (T1 in
// createWorkspacePod, T2 in applyT2PodShape) all read from, closing a
// real gap -- these 3 real call sites previously hardcoded the same
// "2 CPU/4Gi" and "8 CPU/16Gi" numbers independently, with nothing
// enforcing they stayed in sync.
func TestDefaultT1Resources_MatchesDocumentedT1Quota(t *testing.T) {
	// Doc §5.2's T1 quota table: "cpu 2, mem 4Gi".
	if DefaultT1Resources.CPU != "2" {
		t.Errorf("DefaultT1Resources.CPU = %q, want 2", DefaultT1Resources.CPU)
	}
	if DefaultT1Resources.Memory != "4Gi" {
		t.Errorf("DefaultT1Resources.Memory = %q, want 4Gi", DefaultT1Resources.Memory)
	}
}

func TestDefaultT2Resources_HigherThanT1(t *testing.T) {
	t1CPU := resource.MustParse(DefaultT1Resources.CPU)
	t2CPU := resource.MustParse(DefaultT2Resources.CPU)
	if t2CPU.Cmp(t1CPU) <= 0 {
		t.Errorf("DefaultT2Resources.CPU (%s) must exceed DefaultT1Resources.CPU (%s)", DefaultT2Resources.CPU, DefaultT1Resources.CPU)
	}

	t1Mem := resource.MustParse(DefaultT1Resources.Memory)
	t2Mem := resource.MustParse(DefaultT2Resources.Memory)
	if t2Mem.Cmp(t1Mem) <= 0 {
		t.Errorf("DefaultT2Resources.Memory (%s) must exceed DefaultT1Resources.Memory (%s)", DefaultT2Resources.Memory, DefaultT1Resources.Memory)
	}
}

// The real invariant K15 closes: limitRangeMaxFor's per-tier ceiling
// and resourceQuotaFor's per-tier request quota must literally be the
// same values as DefaultT1Resources/DefaultT2Resources, not just
// "happen to match today" -- this test would fail immediately if either
// function's own literal ever drifted from these constants again.
func assertQuantityEquals(t *testing.T, label string, got resource.Quantity, wantLiteral string) {
	t.Helper()
	want := resource.MustParse(wantLiteral)
	if got.Cmp(want) != 0 {
		t.Errorf("%s = %s, want %s", label, got.String(), wantLiteral)
	}
}

func TestLimitRangeMaxFor_MatchesDefaultResourcesExactly(t *testing.T) {
	t1Max := limitRangeMaxFor(TierT1SharedContainer)
	assertQuantityEquals(t, "limitRangeMaxFor(T1) CPU", t1Max[corev1.ResourceCPU], DefaultT1Resources.CPU)
	assertQuantityEquals(t, "limitRangeMaxFor(T1) memory", t1Max[corev1.ResourceMemory], DefaultT1Resources.Memory)

	t2Max := limitRangeMaxFor(TierT2IsolatedMicroVM)
	assertQuantityEquals(t, "limitRangeMaxFor(T2) CPU", t2Max[corev1.ResourceCPU], DefaultT2Resources.CPU)
	assertQuantityEquals(t, "limitRangeMaxFor(T2) memory", t2Max[corev1.ResourceMemory], DefaultT2Resources.Memory)
}

func TestResourceQuotaFor_RequestsMatchDefaultResourcesExactly(t *testing.T) {
	t1Quota := resourceQuotaFor(TierT1SharedContainer)
	assertQuantityEquals(t, "resourceQuotaFor(T1) requests.cpu", t1Quota[corev1.ResourceRequestsCPU], DefaultT1Resources.CPU)
	assertQuantityEquals(t, "resourceQuotaFor(T1) requests.memory", t1Quota[corev1.ResourceRequestsMemory], DefaultT1Resources.Memory)

	t2Quota := resourceQuotaFor(TierT2IsolatedMicroVM)
	assertQuantityEquals(t, "resourceQuotaFor(T2) requests.cpu", t2Quota[corev1.ResourceRequestsCPU], DefaultT2Resources.CPU)
	assertQuantityEquals(t, "resourceQuotaFor(T2) requests.memory", t2Quota[corev1.ResourceRequestsMemory], DefaultT2Resources.Memory)
}
