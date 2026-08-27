#!/bin/bash
# Reference solution for lab.git.branching-strategies task t1
# (feature branch + commit on it). Idempotent: create the branch/commit
# only if missing. Validators: `git branch --list feature/login` shows it;
# `git log feature/login --oneline` is non-empty.
set -euo pipefail
cd ~/repo
git config user.name  >/dev/null 2>&1 || git config user.name  "content-ci"
git config user.email >/dev/null 2>&1 || git config user.email "content-ci@example.dev"

# Make sure there is at least one commit and an app.txt on the base branch.
if ! git rev-parse HEAD >/dev/null 2>&1; then
  echo "base" > app.txt
  git add app.txt
  git commit -q -m "Initial"
fi
base="$(git symbolic-ref --short HEAD)"

git show-ref --verify --quiet refs/heads/feature/login || git branch feature/login "$base"

if [ "$(git log feature/login --oneline | wc -l | tr -d ' ')" -eq \
     "$(git log "$base" --oneline | wc -l | tr -d ' ')" ]; then
  git checkout -q feature/login
  echo "feature login work" >> app.txt
  git add app.txt
  git commit -q -m "Add login feature"
  git checkout -q "$base"
fi
