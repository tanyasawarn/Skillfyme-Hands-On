#!/bin/bash
# Reference solution for lab.docker.networking task t1 (isolated bridge network).
# Idempotent: create only if absent. Validator checks:
# `docker network inspect app-net --format '{{.Driver}}'` -> "bridge".
set -euo pipefail
if ! docker network inspect app-net >/dev/null 2>&1; then
  docker network create app-net
fi
