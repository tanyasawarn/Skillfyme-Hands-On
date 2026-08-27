package fixture

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/tanyasawarn/skillfyme-hands-on/orchestrator/internal/k8s"
)

// fakeTracker is an in-memory AppliedTracker for tests -- doesn't need
// Postgres, mirrors PostgresFixtureTracker's contract exactly.
type fakeTracker struct {
	applied map[string]Checksum // key: envID+"|"+fixtureID
}

func newFakeTracker() *fakeTracker {
	return &fakeTracker{applied: map[string]Checksum{}}
}

func (t *fakeTracker) key(envID, fixtureID string) string { return envID + "|" + fixtureID }

func (t *fakeTracker) IsApplied(ctx context.Context, envID, fixtureID string, checksum Checksum) (bool, error) {
	found, ok := t.applied[t.key(envID, fixtureID)]
	if !ok {
		return false, nil
	}
	return found == checksum, nil
}

func (t *fakeTracker) MarkApplied(ctx context.Context, envID, fixtureID string, checksum Checksum) error {
	t.applied[t.key(envID, fixtureID)] = checksum
	return nil
}

func resetRegistryForTest() (restore func()) {
	old := registry
	oldChecksums := checksums
	registry = map[string]Handler{}
	checksums = map[string]Checksum{}
	return func() {
		registry = old
		checksums = oldChecksums
	}
}

func TestApply_ReturnsErrNoHandlerForUnknownFixture(t *testing.T) {
	defer resetRegistryForTest()()
	err := Apply(context.Background(), &k8s.Provisioner{}, newFakeTracker(), "env-1", "ns-1", "fx.does-not-exist.v1")
	var noHandler ErrNoHandler
	if !errors.As(err, &noHandler) {
		t.Fatalf("expected ErrNoHandler, got %v", err)
	}
	if noHandler.FixtureID != "fx.does-not-exist.v1" {
		t.Errorf("expected FixtureID=fx.does-not-exist.v1, got %s", noHandler.FixtureID)
	}
}

func TestApply_CallsHandlerOnFirstApplication(t *testing.T) {
	defer resetRegistryForTest()()
	called := false
	register("fx.test.v1", func(ctx context.Context, provisioner *k8s.Provisioner, envID, namespace string) error {
		called = true
		return nil
	})

	tracker := newFakeTracker()
	if err := Apply(context.Background(), &k8s.Provisioner{}, tracker, "env-1", "ns-1", "fx.test.v1"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !called {
		t.Fatal("expected handler to be called")
	}
}

// TestApply_SkipsAlreadyAppliedFixture is the "idempotent" half of doc
// §5.5 step 3: a fixture already recorded as applied (same checksum)
// must not run its handler again on a retried Provision() call.
func TestApply_SkipsAlreadyAppliedFixture(t *testing.T) {
	defer resetRegistryForTest()()
	callCount := 0
	register("fx.test.v1", func(ctx context.Context, provisioner *k8s.Provisioner, envID, namespace string) error {
		callCount++
		return nil
	})
	registerChecksum("fx.test.v1", "v1")

	tracker := newFakeTracker()
	if err := Apply(context.Background(), &k8s.Provisioner{}, tracker, "env-1", "ns-1", "fx.test.v1"); err != nil {
		t.Fatalf("first apply: unexpected error: %v", err)
	}
	if err := Apply(context.Background(), &k8s.Provisioner{}, tracker, "env-1", "ns-1", "fx.test.v1"); err != nil {
		t.Fatalf("second apply: unexpected error: %v", err)
	}
	if callCount != 1 {
		t.Errorf("expected handler called exactly once (second call skipped as already-applied), got %d calls", callCount)
	}
}

// TestApply_ReappliesWhenChecksumChanges is the "checksummed" half: a
// fixture whose registered checksum differs from what's recorded as
// applied must re-run, not be silently skipped -- doc §5.5 step 3's own
// wording names checksumming as the mechanism precisely so a changed
// fixture implementation doesn't get silently skipped forever.
func TestApply_ReappliesWhenChecksumChanges(t *testing.T) {
	defer resetRegistryForTest()()
	callCount := 0
	register("fx.test.v1", func(ctx context.Context, provisioner *k8s.Provisioner, envID, namespace string) error {
		callCount++
		return nil
	})

	tracker := newFakeTracker()
	registerChecksum("fx.test.v1", "v1")
	if err := Apply(context.Background(), &k8s.Provisioner{}, tracker, "env-1", "ns-1", "fx.test.v1"); err != nil {
		t.Fatalf("first apply: unexpected error: %v", err)
	}

	registerChecksum("fx.test.v1", "v2") // simulates a handler implementation change
	if err := Apply(context.Background(), &k8s.Provisioner{}, tracker, "env-1", "ns-1", "fx.test.v1"); err != nil {
		t.Fatalf("second apply: unexpected error: %v", err)
	}
	if callCount != 2 {
		t.Errorf("expected handler called twice (checksum changed, must re-apply), got %d calls", callCount)
	}
}

func TestApply_PropagatesHandlerError(t *testing.T) {
	defer resetRegistryForTest()()
	register("fx.test.v1", func(ctx context.Context, provisioner *k8s.Provisioner, envID, namespace string) error {
		return errors.New("real failure")
	})

	err := Apply(context.Background(), &k8s.Provisioner{}, newFakeTracker(), "env-1", "ns-1", "fx.test.v1")
	if err == nil {
		t.Fatal("expected error to propagate")
	}
	var noHandler ErrNoHandler
	if errors.As(err, &noHandler) {
		t.Fatal("a real handler failure must not be mistaken for ErrNoHandler")
	}
}

// TestApplyAll_RunsInOrder confirms doc §5.5 step 3's "ordered"
// requirement.
func TestApplyAll_RunsInOrder(t *testing.T) {
	defer resetRegistryForTest()()
	var order []string
	register("fx.first.v1", func(ctx context.Context, provisioner *k8s.Provisioner, envID, namespace string) error {
		order = append(order, "first")
		return nil
	})
	register("fx.second.v1", func(ctx context.Context, provisioner *k8s.Provisioner, envID, namespace string) error {
		order = append(order, "second")
		return nil
	})

	err := ApplyAll(context.Background(), &k8s.Provisioner{}, newFakeTracker(), "env-1", "ns-1", []string{"fx.first.v1", "fx.second.v1"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(order) != 2 || order[0] != "first" || order[1] != "second" {
		t.Errorf("expected [first, second], got %v", order)
	}
}

// TestApplyAll_UnimplementedFixtureDoesNotBlockLaterOnes is the fix for
// a real bug caught during review: an earlier version of this function
// stopped the whole batch at the first ErrNoHandler, meaning one
// unimplemented fixture in an activity's seed: list would silently
// prevent every fixture listed AFTER it from ever applying, even though
// each is independent, correctly-implemented, content-authored fixture.
func TestApplyAll_UnimplementedFixtureDoesNotBlockLaterOnes(t *testing.T) {
	defer resetRegistryForTest()()
	secondRan := false
	// fx.missing.v1 deliberately never registered.
	register("fx.second.v1", func(ctx context.Context, provisioner *k8s.Provisioner, envID, namespace string) error {
		secondRan = true
		return nil
	})

	err := ApplyAll(context.Background(), &k8s.Provisioner{}, newFakeTracker(), "env-1", "ns-1", []string{"fx.missing.v1", "fx.second.v1"})
	if err == nil {
		t.Fatal("expected an error reporting the missing fixture")
	}
	var noHandler ErrNoHandler
	if !errors.As(err, &noHandler) {
		t.Fatalf("expected ErrNoHandler, got %v", err)
	}
	if !secondRan {
		t.Fatal("expected fx.second.v1 to still run despite fx.missing.v1 being unimplemented")
	}
}

// TestApplyAll_RealHandlerFailureStopsTheBatch is the converse: unlike
// ErrNoHandler, a REAL failure (a fixture that IS implemented but broke)
// must stop the batch immediately -- a later fixture may depend on the
// failed one's state actually having been established.
func TestApplyAll_RealHandlerFailureStopsTheBatch(t *testing.T) {
	defer resetRegistryForTest()()
	secondRan := false
	register("fx.first.v1", func(ctx context.Context, provisioner *k8s.Provisioner, envID, namespace string) error {
		return errors.New("real failure")
	})
	register("fx.second.v1", func(ctx context.Context, provisioner *k8s.Provisioner, envID, namespace string) error {
		secondRan = true
		return nil
	})

	err := ApplyAll(context.Background(), &k8s.Provisioner{}, newFakeTracker(), "env-1", "ns-1", []string{"fx.first.v1", "fx.second.v1"})
	if err == nil {
		t.Fatal("expected the real failure to propagate")
	}
	if secondRan {
		t.Fatal("expected fx.second.v1 NOT to run after fx.first.v1's real failure")
	}
}

func TestApplyAll_MissingFixtureListNamesEveryMissingID(t *testing.T) {
	defer resetRegistryForTest()()
	err := ApplyAll(context.Background(), &k8s.Provisioner{}, newFakeTracker(), "env-1", "ns-1", []string{"fx.missing-a.v1", "fx.missing-b.v1"})
	if err == nil {
		t.Fatal("expected an error")
	}
	if !strings.Contains(err.Error(), "fx.missing-a.v1") || !strings.Contains(err.Error(), "fx.missing-b.v1") {
		t.Errorf("expected both missing fixture ids named in the error, got: %v", err)
	}
}
