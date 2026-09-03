# Phase 3 1.4 / B9 — the AWS side of the platform Git host, applied into
# the PLATFORM account (Stage 1.1), never a learner sandbox. Provides:
#   - an RDS PostgreSQL instance for Forgejo's metadata
#   - an EBS gp3 volume claim story is handled by the k8s StorageClass;
#     this file provisions the RDS + a Route53 record + a security group.
#
# NOT applied — needs real Platform-account credentials. `tofu validate`
# and `tofu fmt` are clean. `tofu plan` needs credentials.

terraform {
  required_version = ">= 1.6"
  required_providers {
    aws = {
      source  = "hashicorp/aws"
      version = "~> 5.0"
    }
    random = {
      source  = "hashicorp/random"
      version = "~> 3.6"
    }
  }

  # Platform-managed remote state (Stage 1.3 provisions the bucket/lock
  # table). Left partial on purpose — `tofu init -backend-config=...`
  # supplies bucket/key/region at apply time.
  backend "s3" {}
}

provider "aws" {
  region = var.region
  # profile / role assumption is supplied via the standard AWS env /
  # shared-config chain — this module does not hardcode a profile.
  default_tags {
    tags = {
      Project   = "practice-engine"
      Component = "git-hosting"
      Phase     = "3"
      ManagedBy = "opentofu"
    }
  }
}

variable "region" {
  type        = string
  description = "AWS region for the platform Git host resources."
  default     = "us-east-1"
}

variable "vpc_id" {
  type        = string
  description = "Platform VPC the RDS instance and k8s cluster live in."
}

variable "db_subnet_group_name" {
  type        = string
  description = "Existing DB subnet group (private subnets) in the platform VPC."
}

variable "cluster_security_group_id" {
  type        = string
  description = "Security group of the k8s nodes that will reach Forgejo's DB."
}

variable "route53_zone_id" {
  type        = string
  description = "Hosted zone for the platform-internal domain."
}

variable "git_hostname" {
  type        = string
  description = "FQDN for the Git host (must match helm values ingress.host)."
  default     = "git.platform.internal"
}

variable "alb_dns_name" {
  type        = string
  description = "DNS name of the ingress controller's load balancer (the Route53 record targets this)."
}

variable "alb_zone_id" {
  type        = string
  description = "Hosted-zone id of the ingress LB (for the alias record)."
}

resource "random_password" "forgejo_db" {
  length  = 32
  special = false
}

resource "aws_security_group" "forgejo_db" {
  name_prefix = "practice-forgejo-db-"
  description = "Forgejo metadata DB — reachable only from the k8s nodes"
  vpc_id      = var.vpc_id

  ingress {
    description     = "PostgreSQL from the practice cluster nodes"
    from_port       = 5432
    to_port         = 5432
    protocol        = "tcp"
    security_groups = [var.cluster_security_group_id]
  }

  egress {
    from_port   = 0
    to_port     = 0
    protocol    = "-1"
    cidr_blocks = ["0.0.0.0/0"]
  }

  lifecycle {
    create_before_destroy = true
  }
}

resource "aws_db_instance" "forgejo" {
  identifier     = "platform-git-db"
  engine         = "postgres"
  engine_version = "16"
  instance_class = "db.t4g.small"

  allocated_storage     = 20
  max_allocated_storage = 200
  storage_type          = "gp3"
  storage_encrypted     = true

  db_name  = "forgejo"
  username = "forgejo"
  password = random_password.forgejo_db.result

  db_subnet_group_name      = var.db_subnet_group_name
  vpc_security_group_ids    = [aws_security_group.forgejo_db.id]
  multi_az                  = false # portfolio metadata; a short recovery window is acceptable (memory.md §7.2 budget)
  publicly_accessible       = false
  deletion_protection       = true
  skip_final_snapshot       = false
  final_snapshot_identifier = "platform-git-db-final"
  backup_retention_period   = 14

  auto_minor_version_upgrade = true
  apply_immediately          = false
}

resource "aws_route53_record" "git" {
  zone_id = var.route53_zone_id
  name    = var.git_hostname
  type    = "A"

  alias {
    name                   = var.alb_dns_name
    zone_id                = var.alb_zone_id
    evaluate_target_health = true
  }
}

output "db_endpoint" {
  value       = aws_db_instance.forgejo.address
  description = "Set as helm values database.host (append :5432)."
}

output "db_password" {
  value       = random_password.forgejo_db.result
  sensitive   = true
  description = "Put into the platform-git-secrets Secret as DB_PASSWD."
}

output "git_url" {
  value = "https://${var.git_hostname}/"
}
