package orchestrator

import "testing"

// parseT3Hints is the pure decode of the "region=<r>;budget=<usd>" string
// a T3 ProvisionRequest packs into network_policy (contracts/orchestrator.proto
// has no dedicated fields; a contract change is a shared-PR item). Same
// "small, security/cost-relevant, isolate cheaply" rationale as
// resolveTier's carve-out.

func TestParseT3Hints_BothKeys(t *testing.T) {
	region, budget := parseT3Hints("region=eu-west-1;budget=12.5", "us-east-1", 5)
	if region != "eu-west-1" {
		t.Errorf("region: want eu-west-1, got %q", region)
	}
	if budget != 12.5 {
		t.Errorf("budget: want 12.5, got %v", budget)
	}
}

func TestParseT3Hints_EmptyFallsBackToDefaults(t *testing.T) {
	region, budget := parseT3Hints("", "us-east-1", 5)
	if region != "us-east-1" || budget != 5 {
		t.Errorf("want defaults (us-east-1, 5), got (%q, %v)", region, budget)
	}
}

func TestParseT3Hints_PartialKeepsOtherDefault(t *testing.T) {
	region, budget := parseT3Hints("budget=9", "us-east-1", 5)
	if region != "us-east-1" {
		t.Errorf("region should keep default us-east-1, got %q", region)
	}
	if budget != 9 {
		t.Errorf("budget: want 9, got %v", budget)
	}
}

func TestParseT3Hints_InvalidBudgetIgnored(t *testing.T) {
	_, budget := parseT3Hints("budget=not-a-number", "us-east-1", 5)
	if budget != 5 {
		t.Errorf("invalid budget should fall back to default 5, got %v", budget)
	}
	_, budget = parseT3Hints("budget=-3", "us-east-1", 5)
	if budget != 5 {
		t.Errorf("negative budget should fall back to default 5, got %v", budget)
	}
}

func TestParseT3Hints_ToleratesWhitespaceAndJunk(t *testing.T) {
	region, budget := parseT3Hints("  region = ap-south-1 ; ; garbage ; budget = 20 ", "us-east-1", 5)
	if region != "ap-south-1" {
		t.Errorf("region: want ap-south-1, got %q", region)
	}
	if budget != 20 {
		t.Errorf("budget: want 20, got %v", budget)
	}
}
