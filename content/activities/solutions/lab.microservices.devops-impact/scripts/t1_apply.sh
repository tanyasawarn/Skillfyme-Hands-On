#!/bin/bash
# Reference solution for lab.microservices.devops-impact task t1.
# Independent deployability: two services, each with its own
# service_version, decoupled from the other. Idempotent: overwrites the
# two descriptors every run with valid, independently-versioned JSON.
# Validators: both files exist; each has a $.service_version (SHELL_JSON).
set -euo pipefail
mkdir -p ~/deploy
cat > ~/deploy/users.deploy.json <<'JSON'
{
  "service": "users",
  "service_version": "1.4.0",
  "replicas": 2,
  "independent_deploy": true
}
JSON
cat > ~/deploy/orders.deploy.json <<'JSON'
{
  "service": "orders",
  "service_version": "2.11.3",
  "replicas": 3,
  "independent_deploy": true
}
JSON
