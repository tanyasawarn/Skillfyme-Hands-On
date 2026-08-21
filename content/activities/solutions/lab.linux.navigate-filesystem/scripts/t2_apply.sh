#!/bin/bash
# Reference solution for lab.linux.navigate-filesystem task t2.
# Idempotent: safe to re-run.
set -euo pipefail
mkdir -p ~/project
touch ~/project/deploy.sh
chmod 700 ~/project/deploy.sh
