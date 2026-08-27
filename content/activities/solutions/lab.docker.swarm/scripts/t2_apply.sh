#!/bin/bash
# Reference solution for lab.docker.swarm task t2 (deploy + scale a
# replicated service). Idempotent: create the service only if absent,
# then always (re)scale to 4. Validators check: `docker service inspect
# web` exits 0, and its replica count is 4.
set -euo pipefail

# t1's swarm must be active; init defensively for isolated runs.
state="$(docker info --format '{{.Swarm.LocalNodeState}}' 2>/dev/null || echo inactive)"
[ "$state" = "active" ] || docker swarm init --advertise-addr 127.0.0.1 >/dev/null

if ! docker service inspect web >/dev/null 2>&1; then
  docker service create --name web --replicas 2 httpd:alpine >/dev/null
fi
docker service scale web=4 >/dev/null
