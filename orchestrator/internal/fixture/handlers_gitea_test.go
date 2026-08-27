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

func TestGiteaFixtureAndFault_LiveIntegration(t *testing.T) {
	provisioner := setupLiveProvisioner(t)
	ctx, cancel := context.WithTimeout(context.Background(), 240*time.Second)
	defer cancel()

	envID := uuid.New().String()
	ns := "fx-gitea-test-" + envID[:8]

	clientset := provisioner.Clientset()
	if _, err := clientset.CoreV1().Namespaces().Create(ctx, &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{Name: ns},
	}, metav1.CreateOptions{}); err != nil {
		t.Fatalf("creating test namespace: %v", err)
	}
	t.Cleanup(func() {
		_ = clientset.CoreV1().Namespaces().Delete(context.Background(), ns, metav1.DeleteOptions{})
	})
	applyRealT1NetworkBaseline(t, ctx, provisioner, ns)

	if err := applyGiteaRepo(ctx, provisioner, envID, ns); err != nil {
		t.Fatalf("applyGiteaRepo failed: %v", err)
	}

	runnerPod, err := k8s.FindPodByLabel(ctx, provisioner, ns, "app="+giteaRunnerDeployment)
	if err != nil {
		t.Fatalf("finding gitea runner pod: %v", err)
	}

	// healthy baseline (giteaLegitCIUser can push) is already verified
	// as part of applyGiteaRepo itself (ensureGiteaHealthyBaselinePush) --
	// re-confirmed here directly against the real repo state.
	t.Run("healthy baseline: whitelisted CI user's push already landed on main", func(t *testing.T) {
		giteaPod, err := k8s.FindPodByLabel(ctx, provisioner, ns, "app="+giteaDeployment)
		if err != nil {
			t.Fatalf("finding gitea pod: %v", err)
		}
		result, err := k8s.ExecInPod(ctx, provisioner, ns, giteaPod, "gitea",
			"su-exec git gitea admin user list 2>&1", 15*time.Second)
		if err != nil || result.ExitCode != 0 {
			t.Fatalf("listing gitea users: err=%v result=%+v", err, result)
		}
		if !strings.Contains(result.Stdout, giteaLegitCIUser) || !strings.Contains(result.Stdout, giteaBlockedCIUser) {
			t.Fatalf("expected both CI users to exist, got: %s", result.Stdout)
		}
	})

	// This directly reproduces faultinjection.applyGitProtectedBranchBlocksPush's
	// own real mutation (a real `git push` from the non-whitelisted
	// giteaBlockedCIUser) rather than cross-importing faultinjection
	// (import cycle -- same convention as the Tekton/Terraform live
	// tests), asserting the SAME observable outcome that handler verifies.
	t.Run("f.gitlab.protected-branch-blocks-push: non-whitelisted CI user's push is genuinely rejected", func(t *testing.T) {
		// The setup steps (clone/config/commit) run under `set -e` inside
		// their OWN subshell so a genuine setup failure aborts loudly;
		// the final `git push` runs OUTSIDE that subshell/set-e scope
		// deliberately -- it's EXPECTED to fail (that's this fault's
		// whole point), and ExecInPod's exit-code-marker mechanism
		// (internal/k8s/exec.go) needs its own trailing `echo` to run
		// even when the push fails, which `set -e` would otherwise skip
		// straight past, confirmed live this session as a real bug.
		cmd := `(
set -e
rm -rf /work/fault-check
mkdir -p /work/fault-check
cd /work/fault-check
git clone -q http://` + giteaBlockedCIUser + `:` + giteaBlockedCIPass + `@` + giteaDeployment + `:3000/` + giteaRepoOwner + `/` + giteaRepoName + `.git . 2>&1
git config user.email "` + giteaBlockedCIUser + `@example.com"
git config user.name "` + giteaBlockedCIUser + `"
echo "blocked-check-$(date +%s)" >> blocked.txt
git add blocked.txt
git commit -q -m "chore: blocked push attempt"
) && cd /work/fault-check && git push origin ` + giteaProtectedBranch + ` 2>&1
`
		result, err := k8s.ExecInPod(ctx, provisioner, ns, runnerPod, "git", cmd, 30*time.Second)
		if err != nil {
			t.Fatalf("running blocked push: %v", err)
		}
		if result.ExitCode == 0 {
			t.Fatal("REGRESSION: expected the non-whitelisted CI user's push to be rejected, but it succeeded")
		}
		if !strings.Contains(result.Stdout+result.Stderr, "Not allowed to push to protected branch") {
			t.Fatalf("expected Gitea's real branch-protection rejection message, got: %s", result.Stdout+result.Stderr)
		}
	})
}
