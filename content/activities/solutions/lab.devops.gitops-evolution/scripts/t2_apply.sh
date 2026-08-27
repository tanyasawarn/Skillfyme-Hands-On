#!/bin/bash
# Reference solution for lab.devops.gitops-evolution task t2 (commit the
# manifest as the source of truth). Idempotent: init only if needed,
# commit app.yaml only if it has no history yet. Validators:
# `git rev-parse --is-inside-work-tree` in ~/app; `git log --oneline --
# app.yaml` non-empty.
set -euo pipefail
cd ~/app
git rev-parse --is-inside-work-tree >/dev/null 2>&1 || git init -q
git config user.name  >/dev/null 2>&1 || git config user.name  "content-ci"
git config user.email >/dev/null 2>&1 || git config user.email "content-ci@example.dev"

# t1 must have created app.yaml; recreate defensively.
[ -f app.yaml ] || printf 'name: sample-app\nreplicas: 2\nimage: sample-app:v1\n' > app.yaml

if ! git log --oneline -- app.yaml | grep -q .; then
  git add app.yaml
  git commit -q -m "Add declarative app manifest"
fi
