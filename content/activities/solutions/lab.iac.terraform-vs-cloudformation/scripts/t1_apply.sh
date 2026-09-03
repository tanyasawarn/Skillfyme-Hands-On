#!/bin/bash
# lab.iac.terraform-vs-cloudformation t1: given ~/infra/main.tf's
# object_storage "artifacts" { versioning = true }, write the equivalent
# CloudFormation JSON to ~/infra/template.json with
# Resources.Artifacts.Properties.Versioning == true. Bootstraps main.tf
# (fx.terraform-bucket-example.v1 has no handler). Pure file authoring;
# no terraform binary needed.
set -uo pipefail
mkdir -p ~/infra
[ -f ~/infra/main.tf ] || cat > ~/infra/main.tf <<'TF'
resource "object_storage" "artifacts" {
  name       = "artifacts"
  versioning = true
}
TF
cat > ~/infra/template.json <<'JSON'
{
  "Resources": {
    "Artifacts": {
      "Type": "Custom::ObjectStorage",
      "Properties": {
        "Name": "artifacts",
        "Versioning": true
      }
    }
  }
}
JSON
jq -e '.Resources.Artifacts.Properties.Versioning == true' ~/infra/template.json >/dev/null
echo "template.json written"
