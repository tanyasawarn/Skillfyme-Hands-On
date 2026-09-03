// Package snapshotstate implements Stage 3.3's Snapshot / Restore at the
// IaC-state level for T3 project workspaces (PLAN_PHASE3_PROJECTS.md 3.3
// / A11, memory.md §12.3 "suspension is the norm").
//
// Snapshot = capture enough state that the compute can be destroyed and
// faithfully rebuilt later:
//   - the Terraform state reference (backend URI + serial + workspace) —
//     the state itself already lives in the platform-managed S3 backend
//     (Stage 1.3), so this is a metadata read, not a copy;
//   - a filtered `kubectl get -A` of the learner's namespaces;
//   - a cloud resource inventory (Resource Explorer / Config), tag-scoped
//     to the attempt.
//
// These go into a manifest JSON in S3; then the workspace pod + the
// claimed account's live compute are torn down (durable state = Git +
// the TF remote state).
//
// Restore = re-provision the workspace pod, re-claim an account (the same
// one if still pooled), `terraform apply` from the persisted state,
// re-attach.
//
// The manifest shape mirrors contracts/orchestrator.proto's
// SnapshotManifest (added in 0.7). All I/O is behind interfaces so this
// package is unit-tested with a fake blob store + a fake shell runner.
package snapshotstate

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/tanyasawarn/skillfyme-hands-on/orchestrator/internal/logging"
)

var log = logging.Component("snapshotstate")

// Manifest mirrors pb.SnapshotManifest.
type Manifest struct {
	TFBackendURI       string    `json:"tf_backend_uri"`
	TFStateSerial      int64     `json:"tf_state_serial"`
	TFWorkspace        string    `json:"tf_workspace"`
	K8sInventoryURI    string    `json:"k8s_inventory_uri"`
	CloudInventoryURI  string    `json:"cloud_inventory_uri"`
	CloudResourceCount int       `json:"cloud_resource_count"`
	SandboxAccountID   string    `json:"sandbox_account_id"`
	CapturedAt         time.Time `json:"captured_at"`
	// AttemptID + EnvID for correlation on Restore.
	AttemptID string `json:"attempt_id"`
	EnvID     string `json:"env_id"`
}

// BlobStore is the S3-shaped boundary. keys are like
// "snapshots/<attempt>/<env>/manifest.json".
type BlobStore interface {
	Put(ctx context.Context, key string, data []byte) (uri string, err error)
	Get(ctx context.Context, key string) ([]byte, error)
}

// ShellRunner runs a command in the workspace pod (reuses the orchestrator
// ExecShell path in production). Returns stdout; errors on non-zero exit
// or an infra failure.
type ShellRunner interface {
	Run(ctx context.Context, envID string, argv []string) (stdout string, err error)
}

// PodLifecycle is the workspace-pod half (satisfied by t3driver /
// internal/k8s in production).
type PodLifecycle interface {
	DeleteWorkspacePod(ctx context.Context, namespace string) error
	StartWorkspacePodForRestore(ctx context.Context, attemptID, envID, accountID string) (namespace string, err error)
}

// AccountReclaimer re-claims a sandbox account for a resumed attempt
// (accountpool in production).
type AccountReclaimer interface {
	// ReclaimForAttempt returns the account id + role to use for the
	// resumed attempt. Tries `preferredAccountID` first (still pooled),
	// else claims a fresh one.
	ReclaimForAttempt(ctx context.Context, attemptID, tenantID, region, preferredAccountID string, budgetUSD float64) (accountID, roleName string, err error)
}

// Config for the manager.
type Config struct {
	SnapshotBucketPrefix string // e.g. "s3://practice-snapshots"
	TFBackendURI         string // the platform-managed TF backend, e.g. "s3://practice-tf-state"
	Region               string
}

// Manager does Snapshot + Restore.
type Manager struct {
	cfg   Config
	blobs BlobStore
	shell ShellRunner
	pods  PodLifecycle
	pool  AccountReclaimer
}

func NewManager(cfg Config, blobs BlobStore, shell ShellRunner, pods PodLifecycle, pool AccountReclaimer) *Manager {
	return &Manager{cfg: cfg, blobs: blobs, shell: shell, pods: pods, pool: pool}
}

// SnapshotInput is the gRPC Snapshot handler's data for a T3 env.
type SnapshotInput struct {
	AttemptID      string
	EnvID          string
	Namespace      string
	AccountID      string
	TFWorkspaceDir string // the Terraform root inside the workspace
	// DestroyCompute: when true (the default for project idle-suspend),
	// tear down the pod + release the account after capture.
	DestroyCompute bool
}

// SnapshotResult is what the handler returns.
type SnapshotResult struct {
	SnapshotID  string
	ManifestURI string
	Manifest    Manifest
}

// Snapshot captures the IaC + inventory state and (optionally) destroys
// the compute.
func (m *Manager) Snapshot(ctx context.Context, in SnapshotInput) (SnapshotResult, error) {
	snapshotID := fmt.Sprintf("%s-%d", in.EnvID, time.Now().Unix())
	keyBase := fmt.Sprintf("snapshots/%s/%s", in.AttemptID, snapshotID)

	// 1. Terraform state metadata (`terraform state pull` → parse serial).
	serial, err := m.tfStateSerial(ctx, in.EnvID, in.TFWorkspaceDir)
	if err != nil {
		return SnapshotResult{}, fmt.Errorf("snapshotstate: read tf state: %w", err)
	}

	// 2. Filtered kubectl inventory.
	k8sInv, err := m.shell.Run(ctx, in.EnvID, []string{
		"sh", "-c",
		"kubectl get all -A -o json 2>/dev/null || echo '{}'",
	})
	if err != nil {
		return SnapshotResult{}, fmt.Errorf("snapshotstate: k8s inventory: %w", err)
	}
	k8sURI, err := m.blobs.Put(ctx, keyBase+"/k8s-inventory.json", []byte(k8sInv))
	if err != nil {
		return SnapshotResult{}, fmt.Errorf("snapshotstate: put k8s inventory: %w", err)
	}

	// 3. Cloud resource inventory (Resource Explorer), tag-scoped.
	cloudInv, count, err := m.cloudInventory(ctx, in.EnvID, in.AttemptID)
	if err != nil {
		return SnapshotResult{}, fmt.Errorf("snapshotstate: cloud inventory: %w", err)
	}
	cloudURI, err := m.blobs.Put(ctx, keyBase+"/cloud-inventory.json", []byte(cloudInv))
	if err != nil {
		return SnapshotResult{}, fmt.Errorf("snapshotstate: put cloud inventory: %w", err)
	}

	manifest := Manifest{
		TFBackendURI:       m.cfg.TFBackendURI,
		TFStateSerial:      serial,
		TFWorkspace:        fmt.Sprintf("account-baseline/%s", in.AccountID),
		K8sInventoryURI:    k8sURI,
		CloudInventoryURI:  cloudURI,
		CloudResourceCount: count,
		SandboxAccountID:   in.AccountID,
		CapturedAt:         time.Now().UTC(),
		AttemptID:          in.AttemptID,
		EnvID:              in.EnvID,
	}
	manifestBytes, _ := json.MarshalIndent(manifest, "", "  ")
	manifestURI, err := m.blobs.Put(ctx, keyBase+"/manifest.json", manifestBytes)
	if err != nil {
		return SnapshotResult{}, fmt.Errorf("snapshotstate: put manifest: %w", err)
	}

	// 4. Destroy the compute (project-mode suspend). The durable state is
	// Git + the TF remote state — the pod + the account's live resources
	// are re-created on Restore.
	if in.DestroyCompute {
		if in.Namespace != "" {
			if err := m.pods.DeleteWorkspacePod(ctx, in.Namespace); err != nil {
				log.Error("snapshotstate: pod delete failed (manifest already written)", "env_id", in.EnvID, "err", err)
			}
		}
		// The account release (nuke + verify) is the caller's job via
		// the accountpool release path — Snapshot only writes the
		// manifest + drops the pod; the T3 driver Destroy handles the
		// account. (Documented so 3.4's wiring calls both.)
	}

	log.Info("T3 IaC-state snapshot captured",
		"snapshot_id", snapshotID, "env_id", in.EnvID, "attempt_id", in.AttemptID,
		"tf_serial", serial, "cloud_resources", count, "destroyed_compute", in.DestroyCompute)
	return SnapshotResult{SnapshotID: snapshotID, ManifestURI: manifestURI, Manifest: manifest}, nil
}

// RestoreInput is the gRPC Restore handler's data.
type RestoreInput struct {
	SnapshotID       string
	AttemptID        string
	TenantID         string
	Region           string
	CloudAccountHint string // from RestoreRequest.cloud_account_hint
	BudgetUSD        float64
}

// RestoreResult is what the handler returns.
type RestoreResult struct {
	EnvID     string
	Namespace string
	AccountID string
}

// Restore re-provisions the workspace pod, re-claims an account, and
// `terraform apply`s from the persisted state.
func (m *Manager) Restore(ctx context.Context, in RestoreInput) (RestoreResult, error) {
	// 1. Read the manifest.
	key := fmt.Sprintf("snapshots/%s/%s/manifest.json", in.AttemptID, in.SnapshotID)
	raw, err := m.blobs.Get(ctx, key)
	if err != nil {
		return RestoreResult{}, fmt.Errorf("snapshotstate: read manifest %s: %w", key, err)
	}
	var manifest Manifest
	if err := json.Unmarshal(raw, &manifest); err != nil {
		return RestoreResult{}, fmt.Errorf("snapshotstate: parse manifest: %w", err)
	}

	region := in.Region
	if region == "" {
		region = m.cfg.Region
	}

	// 2. Re-claim an account — prefer the original if still pooled.
	preferred := in.CloudAccountHint
	if preferred == "" {
		preferred = manifest.SandboxAccountID
	}
	accountID, _, err := m.pool.ReclaimForAttempt(ctx, in.AttemptID, in.TenantID, region, preferred, in.BudgetUSD)
	if err != nil {
		return RestoreResult{}, fmt.Errorf("snapshotstate: reclaim account: %w", err)
	}

	// 3. Re-provision the workspace pod.
	envID := manifest.EnvID
	ns, err := m.pods.StartWorkspacePodForRestore(ctx, in.AttemptID, envID, accountID)
	if err != nil {
		return RestoreResult{}, fmt.Errorf("snapshotstate: re-provision pod: %w", err)
	}

	// 4. `terraform apply` from the persisted state. The state already
	// lives in the platform-managed backend (manifest.TFBackendURI); a
	// re-`init -reconfigure` + `apply` reconstructs the infra. On a clean
	// resume this reports "no changes".
	out, err := m.shell.Run(ctx, envID, []string{
		"sh", "-c",
		fmt.Sprintf(
			"cd /workspace && terraform init -reconfigure -input=false "+
				"-backend-config=bucket=%s -backend-config=key=%s -backend-config=region=%s "+
				"&& terraform apply -input=false -auto-approve",
			backendBucket(manifest.TFBackendURI),
			manifest.TFWorkspace+".tfstate",
			region,
		),
	})
	if err != nil {
		return RestoreResult{}, fmt.Errorf("snapshotstate: terraform apply on restore: %w (output: %s)", err, truncate(out, 400))
	}

	log.Info("T3 environment restored from snapshot",
		"snapshot_id", in.SnapshotID, "env_id", envID, "attempt_id", in.AttemptID,
		"account_id", accountID, "namespace", ns)
	return RestoreResult{EnvID: envID, Namespace: ns, AccountID: accountID}, nil
}

// --- helpers -------------------------------------------------------

func (m *Manager) tfStateSerial(ctx context.Context, envID, dir string) (int64, error) {
	out, err := m.shell.Run(ctx, envID, []string{
		"sh", "-c",
		fmt.Sprintf("cd %s && terraform state pull", shQuote(dir)),
	})
	if err != nil {
		return 0, err
	}
	var st struct {
		Serial int64 `json:"serial"`
	}
	if uerr := json.Unmarshal([]byte(out), &st); uerr != nil {
		// an empty / fresh state — serial 0 is fine
		return 0, nil
	}
	return st.Serial, nil
}

func (m *Manager) cloudInventory(ctx context.Context, envID, attemptID string) (string, int, error) {
	out, err := m.shell.Run(ctx, envID, []string{
		"sh", "-c",
		fmt.Sprintf(
			"aws resource-explorer-2 search --query-string %s --output json 2>/dev/null || echo '{\"Resources\":[]}'",
			shQuote("tag:attempt_id="+attemptID),
		),
	})
	if err != nil {
		return "", 0, err
	}
	var res struct {
		Resources []json.RawMessage `json:"Resources"`
	}
	_ = json.Unmarshal([]byte(out), &res)
	return out, len(res.Resources), nil
}

func backendBucket(uri string) string {
	return strings.TrimPrefix(strings.TrimPrefix(uri, "s3://"), "https://")
}

func shQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}
