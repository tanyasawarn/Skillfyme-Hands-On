#!/bin/bash
# Reference solution for lab.git.internals task t2 (squash three commits
# into one). Idempotent: if the repo already has exactly 2 commits, do
# nothing. Otherwise squash everything after the first commit into one,
# non-interactively. Validator: `git log --oneline | wc -l` == 2.
set -euo pipefail
cd ~/repo
git config user.name  >/dev/null 2>&1 || git config user.name  "content-ci"
git config user.email >/dev/null 2>&1 || git config user.email "content-ci@example.dev"

total="$(git log --oneline | wc -l | tr -d ' ')"
if [ "$total" -eq 2 ]; then
  exit 0
fi
if [ "$total" -lt 2 ]; then
  echo "repo has fewer than 2 commits; fixture missing" >&2
  exit 1
fi

first="$(git rev-list --max-parents=0 HEAD | tail -1)"
# Soft-reset to just after the initial commit, then one commit captures
# the combined tree of all the squashed work -- same end state as an
# interactive rebase that squashes every commit after the first.
git reset -q --soft "$first"
git commit -q -m "Squashed feature work"
