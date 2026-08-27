package envstatus

import "testing"

// PLAN.md K20: pins the exact values previously embedded as bare SQL
// string literals independently in server.go's Provision UPSERT,
// warmpool's Filler UPSERT, and destroyer.go's teardown UPDATE.
func TestEnvStatus_MatchesOriginalSQLLiterals(t *testing.T) {
	if Ready != "READY" {
		t.Errorf("Ready = %q, want %q", Ready, "READY")
	}
	if Destroyed != "DESTROYED" {
		t.Errorf("Destroyed = %q, want %q", Destroyed, "DESTROYED")
	}
}
