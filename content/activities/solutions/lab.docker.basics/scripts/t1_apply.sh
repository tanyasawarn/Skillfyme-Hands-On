#!/bin/bash
# Reference solution for lab.docker.basics task t1 (Author a Dockerfile).
# Idempotent: always overwrites ~/app/Dockerfile with a valid one.
# Validators check: file exists, contains "FROM alpine", contains both
# "COPY" and "CMD".
set -euo pipefail
mkdir -p ~/app
[ -f ~/app/index.html ] || echo '<h1>static site</h1>' > ~/app/index.html
cat > ~/app/Dockerfile <<'EOF'
FROM alpine:3.19
COPY index.html /srv/index.html
CMD ["httpd", "-f", "-h", "/srv"]
EOF
