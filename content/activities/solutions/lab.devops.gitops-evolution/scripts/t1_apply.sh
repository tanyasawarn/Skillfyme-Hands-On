#!/bin/bash
# Reference solution for lab.devops.gitops-evolution task t1 (author a
# declarative manifest). Idempotent: always rewrites ~/app/app.yaml.
# Validators: file exists; valid YAML; contains all of
# "name: sample-app", "replicas: 2", "image: sample-app:v1".
set -euo pipefail
mkdir -p ~/app
cat > ~/app/app.yaml <<'EOF'
name: sample-app
replicas: 2
image: sample-app:v1
EOF
