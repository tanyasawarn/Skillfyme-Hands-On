package audit

import "testing"

// This package's Record method writes directly to Postgres via
// *pgxpool.Pool -- no mock/fake exists anywhere in this codebase for
// pgx (confirmed: reaper, costmeter, regression, all similarly
// Postgres-coupled packages, have zero unit tests for the same reason).
// These tests cover the pure, DB-independent logic instead: the
// nullIfEmpty helper and the closed Action/Outcome value sets, which are
// both real, verifiable properties without needing a live database.

func TestNullIfEmpty_EmptyStringBecomesNil(t *testing.T) {
	if got := nullIfEmpty(""); got != nil {
		t.Errorf("expected nil for empty string, got %v", got)
	}
}

func TestNullIfEmpty_NonEmptyStringPassesThrough(t *testing.T) {
	got := nullIfEmpty("env-123")
	s, ok := got.(string)
	if !ok {
		t.Fatalf("expected a string, got %T", got)
	}
	if s != "env-123" {
		t.Errorf("expected env-123, got %q", s)
	}
}

// TestActionConstants_AllHaveDistinctValues guards against a future
// copy-paste bug where two Action constants accidentally get the same
// string value, which would make env.audit_log.action ambiguous between
// two different real operations.
func TestActionConstants_AllHaveDistinctValues(t *testing.T) {
	actions := []Action{
		ActionProvision,
		ActionDestroy,
		ActionInjectFault,
		ActionMintCredentials,
		ActionExecShell,
		ActionConnect,
		ActionCaptureBaseline,
		ActionCheckRegression,
		ActionExecValidator,
	}
	seen := map[Action]bool{}
	for _, a := range actions {
		if seen[a] {
			t.Errorf("duplicate Action value: %q", a)
		}
		seen[a] = true
		if a == "" {
			t.Error("an Action constant must never be the empty string")
		}
	}
}

func TestOutcomeConstants_AreDistinct(t *testing.T) {
	if Success == Failure {
		t.Fatal("Success and Failure must be distinct values")
	}
	if Success == "" || Failure == "" {
		t.Fatal("Outcome constants must never be the empty string")
	}
}
