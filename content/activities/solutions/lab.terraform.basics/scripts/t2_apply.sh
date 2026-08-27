#!/bin/bash
# Reference solution for lab.terraform.basics task t2 (init + apply).
# Idempotent: re-running init/apply on a converged config is a no-op.
# Depends on t1 having written ~/infra/main.tf; recreate a minimal one
# defensively so t2 also works if applied in isolation.
# Validator: ~/infra/terraform.tfstate exists.
set -euo pipefail
mkdir -p ~/infra
cd ~/infra

if [ ! -f main.tf ]; then
  cat > main.tf <<'EOF'
terraform {
  required_providers {
    local = { source = "hashicorp/local" }
  }
}
provider "local" {}
variable "content" {
  type    = string
  default = "hello from terraform"
}
resource "local_file" "demo" {
  filename = "/tmp/terraform-demo.txt"
  content  = var.content
}
EOF
fi

terraform init -input=false -no-color >/dev/null
terraform apply -auto-approve -input=false -no-color >/dev/null
