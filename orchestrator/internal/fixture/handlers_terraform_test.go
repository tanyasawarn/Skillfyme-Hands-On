package fixture

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/tanyasawarn/skillfyme-hands-on/orchestrator/internal/k8s"
)

func TestTerraformFixture_LiveIntegration(t *testing.T) {
	provisioner := setupLiveProvisioner(t)
	ctx, cancel := context.WithTimeout(context.Background(), 240*time.Second)
	defer cancel()

	envID := uuid.New().String()
	ns := "fx-terraform-test-" + envID[:8]

	clientset := provisioner.Clientset()
	if _, err := clientset.CoreV1().Namespaces().Create(ctx, &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name:   ns,
			Labels: map[string]string{"pod-security.kubernetes.io/enforce": "restricted"},
		},
	}, metav1.CreateOptions{}); err != nil {
		t.Fatalf("creating test namespace: %v", err)
	}
	t.Cleanup(func() {
		_ = clientset.CoreV1().Namespaces().Delete(context.Background(), ns, metav1.DeleteOptions{})
	})
	applyRealT1NetworkBaseline(t, ctx, provisioner, ns)

	if err := applyTerraformWorkspace(ctx, provisioner, envID, ns); err != nil {
		t.Fatalf("applyTerraformWorkspace failed: %v", err)
	}

	runnerPod, err := k8s.FindPodByLabel(ctx, provisioner, ns, "app="+tfRunnerDeployment)
	if err != nil {
		t.Fatalf("finding terraform runner pod: %v", err)
	}

	t.Run("healthy baseline: drift workspace has a real, converged local_file resource", func(t *testing.T) {
		result, err := k8s.ExecInPod(ctx, provisioner, ns, runnerPod, "terraform",
			"cd /work/drift && terraform plan -no-color -detailed-exitcode", 30*time.Second)
		if err != nil {
			t.Fatalf("terraform plan failed: %v", err)
		}
		// -detailed-exitcode: 0 = no changes, 1 = error, 2 = changes present.
		if result.ExitCode != 0 {
			t.Fatalf("expected exit 0 (no drift) on the healthy baseline, got exit %d: %s", result.ExitCode, result.Stdout+result.Stderr)
		}
	})

	t.Run("healthy baseline: module-a and module-b resolved genuinely different real registry versions", func(t *testing.T) {
		aResult, err := k8s.ExecInPod(ctx, provisioner, ns, runnerPod, "terraform",
			"cd /work/module-a && terraform output -raw svg_content_type", 15*time.Second)
		if err != nil || aResult.ExitCode != 0 {
			t.Fatalf("reading module-a output: err=%v result=%+v", err, aResult)
		}
		bResult, err := k8s.ExecInPod(ctx, provisioner, ns, runnerPod, "terraform",
			"cd /work/module-b && terraform output -raw svg_content_type", 15*time.Second)
		if err != nil || bResult.ExitCode != 0 {
			t.Fatalf("reading module-b output: err=%v result=%+v", err, bResult)
		}
		if aResult.Stdout == bResult.Stdout {
			t.Fatalf("expected module-a (v%s) and module-b (v%s) to resolve DIFFERENT real content_type values, both got: %q", tfVersionA, tfVersionB, aResult.Stdout)
		}
		if aResult.Stdout != "image/svg" {
			t.Fatalf("expected module-a (v%s, real HashiCorp module before the Content-Type fix) to resolve 'image/svg', got: %q", tfVersionA, aResult.Stdout)
		}
		if bResult.Stdout != "image/svg+xml" {
			t.Fatalf("expected module-b (v%s, real HashiCorp module after the Content-Type fix) to resolve 'image/svg+xml', got: %q", tfVersionB, bResult.Stdout)
		}
	})

	t.Run("healthy baseline: lock workspace state exists in real MinIO, no lock held", func(t *testing.T) {
		result, err := k8s.ExecInPod(ctx, provisioner, ns, runnerPod, "terraform",
			"cd /work/lock && terraform plan -no-color -detailed-exitcode -lock-timeout=5s", 30*time.Second)
		if err != nil {
			t.Fatalf("terraform plan against the lock workspace failed: %v", err)
		}
		if result.ExitCode != 0 {
			t.Fatalf("expected exit 0 (no drift, no lock contention) on the healthy lock-workspace baseline, got exit %d: %s", result.ExitCode, result.Stdout+result.Stderr)
		}
		if strings.Contains(result.Stdout+result.Stderr, "Error acquiring the state lock") {
			t.Fatalf("expected no lock contention on the healthy baseline, got: %s", result.Stdout+result.Stderr)
		}
	})

	// The three subtests below directly reproduce
	// faultinjection.applyTerraformStateLockOrphan /
	// applyTerraformStateDriftManualChange /
	// applyTerraformModuleVersionPinMismatch's own real mutations rather
	// than cross-importing faultinjection (would create an import cycle
	// -- same convention as TestTektonFaultInjection_LiveIntegration in
	// handlers_tekton_test.go), asserting the SAME observable outcomes
	// those handlers verify.

	t.Run("f.tf.state-lock-orphan: a killed apply leaves a genuinely orphaned S3 lock", func(t *testing.T) {
		launchCmd := `cd /work/lock && terraform apply -auto-approve -no-color -replace=time_sleep.wait -lock-timeout=1s > /tmp/apply.log 2>&1 & echo $!`
		launchResult, err := k8s.ExecInPod(ctx, provisioner, ns, runnerPod, "terraform", launchCmd, 10*time.Second)
		if err != nil {
			t.Fatalf("launching background apply: %v", err)
		}
		pid := strings.TrimSpace(launchResult.Stdout)
		if pid == "" {
			t.Fatalf("no PID captured: %s", launchResult.Stdout+launchResult.Stderr)
		}

		time.Sleep(2 * time.Second)
		if _, err := k8s.ExecInPod(ctx, provisioner, ns, runnerPod, "terraform", "kill -9 "+pid, 10*time.Second); err != nil {
			t.Fatalf("killing in-flight apply: %v", err)
		}
		time.Sleep(1 * time.Second)

		result, err := k8s.ExecInPod(ctx, provisioner, ns, runnerPod, "terraform",
			"cd /work/lock && terraform plan -no-color -lock-timeout=1s 2>&1", 15*time.Second)
		if err != nil {
			t.Fatalf("terraform plan after kill: %v", err)
		}
		if !strings.Contains(result.Stdout+result.Stderr, "Error acquiring the state lock") {
			t.Fatalf("expected the killed apply to leave a genuinely orphaned lock blocking the next plan, got: %s", result.Stdout+result.Stderr)
		}
	})

	t.Run("f.tf.state-drift-manual-change: a hand-edited file produces a real plan diff", func(t *testing.T) {
		driftResult, err := k8s.ExecInPod(ctx, provisioner, ns, runnerPod, "terraform",
			`echo -n "manually-edited-outside-terraform" > /work/drift/tracked.txt`, 10*time.Second)
		if err != nil || driftResult.ExitCode != 0 {
			t.Fatalf("writing drifted content: err=%v result=%+v", err, driftResult)
		}

		planResult, err := k8s.ExecInPod(ctx, provisioner, ns, runnerPod, "terraform",
			"cd /work/drift && terraform plan -no-color -detailed-exitcode; echo EXIT:$?", 30*time.Second)
		if err != nil {
			t.Fatalf("terraform plan after drift: %v", err)
		}
		if !strings.Contains(planResult.Stdout, "EXIT:2") {
			t.Fatalf("expected exit 2 (real changes present) after hand-editing tracked.txt outside Terraform, got: %s", planResult.Stdout)
		}
		if !strings.Contains(planResult.Stdout, "local_file.tracked") {
			t.Fatalf("expected the plan diff to reference local_file.tracked, got: %s", planResult.Stdout)
		}
	})

	t.Run("f.tf.module-version-pin-mismatch: two real workspaces already resolved different real module versions", func(t *testing.T) {
		aResult, err := k8s.ExecInPod(ctx, provisioner, ns, runnerPod, "terraform",
			"cd /work/module-a && terraform output -raw svg_content_type", 15*time.Second)
		if err != nil || aResult.ExitCode != 0 {
			t.Fatalf("reading module-a output: err=%v result=%+v", err, aResult)
		}
		bResult, err := k8s.ExecInPod(ctx, provisioner, ns, runnerPod, "terraform",
			"cd /work/module-b && terraform output -raw svg_content_type", 15*time.Second)
		if err != nil || bResult.ExitCode != 0 {
			t.Fatalf("reading module-b output: err=%v result=%+v", err, bResult)
		}
		if aResult.Stdout == "" || aResult.Stdout == bResult.Stdout {
			t.Fatalf("expected module-a and module-b to have resolved genuinely different real values, got %q and %q", aResult.Stdout, bResult.Stdout)
		}
	})
}
