package validation

import (
	"testing"
)

func TestParseHealthGateJSON_ParsesHTTPProbeCheck(t *testing.T) {
	raw := []map[string]any{
		{"type": "HTTP_PROBE", "url": "http://checkout/healthz", "expect_status": float64(200), "retries": float64(30)},
	}
	checks, err := ParseHealthGateJSON(raw)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(checks) != 1 {
		t.Fatalf("expected 1 check, got %d", len(checks))
	}
	c := checks[0]
	if c.Type != "HTTP_PROBE" || c.URL != "http://checkout/healthz" || c.ExpectStatus != 200 || c.Retries != 30 {
		t.Errorf("unexpected parsed check: %+v", c)
	}
}

func TestParseHealthGateJSON_MissingTypeIsAnError(t *testing.T) {
	raw := []map[string]any{
		{"url": "http://checkout/healthz"},
	}
	_, err := ParseHealthGateJSON(raw)
	if err == nil {
		t.Fatal("expected an error for a check missing its required type field")
	}
}

func TestParseHealthGateJSON_MinimalCheckOmitsOptionalFields(t *testing.T) {
	raw := []map[string]any{
		{"type": "HTTP_PROBE", "url": "http://checkout/healthz"},
	}
	checks, err := ParseHealthGateJSON(raw)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if checks[0].ExpectStatus != 0 || checks[0].Retries != 0 {
		t.Errorf("expected zero-value defaults for unset optional fields, got %+v", checks[0])
	}
}

func TestParseHealthGateJSON_PreservesOrder(t *testing.T) {
	raw := []map[string]any{
		{"type": "HTTP_PROBE", "url": "http://first"},
		{"type": "HTTP_PROBE", "url": "http://second"},
		{"type": "HTTP_PROBE", "url": "http://third"},
	}
	checks, err := ParseHealthGateJSON(raw)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(checks) != 3 {
		t.Fatalf("expected 3 checks, got %d", len(checks))
	}
	for i, want := range []string{"http://first", "http://second", "http://third"} {
		if checks[i].URL != want {
			t.Errorf("checks[%d]: expected URL=%s, got %s (order not preserved)", i, want, checks[i].URL)
		}
	}
}

func TestParseHealthGateJSON_EmptyListIsValid(t *testing.T) {
	checks, err := ParseHealthGateJSON([]map[string]any{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(checks) != 0 {
		t.Errorf("expected 0 checks, got %d", len(checks))
	}
}

func TestParseHealthGateJSON_InvalidExpectStatusType(t *testing.T) {
	raw := []map[string]any{
		{"type": "HTTP_PROBE", "url": "http://x", "expect_status": []int{1, 2}}, // unsupported type
	}
	_, err := ParseHealthGateJSON(raw)
	if err == nil {
		t.Fatal("expected an error for an invalid expect_status type")
	}
}

func TestToIntLoose_HandlesFloat64(t *testing.T) {
	v, err := toIntLoose(float64(200))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if v != 200 {
		t.Errorf("expected 200, got %d", v)
	}
}

func TestToIntLoose_HandlesInt(t *testing.T) {
	v, err := toIntLoose(200)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if v != 200 {
		t.Errorf("expected 200, got %d", v)
	}
}

func TestToIntLoose_HandlesStringDigits(t *testing.T) {
	v, err := toIntLoose("200")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if v != 200 {
		t.Errorf("expected 200, got %d", v)
	}
}

func TestToIntLoose_RejectsUnsupportedType(t *testing.T) {
	_, err := toIntLoose(true)
	if err == nil {
		t.Fatal("expected an error for an unsupported (bool) type")
	}
}

func TestTrimTrailingNewline(t *testing.T) {
	cases := map[string]string{
		"hello\n": "hello",
		"hello":   "hello",
		"":        "",
		"\n":      "",
	}
	for input, want := range cases {
		if got := trimTrailingNewline(input); got != want {
			t.Errorf("trimTrailingNewline(%q) = %q, want %q", input, got, want)
		}
	}
}

// TestRunHealthGate_UnknownCheckTypeIsRejected and
// TestRunHealthGate_K8SAssertIsExplicitlyUnimplemented don't need a real
// K8s cluster/pod -- both fail before ever calling execInPod, at the
// type-switch in RunHealthGate itself.

func TestRunHealthGate_UnknownCheckTypeIsRejected(t *testing.T) {
	err := RunHealthGate(nil, nil, "env-1", []HealthGateCheck{{Type: "NOT_A_REAL_TYPE"}})
	if err == nil {
		t.Fatal("expected an error for an unknown check type")
	}
}

// TestRunHealthGate_K8SAssertIsExplicitlyUnimplemented is a real
// content-safety guard: a K8S_ASSERT health_gate check must fail loudly
// (this test), not silently pass -- doc §3.5's own null-path CI
// principle applied to health gates: a check that always trivially
// passes because it's unimplemented is worse than an honest error.
func TestRunHealthGate_K8SAssertIsExplicitlyUnimplemented(t *testing.T) {
	err := RunHealthGate(nil, nil, "env-1", []HealthGateCheck{{Type: "K8S_ASSERT"}})
	if err == nil {
		t.Fatal("expected an explicit error for K8S_ASSERT, not a silent pass")
	}
}
