# Phase 1/2 — the practice EKS cluster (the one that never got written).
#
# memory.md §5.2: "Practice Cluster (regional, EKS/GKE) … Node group:
# practice-t1 (spot, ARM64 where possible)". Phase 1 shipped on local
# docker-compose k3s only; this is the real regional cluster T1 (and,
# via infra/practice-cluster/sysbox/, T2) run on.
#
# COST-FIRST design for the ₹100/user/month ceiling
# (docs/t2-cost-optimization-100.md):
#   - SINGLE AZ. One NAT gateway, not three (~$32/mo each saved).
#   - T1 nodes: ARM Graviton SPOT (memory.md §10.2 "40–70% on compute").
#   - Cluster-autoscaler min=0 on a schedule; a small always-on floor
#     only during known cohort hours.
#   - No control-plane logging to CloudWatch by default (opt-in var).
#   - EKS control plane is the one unavoidable fixed cost: ~$73/mo.
#
# NOT applied here — needs AWS credentials for the practice account.
# `tofu validate` + `tofu fmt` clean; `tofu plan`/`apply` need creds.

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
      Component = "practice-cluster"
      ManagedBy = "opentofu"
    }
  }
}

# ---------------------------------------------------------------------------
# Variables
# ---------------------------------------------------------------------------

variable "region" {
  type    = string
  default = "ap-south-1"
}

variable "cluster_name" {
  type    = string
  default = "practice-cluster"
}

variable "kubernetes_version" {
  type    = string
  default = "1.31"
}

variable "vpc_cidr" {
  type    = string
  default = "10.42.0.0/16"
}

variable "az_count" {
  type        = number
  default     = 2
  description = <<-EOT
    Number of AZs for SUBNETS (EKS control plane requires >=2 subnets in
    >=2 AZs). But we place only ONE NAT gateway (single-AZ egress) to
    keep cost down — a NAT-AZ outage briefly breaks new-pod image pulls,
    an acceptable trade at this budget. Nodes still spread across the AZs.
  EOT
}

variable "t1_instance_types" {
  type = list(string)
  # ARM Graviton, general purpose. m7g.xlarge = 4 vCPU / 16 GiB —
  # holds ~2 T1 envs (2 vCPU/4 GiB each) or 1 T2 env (up to 8/16) plus
  # headroom. Multiple types = better spot availability.
  default = ["m7g.large", "m7g.xlarge", "m6g.xlarge", "c7g.xlarge"]
}

variable "t1_min_size" {
  type        = number
  default     = 0
  description = "Autoscaler floor. 0 outside cohort hours; raise via a scheduled action during known class times (docs/t2-cost-optimization-100.md)."
}

variable "t1_max_size" {
  type        = number
  default     = 6
  description = "Autoscaler ceiling. Size from peak concurrent T1+T2 envs / envs-per-node."
}

variable "t1_desired_size" {
  type    = number
  default = 1
}

variable "enable_control_plane_logs" {
  type        = bool
  default     = false
  description = "EKS control-plane logging to CloudWatch. Off by default — real $ at chatty API volume. Turn on for a security review window, then off."
}

variable "cluster_admin_principal_arns" {
  type        = list(string)
  default     = []
  description = "IAM principal ARNs granted cluster-admin via EKS access entries (your operator role). The creating principal gets admin automatically."
}

# ---------------------------------------------------------------------------
# Network — single VPC, public + private subnets, ONE NAT gateway
# ---------------------------------------------------------------------------

data "aws_availability_zones" "available" {
  state = "available"
}

locals {
  azs = slice(data.aws_availability_zones.available.names, 0, var.az_count)
  # /20 private + /24 public per AZ out of the /16.
  private_subnets = [for i, az in local.azs : cidrsubnet(var.vpc_cidr, 4, i)]
  public_subnets  = [for i, az in local.azs : cidrsubnet(var.vpc_cidr, 8, i + 200)]
}

resource "aws_vpc" "this" {
  cidr_block           = var.vpc_cidr
  enable_dns_support   = true
  enable_dns_hostnames = true
  tags                 = { Name = var.cluster_name }
}

resource "aws_internet_gateway" "this" {
  vpc_id = aws_vpc.this.id
  tags   = { Name = var.cluster_name }
}

resource "aws_subnet" "public" {
  count                   = var.az_count
  vpc_id                  = aws_vpc.this.id
  cidr_block              = local.public_subnets[count.index]
  availability_zone       = local.azs[count.index]
  map_public_ip_on_launch = true
  tags = {
    Name                                        = "${var.cluster_name}-public-${local.azs[count.index]}"
    "kubernetes.io/role/elb"                    = "1"
    "kubernetes.io/cluster/${var.cluster_name}" = "shared"
  }
}

resource "aws_subnet" "private" {
  count             = var.az_count
  vpc_id            = aws_vpc.this.id
  cidr_block        = local.private_subnets[count.index]
  availability_zone = local.azs[count.index]
  tags = {
    Name                                        = "${var.cluster_name}-private-${local.azs[count.index]}"
    "kubernetes.io/role/internal-elb"           = "1"
    "kubernetes.io/cluster/${var.cluster_name}" = "shared"
  }
}

# ONE NAT gateway in the first public subnet. All private subnets route
# through it. Single point of egress failure, deliberately — a second NAT
# is ~$32/mo + data, and at this budget a brief image-pull outage on a
# NAT-AZ failure is acceptable.
resource "aws_eip" "nat" {
  domain = "vpc"
  tags   = { Name = "${var.cluster_name}-nat" }
}

resource "aws_nat_gateway" "this" {
  allocation_id = aws_eip.nat.id
  subnet_id     = aws_subnet.public[0].id
  tags          = { Name = var.cluster_name }
  depends_on    = [aws_internet_gateway.this]
}

resource "aws_route_table" "public" {
  vpc_id = aws_vpc.this.id
  route {
    cidr_block = "0.0.0.0/0"
    gateway_id = aws_internet_gateway.this.id
  }
  tags = { Name = "${var.cluster_name}-public" }
}

resource "aws_route_table" "private" {
  vpc_id = aws_vpc.this.id
  route {
    cidr_block     = "0.0.0.0/0"
    nat_gateway_id = aws_nat_gateway.this.id
  }
  tags = { Name = "${var.cluster_name}-private" }
}

resource "aws_route_table_association" "public" {
  count          = var.az_count
  subnet_id      = aws_subnet.public[count.index].id
  route_table_id = aws_route_table.public.id
}

resource "aws_route_table_association" "private" {
  count          = var.az_count
  subnet_id      = aws_subnet.private[count.index].id
  route_table_id = aws_route_table.private.id
}

# Gateway VPC endpoint for S3 — ECR image layers come from S3; this keeps
# that traffic off the NAT (real data-transfer saving on every cold pull).
resource "aws_vpc_endpoint" "s3" {
  vpc_id            = aws_vpc.this.id
  service_name      = "com.amazonaws.${var.region}.s3"
  vpc_endpoint_type = "Gateway"
  route_table_ids   = [aws_route_table.private.id]
  tags              = { Name = "${var.cluster_name}-s3" }
}

# ---------------------------------------------------------------------------
# EKS cluster
# ---------------------------------------------------------------------------

data "aws_iam_policy_document" "cluster_assume" {
  statement {
    actions = ["sts:AssumeRole"]
    principals {
      type        = "Service"
      identifiers = ["eks.amazonaws.com"]
    }
  }
}

resource "aws_iam_role" "cluster" {
  name               = "${var.cluster_name}-cluster"
  assume_role_policy = data.aws_iam_policy_document.cluster_assume.json
}

resource "aws_iam_role_policy_attachment" "cluster" {
  role       = aws_iam_role.cluster.name
  policy_arn = "arn:aws:iam::aws:policy/AmazonEKSClusterPolicy"
}

resource "aws_eks_cluster" "this" {
  name     = var.cluster_name
  version  = var.kubernetes_version
  role_arn = aws_iam_role.cluster.arn

  # EKS access entries (not the legacy aws-auth configmap).
  access_config {
    authentication_mode                         = "API"
    bootstrap_cluster_creator_admin_permissions = true
  }

  vpc_config {
    subnet_ids              = concat(aws_subnet.private[*].id, aws_subnet.public[*].id)
    endpoint_private_access = true
    endpoint_public_access  = true # tighten to your office CIDR via public_access_cidrs for a real deployment
  }

  enabled_cluster_log_types = var.enable_control_plane_logs ? ["api", "audit", "authenticator", "controllerManager", "scheduler"] : []

  depends_on = [aws_iam_role_policy_attachment.cluster]
}

resource "aws_eks_access_entry" "admins" {
  for_each      = toset(var.cluster_admin_principal_arns)
  cluster_name  = aws_eks_cluster.this.name
  principal_arn = each.value
  type          = "STANDARD"
}

resource "aws_eks_access_policy_association" "admins" {
  for_each      = toset(var.cluster_admin_principal_arns)
  cluster_name  = aws_eks_cluster.this.name
  principal_arn = each.value
  policy_arn    = "arn:aws:eks::aws:cluster-access-policy/AmazonEKSClusterAdminPolicy"
  access_scope { type = "cluster" }
}

# ---------------------------------------------------------------------------
# OIDC provider (for IRSA — the orchestrator pod's ServiceAccount in
# Step D assumes a role via this; also used by cluster-autoscaler).
# ---------------------------------------------------------------------------

data "tls_certificate" "oidc" {
  url = aws_eks_cluster.this.identity[0].oidc[0].issuer
}

resource "aws_iam_openid_connect_provider" "this" {
  url             = aws_eks_cluster.this.identity[0].oidc[0].issuer
  client_id_list  = ["sts.amazonaws.com"]
  thumbprint_list = [data.tls_certificate.oidc.certificates[0].sha1_fingerprint]
}

# ---------------------------------------------------------------------------
# T1 node group — ARM Graviton SPOT. T2 pods land here too (no separate
# pool at this budget); the T2 runtime (Sysbox) is installed onto these
# nodes by infra/practice-cluster/sysbox/ (a DaemonSet) after this applies.
#
# gVisor (the T1 runtime, RuntimeClass "gvisor" / handler "runsc") is NOT
# installed by anything in this repo on the EKS path — infra/practice-
# cluster/sysbox/ is Sysbox only. On EKS it needs a custom AMI or a
# privileged runsc-install DaemonSet, not yet written. For the gVisor-
# ready regional cluster see infra/practice-cluster/gke/ (GKE Sandbox
# gives the "gvisor" RuntimeClass as a one-flag node-pool feature).
# ---------------------------------------------------------------------------

data "aws_iam_policy_document" "node_assume" {
  statement {
    actions = ["sts:AssumeRole"]
    principals {
      type        = "Service"
      identifiers = ["ec2.amazonaws.com"]
    }
  }
}

resource "aws_iam_role" "node" {
  name               = "${var.cluster_name}-node"
  assume_role_policy = data.aws_iam_policy_document.node_assume.json
}

resource "aws_iam_role_policy_attachment" "node" {
  for_each = toset([
    "arn:aws:iam::aws:policy/AmazonEKSWorkerNodePolicy",
    "arn:aws:iam::aws:policy/AmazonEKS_CNI_Policy",
    "arn:aws:iam::aws:policy/AmazonEC2ContainerRegistryReadOnly",
    "arn:aws:iam::aws:policy/AmazonSSMManagedInstanceCore",
  ])
  role       = aws_iam_role.node.name
  policy_arn = each.value
}

resource "aws_eks_node_group" "t1" {
  cluster_name    = aws_eks_cluster.this.name
  node_group_name = "practice-t1"
  node_role_arn   = aws_iam_role.node.arn
  subnet_ids      = aws_subnet.private[*].id

  capacity_type  = "SPOT"
  instance_types = var.t1_instance_types
  ami_type       = "AL2023_ARM_64_STANDARD"
  disk_size      = 50

  scaling_config {
    min_size     = var.t1_min_size
    max_size     = var.t1_max_size
    desired_size = var.t1_desired_size
  }

  update_config {
    max_unavailable = 1
  }

  # memory.md §5.2: "Taints: workload=learner:NoSchedule". The
  # orchestrator's workspace pod carries the matching toleration
  # (internal/k8s/provision.go). Platform pods (orchestrator, session
  # broker, reaper) get their own toleration OR a tiny separate pool —
  # see infra/practice-cluster/platform-nodes/ (Step D).
  taint {
    key    = "workload"
    value  = "learner"
    effect = "NO_SCHEDULE"
  }

  labels = {
    "practiceengine.dev/tier" = "t1"
  }

  tags = {
    "k8s.io/cluster-autoscaler/enabled"                                     = "true"
    "k8s.io/cluster-autoscaler/${var.cluster_name}"                         = "owned"
    "k8s.io/cluster-autoscaler/node-template/taint/workload"                = "learner:NoSchedule"
    "k8s.io/cluster-autoscaler/node-template/label/practiceengine.dev/tier" = "t1"
  }

  lifecycle {
    ignore_changes = [scaling_config[0].desired_size]
  }

  depends_on = [aws_iam_role_policy_attachment.node]
}

# ---------------------------------------------------------------------------
# EKS managed addons (kept minimal)
# ---------------------------------------------------------------------------

resource "aws_eks_addon" "vpc_cni" {
  cluster_name  = aws_eks_cluster.this.name
  addon_name    = "vpc-cni"
  addon_version = null # let EKS pick the default-compatible version
}

resource "aws_eks_addon" "coredns" {
  cluster_name = aws_eks_cluster.this.name
  addon_name   = "coredns"
  depends_on   = [aws_eks_node_group.t1]
}

resource "aws_eks_addon" "kube_proxy" {
  cluster_name = aws_eks_cluster.this.name
  addon_name   = "kube-proxy"
}

# ---------------------------------------------------------------------------
# Outputs — consumed by the sysbox/ and t2-nodepool-kata/ and orchestrator
# deployment modules.
# ---------------------------------------------------------------------------

output "cluster_name" {
  value = aws_eks_cluster.this.name
}

output "cluster_endpoint" {
  value = aws_eks_cluster.this.endpoint
}

output "cluster_ca" {
  value = aws_eks_cluster.this.certificate_authority[0].data
}

output "oidc_provider_arn" {
  value = aws_iam_openid_connect_provider.this.arn
}

output "oidc_provider_url" {
  value = aws_iam_openid_connect_provider.this.url
}

output "private_subnet_ids" {
  value = aws_subnet.private[*].id
}

output "node_role_arn" {
  value = aws_iam_role.node.arn
}

output "vpc_id" {
  value = aws_vpc.this.id
}

output "kubeconfig_command" {
  value = "aws eks update-kubeconfig --region ${var.region} --name ${aws_eks_cluster.this.name}"
}

# Cost sanity — surface the fixed monthly floor so it's never a surprise.
output "estimated_fixed_monthly_usd" {
  value = {
    eks_control_plane = 73
    nat_gateway       = 33
    eip               = 4
    note              = "Plus SPOT node-hours (autoscaled, min can be 0) + EBS gp3 50GB/node (~$4/node/mo) + data transfer. This ~$110/mo floor is amortised across ALL learners — at 200 learners that's ~$0.55 (~₹45)/user/mo fixed; at 50 learners ~₹180/user, over the ₹100 budget until the cohort grows. See docs/t2-cost-optimization-100.md."
  }
}
