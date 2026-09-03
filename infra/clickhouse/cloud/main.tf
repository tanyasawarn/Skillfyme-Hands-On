# Phase 3 1.5 — ClickHouse Cloud dev-tier service (D-P3-6). NOT applied —
# needs a ClickHouse Cloud org + API key. `tofu validate` + `tofu fmt`
# clean.
#
# After apply: run cloud/apply-schema.sh to load ../compose/init/01-schema.sql
# into the service (the same schema the local deploy uses).

terraform {
  required_version = ">= 1.6"
  required_providers {
    clickhouse = {
      source  = "ClickHouse/clickhouse"
      version = "~> 3.0"
    }
  }
  backend "s3" {}
}

# Credentials from CLICKHOUSE_CLOUD_TOKEN_KEY / CLICKHOUSE_CLOUD_TOKEN_SECRET
# (or a ~/.clickhouse-cloud credentials file). The provider block stays
# empty so no secret is committed.
provider "clickhouse" {}

variable "organization_id" {
  type        = string
  description = "ClickHouse Cloud organization id."
}

variable "region" {
  type        = string
  description = "Cloud region for the service (align with the practice cluster region — D-P3-5)."
  default     = "us-east-1"
}

variable "cloud_provider" {
  type    = string
  default = "aws"
}

variable "ip_allowlist" {
  type        = list(object({ source = string, description = string }))
  description = "CIDRs allowed to reach the service (the practice cluster's NAT egress)."
  default     = []
}

variable "service_password_hash" {
  type        = string
  sensitive   = true
  description = "SHA-256 hash of the service's default-user password (echo -n '<pw>' | sha256sum). Supplied at apply time; never committed."
}

resource "clickhouse_service" "analytics" {
  name           = "practice-engine-analytics"
  cloud_provider = var.cloud_provider
  region         = var.region
  tier           = "development" # dev tier for Phase 3; scale up / go self-hosted in Phase 5

  idle_scaling          = true
  idle_timeout_minutes  = 15
  min_replica_memory_gb = 8
  max_replica_memory_gb = 48

  ip_access = length(var.ip_allowlist) > 0 ? var.ip_allowlist : [
    { source = "0.0.0.0/0", description = "PLACEHOLDER — replace with the practice cluster egress CIDR before apply" }
  ]

  password_hash = var.service_password_hash
}

output "service_endpoint_https" {
  value = one([
    for e in clickhouse_service.analytics.endpoints : e.host
    if e.protocol == "https"
  ])
}

output "service_id" {
  value = clickhouse_service.analytics.id
}
