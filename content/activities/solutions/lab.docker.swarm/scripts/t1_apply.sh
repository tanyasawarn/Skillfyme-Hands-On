#!/bin/bash
# Reference solution for lab.docker.swarm task t1 (Initialise the Swarm).
# Idempotent: `docker swarm init` errors if already in a swarm, so guard
# on the current state. Validator checks:
# `docker info --format '{{.Swarm.LocalNodeState}}'` contains "active".
set -euo pipefail
state="$(docker info --format '{{.Swarm.LocalNodeState}}' 2>/dev/null || echo inactive)"
if [ "$state" != "active" ]; then
  # --advertise-addr keeps init deterministic on a multi-NIC host.
  docker swarm init --advertise-addr 127.0.0.1 >/dev/null
fi
