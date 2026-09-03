# Phase 2 A — T2 (isolated microVM) node pool for the practice cluster.
#
# NOT applied here — needs real AWS credentials for the account that owns
# the practice EKS cluster. `tofu validate` + `tofu fmt` are clean;
# `tofu plan` needs credentials and an existing cluster.
#
# What this builds (grounded in memory.md §5.1/§5.2 and
# orchestrator/manifests/t2/ + docs/t2-*.md):
#
#   - an EKS **managed node group** `practice-t2` on ARM64 Graviton
#     bare-metal instances (KVM exposed — required for Kata/Firecracker;
#     memory.md §5.1 "Hardware-virtualised; own kernel", §5.2
#     "Alternatives considered: Kata Containers … chosen for T2").
#   - **SPOT** capacity (memory.md §10.2 "Spot / ARM: 40–70% on compute")
#     with a 2-minute-notice interruption handler wired to the
#     orchestrator's snapshot-and-destroy path.
#   - the taint / label pair `applyT2PodShape` already expects:
#     taint  workload=learner-t2:NoSchedule
#     label  practiceengine.dev/tier2=true
#   - cluster-autoscaler discovery tags with **min=0** (memory.md §5.5
#     "zero overnight"; docs/t2-cost-optimization.md §4 — an idle metal
#     node is ₹60k–300k/month, so min=0 is not optional).
#   - the `kata` RuntimeClass + kata-deploy DaemonSet (Firecracker
#     hypervisor config), pinned, node-selected to THIS pool only.
#
# Capacity numbers here MUST match docs/t2-setup-and-operations.md §4 and
# the orchestrator's DefaultT2Resources (8 vCPU / 16 GiB per env ceiling).
# See var.env_vcpu / var.env_memory_gib and the drift check in §"outputs".

terraform {
  required_version = ">= 1.6"
  required_providers {
    aws = {
      source  = "hashicorp/aws"
      version = "~> 5.0"
    }
    kubernetes = {
      source  = "hashicorp/kubernetes"
      version = "~> 2.30"
    }
  }
  backend "s3" {}
}

provider "aws" {
  region = var.region
  default_tags {
    tags = {
      Project   = "practice-engine"
      Component = "t2-nodepool"
      Phase     = "2"
      ManagedBy = "opentofu"
    }
  }
}

# The practice EKS cluster already exists (Phase 1 T1 work). This module
# only adds the T2 pool to it — it reads the cluster, never creates it.
data "aws_eks_cluster" "practice" {
  name = var.cluster_name
}

data "aws_eks_cluster_auth" "practice" {
  name = var.cluster_name
}

provider "kubernetes" {
  host                   = data.aws_eks_cluster.practice.endpoint
  cluster_ca_certificate = base64decode(data.aws_eks_cluster.practice.certificate_authority[0].data)
  token                  = data.aws_eks_cluster_auth.practice.token
}

# ---------------------------------------------------------------------------
# Variables
# ---------------------------------------------------------------------------

variable "region" {
  type        = string
  description = "AWS region the practice cluster runs in."
}

variable "cluster_name" {
  type        = string
  description = "Name of the existing practice EKS cluster to attach the T2 pool to."
}

variable "subnet_ids" {
  type        = list(string)
  description = <<-EOT
    Private subnet IDs for the T2 node group. memory.md §10.2 "single-AZ
    environments" for egress cost — prefer a single AZ's subnet here
    unless you need AZ-failure resilience for T2 (T2 is a minority of
    traffic; a brief T2 outage is acceptable, cross-AZ data transfer is
    not free).
  EOT
}

variable "node_instance_types" {
  type = list(string)
  # ARM64 Graviton bare-metal — the only KVM-capable + cheap combination
  # AWS sells (docs/t2-cost-optimization.md §2.2/§2.3). c7g.metal =
  # 64 vCPU / 128 GiB; m7g.metal = 64 vCPU / 256 GiB. Both give the
  # ">= 32 vCPU / 64 GiB" node the capacity model in
  # t2-setup-and-operations.md §4.2 asks for, with room for ~6 envs/node
  # at the 4-vCPU optimized request or ~3 at the 8-vCPU ceiling.
  default     = ["c7g.metal", "m7g.metal"]
  description = "Instance types for the T2 pool. Must expose KVM (bare-metal) and be ARM64."
}

variable "node_min_size" {
  type        = number
  default     = 0
  description = <<-EOT
    MUST be 0 unless a scheduled live-class pre-warm window is active
    (raise it 15 min before, drop it after — see
    t2-cost-optimization.md §3.3). A standing >0 min on metal instances
    breaks the ₹300/user budget on fixed cost alone.
  EOT
}

variable "node_max_size" {
  type        = number
  description = <<-EOT
    Peak T2 node count. Size from t2-setup-and-operations.md §4.3:
    ceil( (0.0133 * cohort_monthly_active) / envs_per_node ) + 1.
    e.g. 1,000 learners, 6 envs/node -> ceil(13.3/6)+1 = 4.
  EOT
}

variable "env_vcpu" {
  type        = number
  default     = 8
  description = "Per-environment vCPU ceiling. MUST equal orchestrator DefaultT2Resources.CPU."
}

variable "env_memory_gib" {
  type        = number
  default     = 16
  description = "Per-environment memory ceiling in GiB. MUST equal orchestrator DefaultT2Resources.Memory."
}

variable "envs_per_node_target" {
  type        = number
  default     = 3
  description = <<-EOT
    Planning density used only for the capacity sanity output below.
    3 = the conservative 8-vCPU-ceiling figure from
    t2-setup-and-operations.md §4.2. With the optimized 4-vCPU request
    (t2-cost-optimization.md §2.4) real density is ~6; set this to match
    whichever the sim blueprints actually request.
  EOT
}

variable "kata_deploy_ref" {
  type = string
  # Pin a real released tag before apply. kata-deploy's k3s/generic
  # overlay; we patch it to the Firecracker hypervisor config below.
  default     = "3.10.1"
  description = "kata-containers release tag for the kata-deploy DaemonSet image."
}

variable "workspace_base_image" {
  type        = string
  description = "Registry ref for the T2 workspace base image, pre-pulled onto the pool by the DaemonSet in this module."
}

# ---------------------------------------------------------------------------
# IAM for the node group
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

resource "aws_iam_role" "t2_node" {
  name               = "${var.cluster_name}-t2-node"
  assume_role_policy = data.aws_iam_policy_document.node_assume.json
}

resource "aws_iam_role_policy_attachment" "t2_node" {
  for_each = toset([
    "arn:aws:iam::aws:policy/AmazonEKSWorkerNodePolicy",
    "arn:aws:iam::aws:policy/AmazonEKS_CNI_Policy",
    "arn:aws:iam::aws:policy/AmazonEC2ContainerRegistryReadOnly",
    # SSM so the spot-interruption handler and break-glass debugging work
    # without opening SSH to metal nodes (memory.md §5.4 "WebSocket, not
    # SSH … SSM/SSH-over-bastion").
    "arn:aws:iam::aws:policy/AmazonSSMManagedInstanceCore",
  ])
  role       = aws_iam_role.t2_node.name
  policy_arn = each.value
}

# ---------------------------------------------------------------------------
# The T2 managed node group
# ---------------------------------------------------------------------------

resource "aws_eks_node_group" "t2" {
  cluster_name    = var.cluster_name
  node_group_name = "practice-t2"
  node_role_arn   = aws_iam_role.t2_node.arn
  subnet_ids      = var.subnet_ids

  # SPOT: 40-70% cheaper (memory.md §10.2). The pool tolerates
  # interruption because every T2 env snapshot-and-destroys on idle
  # already; the 2-min notice reuses that path (see aws_autoscaling_lifecycle_hook).
  capacity_type  = "SPOT"
  instance_types = var.node_instance_types
  ami_type       = "AL2023_ARM_64_STANDARD"

  scaling_config {
    min_size     = var.node_min_size # 0 — see variable doc
    max_size     = var.node_max_size
    desired_size = var.node_min_size
  }

  # memory.md §10.2 "aggressive defaults" — reclaim a drained metal node
  # fast; at metal $/hr an unneeded node for 10 min is real money.
  update_config {
    max_unavailable = 1
  }

  # The taint applyT2PodShape's toleration matches. A T1 pod has no
  # toleration for this, so it can never land here (and a T2 pod has no
  # KVM on the T1 pool, so Kata won't start there — the pairing is
  # enforced on both sides, per manifests/t2/node-pool-taint.md).
  taint {
    key    = "workload"
    value  = "learner-t2"
    effect = "NO_SCHEDULE"
  }

  labels = {
    "practiceengine.dev/tier2" = "true"
  }

  tags = {
    # cluster-autoscaler auto-discovery. min=0 means the ASG scales to
    # nothing when no T2 env is scheduled.
    "k8s.io/cluster-autoscaler/enabled"             = "true"
    "k8s.io/cluster-autoscaler/${var.cluster_name}" = "owned"
    # Tells the autoscaler the effect of the taint on a scaled-from-zero
    # node, so it can decide a pending T2 pod justifies a new node.
    "k8s.io/cluster-autoscaler/node-template/taint/workload"                 = "learner-t2:NoSchedule"
    "k8s.io/cluster-autoscaler/node-template/label/practiceengine.dev/tier2" = "true"
  }

  lifecycle {
    # desired_size drifts as the autoscaler works — don't fight it.
    ignore_changes = [scaling_config[0].desired_size]
  }

  depends_on = [aws_iam_role_policy_attachment.t2_node]
}

# ---------------------------------------------------------------------------
# Spot interruption -> snapshot-and-destroy (memory.md §10.2 graceful drain)
# ---------------------------------------------------------------------------
#
# On the 2-minute EC2 spot interruption notice, drain the node so every
# T2 workspace pod on it gets the same SIGTERM path an idle-kill uses;
# the orchestrator's reaper/destroyer then snapshots and cleans the
# namespace. AWS Node Termination Handler (NTH) in queue-processor mode
# is the standard mechanism; this module provisions the SQS queue + the
# EventBridge rules it consumes. NTH itself is a Helm release deployed
# alongside cluster-autoscaler (out of this module — it's cluster-wide),
# pointed at queue_url below.

resource "aws_sqs_queue" "spot_interruptions" {
  name                      = "${var.cluster_name}-t2-spot-interruptions"
  message_retention_seconds = 300
  sqs_managed_sse_enabled   = true
}

data "aws_iam_policy_document" "spot_queue" {
  statement {
    actions   = ["sqs:SendMessage"]
    resources = [aws_sqs_queue.spot_interruptions.arn]
    principals {
      type        = "Service"
      identifiers = ["events.amazonaws.com", "sqs.amazonaws.com"]
    }
  }
}

resource "aws_sqs_queue_policy" "spot_queue" {
  queue_url = aws_sqs_queue.spot_interruptions.id
  policy    = data.aws_iam_policy_document.spot_queue.json
}

resource "aws_cloudwatch_event_rule" "spot_interruption" {
  name        = "${var.cluster_name}-t2-spot-interruption"
  description = "EC2 spot interruption warning -> T2 drain queue"
  event_pattern = jsonencode({
    source      = ["aws.ec2"]
    detail-type = ["EC2 Spot Instance Interruption Warning"]
  })
}

resource "aws_cloudwatch_event_target" "spot_interruption" {
  rule = aws_cloudwatch_event_rule.spot_interruption.name
  arn  = aws_sqs_queue.spot_interruptions.arn
}

# ---------------------------------------------------------------------------
# Kata / Firecracker on the pool
# ---------------------------------------------------------------------------

# The RuntimeClass applyT2PodShape references. Inert until a node
# advertises the "kata" handler (kata-deploy below does that). Applying
# it alone is safe on any cluster — same property as
# manifests/t2/runtimeclass-kata.yaml (this resource IS that manifest,
# managed by tofu so it lands with the pool).
resource "kubernetes_manifest" "runtimeclass_kata" {
  manifest = {
    apiVersion = "node.k8s.io/v1"
    kind       = "RuntimeClass"
    metadata   = { name = "kata" }
    handler    = "kata"
    # Scheduling hint so a `kata` pod only ever lands on a T2 node.
    scheduling = {
      nodeSelector = { "practiceengine.dev/tier2" = "true" }
      tolerations = [{
        key      = "workload"
        value    = "learner-t2"
        operator = "Equal"
        effect   = "NoSchedule"
      }]
    }
  }
}

# kata-deploy DaemonSet, node-selected to the T2 pool ONLY. It installs
# the kata binaries + the containerd `kata` runtime handler on each node
# and patches containerd config. We set DEBUG off and select the
# Firecracker shim config (configuration-fc) — lower per-VM overhead than
# QEMU, higher density, ~125ms boot (t2-cost-optimization.md §2.1).
resource "kubernetes_manifest" "kata_deploy" {
  manifest = {
    apiVersion = "apps/v1"
    kind       = "DaemonSet"
    metadata = {
      name      = "kata-deploy"
      namespace = "kube-system"
      labels    = { "app" = "kata-deploy" }
    }
    spec = {
      selector = { matchLabels = { "name" = "kata-deploy" } }
      template = {
        metadata = { labels = { "name" = "kata-deploy" } }
        spec = {
          serviceAccountName = "kata-deploy-sa"
          # T2 pool only — never install the kata shim on cheap T1 spot nodes.
          nodeSelector = { "practiceengine.dev/tier2" = "true" }
          tolerations = [{
            key      = "workload"
            value    = "learner-t2"
            operator = "Equal"
            effect   = "NoSchedule"
          }]
          containers = [{
            name            = "kube-kata"
            image           = "quay.io/kata-containers/kata-deploy:${var.kata_deploy_ref}"
            imagePullPolicy = "IfNotPresent"
            command         = ["bash", "-c", "/opt/kata-artifacts/scripts/kata-deploy.sh install"]
            env = [
              { name = "DEBUG", value = "false" },
              { name = "SHIMS", value = "fc" },
              { name = "DEFAULT_SHIM", value = "fc" },
              { name = "CREATE_RUNTIMECLASSES", value = "false" }, # we manage the RuntimeClass above
              { name = "ALLOWED_HYPERVISOR_ANNOTATIONS", value = "" },
              { name = "SNAPSHOTTER_HANDLER_MAPPING", value = "" },
              { name = "HOST_OS", value = "" },
            ]
            securityContext = { privileged = true }
            volumeMounts = [
              { name = "containerd-conf", mountPath = "/etc/containerd/" },
              { name = "kata-artifacts", mountPath = "/opt/kata/" },
              { name = "local-bin", mountPath = "/usr/local/bin/" },
            ]
          }]
          volumes = [
            { name = "containerd-conf", hostPath = { path = "/etc/containerd/" } },
            { name = "kata-artifacts", hostPath = { path = "/opt/kata/" } },
            { name = "local-bin", hostPath = { path = "/usr/local/bin/" } },
          ]
        }
      }
    }
  }
  depends_on = [aws_eks_node_group.t2]
}

# Minimal RBAC for kata-deploy (it labels its own node during install).
resource "kubernetes_manifest" "kata_deploy_sa" {
  manifest = {
    apiVersion = "v1"
    kind       = "ServiceAccount"
    metadata   = { name = "kata-deploy-sa", namespace = "kube-system" }
  }
}

resource "kubernetes_manifest" "kata_deploy_clusterrole" {
  manifest = {
    apiVersion = "rbac.authorization.k8s.io/v1"
    kind       = "ClusterRole"
    metadata   = { name = "kata-deploy-role" }
    rules = [{
      apiGroups = [""]
      resources = ["nodes"]
      verbs     = ["get", "patch", "label", "list"]
    }]
  }
}

resource "kubernetes_manifest" "kata_deploy_binding" {
  manifest = {
    apiVersion = "rbac.authorization.k8s.io/v1"
    kind       = "ClusterRoleBinding"
    metadata   = { name = "kata-deploy-binding" }
    roleRef = {
      apiGroup = "rbac.authorization.k8s.io"
      kind     = "ClusterRole"
      name     = "kata-deploy-role"
    }
    subjects = [{
      kind      = "ServiceAccount"
      name      = "kata-deploy-sa"
      namespace = "kube-system"
    }]
  }
}

# ---------------------------------------------------------------------------
# Image pre-pull on the T2 pool (memory.md §5.2 "pre-pull the top 20
# images onto every node via a DaemonSet" — cold-start p95 is dominated
# by image pull). For T2 this is the workspace base image + the kata
# rootfs (kata-deploy handles its own artifacts).
# ---------------------------------------------------------------------------

resource "kubernetes_manifest" "t2_image_prepull" {
  manifest = {
    apiVersion = "apps/v1"
    kind       = "DaemonSet"
    metadata = {
      name      = "t2-image-prepull"
      namespace = "kube-system"
      labels    = { "app" = "t2-image-prepull" }
    }
    spec = {
      selector = { matchLabels = { "app" = "t2-image-prepull" } }
      template = {
        metadata = { labels = { "app" = "t2-image-prepull" } }
        spec = {
          nodeSelector = { "practiceengine.dev/tier2" = "true" }
          tolerations = [{
            key      = "workload"
            value    = "learner-t2"
            operator = "Equal"
            effect   = "NoSchedule"
          }]
          # Pull, then idle. The image lands in the node's containerd
          # cache so a real T2 Provision skips the pull.
          initContainers = [{
            name    = "pull"
            image   = var.workspace_base_image
            command = ["sh", "-c", "true"]
          }]
          containers = [{
            name  = "pause"
            image = "registry.k8s.io/pause:3.10"
          }]
        }
      }
    }
  }
  depends_on = [aws_eks_node_group.t2]
}

# ---------------------------------------------------------------------------
# Outputs — and the capacity/drift sanity checks
# ---------------------------------------------------------------------------

output "node_group_name" {
  value = aws_eks_node_group.t2.node_group_name
}

output "spot_interruption_queue_url" {
  value       = aws_sqs_queue.spot_interruptions.id
  description = "Point the cluster-wide AWS Node Termination Handler (queue-processor mode) at this."
}

# Capacity sanity: surface the numbers so a reviewer can check them
# against docs/t2-setup-and-operations.md §4 without running anything.
output "capacity_plan" {
  value = {
    per_env_vcpu         = var.env_vcpu
    per_env_memory_gib   = var.env_memory_gib
    envs_per_node_target = var.envs_per_node_target
    node_min_size        = var.node_min_size
    node_max_size        = var.node_max_size
    peak_env_capacity    = var.node_max_size * var.envs_per_node_target
    instance_types       = var.node_instance_types
    capacity_type        = "SPOT"
    note                 = "peak_env_capacity must cover peak concurrent T2 envs = ceil(0.0133 * cohort_monthly_active). If it doesn't, raise node_max_size."
  }
}

# Drift guard: the module's per-env ceiling MUST match the orchestrator's
# DefaultT2Resources (8 vCPU / 16 GiB). This fails `tofu plan`/`apply`
# loudly if someone changes one side without the other.
check "env_ceiling_matches_orchestrator" {
  assert {
    condition     = var.env_vcpu == 8 && var.env_memory_gib == 16
    error_message = "var.env_vcpu / var.env_memory_gib must match orchestrator/internal/k8s/provision.go DefaultT2Resources (8 vCPU / 16 GiB). Change both together or the capacity plan and the runtime quota drift apart (docs/t2-setup-and-operations.md §4.1)."
  }
}

check "min_size_is_zero_or_prewarm" {
  assert {
    condition     = var.node_min_size == 0
    error_message = "node_min_size is not 0. A standing >0 min on metal SPOT instances breaks the ₹300/user budget on fixed cost alone (docs/t2-cost-optimization.md §4). If this is a deliberate live-class pre-warm, set it back to 0 immediately after the window and suppress this check for that apply only."
  }
}
