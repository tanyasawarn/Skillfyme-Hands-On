package snapshotstate

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
)

type memBlobs struct {
	mu   sync.Mutex
	data map[string][]byte
}

func newMemBlobs() *memBlobs { return &memBlobs{data: map[string][]byte{}} }

func (b *memBlobs) Put(_ context.Context, key string, data []byte) (string, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.data[key] = append([]byte(nil), data...)
	return "s3://bucket/" + key, nil
}
func (b *memBlobs) Get(_ context.Context, key string) ([]byte, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	v, ok := b.data[key]
	if !ok {
		return nil, errors.New("not found: " + key)
	}
	return v, nil
}

type scriptedShell struct {
	// keyed by a substring of the joined argv
	responses map[string]string
	err       error
	calls     []string
}

func (s *scriptedShell) Run(_ context.Context, envID string, argv []string) (string, error) {
	joined := strings.Join(argv, " ")
	s.calls = append(s.calls, joined)
	if s.err != nil {
		return "", s.err
	}
	for k, v := range s.responses {
		if strings.Contains(joined, k) {
			return v, nil
		}
	}
	return "{}", nil
}

type fakePods struct {
	deleted    int
	restored   int
	restoreErr error
}

func (p *fakePods) DeleteWorkspacePod(context.Context, string) error {
	p.deleted++
	return nil
}
func (p *fakePods) StartWorkspacePodForRestore(_ context.Context, _, envID, _ string) (string, error) {
	if p.restoreErr != nil {
		return "", p.restoreErr
	}
	p.restored++
	return "env-" + envID, nil
}

type fakeReclaimer struct {
	lastPreferred string
	returnAccount string
	err           error
}

func (r *fakeReclaimer) ReclaimForAttempt(_ context.Context, _, _, _, preferred string, _ float64) (string, string, error) {
	r.lastPreferred = preferred
	if r.err != nil {
		return "", "", r.err
	}
	acct := r.returnAccount
	if acct == "" {
		acct = preferred
	}
	return acct, "LearnerSandboxRole", nil
}

func newManager(t *testing.T) (*Manager, *memBlobs, *scriptedShell, *fakePods, *fakeReclaimer) {
	t.Helper()
	blobs := newMemBlobs()
	shell := &scriptedShell{responses: map[string]string{
		"terraform state pull":        `{"serial": 42, "lineage": "abc"}`,
		"kubectl get all":             `{"items":[{"kind":"Pod"}]}`,
		"resource-explorer-2 search":  `{"Resources":[{"Arn":"a"},{"Arn":"b"},{"Arn":"c"}]}`,
		"terraform init -reconfigure": "Apply complete! Resources: 0 added, 0 changed, 0 destroyed.",
	}}
	pods := &fakePods{}
	pool := &fakeReclaimer{}
	m := NewManager(Config{
		SnapshotBucketPrefix: "s3://practice-snapshots",
		TFBackendURI:         "s3://practice-tf-state",
		Region:               "us-east-1",
	}, blobs, shell, pods, pool)
	return m, blobs, shell, pods, pool
}

func TestSnapshot_WritesManifestAndDestroysCompute(t *testing.T) {
	m, blobs, _, pods, _ := newManager(t)

	res, err := m.Snapshot(context.Background(), SnapshotInput{
		AttemptID:      "att-1",
		EnvID:          "env-abc",
		Namespace:      "env-env-abc",
		AccountID:      "111111111111",
		TFWorkspaceDir: "/workspace",
		DestroyCompute: true,
	})
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if res.Manifest.TFStateSerial != 42 {
		t.Errorf("expected tf serial 42, got %d", res.Manifest.TFStateSerial)
	}
	if res.Manifest.CloudResourceCount != 3 {
		t.Errorf("expected 3 cloud resources, got %d", res.Manifest.CloudResourceCount)
	}
	if res.Manifest.SandboxAccountID != "111111111111" {
		t.Errorf("manifest account id wrong: %s", res.Manifest.SandboxAccountID)
	}
	// manifest + both inventories persisted
	if len(blobs.data) != 3 {
		t.Errorf("expected 3 blobs (manifest + 2 inventories), got %d", len(blobs.data))
	}
	if pods.deleted != 1 {
		t.Errorf("DestroyCompute should have deleted the pod, deleted=%d", pods.deleted)
	}
}

func TestSnapshot_KeepsComputeWhenNotAsked(t *testing.T) {
	m, _, _, pods, _ := newManager(t)
	_, err := m.Snapshot(context.Background(), SnapshotInput{
		AttemptID: "att-1", EnvID: "env-x", Namespace: "env-env-x",
		AccountID: "1", TFWorkspaceDir: "/workspace", DestroyCompute: false,
	})
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if pods.deleted != 0 {
		t.Errorf("pod should not be deleted when DestroyCompute=false")
	}
}

func TestSnapshot_ShellFailureIsAnError(t *testing.T) {
	m, _, shell, _, _ := newManager(t)
	shell.err = errors.New("pod unreachable")
	_, err := m.Snapshot(context.Background(), SnapshotInput{
		AttemptID: "a", EnvID: "e", AccountID: "1", TFWorkspaceDir: "/w",
	})
	if err == nil {
		t.Fatal("expected Snapshot to fail when the shell runner errors")
	}
}

func TestRestore_ReclaimsAccountReprovisionsPodAndApplies(t *testing.T) {
	m, _, shell, pods, pool := newManager(t)
	// take a snapshot first
	snap, err := m.Snapshot(context.Background(), SnapshotInput{
		AttemptID: "att-9", EnvID: "env-r", Namespace: "env-env-r",
		AccountID: "222222222222", TFWorkspaceDir: "/workspace", DestroyCompute: true,
	})
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}

	res, err := m.Restore(context.Background(), RestoreInput{
		SnapshotID:       snap.SnapshotID,
		AttemptID:        "att-9",
		TenantID:         "ten-1",
		Region:           "us-east-1",
		CloudAccountHint: "222222222222",
		BudgetUSD:        20,
	})
	if err != nil {
		t.Fatalf("Restore: %v", err)
	}
	if res.EnvID != "env-r" {
		t.Errorf("restored env id wrong: %s", res.EnvID)
	}
	if pool.lastPreferred != "222222222222" {
		t.Errorf("Restore should prefer the original account, tried %s", pool.lastPreferred)
	}
	if pods.restored != 1 {
		t.Errorf("Restore should re-provision the pod once, restored=%d", pods.restored)
	}
	// a terraform apply happened on restore
	sawApply := false
	for _, c := range shell.calls {
		if strings.Contains(c, "terraform apply") {
			sawApply = true
		}
	}
	if !sawApply {
		t.Errorf("Restore should run `terraform apply`, calls=%v", shell.calls)
	}
}

func TestRestore_MissingManifestIsAnError(t *testing.T) {
	m, _, _, _, _ := newManager(t)
	_, err := m.Restore(context.Background(), RestoreInput{
		SnapshotID: "does-not-exist", AttemptID: "att-x",
	})
	if err == nil {
		t.Fatal("expected Restore to fail for a missing manifest")
	}
}
