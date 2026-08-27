#!/bin/bash
# Reference solution for lab.git.workflow-patterns task t1 (ship a change
# via a short-lived branch, GitHub Flow). Idempotent: if the feature
# branch is already gone and main has the fix, do nothing. Validators:
# `git log main --oneline` non-empty; `git branch --list
# feature/readme-typo` is empty.
set -euo pipefail
cd ~/repo
git config user.name  >/dev/null 2>&1 || git config user.name  "content-ci"
git config user.email >/dev/null 2>&1 || git config user.email "content-ci@example.dev"

base="main"
git show-ref --verify --quiet refs/heads/main || base="$(git symbolic-ref --short HEAD)"
git checkout -q "$base"

[ -f README.md ] || { echo "# project" > README.md; git add README.md; git commit -q -m "Add README"; }

if git branch --list feature/readme-typo | grep -q feature; then
  git branch -D feature/readme-typo
fi

# Do a fresh short-lived-branch cycle so the "fix typo" commit is on main
# via a real merge, then delete the branch.
git checkout -q -b feature/readme-typo
printf '\nFixed a typo.\n' >> README.md
git add README.md
git commit -q -m "Fix README typo"
git checkout -q "$base"
git merge --no-edit feature/readme-typo >/dev/null
git branch -d feature/readme-typo
