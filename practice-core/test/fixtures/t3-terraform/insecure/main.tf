# Phase 3 1.8 fixture — a config with deliberate security misconfigurations
# for tfsec/checkov/trivy to flag. NOT applied (no cloud creds); the
# STATIC_ANALYSIS executor scans source, not state.
terraform {
  required_version = ">= 1.0"
}

resource "aws_s3_bucket" "public" {
  bucket = "phase3-fixture-public-bucket"
}

resource "aws_s3_bucket_acl" "public" {
  bucket = aws_s3_bucket.public.id
  acl    = "public-read"
}

resource "aws_db_instance" "unencrypted" {
  identifier          = "phase3-fixture-db"
  engine              = "postgres"
  instance_class      = "db.t3.micro"
  allocated_storage   = 20
  username            = "admin"
  password            = "hardcoded-password-in-tf"
  storage_encrypted   = false
  skip_final_snapshot = true
  publicly_accessible = true
}

resource "aws_security_group" "wide_open" {
  name = "phase3-fixture-sg"
  ingress {
    from_port   = 0
    to_port     = 65535
    protocol    = "tcp"
    cidr_blocks = ["0.0.0.0/0"]
  }
}
