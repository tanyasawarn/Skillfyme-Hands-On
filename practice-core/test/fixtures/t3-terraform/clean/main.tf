# Phase 3 1.8 fixture — a clean, drift-free config with a LOCAL backend.
# null_resource only, so `terraform init && apply` need no cloud creds.
terraform {
  required_version = ">= 1.0"
}

resource "null_resource" "app" {
  triggers = {
    version = "v1"
  }
}

resource "null_resource" "db" {
  triggers = {
    engine = "postgres"
  }
}
