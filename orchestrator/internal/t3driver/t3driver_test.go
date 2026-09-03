package t3driver

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"

	"github.com/tanyasawarn/skillfyme-hands-on/orchestrator/internal/accountpool"
	"github.com/tanyasawarn/skillfyme-hands-on/orchestrator/internal/cloudaws"
	"github.com/tanyasawarn/skillfyme-hands-on/orchestrator/internal/credbroker"
	"github.com/tanyasawarn/skillfyme-hands-on/orchestrator/internal/testsupport"
)

type nopEvents struct{}

func (nopEvents) PublishAccountClaimed(context.Context, string, string, string)          {}
func (nopEvents) PublishAccountNuked(context.Context, string, string, bool, int)         {}
func (nopEvents) PublishAccountQuarantined(context.Context, string, string, string, int) {}

type memWriter struct{ n int }

func (w *memWriter) Write(context.Context, string, cloudaws.AssumeRoleResult) error {
	w.n++
	return nil
}

type staticToken struct{}

func (staticToken) WebIdentityToken(context.Context, string) (string, error) {
	return "jwt", nil
}

func newDriver(t *testing.T) (*Driver, *accountpool.Manager, *cloudaws.FakeClient, *FakePodManager) {
	t.Helper()
	db := testsupport.NewPostgres(t)
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatalf("miniredis: %v", err)
	}
	t.Cleanup(mr.Close)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })

	fake := cloudaws.NewFakeClient()
	pool := accountpool.NewManager(db, rdb, fake, nopEvents{})
	broker := credbroker.NewRegistry()
	pods := NewFakePodManager()

	d := NewDriver(
		Config{
			EditorImage:         "registry/openvscode:v1",
			Region:              "us-east-1",
			WSGatewayBaseURL:    "wss://gw",
			CredBrokerTTL:       time.Hour,
			CredRefreshFraction: 0.5,
		},
		pool, broker, pods, StubTokenMinter{}, fake, staticToken{}, &memWriter{},
	)
	return d, pool, fake, pods
}

func seedAvailable(t *testing.T, pool *accountpool.Manager, id string) {
	t.Helper()
	if err := pool.RegisterAvailableAccount(context.Background(), id, "us-east-1"); err != nil {
		t.Fatalf("seed: %v", err)
	}
}

func poolState(t *testing.T, pool *accountpool.Manager, id string) string {
	t.Helper()
	s, err := pool.StateOf(context.Background(), id)
	if err != nil {
		t.Fatalf("poolState: %v", err)
	}
	return s
}

func TestProvision_ClaimsAccountStartsBrokerAndPod(t *testing.T) {
	d, pool, fake, pods := newDriver(t)
	seedAvailable(t, pool, "111111111111")

	res, err := d.Provision(context.Background(), ProvisionInput{
		AttemptID: "aaaaaaaa-0000-0000-0000-000000000001",
		TenantID:  "ten-1",
		EnvID:     "env-abc",
		Region:    "us-east-1",
		BudgetUSD: 25,
	})
	if err != nil {
		t.Fatalf("Provision: %v", err)
	}
	if res.AccountID != "111111111111" || res.Namespace != "env-env-abc" {
		t.Fatalf("unexpected result: %+v", res)
	}
	if pods.Started != 1 || pods.PodCount() != 1 {
		t.Errorf("expected 1 workspace pod started, got started=%d count=%d", pods.Started, pods.PodCount())
	}
	// claim path ran baseline + budget + sku tag
	if fake.CallCount("ApplyBaseline") != 1 || fake.CallCount("PutAccountBudget") != 1 {
		t.Errorf("claim AWS calls missing: %v", fake.Calls)
	}
	if res.CredsExpireAt.Before(time.Now().Add(50 * time.Minute)) {
		t.Errorf("expected ~1h creds expiry, got %s", time.Until(res.CredsExpireAt))
	}
}

func TestProvision_PodFailureReleasesAccountAndStopsBroker(t *testing.T) {
	d, pool, _, pods := newDriver(t)
	seedAvailable(t, pool, "222222222222")
	pods.StartErr = errors.New("pod scheduling failed")

	_, err := d.Provision(context.Background(), ProvisionInput{
		AttemptID: "aaaaaaaa-0000-0000-0000-000000000002",
		TenantID:  "t", EnvID: "env-x", Region: "us-east-1", BudgetUSD: 10,
	})
	if err == nil {
		t.Fatal("expected Provision to fail when the pod won't start")
	}
	// account must not be stuck IN_USE
	st := poolState(t, pool, "222222222222")
	if st == "IN_USE" {
		t.Errorf("account left IN_USE after a failed provision (state=%s)", st)
	}
	if len(d.broker.Active()) != 0 {
		t.Errorf("broker not stopped after failed provision: %v", d.broker.Active())
	}
}

func TestConnect_ReturnsProxiedURLsWithSessionToken(t *testing.T) {
	d, _, _, _ := newDriver(t)
	c, err := d.Connect(context.Background(), "aaaaaaaa-0000-0000-0000-000000000003", "env-yz")
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	if c.SessionToken == "" {
		t.Fatal("expected a session token")
	}
	for _, u := range []string{c.EditorURL, c.TerminalWSURL} {
		if u[:5] != "wss:/" {
			t.Errorf("expected a wss:// URL through the gateway, got %s", u)
		}
		if !contains(u, "session="+c.SessionToken) {
			t.Errorf("URL %s missing the session token", u)
		}
	}
}

func TestDestroy_StopsBrokerReleasesAccountDeletesPod(t *testing.T) {
	d, pool, fake, pods := newDriver(t)
	seedAvailable(t, pool, "333333333333")
	res, err := d.Provision(context.Background(), ProvisionInput{
		AttemptID: "aaaaaaaa-0000-0000-0000-000000000004",
		TenantID:  "t", EnvID: "env-d", Region: "us-east-1", BudgetUSD: 10,
	})
	if err != nil {
		t.Fatalf("Provision: %v", err)
	}
	fake.NukeFn = func(string) (cloudaws.NukeResult, error) {
		return cloudaws.NukeResult{Verified: true}, nil
	}

	if err := d.Destroy(context.Background(), "aaaaaaaa-0000-0000-0000-000000000004", "env-d", res.AccountID, res.Namespace); err != nil {
		t.Fatalf("Destroy: %v", err)
	}
	if len(d.broker.Active()) != 0 {
		t.Errorf("broker still active after Destroy: %v", d.broker.Active())
	}
	if pods.Deleted != 1 || pods.PodCount() != 0 {
		t.Errorf("workspace pod not deleted: deleted=%d count=%d", pods.Deleted, pods.PodCount())
	}
	if poolState(t, pool, "333333333333") != "AVAILABLE" {
		t.Errorf("account not released to AVAILABLE after Destroy")
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
