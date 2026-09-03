package fixture

import (
	"context"
	"fmt"

	"github.com/tanyasawarn/skillfyme-hands-on/orchestrator/internal/k8s"
	"github.com/tanyasawarn/skillfyme-hands-on/orchestrator/internal/validation"
)

func init() {
	register("fx.git-repo-empty.v1", applyGitRepoEmpty)
	registerChecksum("fx.git-repo-empty.v1", "v1")

	register("fx.git-repo-three-commits.v1", applyGitRepoThreeCommits)
	registerChecksum("fx.git-repo-three-commits.v1", "v1")

	register("fx.git-repo-v1.2.3.v1", applyGitRepoV123)
	registerChecksum("fx.git-repo-v1.2.3.v1", "v1")

	register("fx.git-repo-conflict-setup.v1", applyGitRepoConflictSetup)
	registerChecksum("fx.git-repo-conflict-setup.v1", "v1")
}

// The git.* labs all `cd ~/repo` in their instructions, hints, and
// reference solutions. ~/repo (/home/ubuntu/repo) is the learner's HOME,
// which is a real writable directory in the workspace container
// (linux-tools:v1 ships a uid-1000 `ubuntu` user with /home/ubuntu).
// These fixtures create that repo in the state each lab's starting
// narrative assumes, via the workspace pod's own `git` (linux-tools:v1
// includes it) -- the same "run a shell command in the workspace pod"
// mechanism fx.node-app-repo.v1 uses. A real external git clone (doc
// §3.2's illustrative description) needs a content repository this
// platform doesn't host yet; a locally-initialised repo with authored
// history is the honest subset that makes the lab's golden path runnable.

// gitInitPreamble sets HOME/identity and (re)creates an empty ~/repo.
// Idempotent: every fixture below starts from a clean repo so a re-run
// (doc §5.5 "idempotent, ordered") lands the same state.
const gitInitPreamble = `set -euo pipefail
export HOME=/home/ubuntu
cd "$HOME"
rm -rf repo
mkdir repo
cd repo
git init -q -b main
git config user.name  "content-fixture"
git config user.email "fixture@practiceengine.dev"
`

func runGitFixture(ctx context.Context, provisioner *k8s.Provisioner, envID, script string) error {
	res, err := validation.ExecShell(ctx, provisioner, envID, gitInitPreamble+script, 30_000)
	if err != nil {
		return fmt.Errorf("seeding git repo: %w", err)
	}
	if res.ExitCode != 0 {
		return fmt.Errorf("seeding git repo: exit %d: %s", res.ExitCode, res.Stderr)
	}
	return nil
}

// fx.git-repo-empty.v1: an initialised repo with exactly one commit on
// `main` (a README). lab.git.basics / lab.git.workflow-patterns /
// lab.github.* / lab.gitlab.* start here. It is "empty" in the sense that
// there is no project content yet -- but it has a HEAD and a `main`
// branch, because a repo with zero commits has no branch ref at all and
// every solution that does `git checkout main` / `git switch main` fails
// with "pathspec 'main' did not match" against a truly empty repo.
func applyGitRepoEmpty(ctx context.Context, provisioner *k8s.Provisioner, envID, namespace string) error {
	return runGitFixture(ctx, provisioner, envID, `printf '# project\n' > README.md
git add README.md
git commit -q -m "Initial commit"
echo "seeded: repo at ~/repo with one commit on main"
`)
}

// fx.git-repo-three-commits.v1: linear history of three commits on main.
// lab.git.internals inspects tree/commit objects of this history.
func applyGitRepoThreeCommits(ctx context.Context, provisioner *k8s.Provisioner, envID, namespace string) error {
	return runGitFixture(ctx, provisioner, envID, `printf 'line 1\n' > file.txt
git add file.txt
git commit -q -m "First commit"
printf 'line 1\nline 2\n' > file.txt
git commit -q -am "Second commit"
printf 'line 1\nline 2\nline 3\n' > file.txt
git commit -q -am "Third commit"
echo "seeded: 3-commit linear history"
`)
}

// fx.git-repo-v1.2.3.v1: history with an annotated v1.2.3 tag at HEAD.
// lab.git.release-management starts from v1.2.3 and asks the learner to
// cut the next release tag.
func applyGitRepoV123(ctx context.Context, provisioner *k8s.Provisioner, envID, namespace string) error {
	return runGitFixture(ctx, provisioner, envID, `printf 'v1\n' > app.txt
git add app.txt
git commit -q -m "Initial release"
printf 'v1\nfeature a\n' > app.txt
git commit -q -am "Add feature a"
git tag -a v1.2.3 -m "Release v1.2.3"
echo "seeded: history tagged v1.2.3 (annotated) at HEAD"
`)
}

// fx.git-repo-conflict-setup.v1: main and a `conflicting-change` branch
// that have both edited the same line, so a later merge conflicts.
// lab.git.branching-strategies' conflict-resolution task starts here.
//
// Branch name is deliberately NOT `feature` -- that lab's other task
// creates `feature/login`, and git refuses to create a ref under a path
// that already exists as a ref ("'refs/heads/feature' exists; cannot
// create 'refs/heads/feature/login'"). `conflicting-change` has no such
// collision.
func applyGitRepoConflictSetup(ctx context.Context, provisioner *k8s.Provisioner, envID, namespace string) error {
	return runGitFixture(ctx, provisioner, envID, `printf '# project\n' > README.md
git add README.md
git commit -q -m "Initial commit"
printf 'shared line\n' > conflict.txt
git add conflict.txt
git commit -q -m "Add conflict.txt"
git checkout -q -b conflicting-change
printf 'change from the conflicting-change branch\n' > conflict.txt
git commit -q -am "conflicting-change edits the shared line"
git checkout -q main
printf 'change from main\n' > conflict.txt
git commit -q -am "main edits the shared line"
echo "seeded: main vs conflicting-change both edited conflict.txt (merge will conflict)"
`)
}
