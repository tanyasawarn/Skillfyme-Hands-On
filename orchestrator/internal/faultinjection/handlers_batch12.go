package faultinjection

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/tanyasawarn/skillfyme-hands-on/orchestrator/internal/k8s"
)

// Twelfth batch: the three Terraform faults, all backed by
// fx.terraform-workspace.v1 (internal/fixture/handlers_terraform.go).
// Registered as DynamicHandler since each needs *k8s.Provisioner for
// ExecInPod (running the real `terraform` binary inside the fixture's
// own runner pod).
func init() {
	registerDynamic("f.tf.state-lock-orphan", applyTerraformStateLockOrphan)
	registerDynamic("f.tf.state-drift-manual-change", applyTerraformStateDriftManualChange)
	registerDynamic("f.tf.module-version-pin-mismatch", applyTerraformModuleVersionPinMismatch)
}

const terraformRunnerPodLabelSelector = "app=practice-terraform-runner"

// tfRegistryModuleSource/tfVersionA/tfVersionB mirror
// internal/fixture/handlers_terraform.go's own constants of the same
// name -- duplicated here (not imported; fixture's are unexported)
// following this codebase's existing cross-package pattern (see
// helmReleaseNameConst in handlers_batch10.go for the same reasoning).
const (
	tfRegistryModuleSource = "hashicorp/dir/template"
	tfVersionA             = "1.0.0"
	tfVersionB             = "1.0.2"
)

// applyTerraformStateLockOrphan: content/faults/f.tf.state-lock-orphan.yaml
// params: workspace (must be "lock", the fixture's real S3-backed
// workspace name -- content-authored, validated against the fixture's
// only lock-capable workspace), lock_id (accepted for the content
// author's own reference/display; the REAL lock's ID is whatever
// Terraform itself generates, not injectable -- this fault produces a
// genuinely orphaned lock, not a fake one, so it can't pre-choose the ID).
//
// Runs a real `terraform apply` against the fixture's MinIO-backed "lock"
// workspace and kills it (SIGKILL) mid-flight -- confirmed live (this
// session) that only a REMOTE backend (this one: S3-compatible MinIO,
// with use_lockfile's conditional-write locking) produces a genuinely
// orphaned lock this way; a local backend's OS-level flock self-releases
// the instant the holding process dies, even via SIGKILL, so a lock
// orphan is structurally NOT reproducible against a local backend (see
// fx.terraform-workspace.v1's own doc comment for the full live-verified
// reasoning). The lock genuinely blocks every subsequent apply/plan
// against this workspace until `terraform force-unlock <lock-id>` is run,
// matching the fault's own canonical_diagnostic_path exactly.
func applyTerraformStateLockOrphan(ctx context.Context, provisioner *k8s.Provisioner, namespace string, params map[string]string) (Result, error) {
	workspace := params["workspace"]
	if workspace == "" {
		return Result{}, fmt.Errorf("f.tf.state-lock-orphan requires param: workspace")
	}
	if workspace != "lock" {
		return Result{}, fmt.Errorf("f.tf.state-lock-orphan: workspace %q does not match the fixture's real lock-capable workspace %q", workspace, "lock")
	}

	runnerPod, err := k8s.FindPodByLabel(ctx, provisioner, namespace, terraformRunnerPodLabelSelector)
	if err != nil {
		return Result{}, fmt.Errorf("finding terraform runner pod: %w", err)
	}

	// Already orphaned from a prior call: idempotent, don't re-kill (a
	// second concurrent apply against an already-locked workspace would
	// just fail immediately with the same lock error, not add a second
	// lock -- S3's conditional-write locking is single-holder).
	checkCmd := "cd /work/lock && terraform plan -no-color -lock-timeout=1s 2>&1"
	checkResult, err := k8s.ExecInPod(ctx, provisioner, namespace, runnerPod, "terraform", checkCmd, 15*time.Second)
	if err == nil && strings.Contains(checkResult.Stdout+checkResult.Stderr, "Error acquiring the state lock") {
		return Result{Applied: true, SymptomVerified: true}, nil
	}

	// Start a real apply in the background and SIGKILL it mid-flight --
	// a graceful kill (SIGTERM/Ctrl-C) lets Terraform's own signal
	// handler release the lock cleanly, which would NOT reproduce this
	// fault; SIGKILL bypasses that, confirmed live to leave the S3 lock
	// object behind exactly like a real crashed CI job would.
	// -replace=time_sleep.wait forces a genuine destroy+recreate of the
	// resource (giving a real ~8s window to kill mid-apply) -- without
	// it, re-running apply against the already-converged baseline is a
	// same-second no-op (nothing to create), confirmed live: the first
	// version of this handler's kill arrived AFTER the (instant) apply
	// had already finished, so the "orphaned" lock was never actually
	// produced.
	launchCmd := `cd /work/lock && terraform apply -auto-approve -no-color -replace=time_sleep.wait -lock-timeout=1s > /tmp/apply.log 2>&1 & echo $!`
	launchResult, err := k8s.ExecInPod(ctx, provisioner, namespace, runnerPod, "terraform", launchCmd, 10*time.Second)
	if err != nil {
		return Result{}, fmt.Errorf("launching background apply: %w", err)
	}
	pid := strings.TrimSpace(launchResult.Stdout)
	if pid == "" {
		return Result{}, fmt.Errorf("launching background apply: no PID captured (output: %s)", launchResult.Stdout+launchResult.Stderr)
	}

	time.Sleep(2 * time.Second)

	killCmd := fmt.Sprintf("kill -9 %s 2>&1 || true", pid)
	if _, err := k8s.ExecInPod(ctx, provisioner, namespace, runnerPod, "terraform", killCmd, 10*time.Second); err != nil {
		return Result{}, fmt.Errorf("killing in-flight apply: %w", err)
	}

	time.Sleep(1 * time.Second)

	verifyCmd := "cd /work/lock && terraform plan -no-color -lock-timeout=1s 2>&1"
	verifyResult, err := k8s.ExecInPod(ctx, provisioner, namespace, runnerPod, "terraform", verifyCmd, 15*time.Second)
	verified := err == nil && strings.Contains(verifyResult.Stdout+verifyResult.Stderr, "Error acquiring the state lock")
	return Result{Applied: true, SymptomVerified: verified}, nil
}

// applyTerraformStateDriftManualChange: content/faults/f.tf.state-drift-manual-change.yaml
// params: resource_address (must be "local_file.tracked", the fixture's
// only drift-capable resource), drifted_field (accepted for the content
// author's own reference; this fixture's real_file resource has exactly
// one meaningful mutable field -- content -- so the drift always targets
// that field regardless of the string passed, same "content-authored,
// not validated against real structure" stance as
// f.k8s.networkpolicy-overblocks-traffic's missing_dependency).
//
// Directly overwrites the drift workspace's tracked.txt file on disk
// (bypassing Terraform entirely, exactly like a real "someone changed it
// by hand" incident), producing a genuine `terraform plan` diff against
// the still-unchanged state file.
func applyTerraformStateDriftManualChange(ctx context.Context, provisioner *k8s.Provisioner, namespace string, params map[string]string) (Result, error) {
	resourceAddress := params["resource_address"]
	if resourceAddress == "" {
		return Result{}, fmt.Errorf("f.tf.state-drift-manual-change requires param: resource_address")
	}
	if resourceAddress != "local_file.tracked" {
		return Result{}, fmt.Errorf("f.tf.state-drift-manual-change: resource_address %q does not match the fixture's real drift-capable resource %q", resourceAddress, "local_file.tracked")
	}

	runnerPod, err := k8s.FindPodByLabel(ctx, provisioner, namespace, terraformRunnerPodLabelSelector)
	if err != nil {
		return Result{}, fmt.Errorf("finding terraform runner pod: %w", err)
	}

	driftCmd := `echo -n "manually-edited-outside-terraform" > /work/drift/tracked.txt`
	driftResult, err := k8s.ExecInPod(ctx, provisioner, namespace, runnerPod, "terraform", driftCmd, 10*time.Second)
	if err != nil {
		return Result{}, fmt.Errorf("writing drifted file content: %w", err)
	}
	if driftResult.ExitCode != 0 {
		return Result{}, fmt.Errorf("writing drifted file content failed (exit %d): %s", driftResult.ExitCode, driftResult.Stdout+driftResult.Stderr)
	}

	// -detailed-exitcode: 0 = no changes, 1 = error, 2 = changes present
	// -- 2 is the genuinely-verified success case here (real drift shows
	// up as a real plan diff), not an error to propagate.
	planCmd := "cd /work/drift && terraform plan -no-color -detailed-exitcode; echo EXIT:$?"
	planResult, err := k8s.ExecInPod(ctx, provisioner, namespace, runnerPod, "terraform", planCmd, 30*time.Second)
	if err != nil {
		return Result{}, fmt.Errorf("running terraform plan after drift: %w", err)
	}
	verified := strings.Contains(planResult.Stdout, "EXIT:2") && strings.Contains(planResult.Stdout, "local_file.tracked")
	return Result{Applied: true, SymptomVerified: verified}, nil
}

// applyTerraformModuleVersionPinMismatch: content/faults/f.tf.module-version-pin-mismatch.yaml
// params: module_source (must match the fixture's real registry module),
// version_a/version_b (must match the fixture's own two real, currently-
// published versions -- content-authored, validated against what the
// fixture actually resolved rather than silently accepting arbitrary
// version strings the fixture never applied).
//
// Unlike the other two faults, this one needs no mutation at apply time
// -- fx.terraform-workspace.v1's own healthy baseline ALREADY lays out
// two real workspaces (module-a, module-b) each calling the SAME real
// HashiCorp registry module (hashicorp/dir/template) at two different
// real, currently-published versions, and confirms live they resolve
// genuinely different behavior (Content-Type "image/svg" vs.
// "image/svg+xml" for an identical input file -- see the fixture's own
// doc comment for why this specific module/version pair was chosen: real
// registry version resolution with zero cloud-credential dependency).
// This handler's job is verifying that real, already-existing divergence
// is actually present -- matching the fault's own diagnostic path
// ("terraform init -upgrade -> notice different resolved module
// versions") without re-doing what the fixture already did.
func applyTerraformModuleVersionPinMismatch(ctx context.Context, provisioner *k8s.Provisioner, namespace string, params map[string]string) (Result, error) {
	moduleSource := params["module_source"]
	versionA := params["version_a"]
	versionB := params["version_b"]
	if moduleSource == "" || versionA == "" || versionB == "" {
		return Result{}, fmt.Errorf("f.tf.module-version-pin-mismatch requires params: module_source, version_a, version_b")
	}
	if moduleSource != tfRegistryModuleSource {
		return Result{}, fmt.Errorf("f.tf.module-version-pin-mismatch: module_source %q does not match the fixture's real registry module %q", moduleSource, tfRegistryModuleSource)
	}
	if versionA != tfVersionA || versionB != tfVersionB {
		return Result{}, fmt.Errorf("f.tf.module-version-pin-mismatch: version_a/version_b (%q, %q) must match the fixture's real resolved versions (%q, %q)", versionA, versionB, tfVersionA, tfVersionB)
	}

	runnerPod, err := k8s.FindPodByLabel(ctx, provisioner, namespace, terraformRunnerPodLabelSelector)
	if err != nil {
		return Result{}, fmt.Errorf("finding terraform runner pod: %w", err)
	}

	aResult, err := k8s.ExecInPod(ctx, provisioner, namespace, runnerPod, "terraform",
		"cd /work/module-a && terraform output -raw svg_content_type", 15*time.Second)
	if err != nil {
		return Result{}, fmt.Errorf("reading module-a output: %w", err)
	}
	bResult, err := k8s.ExecInPod(ctx, provisioner, namespace, runnerPod, "terraform",
		"cd /work/module-b && terraform output -raw svg_content_type", 15*time.Second)
	if err != nil {
		return Result{}, fmt.Errorf("reading module-b output: %w", err)
	}

	verified := aResult.ExitCode == 0 && bResult.ExitCode == 0 &&
		aResult.Stdout != "" && aResult.Stdout != bResult.Stdout
	return Result{Applied: true, SymptomVerified: verified}, nil
}
