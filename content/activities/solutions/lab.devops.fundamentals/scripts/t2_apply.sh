#!/bin/bash
# Reference solution for lab.devops.fundamentals task t2.
# Idempotent: safe to re-run, always leaves the global git identity set.
set -euo pipefail
git config --global user.name "devops-learner"
git config --global user.email "learner@example.dev"
