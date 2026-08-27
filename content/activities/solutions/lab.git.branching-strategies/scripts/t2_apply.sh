#!/bin/bash
# Reference solution for lab.git.branching-strategies task t2 (resolve a
# merge conflict). Idempotent: if main already has a merge commit and
# app.txt has no conflict markers, do nothing. Otherwise perform the
# conflicting merge and resolve it keeping both sides.
# Validators: no conflict markers in ~/repo/app.txt; `git log main
# --oneline --merges` is non-empty.
set -euo pipefail
cd ~/repo
git config user.name  >/dev/null 2>&1 || git config user.name  "content-ci"
git config user.email >/dev/null 2>&1 || git config user.email "content-ci@example.dev"

base="main"
git show-ref --verify --quiet refs/heads/main || base="$(git symbolic-ref --short HEAD)"

if git log "$base" --oneline --merges | grep -q . \
   && ! grep -qE '^(<<<<<<<|=======|>>>>>>>)' app.txt; then
  exit 0
fi

git checkout -q "$base"

# Ensure feature/login exists with a divergent last line (t1 normally
# does this; recreate defensively).
if ! git show-ref --verify --quiet refs/heads/feature/login; then
  git branch feature/login
  git checkout -q feature/login
  echo "feature side" >> app.txt
  git add app.txt && git commit -q -m "feature change"
  git checkout -q "$base"
fi

# Make main conflict on the same last line.
echo "main side" >> app.txt
git add app.txt && git commit -q -m "main change" || true

if ! git merge --no-edit feature/login >/dev/null 2>&1; then
  # Resolve: keep both sides, drop the markers.
  sed -i.bak -E '/^(<<<<<<<|=======|>>>>>>>)/d' app.txt && rm -f app.txt.bak
  git add app.txt
  git commit -q --no-edit
fi
