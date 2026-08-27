#!/bin/bash
# Reference solution for lab.terraform.basics task t1 (write + apply a
# local_file resource, content via a variable). Idempotent: rewrites
# main.tf, then `terraform init` + `apply` (both no-ops when converged).
# Validators: main.tf exists and contains "variable"; /tmp/terraform-demo.txt
# exists and contains "hello from terraform".
set -euo pipefail
mkdir -p ~/infra
cd ~/infra

cat > main.tf <<'EOF'
terraform {
  required_providers {
    local = {
      source = "hashicorp/local"
    }
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

terraform init -input=false -no-color >/dev/null
terraform apply -auto-approve -input=false -no-color >/dev/null
