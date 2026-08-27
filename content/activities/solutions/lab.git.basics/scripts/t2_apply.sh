#!/bin/bash
# Reference solution for lab.git.basics task t2 (revert a bad commit).
# Idempotent: only performs the delete+revert cycle if a revert commit
# isn't already present. Validators: README.md exists; `git log --oneline`
# contains "revert" (case-insensitive).
set -euo pipefail
cd ~/repo
git config user.name  >/dev/null 2>&1 || git config user.name  "content-ci"
git config user.email >/dev/null 2>&1 || git config user.email "content-ci@example.dev"

if git log --oneline | grep -qi revert; then
  # Already reverted; make sure README.md is present (it should be).
  [ -f README.md ] || git checkout -- README.md
  exit 0
fi

# Ensure README.md exists to delete.
[ -f README.md ] || { echo "# repo" > README.md; git add README.md; git commit -q -m "Add README"; }

git rm -q README.md
git commit -q -m "Remove README (mistake)"
bad="$(git rev-parse HEAD)"
git revert --no-edit "$bad"
