# Phase 1 — the regional practice cluster, gVisor-capable path.
#
# memory.md §5.2: "Practice Cluster (regional, EKS/GKE) … Node group:
# practice-t1 … RuntimeClass: gvisor". PLAN.md M1.1: "gVisor RuntimeClass,
# node pool, namespace-per-environment template". Phase 1 shipped on local
# docker-compose k3s only; this is the real regional cluster T1
# environments run on.
#
# WHY GKE HERE (the sibling ../cluster/ module is EKS):
#   The orchestrator sets spec.runtimeClassName = "gvisor" on T1 pods when
#   ORCHESTRATOR_GVISOR_ENABLED=true (internal/k8s/provision.go
#   runtimeClassForT1). Something has to install the "runsc" runtime
#   handler on the learner nodes and create that RuntimeClass. GKE Sandbox
#   does exactly this with one node-pool flag (sandbox_config.sandbox_type
#   = "gvisor"): GKE wires containerd, ships runsc, and creates a
#   RuntimeClass named "gvisor" with handler "runsc" — byte-for-byte what
#   the orchestrator already emits. On EKS the same outcome needs a custom
#   AMI or a privileged DaemonSet that rewrites every node's containerd
#   config; nothing in this repo does that yet. ../cluster/ stays as the
#   AWS-native option.
#
# COST (asia-south1, spot practice-t1, delete same day):
#   - Regional control plane: ~$0.10/hr (~$73/mo). Regional is a hard
#     requirement (HA masters across 3 zones), so this is accepted.
#   - platform pool: 3 × e2-standard-4 on-demand  (~$0.40/hr total)
#   - practice-t1 pool: 3 × e2-standard-4 SPOT    (~$0.16/hr total)
#   A few USD for a day. `tofu destroy` removes everything; the test
#   creates no PVC / LoadBalancer / static IP, so nothing is left behind.
#
# NOT applied from a dev session without gcloud auth + a billing-enabled
# project. `tofu validate` + `tofu fmt` are clean; `tofu plan`/`apply`
# need Application Default Credentials and var.project_id.

terraform {
  required_version = ">= 1.6"
  required_providers {
    # google-beta, not google: GKE Sandbox (node_config.sandbox_config /
    # gVisor) is exposed ONLY by the beta provider, even though the
    # feature is GA in GKE itself. This is a long-standing provider quirk
    # (google-beta only). The beta provider is a superset of google and
    # every other resource here behaves identically under it.
    google-beta = {
      source  = "hashicorp/google-beta"
      version = "~> 6.0"
    }
  }
  # Local state for this throwaway test cluster — it is created and
  # destroyed in the same session. Add a `backend "gcs" {}` block and
  # re-init if this is ever kept longer-lived.
}

provider "google-beta" {
  project = var.project_id
  region  = var.region
}

# ---------------------------------------------------------------------------
# Variables
# ---------------------------------------------------------------------------

variable "project_id" {
  type        = string
  description = "GCP project id. Must have billing enabled and the container.googleapis.com + compute.googleapis.com APIs on."
}

variable "region" {
  type        = string
  default     = "asia-south1"
  description = "Regional cluster location (control plane replicated across this region's zones). Matches memory.md 'regional' and ../cluster/ ap-south-1."
}

variable "cluster_name" {
  type    = string
  default = "practice-cluster"
}

variable "k8s_release_channel" {
  type        = string
  default     = "REGULAR"
  description = <<-EOT
    GKE release channel — the reproducible-version strategy. REGULAR
    currently serves ~1.30–1.31 (the ../cluster/ EKS module pins 1.31).
    Pin an exact min_master_version instead only if a specific version is
    required; a channel keeps patch upgrades managed.
  EOT
}

variable "platform_machine_type" {
  type    = string
  default = "e2-standard-4"
}

variable "t1_machine_type" {
  type        = string
  default     = "e2-standard-4"
  description = "4 vCPU / 16 GiB — same class as the EKS module's m7g.xlarge; holds ~2 T1 envs (2 vCPU/4 GiB each) plus headroom. GKE Sandbox requires a supported machine type (e2-standard-* qualifies; e2-micro/small do not)."
}

variable "platform_nodes_per_zone" {
  type        = number
  default     = 1
  description = "Untainted pool for platform pods (orchestrator, practice-core, datastores). 1 × 3 zones = 3 nodes."
}

variable "t1_nodes_per_zone" {
  type        = number
  default     = 1
  description = "Learner pool (gVisor). 1 × 3 zones = 3 nodes — the minimum that still proves 'multiple worker nodes'. Raise for real cohort load."
}

variable "t1_use_spot" {
  type        = bool
  default     = true
  description = "SPOT the learner pool (memory.md §10.2 'Spot / ARM: 40–70% on compute'). Platform pool stays on-demand so control-loop pods aren't preempted."
}

variable "enable_network_policy_enforcement" {
  type        = bool
  default     = true
  description = "Dataplane V2 (eBPF) — enforces the default-deny NetworkPolicies the orchestrator's T1 namespace template applies. Off would let those policies be silently ignored."
}

# ---------------------------------------------------------------------------
# Cluster — regional GKE Standard, VPC-native, Dataplane V2
# ---------------------------------------------------------------------------

resource "google_container_cluster" "this" {
  provider = google-beta
  name     = var.cluster_name
  location = var.region # region (not zone) => regional control plane

  # Manage node pools separately, never the default one.
  remove_default_node_pool = true
  initial_node_count       = 1

  release_channel {
    channel = var.k8s_release_channel
  }

  # VPC-native (alias IPs) — required for Dataplane V2 and the standard
  # for new clusters. Empty block => GKE picks secondary ranges.
  ip_allocation_policy {}

  # Dataplane V2 gives NetworkPolicy enforcement natively (no separate
  # Calico add-on). network_policy{} is intentionally NOT set alongside
  # this — the two are mutually exclusive.
  datapath_provider = var.enable_network_policy_enforcement ? "ADVANCED_DATAPATH" : "DATAPATH_PROVIDER_UNSPECIFIED"

  # Keep the test cluster cheap and simple: public endpoint, no
  # private-nodes NAT. Tighten (private nodes + authorized networks) for
  # a real long-lived deployment.
  deletion_protection = false

  # Labels so `gcloud`/console/billing can find every object this test
  # created, and `tofu destroy` + the orphan sweep can verify cleanup.
  resource_labels = {
    project   = "practice-engine"
    component = "practice-cluster"
    managedby = "opentofu"
    phase     = "phase1"
  }

  lifecycle {
    ignore_changes = [initial_node_count]
  }
}

# ---------------------------------------------------------------------------
# platform node pool — untainted, on-demand. Runs orchestrator /
# practice-core / Postgres / Redis / NATS (the in-cluster topology in
# orchestrator/manifests/platform/). GKE Sandbox CANNOT run system pods,
# so the platform must have a non-sandboxed pool.
# ---------------------------------------------------------------------------

resource "google_container_node_pool" "platform" {
  provider   = google-beta
  name       = "platform"
  cluster    = google_container_cluster.this.id
  node_count = var.platform_nodes_per_zone # per zone, in a regional cluster

  node_config {
    machine_type = var.platform_machine_type
    disk_size_gb = 50
    disk_type    = "pd-balanced"

    oauth_scopes = ["https://www.googleapis.com/auth/cloud-platform"]

    labels = {
      "practiceengine.dev/pool" = "platform"
    }

    # Shielded nodes — cheap hardening, no behavioural cost.
    shielded_instance_config {
      enable_secure_boot          = true
      enable_integrity_monitoring = true
    }
  }

  management {
    auto_repair  = true
    auto_upgrade = true
  }
}

# ---------------------------------------------------------------------------
# practice-t1 node pool — GKE Sandbox (gVisor), SPOT, tainted+labelled to
# match the orchestrator exactly:
#   taint  workload=learner:NoSchedule   (createWorkspacePod tolerates it)
#   label  practiceengine.dev/tier=t1    (sysbox/ DaemonSet + prepull DS
#                                         nodeSelector; kept for parity
#                                         even though T2 isn't in Phase 1)
# GKE auto-creates `RuntimeClass gvisor` (handler runsc) when any pool has
# sandbox_config set.
# ---------------------------------------------------------------------------

resource "google_container_node_pool" "practice_t1" {
  provider   = google-beta
  name       = "practice-t1"
  cluster    = google_container_cluster.this.id
  node_count = var.t1_nodes_per_zone

  node_config {
    machine_type = var.t1_machine_type
    disk_size_gb = 50
    disk_type    = "pd-balanced"
    spot         = var.t1_use_spot

    oauth_scopes = ["https://www.googleapis.com/auth/cloud-platform"]

    # THE gVisor SWITCH. This is the whole reason this module is GKE.
    sandbox_config {
      sandbox_type = "gvisor"
    }

    labels = {
      "practiceengine.dev/tier" = "t1"
      "practiceengine.dev/pool" = "practice-t1"
    }

    # memory.md §5.2: "Taints: workload=learner:NoSchedule". The
    # orchestrator's workspace pod carries the matching toleration
    # (internal/k8s/provision.go createWorkspacePod).
    taint {
      key    = "workload"
      value  = "learner"
      effect = "NO_SCHEDULE"
    }

    shielded_instance_config {
      enable_secure_boot          = true
      enable_integrity_monitoring = true
    }
  }

  management {
    auto_repair  = true
    auto_upgrade = true
  }
}

# ---------------------------------------------------------------------------
# Outputs
# ---------------------------------------------------------------------------

output "cluster_name" {
  value = google_container_cluster.this.name
}

output "region" {
  value = google_container_cluster.this.location
}

output "cluster_endpoint" {
  value = google_container_cluster.this.endpoint
}

output "get_credentials_command" {
  value = "gcloud container clusters get-credentials ${google_container_cluster.this.name} --region ${google_container_cluster.this.location} --project ${var.project_id}"
}

output "expected_worker_node_count" {
  value = 3 * (var.platform_nodes_per_zone + var.t1_nodes_per_zone)
}

output "gvisor_runtimeclass" {
  value       = "gvisor"
  description = "GKE creates this RuntimeClass (handler: runsc) automatically because practice-t1 has sandbox_config. Matches internal/k8s/provision.go runtimeClassForT1(true)."
}

output "destroy_command" {
  value = "cd infra/practice-cluster/gke && tofu destroy"
}

# Cost sanity — surface the floor so it is never a surprise.
output "estimated_hourly_usd" {
  value = {
    regional_control_plane = 0.10
    platform_pool_ondemand = format("~%.2f", 3 * var.platform_nodes_per_zone * 0.134)
    practice_t1_pool       = format("~%.2f", 3 * var.t1_nodes_per_zone * (var.t1_use_spot ? 0.054 : 0.134))
    note                   = "e2-standard-4 asia-south1 rough rates; SPOT ~60% off. Plus 50GB pd-balanced/node (~$0.007/hr each) + egress. Delete same day => a few USD total."
  }
}
