# Phase 3 1.1 — AWS Organization + OU scaffold + centralised security
# services, applied in the PAYER (management) account.
#
# NOT applied — creating an Organization is near-irreversible and needs
# real payer-account admin credentials. `tofu validate` + `tofu fmt` are
# clean; `tofu plan` needs credentials.
#
# Produces:
#   - the Organization (all features enabled)
#   - OUs: Platform / ContentCI / LearnerSandboxes
#   - a delegated-admin-ready org CloudTrail (all regions, to an S3 bucket
#     in the payer account, SCP-undeletable from members via 1.2)
#   - AWS Config recorder + delivery channel (payer account)
#   - GuardDuty detector (payer account; member accounts auto-enrolled)
#
# The account-vending itself (`aws organizations create-account` into
# LearnerSandboxes) is done imperatively by the orchestrator's Account
# Pool Manager (Stage 2.4), not here — this file only builds the org
# structure those accounts land in.

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
      Component = "aws-org"
      Phase     = "3"
      ManagedBy = "opentofu"
    }
  }
}

variable "region" {
  type        = string
  description = "Home region for org-level resources (CloudTrail bucket, Config)."
  default     = "us-east-1"
}

variable "cloudtrail_bucket_name" {
  type        = string
  description = "Globally-unique S3 bucket name for the org trail."
}

# --- Organization ----------------------------------------------------------
resource "aws_organizations_organization" "this" {
  feature_set = "ALL"

  aws_service_access_principals = [
    "cloudtrail.amazonaws.com",
    "config.amazonaws.com",
    "guardduty.amazonaws.com",
    "sso.amazonaws.com",
    "account.amazonaws.com",
  ]

  enabled_policy_types = [
    "SERVICE_CONTROL_POLICY",
    "TAG_POLICY",
  ]
}

resource "aws_organizations_organizational_unit" "platform" {
  name      = "Platform"
  parent_id = aws_organizations_organization.this.roots[0].id
}

resource "aws_organizations_organizational_unit" "content_ci" {
  name      = "ContentCI"
  parent_id = aws_organizations_organization.this.roots[0].id
}

resource "aws_organizations_organizational_unit" "learner_sandboxes" {
  name      = "LearnerSandboxes"
  parent_id = aws_organizations_organization.this.roots[0].id
}

# --- Centralised CloudTrail (org trail) -----------------------------------
resource "aws_s3_bucket" "cloudtrail" {
  bucket = var.cloudtrail_bucket_name
}

resource "aws_s3_bucket_public_access_block" "cloudtrail" {
  bucket                  = aws_s3_bucket.cloudtrail.id
  block_public_acls       = true
  block_public_policy     = true
  ignore_public_acls      = true
  restrict_public_buckets = true
}

resource "aws_s3_bucket_versioning" "cloudtrail" {
  bucket = aws_s3_bucket.cloudtrail.id
  versioning_configuration {
    status = "Enabled"
  }
}

data "aws_caller_identity" "current" {}

resource "aws_s3_bucket_policy" "cloudtrail" {
  bucket = aws_s3_bucket.cloudtrail.id
  policy = jsonencode({
    Version = "2012-10-17"
    Statement = [
      {
        Sid       = "AWSCloudTrailAclCheck"
        Effect    = "Allow"
        Principal = { Service = "cloudtrail.amazonaws.com" }
        Action    = "s3:GetBucketAcl"
        Resource  = aws_s3_bucket.cloudtrail.arn
      },
      {
        Sid       = "AWSCloudTrailWrite"
        Effect    = "Allow"
        Principal = { Service = "cloudtrail.amazonaws.com" }
        Action    = "s3:PutObject"
        Resource  = "${aws_s3_bucket.cloudtrail.arn}/AWSLogs/${aws_organizations_organization.this.id}/*"
        Condition = {
          StringEquals = { "s3:x-amz-acl" = "bucket-owner-full-control" }
        }
      },
    ]
  })
}

resource "aws_cloudtrail" "org" {
  name                          = "practice-engine-org-trail"
  s3_bucket_name                = aws_s3_bucket.cloudtrail.id
  is_organization_trail         = true
  is_multi_region_trail         = true
  include_global_service_events = true
  enable_log_file_validation    = true

  depends_on = [aws_s3_bucket_policy.cloudtrail]
}

# --- AWS Config (payer account recorder) ---------------------------------
resource "aws_iam_role" "config" {
  name = "practice-engine-config"
  assume_role_policy = jsonencode({
    Version = "2012-10-17"
    Statement = [{
      Effect    = "Allow"
      Principal = { Service = "config.amazonaws.com" }
      Action    = "sts:AssumeRole"
    }]
  })
}

resource "aws_iam_role_policy_attachment" "config" {
  role       = aws_iam_role.config.name
  policy_arn = "arn:aws:iam::aws:policy/service-role/AWS_ConfigRole"
}

resource "aws_config_configuration_recorder" "this" {
  name     = "practice-engine"
  role_arn = aws_iam_role.config.arn
  recording_group {
    all_supported                 = true
    include_global_resource_types = true
  }
}

resource "aws_config_delivery_channel" "this" {
  name           = "practice-engine"
  s3_bucket_name = aws_s3_bucket.cloudtrail.id
  depends_on     = [aws_config_configuration_recorder.this]
}

resource "aws_config_configuration_recorder_status" "this" {
  name       = aws_config_configuration_recorder.this.name
  is_enabled = true
  depends_on = [aws_config_delivery_channel.this]
}

# --- GuardDuty (payer account; org auto-enable) -------------------------
resource "aws_guardduty_detector" "this" {
  enable = true
}

resource "aws_guardduty_organization_configuration" "this" {
  auto_enable_organization_members = "ALL"
  detector_id                      = aws_guardduty_detector.this.id
}

# --- outputs -----------------------------------------------------------
output "organization_id" {
  value = aws_organizations_organization.this.id
}

output "ou_ids" {
  value = {
    platform          = aws_organizations_organizational_unit.platform.id
    content_ci        = aws_organizations_organizational_unit.content_ci.id
    learner_sandboxes = aws_organizations_organizational_unit.learner_sandboxes.id
  }
}

output "root_id" {
  value = aws_organizations_organization.this.roots[0].id
}
