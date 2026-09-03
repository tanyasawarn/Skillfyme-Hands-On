package t3driver

import (
	"context"
	"fmt"
	"sync"
)

// FakePodManager is an in-memory PodManager for tests.
type FakePodManager struct {
	mu       sync.Mutex
	pods     map[string]WorkspacePod // namespace -> pod
	StartErr error
	Started  int
	Deleted  int
}

func NewFakePodManager() *FakePodManager {
	return &FakePodManager{pods: map[string]WorkspacePod{}}
}

func (f *FakePodManager) StartWorkspacePod(_ context.Context, in StartPodInput) (WorkspacePod, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.StartErr != nil {
		return WorkspacePod{}, f.StartErr
	}
	ns := "env-" + in.EnvID
	pod := WorkspacePod{
		Namespace: ns,
		PodName:   "workspace",
		EditorURL: "http://127.0.0.1:3000",
	}
	f.pods[ns] = pod
	f.Started++
	return pod, nil
}

func (f *FakePodManager) DeleteWorkspacePod(_ context.Context, namespace string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.pods, namespace)
	f.Deleted++
	return nil
}

func (f *FakePodManager) PodCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.pods)
}

// StubTokenMinter returns a deterministic token.
type StubTokenMinter struct{ Err error }

func (s StubTokenMinter) Register(attemptID, envID string) (string, error) {
	if s.Err != nil {
		return "", s.Err
	}
	return fmt.Sprintf("sess-%s-%s", attemptID[:min(8, len(attemptID))], envID[:min(8, len(envID))]), nil
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
