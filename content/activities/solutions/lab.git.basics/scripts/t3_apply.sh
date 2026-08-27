#!/bin/bash
# Reference solution for lab.git.basics task t3 (ignore generated files).
# Idempotent: append the ignore rule only if not already present.
# Validator: build.log does NOT appear in `git status --porcelain`.
set -euo pipefail
cd ~/repo
[ -f build.log ] || echo "generated $(date +%s)" > build.log
if [ ! -f .gitignore ] || ! grep -qx 'build.log' .gitignore; then
  echo 'build.log' >> .gitignore
fi
