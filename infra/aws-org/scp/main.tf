# Phase 3 1.2 — SCP framework. Creates the 6 policy documents and attaches
# them to the LearnerSandboxes OU (and the org root where appropriate).
#
# NOT applied — needs the Organization from ../main.tf (1.1). `tofu fmt`
# clean; the 6 *.json documents are JSON-validated (see verify/ and the
# CI check). `tofu validate` needs the aws provider but no credentials.

terraform {
  required_version = ">= 1.6"
  required_providers {
    aws = {
      source  = "hashicorp/aws"
      version = "~> 5.0"
    }
  }
  backend "s3" {}
}

provider "aws" {
  region = var.region
  default_tags {
    tags = {
      Project   = "practice-engine"
      Component = "aws-org-scp"
      Phase     = "3"
      ManagedBy = "opentofu"
    }
  }
}

variable "region" {
  type    = string
  default = "us-east-1"
}

variable "learner_sandboxes_ou_id" {
  type        = string
  description = "OU id from the aws-org module's `ou_ids.learner_sandboxes` output."
}

variable "org_root_id" {
  type        = string
  description = "Org root id from the aws-org module's `root_id` output."
}

variable "allowed_region_1" {
  type        = string
  description = "First permitted region (D-P3-5). e.g. us-east-1"
}

variable "allowed_region_2" {
  type        = string
  description = "Second permitted region (D-P3-5). e.g. us-west-2"
}

locals {
  # 01 is templated with the two allowed regions; the rest are static.
  region_deny_policy = templatefile("${path.module}/01-region-deny.json", {
    allowed_region_1 = var.allowed_region_1
    allowed_region_2 = var.allowed_region_2
  })

  static_policies = {
    "expensive-sku-deny"  = file("${path.module}/02-expensive-sku-deny.json")
    "org-boundary-deny"   = file("${path.module}/03-org-boundary-deny.json")
    "iam-hardening-deny"  = file("${path.module}/04-iam-hardening-deny.json")
    "public-sharing-deny" = file("${path.module}/05-public-sharing-deny.json")
    "mail-abuse-deny"     = file("${path.module}/06-mail-abuse-deny.json")
  }
}

resource "aws_organizations_policy" "region_deny" {
  name        = "practice-region-deny"
  description = "Deny all API calls outside the two allowed regions (global services excepted)."
  type        = "SERVICE_CONTROL_POLICY"
  content     = local.region_deny_policy
}

resource "aws_organizations_policy" "static" {
  for_each    = local.static_policies
  name        = "practice-${each.key}"
  description = "Phase 3 SCP: ${each.key}"
  type        = "SERVICE_CONTROL_POLICY"
  content     = each.value
}

# region-deny + expensive-sku + public-sharing + mail-abuse → LearnerSandboxes OU
resource "aws_organizations_policy_attachment" "region_deny_sandboxes" {
  policy_id = aws_organizations_policy.region_deny.id
  target_id = var.learner_sandboxes_ou_id
}

resource "aws_organizations_policy_attachment" "sandbox_scoped" {
  for_each = toset([
    "expensive-sku-deny",
    "public-sharing-deny",
    "mail-abuse-deny",
    "iam-hardening-deny",
  ])
  policy_id = aws_organizations_policy.static[each.value].id
  target_id = var.learner_sandboxes_ou_id
}

# org-boundary-deny protects security services + the nuke role everywhere →
# attach at the org root so no member account (incl. Platform) can tamper.
resource "aws_organizations_policy_attachment" "org_boundary_root" {
  policy_id = aws_organizations_policy.static["org-boundary-deny"].id
  target_id = var.org_root_id
}

output "policy_ids" {
  value = merge(
    { "region-deny" = aws_organizations_policy.region_deny.id },
    { for k, v in aws_organizations_policy.static : k => v.id }
  )
}
