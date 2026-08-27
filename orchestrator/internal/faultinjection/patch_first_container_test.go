package faultinjection

import (
	"encoding/json"
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/types"
)

// PLAN.md Phase 3's U11: patchFirstContainer replaces 4 hand-built
// fmt.Sprintf JSON patch strings with real struct marshaling. These
// tests confirm the marshaled output is byte-for-byte equivalent to
// what each original call site produced (parsed as JSON and compared
// structurally, not string-compared, since map/struct key ordering
// isn't guaranteed identical) -- a real behavior-preservation check,
// not just "does it produce valid JSON."
//
// A first attempt at this reused corev1.Container/corev1.Probe directly
// for mutate's parameter type -- these very tests caught it producing
// WRONG output (corev1.Probe's timing fields are `int32,omitempty`, so
// an explicitly-set 0 marshals identically to never-touched, silently
// dropping the "initialDelaySeconds":0 every original patch relied on
// to force-reset that field under strategic-merge-patch semantics).
// containerPatch's ReadinessProbe timing fields are *int32 specifically
// so these tests could catch that class of bug -- see
// faultinjection.go's own doc comment on containerPatch for the full
// account.

func mustParseJSON(t *testing.T, b []byte) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("output is not valid JSON: %v\nraw: %s", err, b)
	}
	return m
}

func TestPatchFirstContainer_ReturnsStrategicMergePatchType(t *testing.T) {
	_, patchType, err := patchFirstContainer("app", func(c *containerPatch) {
		c.Image = "broken:v1"
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if patchType != types.StrategicMergePatchType {
		t.Errorf("expected StrategicMergePatchType, got %v", patchType)
	}
}

func TestPatchFirstContainer_ImagePatch_MatchesOriginalShape(t *testing.T) {
	// Original (handlers_batch2.go, applyRolloutStuckBadImageTag):
	// `{"spec":{"template":{"spec":{"containers":[{"name":%q,"image":%q}]}}}}`
	patchBytes, _, err := patchFirstContainer("checkout", func(c *containerPatch) {
		c.Image = "checkout:bad-tag-xyz"
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	original := []byte(`{"spec":{"template":{"spec":{"containers":[{"name":"checkout","image":"checkout:bad-tag-xyz"}]}}}}`)
	got := mustParseJSON(t, patchBytes)
	want := mustParseJSON(t, original)
	assertDeepEqualJSON(t, got, want)
}

func TestPatchFirstContainer_MemoryLimitPatch_MatchesOriginalShape(t *testing.T) {
	// Original (handlers.go, applyMemoryLimitTooLow):
	// `{"spec":{"template":{"spec":{"containers":[{"name":%q,"resources":{"limits":{"memory":%q}}}]}}}}`
	limitQty := resource.MustParse("32Mi")
	patchBytes, _, err := patchFirstContainer("checkout", func(c *containerPatch) {
		c.Resources = &containerPatchResources{
			Limits: map[corev1.ResourceName]resource.Quantity{corev1.ResourceMemory: limitQty},
		}
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	original := []byte(`{"spec":{"template":{"spec":{"containers":[{"name":"checkout","resources":{"limits":{"memory":"32Mi"}}}]}}}}`)
	got := mustParseJSON(t, patchBytes)
	want := mustParseJSON(t, original)
	assertDeepEqualJSON(t, got, want)
}

func TestPatchFirstContainer_ReadinessProbeTimeoutPatch_MatchesOriginalShape(t *testing.T) {
	// Original (handlers.go, applyReadinessProbeTooAggressive):
	// `{"spec":{"template":{"spec":{"containers":[{"name":%q,"readinessProbe":{"timeoutSeconds":%d,"initialDelaySeconds":0}}]}}}}`
	patchBytes, _, err := patchFirstContainer("checkout", func(c *containerPatch) {
		c.Readiness = &containerPatchProbe{
			TimeoutSeconds:      int32Ptr(1),
			InitialDelaySeconds: int32Ptr(0),
		}
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	original := []byte(`{"spec":{"template":{"spec":{"containers":[{"name":"checkout","readinessProbe":{"timeoutSeconds":1,"initialDelaySeconds":0}}]}}}}`)
	got := mustParseJSON(t, patchBytes)
	want := mustParseJSON(t, original)
	assertDeepEqualJSON(t, got, want)
}

// TestPatchFirstContainer_ExplicitZeroInitialDelayIsPreserved is the
// specific regression test for the bug this file's own header comment
// describes: InitialDelaySeconds explicitly set to 0 must appear in the
// marshaled output (forcing a reset under strategic-merge-patch), not
// be silently omitted as if it were never set.
func TestPatchFirstContainer_ExplicitZeroInitialDelayIsPreserved(t *testing.T) {
	patchBytes, _, err := patchFirstContainer("checkout", func(c *containerPatch) {
		c.Readiness = &containerPatchProbe{InitialDelaySeconds: int32Ptr(0)}
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	got := mustParseJSON(t, patchBytes)
	probe := extractContainer(t, got)["readinessProbe"].(map[string]any)
	if _, present := probe["initialDelaySeconds"]; !present {
		t.Fatal("CORRECTNESS REGRESSION: explicitly-set initialDelaySeconds=0 was omitted from the patch -- this silently fails to reset a container whose probe already had a nonzero delay")
	}
	if probe["initialDelaySeconds"] != float64(0) {
		t.Errorf("expected initialDelaySeconds=0, got %v", probe["initialDelaySeconds"])
	}
}

func TestPatchFirstContainer_UnsetReadinessFieldsAreOmitted(t *testing.T) {
	// The other half of the same distinction: a field the caller never
	// touches (nil pointer) must be genuinely absent from the patch, not
	// present-as-null -- an explicit JSON null under strategic-merge-
	// patch semantics means "delete this field," which would be a
	// different, wrong, unintended mutation.
	patchBytes, _, err := patchFirstContainer("checkout", func(c *containerPatch) {
		c.Readiness = &containerPatchProbe{TimeoutSeconds: int32Ptr(1)}
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	got := mustParseJSON(t, patchBytes)
	probe := extractContainer(t, got)["readinessProbe"].(map[string]any)
	if _, present := probe["initialDelaySeconds"]; present {
		t.Error("expected initialDelaySeconds to be entirely absent (not null, not present) when never set")
	}
	if _, present := probe["periodSeconds"]; present {
		t.Error("expected periodSeconds to be entirely absent when never set")
	}
}

func TestPatchFirstContainer_ReadinessProbeExecCommandPatch_MatchesOriginalShape(t *testing.T) {
	// Original (handlers_batch2.go, applyStatefulSetOrdinalStuck):
	// `{"spec":{"template":{"spec":{"containers":[{"name":%q,"readinessProbe":{"exec":{"command":["sh","-c",%q]},"initialDelaySeconds":0,"periodSeconds":5}}]}}}}`
	probeCmd := `test "$(hostname)" != "web-2"`
	patchBytes, _, err := patchFirstContainer("web", func(c *containerPatch) {
		c.Readiness = &containerPatchProbe{
			Exec:                &corev1.ExecAction{Command: []string{"sh", "-c", probeCmd}},
			InitialDelaySeconds: int32Ptr(0),
			PeriodSeconds:       int32Ptr(5),
		}
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	original := []byte(`{"spec":{"template":{"spec":{"containers":[{"name":"web","readinessProbe":{"exec":{"command":["sh","-c","test \"$(hostname)\" != \"web-2\""]},"initialDelaySeconds":0,"periodSeconds":5}}]}}}}`)
	got := mustParseJSON(t, patchBytes)
	want := mustParseJSON(t, original)
	assertDeepEqualJSON(t, got, want)
}

// TestPatchFirstContainer_InjectionSafety is the real security regression
// test this whole extraction exists for: a value containing quote/
// backslash/control characters that would corrupt a hand-built
// fmt.Sprintf("%q", ...) string OR a naive raw-string-interpolation
// approach must still produce structurally valid JSON with the exact
// literal value preserved -- real struct marshaling makes this
// guaranteed by construction rather than by careful %q discipline at
// every call site.
func TestPatchFirstContainer_InjectionSafety(t *testing.T) {
	malicious := `payload", "otherField": {"escalate": true}, "x":"`
	patchBytes, _, err := patchFirstContainer("checkout", func(c *containerPatch) {
		c.Image = malicious
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	got := mustParseJSON(t, patchBytes)
	container := extractContainer(t, got)
	if container["image"] != malicious {
		t.Errorf("expected the malicious string preserved verbatim as the image value, got %v", container["image"])
	}
	if _, injected := container["otherField"]; injected {
		t.Fatal("SECURITY REGRESSION: attacker-controlled input injected a sibling JSON field into the patch structure")
	}
}

func extractContainer(t *testing.T, patch map[string]any) map[string]any {
	t.Helper()
	containers := patch["spec"].(map[string]any)["template"].(map[string]any)["spec"].(map[string]any)["containers"].([]any)
	return containers[0].(map[string]any)
}

func assertDeepEqualJSON(t *testing.T, got, want map[string]any) {
	t.Helper()
	gotBytes, _ := json.Marshal(got)
	wantBytes, _ := json.Marshal(want)
	// Re-marshal both through the same encoder so key ordering is
	// consistent for a straightforward byte comparison after
	// round-tripping through map[string]any (which normalizes ordering
	// deterministically within a single process's json package).
	if string(gotBytes) != string(wantBytes) {
		t.Errorf("JSON structures differ:\n got:  %s\n want: %s", gotBytes, wantBytes)
	}
}
