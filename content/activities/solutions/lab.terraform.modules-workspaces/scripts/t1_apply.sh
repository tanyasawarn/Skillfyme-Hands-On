#!/bin/bash
# lab.terraform.modules-workspaces t1: ~/infra/modules/file/main.tf
# defining a module with a `filename` variable and a local_file resource.
# Pure file authoring; the t1 validators only read the .tf file.
set -uo pipefail
mkdir -p ~/infra/modules/file
cat > ~/infra/modules/file/main.tf <<'TF'
variable "filename" {
  type = string
}

resource "local_file" "this" {
  filename = var.filename
  content  = "module output"
}
TF
echo "module main.tf written"
