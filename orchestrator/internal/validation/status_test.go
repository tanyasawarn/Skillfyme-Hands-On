package validation

import "testing"

// PLAN.md K20: pins the exact values previously repeated as bare
// string literals ~35 times across handlers.go plus a 36th independent
// copy in server.go's ExecValidator error path.
func TestStatusConstants_MatchOriginalStringValues(t *testing.T) {
	if StatusPass != "PASS" {
		t.Errorf("StatusPass = %q, want %q", StatusPass, "PASS")
	}
	if StatusFail != "FAIL" {
		t.Errorf("StatusFail = %q, want %q", StatusFail, "FAIL")
	}
	if StatusError != "ERROR" {
		t.Errorf("StatusError = %q, want %q", StatusError, "ERROR")
	}
}
