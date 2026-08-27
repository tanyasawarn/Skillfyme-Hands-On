#!/bin/bash
# Reference solution for lab.git.basics task t1 (init + two commits).
# Idempotent: only creates the repo/commits if the ">= 2 commits"
# condition isn't already met. Validator: `git log --oneline | wc -l` >= 2.
set -euo pipefail
mkdir -p ~/repo
cd ~/repo

git rev-parse --is-inside-work-tree >/dev/null 2>&1 || git init -q

# A bare `git config` on the box may be missing in a fresh env; set a
# local identity so commits succeed regardless.
git config user.name  >/dev/null 2>&1 || git config user.name  "content-ci"
git config user.email >/dev/null 2>&1 || git config user.email "content-ci@example.dev"

count="$(git log --oneline 2>/dev/null | wc -l | tr -d ' ')"
if [ "$count" -lt 2 ]; then
  [ -f README.md ] || echo "# repo" > README.md
  git add README.md
  git commit -q -m "Add README" || true
  [ -f notes.md ] || echo "notes" > notes.md
  git add notes.md
  git commit -q -m "Add notes" || true
fi
