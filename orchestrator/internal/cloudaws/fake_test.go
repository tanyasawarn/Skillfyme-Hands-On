package cloudaws

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestFakeClient_DefaultsAreBenign(t *testing.T) {
	f := NewFakeClient()
	ctx := context.Background()

	cr, err := f.AssumeRoleWithWebIdentity(ctx, "111111111111", "LearnerSandboxRole", "jwt", time.Hour)
	if err != nil || cr.AccessKeyID == "" || cr.SessionToken == "" {
		t.Fatalf("assume-role default: %+v err=%v", cr, err)
	}
	if time.Until(cr.Expiration) < 50*time.Minute {
		t.Errorf("expected ~1h expiry, got %s", time.Until(cr.Expiration))
	}

	nr, err := f.RunNuke(ctx, "111111111111")
	if err != nil || !nr.Verified || nr.ResourcesRemaining != 0 {
		t.Fatalf("nuke default should verify clean: %+v err=%v", nr, err)
	}

	rn, err := f.ApplyBaseline(ctx, "111111111111", "att", "ten")
	if err != nil || rn != "LearnerSandboxRole" {
		t.Fatalf("baseline default: %q err=%v", rn, err)
	}
}

func TestFakeClient_RecordsCallsAndScriptsOutcomes(t *testing.T) {
	f := NewFakeClient()
	ctx := context.Background()

	f.NukeFn = func(string) (NukeResult, error) {
		return NukeResult{Verified: false, ResourcesRemaining: 2}, nil
	}
	f.BaselineFn = func(string, string) (string, error) { return "", errors.New("tf failed") }

	nr, _ := f.RunNuke(ctx, "acct-1")
	if nr.Verified {
		t.Error("scripted NukeFn not used")
	}
	if _, err := f.ApplyBaseline(ctx, "acct-1", "a", "t"); err == nil {
		t.Error("scripted BaselineFn error not returned")
	}

	_ = f.PutAccountBudget(ctx, "acct-1", 25, []BudgetThreshold{{Percent: 50}, {Percent: 100}})
	_ = f.SetSkuExceptionTag(ctx, "acct-1", []string{"p4d.24xlarge"})

	if f.CallCount("RunNuke") != 1 || f.CallCount("ApplyBaseline") != 1 ||
		f.CallCount("PutAccountBudget") != 1 || f.CallCount("SetSkuExceptionTag") != 1 {
		t.Errorf("call recording wrong: %v", f.Calls)
	}
	if f.CallCount("") != 4 {
		t.Errorf("expected 4 total recorded calls, got %d (%v)", f.CallCount(""), f.Calls)
	}
}
