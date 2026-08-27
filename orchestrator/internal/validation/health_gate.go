package validation

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"github.com/tanyasawarn/skillfyme-hands-on/orchestrator/internal/k8s"
)

// HealthGateCheck mirrors contracts/activity_spec.schema.json's
// health_gate array items -- doc §3.2/§7.3 worked example: "checks that
// MUST pass before any fault in `faults` is applied." Previously
// declared on the wire and in content (see
// content/activities/sim.sre.checkout-latency-incident.yaml's own
// health_gate: block) but never read by Provision() -- this is the real
// implementation, closing that gap.
type HealthGateCheck struct {
	Type         string // "HTTP_PROBE" or "K8S_ASSERT" -- schema's own enum
	URL          string
	ExpectStatus int
	Retries      int
}

const (
	healthGateDefaultRetries       = 10
	healthGateRetryIntervalSeconds = 2
)

// RunHealthGate executes every check in checks, IN ORDER, against
// envID's workspace pod -- doc's own step ordering (§5.5: "create pods
// -> fixture apply -> health gate -> [faults, for PRODUCTION_SIM]")
// means fixtures have already run and established whatever baseline
// state a check is probing by the time this runs. Returns the first
// check that fails to pass within its retry budget; a later check is
// never attempted once an earlier one has definitively failed (a broken
// baseline invalidates whatever a later check would have measured
// anyway).
//
// K8S_ASSERT health-gate checks are deliberately NOT implemented here --
// unlike HTTP_PROBE (which needs the workspace pod's own network view,
// hence execInPod), a K8S_ASSERT-shaped health check would read K8s
// object status directly via the orchestrator's own cluster access, the
// exact mechanism internal/validation's execK8sAssert already
// implements for the validator type of the same name. No content
// authored today declares a K8S_ASSERT health_gate check (confirmed:
// only sim.sre.checkout-latency-incident.yaml has a health_gate block at
// all, and it's HTTP_PROBE) -- returns a clear, typed error rather than
// silently no-op'ing if one ever is, so a content author gets an honest
// "not implemented yet" instead of a health gate that always passes
// trivially (exactly the class of bug doc §3.5's null-path CI exists to
// catch).
func RunHealthGate(ctx context.Context, provisioner *k8s.Provisioner, envID string, checks []HealthGateCheck) error {
	for _, check := range checks {
		switch check.Type {
		case "HTTP_PROBE":
			if err := runHTTPProbeHealthCheck(ctx, provisioner, envID, check); err != nil {
				return fmt.Errorf("health gate HTTP_PROBE %s: %w", check.URL, err)
			}
		case "K8S_ASSERT":
			return fmt.Errorf("health gate check type K8S_ASSERT is not implemented yet (only HTTP_PROBE is) -- content declared one anyway, which would otherwise silently pass trivially")
		default:
			return fmt.Errorf("unknown health gate check type %q", check.Type)
		}
	}
	return nil
}

// runHTTPProbeHealthCheck retries an HTTP GET against check.URL from
// inside the workspace pod (same network-boundary-honest reasoning as
// execHTTPSLO: this measures what the environment can actually reach,
// not an orchestrator-side bypass) until it returns check.ExpectStatus
// or the retry budget is exhausted.
func runHTTPProbeHealthCheck(ctx context.Context, provisioner *k8s.Provisioner, envID string, check HealthGateCheck) error {
	if check.URL == "" {
		return fmt.Errorf("HTTP_PROBE requires a url")
	}
	expectStatus := check.ExpectStatus
	if expectStatus == 0 {
		expectStatus = 200 // schema doesn't mark expect_status required; 200 is the sane default for "is this healthy"
	}
	retries := check.Retries
	if retries <= 0 {
		retries = healthGateDefaultRetries
	}

	// One exec call runs the whole retry loop internally -- same
	// rationale as execHTTPSLO's own comment on why N separate execInPod
	// calls would be both slower and less timing-accurate than one
	// script looping with sleeps.
	script := fmt.Sprintf(
		`for i in $(seq 1 %d); do code=$(curl -s -o /dev/null -w '%%{http_code}' --max-time 5 %s); if [ "$code" = "%d" ]; then echo "HEALTHY"; exit 0; fi; sleep %d; done; echo "UNHEALTHY:$code"; exit 1`,
		retries, shellQuote(check.URL), expectStatus, healthGateRetryIntervalSeconds,
	)

	// Context deadline needs headroom for the full retry loop
	// (retries * (max curl time + sleep interval)), not the caller's
	// default -- a health gate legitimately waiting through several
	// retries for a service to come up must not be cut off mid-loop by
	// an unrelated shorter deadline.
	timeout := time.Duration(retries*(5+healthGateRetryIntervalSeconds)) * time.Second
	execCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	out, err := execInPod(execCtx, provisioner, envID, script)
	if err != nil {
		return fmt.Errorf("executing health probe: %w", err)
	}
	if out.ExitCode != 0 {
		return fmt.Errorf("did not become healthy (expected HTTP %d) within %d retries: %s", expectStatus, retries, trimTrailingNewline(out.Stdout))
	}
	return nil
}

func trimTrailingNewline(s string) string {
	if len(s) > 0 && s[len(s)-1] == '\n' {
		return s[:len(s)-1]
	}
	return s
}

// ParseHealthGateJSON decodes contracts/activity_spec.schema.json's
// health_gate array (already unmarshalled from the activity's
// spec_jsonb as []map[string]any by the caller -- practice-core sends
// this to the orchestrator as a JSON string on the wire, since
// ProvisionRequest has no typed health_gate field, matching how
// InjectFaultRequest.params is a plain wire-level map rather than a
// richly typed structure) into []HealthGateCheck.
func ParseHealthGateJSON(raw []map[string]any) ([]HealthGateCheck, error) {
	checks := make([]HealthGateCheck, 0, len(raw))
	for i, item := range raw {
		typ, _ := item["type"].(string)
		if typ == "" {
			return nil, fmt.Errorf("health_gate[%d]: missing required field \"type\"", i)
		}
		check := HealthGateCheck{Type: typ}
		if url, ok := item["url"].(string); ok {
			check.URL = url
		}
		if expectStatus, ok := item["expect_status"]; ok {
			v, err := toIntLoose(expectStatus)
			if err != nil {
				return nil, fmt.Errorf("health_gate[%d]: invalid expect_status: %w", i, err)
			}
			check.ExpectStatus = v
		}
		if retries, ok := item["retries"]; ok {
			v, err := toIntLoose(retries)
			if err != nil {
				return nil, fmt.Errorf("health_gate[%d]: invalid retries: %w", i, err)
			}
			check.Retries = v
		}
		checks = append(checks, check)
	}
	return checks, nil
}

// toIntLoose handles both JSON's native float64 (the common case when
// this map came from encoding/json.Unmarshal into map[string]any) and a
// plain int (the common case in Go-constructed test fixtures) without
// forcing every caller to know which one they're holding.
func toIntLoose(v any) (int, error) {
	switch t := v.(type) {
	case float64:
		return int(t), nil
	case int:
		return t, nil
	case string:
		return strconv.Atoi(t)
	default:
		return 0, fmt.Errorf("unsupported type %T", v)
	}
}
