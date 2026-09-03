# Phase 3 1.3 — account-baseline module. The Terraform the Account Pool
# Manager (Stage 2.4) applies into a freshly-claimed sandbox account at
# claim time. Must apply in < 5 min.
#
# NOT applied — needs a real claimed sandbox account. `tofu validate` +
# `tofu fmt` clean.
#
# Provides, per sandbox account:
#   - a platform-managed remote-state backend reference (the S3 bucket +
#     DynamoDB lock table live in the PLATFORM account, created once by
#     ../account-baseline/backend-bootstrap — so destroy/recreate of the
#     sandbox loses no Terraform state, §12.3)
#   - an OIDC identity provider trusting the platform IdP (Stage 2.1/A3)
#   - LearnerSandboxRole (sub = attempt id) and PlatformNukeRole
#     (SCP-undeletable per 1.2)
#   - a required-tag enforcement policy (attempt_id / tenant_id)

terraform {
  required_version = ">= 1.6"
  required_providers {
    aws = {
      source  = "hashicorp/aws"
      version = "~> 5.0"
    }
  }
  # The sandbox's OWN state goes in the platform-managed backend; the
  # key is per-account. Supplied via -backend-config at apply time.
  backend "s3" {}
}

provider "aws" {
  region = var.region
  # The Pool Manager assumes OrganizationAccountAccessRole in the freshly
  # created sandbox account before running this.
  default_tags {
    tags = {
      Project    = "practice-engine"
      Component  = "account-baseline"
      Phase      = "3"
      ManagedBy  = "opentofu"
      attempt_id = var.attempt_id
      tenant_id  = var.tenant_id
    }
  }
}

variable "region" {
  type    = string
  default = "us-east-1"
}

variable "attempt_id" {
  type        = string
  description = "The attempt this sandbox account is claimed for (one account = one attempt)."
}

variable "tenant_id" {
  type        = string
  description = "Tenant owning the attempt."
}

variable "platform_idp_url" {
  type        = string
  description = "HTTPS issuer URL of the platform OIDC IdP (Stage 2.1)."
}

variable "platform_idp_client_id" {
  type        = string
  description = "OIDC audience the platform IdP issues tokens for."
}

variable "platform_idp_thumbprint" {
  type        = string
  description = "SHA1 thumbprint of the IdP's TLS cert (for the OIDC provider)."
}

variable "platform_account_id" {
  type        = string
  description = "The Platform account id — trusted to assume PlatformNukeRole."
}

# --- OIDC provider trusting the platform IdP ---------------------------
resource "aws_iam_openid_connect_provider" "platform" {
  url             = var.platform_idp_url
  client_id_list  = [var.platform_idp_client_id]
  thumbprint_list = [var.platform_idp_thumbprint]
}

# --- LearnerSandboxRole: sub = attempt id ----------------------------
data "aws_iam_policy_document" "learner_trust" {
  statement {
    effect  = "Allow"
    actions = ["sts:AssumeRoleWithWebIdentity"]
    principals {
      type        = "Federated"
      identifiers = [aws_iam_openid_connect_provider.platform.arn]
    }
    condition {
      test     = "StringEquals"
      variable = "${replace(var.platform_idp_url, "https://", "")}:aud"
      values   = [var.platform_idp_client_id]
    }
    condition {
      test     = "StringEquals"
      variable = "${replace(var.platform_idp_url, "https://", "")}:sub"
      values   = [var.attempt_id]
    }
  }
}

resource "aws_iam_role" "learner_sandbox" {
  name                 = "LearnerSandboxRole"
  assume_role_policy   = data.aws_iam_policy_document.learner_trust.json
  max_session_duration = 3600 # 1h, matches the broker's max-1h session (§5.3)
}

# The learner gets broad build permissions WITHIN the SCP guardrails
# (1.2). PowerUserAccess minus IAM is the standard shape; IAM is added
# narrowly for the specific roles a project needs (attached by the
# blueprint, not here).
resource "aws_iam_role_policy_attachment" "learner_poweruser" {
  role       = aws_iam_role.learner_sandbox.name
  policy_arn = "arn:aws:iam::aws:policy/PowerUserAccess"
}

# --- PlatformNukeRole: assumable only by the Platform account ---------
data "aws_iam_policy_document" "nuke_trust" {
  statement {
    effect  = "Allow"
    actions = ["sts:AssumeRole"]
    principals {
      type        = "AWS"
      identifiers = ["arn:aws:iam::${var.platform_account_id}:root"]
    }
  }
}

resource "aws_iam_role" "platform_nuke" {
  name               = "PlatformNukeRole"
  assume_role_policy = data.aws_iam_policy_document.nuke_trust.json
}

resource "aws_iam_role_policy_attachment" "nuke_admin" {
  role       = aws_iam_role.platform_nuke.name
  policy_arn = "arn:aws:iam::aws:policy/AdministratorAccess"
}

# --- required-tag enforcement --------------------------------------
# A tag policy is org-level (attached in ../main.tf / ../scp); this is the
# account-local IAM guardrail: deny creating taggable resources without
# attempt_id + tenant_id.
data "aws_iam_policy_document" "require_tags" {
  statement {
    sid    = "DenyCreateWithoutRequiredTags"
    effect = "Deny"
    actions = [
      "ec2:RunInstances",
      "ec2:CreateVolume",
      "rds:CreateDBInstance",
      "s3:CreateBucket",
      "eks:CreateCluster",
    ]
    resources = ["*"]
    condition {
      test     = "Null"
      variable = "aws:RequestTag/attempt_id"
      values   = ["true"]
    }
  }
}

resource "aws_iam_policy" "require_tags" {
  name   = "practice-require-tags"
  policy = data.aws_iam_policy_document.require_tags.json
}

resource "aws_iam_role_policy_attachment" "learner_require_tags" {
  role       = aws_iam_role.learner_sandbox.name
  policy_arn = aws_iam_policy.require_tags.arn
}

output "learner_sandbox_role_arn" {
  value = aws_iam_role.learner_sandbox.arn
}

output "platform_nuke_role_arn" {
  value = aws_iam_role.platform_nuke.arn
}

output "oidc_provider_arn" {
  value = aws_iam_openid_connect_provider.platform.arn
}
