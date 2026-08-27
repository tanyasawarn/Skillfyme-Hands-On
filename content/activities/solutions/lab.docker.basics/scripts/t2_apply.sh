#!/bin/bash
# Reference solution for lab.docker.basics task t2 (Build a tagged image).
# Idempotent: `docker build` is a no-op re-run when nothing changed; the
# tag is reapplied regardless. Validator checks: `docker image inspect
# static-site:v1` exits 0.
set -euo pipefail
cd ~/app
docker build -t static-site:v1 .
