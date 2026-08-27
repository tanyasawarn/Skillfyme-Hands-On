#!/bin/bash
# Reference solution for lab.iac.fundamentals task t1 (define a resource
# declaratively). Idempotent: always rewrites ~/infra/storage.json.
# Validators: file exists; valid JSON; $.resource_type == "object_storage".
set -euo pipefail
mkdir -p ~/infra
cat > ~/infra/storage.json <<'EOF'
{
  "resource_type": "object_storage",
  "name": "app-artifacts",
  "versioning_enabled": true
}
EOF
