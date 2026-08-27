package faultinjection

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/tanyasawarn/skillfyme-hands-on/orchestrator/internal/k8s"
)

// Thirteenth batch: f.gitlab.protected-branch-blocks-push, backed by
// fx.gitea-repo.v1 (internal/fixture/handlers_gitea.go) provisioning a
// real Gitea instance -- a documented, API-compatible substitute for
// GitLab (Gitea's branch-protection REST API and pre-receive-hook
// enforcement are a real, faithful analog of GitLab's "protected
// branches" feature, same "API-compatible substitute" precedent as
// fx.ansible-target.v1 standing in for a generic CI-runner). Registered
// as DynamicHandler since it needs *k8s.Provisioner for ExecInPod
// (running real `git` operations inside the fixture's own runner pod).
func init() {
	registerDynamic("f.gitlab.protected-branch-blocks-push", applyGitProtectedBranchBlocksPush)
}

const gitProtectedBranchRunnerPodLabelSelector = "app=practice-gitea-runner"

// giteaDeployment/giteaRepoOwner/giteaRepoName/giteaBlockedCIUser/
// giteaBlockedCIPass mirror internal/fixture/handlers_gitea.go's own
// constants of the same name -- duplicated here (not imported; fixture's
// are unexported) following this codebase's existing cross-package
// pattern (see helmReleaseNameConst in handlers_batch10.go).
const (
	giteaDeploymentConst    = "practice-gitea"
	giteaRepoOwnerConst     = "admin"
	giteaRepoNameConst      = "practice-repo"
	giteaBlockedCIUserConst = "ci-blocked"
	giteaBlockedCIPassConst = "ci-blocked-dev-pw-12345"
)

// applyGitProtectedBranchBlocksPush: content/faults/f.gitlab.protected-branch-blocks-push.yaml
// params: branch (must be "main", the fixture's real protected branch --
// content-authored, validated against the fixture's actual protection
// rule).
//
// Runs a real `git push` from the fixture's own non-whitelisted CI user
// (giteaBlockedCIUser -- a "write" collaborator, but NOT on the branch's
// real push whitelist) against the real protected branch. Live-verified
// (this session, both outside and inside the cluster): Gitea's real
// pre-receive hook genuinely rejects this push with "Not allowed to push
// to protected branch <branch>" -- the exact real error the fault's own
// canonical_diagnostic_path describes -- while the fixture's already-
// whitelisted giteaLegitCIUser's identical operation succeeds (verified
// as part of the fixture's own healthy baseline).
func applyGitProtectedBranchBlocksPush(ctx context.Context, provisioner *k8s.Provisioner, namespace string, params map[string]string) (Result, error) {
	branch := params["branch"]
	if branch == "" {
		return Result{}, fmt.Errorf("f.gitlab.protected-branch-blocks-push requires param: branch")
	}
	if branch != "main" {
		return Result{}, fmt.Errorf("f.gitlab.protected-branch-blocks-push: branch %q does not match the fixture's real protected branch %q", branch, "main")
	}

	runnerPod, err := k8s.FindPodByLabel(ctx, provisioner, namespace, gitProtectedBranchRunnerPodLabelSelector)
	if err != nil {
		return Result{}, fmt.Errorf("finding gitea runner pod: %w", err)
	}

	// Setup steps (clone/config/commit) run under `set -e` inside their
	// OWN subshell so a genuine setup failure aborts loudly; the final
	// `git push` runs outside that scope deliberately -- it's EXPECTED
	// to fail (that's this fault's whole point), and ExecInPod's
	// exit-code-marker mechanism (internal/k8s/exec.go) needs its own
	// trailing echo to run even when the push fails, which `set -e`
	// would otherwise skip past entirely -- confirmed live this session
	// as a real bug during this handler's own build.
	cmd := fmt.Sprintf(`(
set -e
rm -rf /work/fault-attempt
mkdir -p /work/fault-attempt
cd /work/fault-attempt
git clone -q http://%s:%s@%s:3000/%s/%s.git . 2>&1
git config user.email "%s@example.com"
git config user.name "%s"
echo "fault-attempt-$(date +%%s)" >> fault.txt
git add fault.txt
git commit -q -m "chore: fault push attempt"
) && cd /work/fault-attempt && git push origin %s 2>&1`,
		giteaBlockedCIUserConst, giteaBlockedCIPassConst, giteaDeploymentConst, giteaRepoOwnerConst, giteaRepoNameConst,
		giteaBlockedCIUserConst, giteaBlockedCIUserConst, branch,
	)

	result, err := k8s.ExecInPod(ctx, provisioner, namespace, runnerPod, "git", cmd, 30*time.Second)
	if err != nil {
		return Result{}, fmt.Errorf("running blocked push attempt: %w", err)
	}

	verified := result.ExitCode != 0 && strings.Contains(result.Stdout+result.Stderr, "Not allowed to push to protected branch")
	return Result{Applied: true, SymptomVerified: verified}, nil
}
