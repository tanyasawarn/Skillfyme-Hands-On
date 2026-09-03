package credbroker

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/tanyasawarn/skillfyme-hands-on/orchestrator/internal/cloudaws"
)

type memWriter struct {
	mu     sync.Mutex
	writes []cloudaws.AssumeRoleResult
}

func (w *memWriter) Write(_ context.Context, _ string, c cloudaws.AssumeRoleResult) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.writes = append(w.writes, c)
	return nil
}
func (w *memWriter) count() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return len(w.writes)
}

type staticToken struct{ err error }

func (s staticToken) WebIdentityToken(context.Context, string) (string, error) {
	return "jwt-for-attempt", s.err
}

func TestStart_MintsAndWritesInitialCreds(t *testing.T) {
	fake := cloudaws.NewFakeClient()
	w := &memWriter{}
	b := New(Config{AttemptID: "att-1", AccountID: "111111111111", RoleName: "LearnerSandboxRole", CredTTL: time.Hour},
		fake, w, staticToken{})

	exp, err := b.Start(context.Background())
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer b.Stop()
	if w.count() != 1 {
		t.Fatalf("expected 1 initial creds write, got %d", w.count())
	}
	if time.Until(exp) < 50*time.Minute {
		t.Errorf("expected ~1h expiry, got %s", time.Until(exp))
	}
	if fake.CallCount("AssumeRoleWithWebIdentity") != 1 {
		t.Errorf("expected 1 assume-role call, got %v", fake.Calls)
	}
}

func TestStart_FailsWhenTokenSourceErrors(t *testing.T) {
	b := New(Config{AttemptID: "att-1", CredTTL: time.Hour},
		cloudaws.NewFakeClient(), &memWriter{}, staticToken{err: errors.New("idp down")})
	if _, err := b.Start(context.Background()); err == nil {
		t.Fatal("expected Start to fail when the IdP token source errors")
	}
}

func TestRefreshLoop_RefreshesBeforeExpiry(t *testing.T) {
	fake := cloudaws.NewFakeClient()
	w := &memWriter{}
	// tiny TTL so 50% refresh fires fast
	b := New(Config{AttemptID: "att-1", AccountID: "1", RoleName: "r", CredTTL: 200 * time.Millisecond, RefreshFraction: 0.5},
		fake, w, staticToken{})
	if _, err := b.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	// wait for a couple of refresh cycles
	time.Sleep(350 * time.Millisecond)
	b.Stop()
	if w.count() < 2 {
		t.Errorf("expected the refresh loop to write creds at least twice, got %d", w.count())
	}
}

func TestStop_HaltsRefreshLoop(t *testing.T) {
	fake := cloudaws.NewFakeClient()
	w := &memWriter{}
	b := New(Config{AttemptID: "att-1", AccountID: "1", RoleName: "r", CredTTL: 100 * time.Millisecond, RefreshFraction: 0.5},
		fake, w, staticToken{})
	if _, err := b.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	b.Stop()
	if !b.Stopped() {
		t.Fatal("Stopped() should be true after Stop()")
	}
	countAtStop := w.count()
	time.Sleep(300 * time.Millisecond) // several refresh intervals
	if w.count() != countAtStop {
		t.Errorf("no refresh should happen after Stop(): %d -> %d", countAtStop, w.count())
	}
}

func TestStop_Idempotent(t *testing.T) {
	b := New(Config{AttemptID: "att-1", CredTTL: time.Hour}, cloudaws.NewFakeClient(), &memWriter{}, staticToken{})
	if _, err := b.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	b.Stop()
	b.Stop() // must not panic / block
}

func TestRegistry_StopForStopsTheBroker(t *testing.T) {
	reg := NewRegistry()
	fake := cloudaws.NewFakeClient()
	w := &memWriter{}
	_, err := reg.Add(context.Background(),
		Config{AttemptID: "att-1", AccountID: "1", RoleName: "r", CredTTL: 80 * time.Millisecond, RefreshFraction: 0.5},
		fake, w, staticToken{})
	if err != nil {
		t.Fatalf("Add: %v", err)
	}
	if got := reg.Active(); len(got) != 1 {
		t.Fatalf("expected 1 active broker, got %v", got)
	}
	reg.StopFor("att-1")
	if got := reg.Active(); len(got) != 0 {
		t.Fatalf("expected 0 active brokers after StopFor, got %v", got)
	}
	countAtStop := w.count()
	time.Sleep(250 * time.Millisecond)
	if w.count() != countAtStop {
		t.Errorf("no refresh after StopFor: %d -> %d", countAtStop, w.count())
	}
}
