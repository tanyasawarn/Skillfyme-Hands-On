package cloudcost

import (
	"context"
	"testing"
	"time"

	"github.com/tanyasawarn/skillfyme-hands-on/orchestrator/internal/cloudaws"
	"github.com/tanyasawarn/skillfyme-hands-on/orchestrator/internal/testsupport"
)

func newTestPoller(t *testing.T) (*Poller, *cloudaws.FakeClient) {
	t.Helper()
	db := testsupport.NewPostgres(t)
	fake := cloudaws.NewFakeClient()
	return NewPoller(db, fake), fake
}

func seedInUse(t *testing.T, p *Poller, accountID, attemptID string) {
	t.Helper()
	_, err := p.db.Exec(context.Background(),
		`INSERT INTO env.cloud_account (aws_account_id, state, region, attempt_id, budget_usd)
		 VALUES ($1, 'IN_USE', 'us-east-1', $2, 10)
		 ON CONFLICT (aws_account_id) DO UPDATE SET state='IN_USE', attempt_id=$2, budget_usd=10`,
		accountID, attemptID)
	if err != nil {
		t.Fatalf("seed: %v", err)
	}
}

func costRow(t *testing.T, p *Poller, attemptID string) (float64, int) {
	t.Helper()
	var total float64
	var n int
	err := p.db.QueryRow(context.Background(),
		`SELECT COALESCE(SUM(cloud_cost_usd), 0), COUNT(*)
		   FROM env.usage_meter WHERE attempt_id = $1`, attemptID).Scan(&total, &n)
	if err != nil {
		t.Fatalf("costRow: %v", err)
	}
	return total, n
}

func TestPollOnce_WritesCloudCostRows(t *testing.T) {
	p, fake := newTestPoller(t)
	seedInUse(t, p, "111111111111", "aaaaaaaa-0000-0000-0000-000000000001")

	win := time.Now().Truncate(time.Hour).Add(-2 * time.Hour)
	fake.CostFn = func(accountID string, _ time.Time) ([]cloudaws.AccountCost, error) {
		return []cloudaws.AccountCost{
			{AccountID: accountID, AttemptID: "aaaaaaaa-0000-0000-0000-000000000001",
				WindowStart: win, WindowEnd: win.Add(time.Hour), AmountUSD: 0.42, Source: "ce"},
		}, nil
	}

	n, err := p.pollOnce(context.Background(), time.Now().Add(-25*time.Hour))
	if err != nil {
		t.Fatalf("pollOnce: %v", err)
	}
	if n != 1 {
		t.Fatalf("expected 1 row written, got %d", n)
	}
	total, rows := costRow(t, p, "aaaaaaaa-0000-0000-0000-000000000001")
	if rows != 1 || total < 0.41 || total > 0.43 {
		t.Errorf("usage_meter: rows=%d total=%.4f, want 1 row ~0.42", rows, total)
	}
}

func TestPollOnce_IsIdempotentPerWindow(t *testing.T) {
	p, fake := newTestPoller(t)
	seedInUse(t, p, "222222222222", "aaaaaaaa-0000-0000-0000-000000000002")
	win := time.Now().Truncate(time.Hour).Add(-3 * time.Hour)
	fake.CostFn = func(accountID string, _ time.Time) ([]cloudaws.AccountCost, error) {
		return []cloudaws.AccountCost{
			{AccountID: accountID, AttemptID: "aaaaaaaa-0000-0000-0000-000000000002",
				WindowStart: win, WindowEnd: win.Add(time.Hour), AmountUSD: 0.10},
		}, nil
	}
	for i := 0; i < 3; i++ {
		if _, err := p.pollOnce(context.Background(), time.Now().Add(-25*time.Hour)); err != nil {
			t.Fatalf("pollOnce %d: %v", i, err)
		}
	}
	_, rows := costRow(t, p, "aaaaaaaa-0000-0000-0000-000000000002")
	if rows != 1 {
		t.Errorf("re-polling the same window must not duplicate rows, got %d", rows)
	}
}

func TestReconcile_OverwritesCEWithCURFigure(t *testing.T) {
	p, fake := newTestPoller(t)
	seedInUse(t, p, "333333333333", "aaaaaaaa-0000-0000-0000-000000000003")
	win := time.Now().Truncate(24 * time.Hour).Add(-24 * time.Hour)

	// first the hourly CE poll writes an estimate
	fake.CostFn = func(accountID string, _ time.Time) ([]cloudaws.AccountCost, error) {
		return []cloudaws.AccountCost{
			{AccountID: accountID, AttemptID: "aaaaaaaa-0000-0000-0000-000000000003",
				WindowStart: win, WindowEnd: win.Add(time.Hour), AmountUSD: 1.00},
		}, nil
	}
	if _, err := p.pollOnce(context.Background(), time.Now().Add(-25*time.Hour)); err != nil {
		t.Fatalf("pollOnce: %v", err)
	}

	// then the daily CUR reconciliation corrects it to the authoritative figure
	fake.CURFn = func(_ time.Time) ([]cloudaws.AccountCost, error) {
		return []cloudaws.AccountCost{
			{AccountID: "333333333333", AttemptID: "aaaaaaaa-0000-0000-0000-000000000003",
				WindowStart: win, WindowEnd: win.Add(time.Hour), AmountUSD: 1.37, Source: "cur"},
		}, nil
	}
	n, err := p.reconcile(context.Background(), win)
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if n != 1 {
		t.Fatalf("expected 1 row reconciled, got %d", n)
	}
	total, rows := costRow(t, p, "aaaaaaaa-0000-0000-0000-000000000003")
	if rows != 1 || total < 1.36 || total > 1.38 {
		t.Errorf("CUR should overwrite the CE estimate in place: rows=%d total=%.4f, want 1 row ~1.37", rows, total)
	}
}

func TestPollOnce_SkipsAccountsWithNoAttempt(t *testing.T) {
	p, fake := newTestPoller(t)
	// an AVAILABLE account — no attempt, must be skipped
	_, err := p.db.Exec(context.Background(),
		`INSERT INTO env.cloud_account (aws_account_id, state, region) VALUES ('444444444444', 'AVAILABLE', 'us-east-1')`)
	if err != nil {
		t.Fatalf("seed: %v", err)
	}
	fake.CostFn = func(string, time.Time) ([]cloudaws.AccountCost, error) {
		t.Fatal("GetCostSince must not be called for an AVAILABLE account")
		return nil, nil
	}
	n, err := p.pollOnce(context.Background(), time.Now().Add(-25*time.Hour))
	if err != nil || n != 0 {
		t.Fatalf("expected 0 rows for an idle pool, n=%d err=%v", n, err)
	}
}
