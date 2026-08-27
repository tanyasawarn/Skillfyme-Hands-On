package faultinjection

import (
	"context"
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

func TestApplyTrafficSpike(t *testing.T) {
	clientset := fake.NewSimpleClientset()

	result, err := applyTrafficSpike(context.Background(), clientset, testNamespace, map[string]string{
		"target_url": "http://checkout.env-test.svc.cluster.local",
		"rps":        "10",
		"duration_s": "60",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Applied || !result.SymptomVerified {
		t.Fatalf("expected Applied=true SymptomVerified=true, got %+v", result)
	}

	job, err := clientset.BatchV1().Jobs(testNamespace).Get(context.Background(), "fault-traffic-spike", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("expected load-spike Job to be created: %v", err)
	}
	if len(job.Spec.Template.Spec.Containers) != 10 {
		t.Errorf("expected 10 worker containers (rps=10, under maxWorkers cap), got %d", len(job.Spec.Template.Spec.Containers))
	}
	for _, c := range job.Spec.Template.Spec.Containers {
		if c.SecurityContext == nil || c.SecurityContext.AllowPrivilegeEscalation == nil || *c.SecurityContext.AllowPrivilegeEscalation {
			t.Errorf("worker %s: expected AllowPrivilegeEscalation=false to satisfy namespace PodSecurity restricted", c.Name)
		}
	}
	if job.Spec.Template.Spec.SecurityContext == nil || job.Spec.Template.Spec.SecurityContext.RunAsUser == nil || *job.Spec.Template.Spec.SecurityContext.RunAsUser != 1000 {
		t.Error("expected pod SecurityContext.RunAsUser=1000 to match the namespace's PodSecurity restricted enforcement")
	}
}

// TestApplyTrafficSpike_TargetURLNeverInterpolatedIntoScript is a security
// regression test for a real shell-injection vulnerability found and
// fixed in this handler: target_url used to be embedded directly into
// the Job's shell script text via fmt.Sprintf(..., %q, targetURL). Go's
// %q escapes Go-string metacharacters (quotes, control chars) but NOT
// shell metacharacters like $(...) or backticks, so a target_url
// containing a command substitution would have executed as a real shell
// command inside the load-generator container. The fix passes target_url
// as a shell ARGUMENT ($1) instead of interpolating it into the script.
// This test asserts that property structurally: the script text itself
// must never contain the raw target_url value, regardless of what that
// value is -- the value must only ever appear in Command's argv, as a
// separate, distinct element from the script.
func TestApplyTrafficSpike_TargetURLNeverInterpolatedIntoScript(t *testing.T) {
	clientset := fake.NewSimpleClientset()
	maliciousURL := `$(touch /tmp/pwned)`

	_, err := applyTrafficSpike(context.Background(), clientset, testNamespace, map[string]string{
		"target_url": maliciousURL,
		"rps":        "1",
		"duration_s": "5",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	job, err := clientset.BatchV1().Jobs(testNamespace).Get(context.Background(), "fault-traffic-spike", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("expected job to exist: %v", err)
	}
	for _, c := range job.Spec.Template.Spec.Containers {
		if len(c.Command) < 3 {
			t.Fatalf("expected Command to have at least [sh, -c, script], got %v", c.Command)
		}
		script := c.Command[2]
		if strings.Contains(script, maliciousURL) {
			t.Fatalf("SECURITY REGRESSION: malicious target_url was interpolated directly into the shell script text: %q", script)
		}
		// The malicious value must appear ONLY as a distinct argv
		// element (positional parameter $1's real value), never fused
		// into the script string itself.
		foundAsArg := false
		for _, arg := range c.Command[3:] {
			if arg == maliciousURL {
				foundAsArg = true
			}
		}
		if !foundAsArg {
			t.Fatalf("expected target_url to appear as a separate Command argument (passed via $1), got Command=%v", c.Command)
		}
	}
}

func TestApplyTrafficSpike_WorkerCountCappedAtMax(t *testing.T) {
	clientset := fake.NewSimpleClientset()

	_, err := applyTrafficSpike(context.Background(), clientset, testNamespace, map[string]string{
		"target_url": "http://checkout.env-test.svc.cluster.local",
		"rps":        "5000",
		"duration_s": "30",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	job, err := clientset.BatchV1().Jobs(testNamespace).Get(context.Background(), "fault-traffic-spike", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("expected job to exist: %v", err)
	}
	if len(job.Spec.Template.Spec.Containers) != 50 {
		t.Errorf("expected worker count capped at 50, got %d", len(job.Spec.Template.Spec.Containers))
	}
}

func TestApplyTrafficSpike_Idempotent(t *testing.T) {
	clientset := fake.NewSimpleClientset()
	params := map[string]string{
		"target_url": "http://checkout.env-test.svc.cluster.local",
		"rps":        "10",
		"duration_s": "60",
	}

	if _, err := applyTrafficSpike(context.Background(), clientset, testNamespace, params); err != nil {
		t.Fatalf("first apply: unexpected error: %v", err)
	}
	result, err := applyTrafficSpike(context.Background(), clientset, testNamespace, params)
	if err != nil {
		t.Fatalf("second apply: unexpected error: %v", err)
	}
	if !result.Applied || !result.SymptomVerified {
		t.Fatalf("expected idempotent re-apply to report Applied=true SymptomVerified=true, got %+v", result)
	}

	jobs, _ := clientset.BatchV1().Jobs(testNamespace).List(context.Background(), metav1.ListOptions{})
	if len(jobs.Items) != 1 {
		t.Fatalf("expected still exactly 1 job after re-apply, got %d", len(jobs.Items))
	}
}

func TestApplyTrafficSpike_MissingParams(t *testing.T) {
	clientset := fake.NewSimpleClientset()
	cases := []map[string]string{
		{},
		{"target_url": "http://x"},
		{"target_url": "http://x", "rps": "10"},
		{"rps": "10", "duration_s": "60"},
	}
	for _, params := range cases {
		if _, err := applyTrafficSpike(context.Background(), clientset, testNamespace, params); err == nil {
			t.Errorf("expected error for incomplete params %+v", params)
		}
	}
}

func TestApplyTrafficSpike_InvalidRPS(t *testing.T) {
	clientset := fake.NewSimpleClientset()
	cases := []string{"0", "-5", "not-a-number"}
	for _, rps := range cases {
		_, err := applyTrafficSpike(context.Background(), clientset, testNamespace, map[string]string{
			"target_url": "http://x",
			"rps":        rps,
			"duration_s": "30",
		})
		if err == nil {
			t.Errorf("expected error for invalid rps=%q", rps)
		}
	}
}

func TestApplyTrafficSpike_InvalidDuration(t *testing.T) {
	clientset := fake.NewSimpleClientset()
	cases := []string{"0", "-1", "soon"}
	for _, duration := range cases {
		_, err := applyTrafficSpike(context.Background(), clientset, testNamespace, map[string]string{
			"target_url": "http://x",
			"rps":        "10",
			"duration_s": duration,
		})
		if err == nil {
			t.Errorf("expected error for invalid duration_s=%q", duration)
		}
	}
}

func TestFaultRegistry_TrafficSpikeRegistered(t *testing.T) {
	if _, ok := registry["f.load.traffic-spike"]; !ok {
		t.Error("expected f.load.traffic-spike to be registered")
	}
}
