#!/bin/bash
# Reference solution for lab.git.release-management task t1 (tag a
# backward-compatible feature release). v1.2.3 -> feature bump -> v1.3.0,
# annotated. Idempotent: create the tag only if absent. Validators:
# `git tag --list v1.3.0` shows it; `git cat-file -t v1.3.0` == "tag"
# (annotated, not lightweight).
set -euo pipefail
cd ~/repo
git config user.name  >/dev/null 2>&1 || git config user.name  "content-ci"
git config user.email >/dev/null 2>&1 || git config user.email "content-ci@example.dev"

if git tag --list v1.3.0 | grep -q v1.3.0; then
  # Ensure it's annotated; if a prior lightweight tag exists, replace it.
  if [ "$(git cat-file -t v1.3.0)" != "tag" ]; then
    git tag -d v1.3.0
    git tag -a v1.3.0 -m "Release v1.3.0: backward-compatible feature"
  fi
  exit 0
fi
git tag -a v1.3.0 -m "Release v1.3.0: backward-compatible feature"
