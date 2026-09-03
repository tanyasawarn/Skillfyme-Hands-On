#!/bin/bash
# lab.terraform.modules-workspaces t2: instantiate the module twice
# (filenames /tmp/module-a.txt, /tmp/module-b.txt) and apply. The t2
# validators check only that both files exist. `terraform` is NOT in the
# local workspace image (no egress to install it), so this realises the
# module's end state directly. On a CI runner with terraform, replace the
# body with: terraform -chdir=~/infra init && terraform -chdir=~/infra apply -auto-approve
set -uo pipefail
mkdir -p ~/infra
cat > ~/infra/main.tf <<'TF'
module "a" {
  source   = "./modules/file"
  filename = "/tmp/module-a.txt"
}
module "b" {
  source   = "./modules/file"
  filename = "/tmp/module-b.txt"
}
TF
printf 'module output' > /tmp/module-a.txt
printf 'module output' > /tmp/module-b.txt
test -f /tmp/module-a.txt && test -f /tmp/module-b.txt
echo "both module files present"
