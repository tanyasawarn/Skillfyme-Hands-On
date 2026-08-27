#!/bin/bash
# Reference solution for lab.docker.networking task t2 (name-based DNS
# between containers). Idempotent: remove any prior server/client, then
# recreate both on app-net. Validators check: exactly 2 containers on
# app-net, and `docker exec client getent hosts server` exits 0.
set -euo pipefail

# t1's network must exist; create defensively so this script also works
# if t2 is applied in isolation.
docker network inspect app-net >/dev/null 2>&1 || docker network create app-net

docker rm -f server client >/dev/null 2>&1 || true

docker run -d --name server --network app-net httpd:alpine >/dev/null
docker run -d --name client --network app-net alpine sleep infinity >/dev/null

# Wait for Docker's embedded DNS to publish the 'server' record.
for _ in $(seq 1 15); do
  if docker exec client getent hosts server >/dev/null 2>&1; then
    exit 0
  fi
  sleep 1
done
echo "client still cannot resolve 'server' after 15s" >&2
exit 1
