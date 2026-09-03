package main

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/nats-io/nats.go"
	"github.com/redis/go-redis/v9"

	"github.com/tanyasawarn/skillfyme-hands-on/orchestrator/internal/accountpool"
	"github.com/tanyasawarn/skillfyme-hands-on/orchestrator/internal/cloudaws"
	"github.com/tanyasawarn/skillfyme-hands-on/orchestrator/internal/cloudbudget"
	"github.com/tanyasawarn/skillfyme-hands-on/orchestrator/internal/cloudcost"
	"github.com/tanyasawarn/skillfyme-hands-on/orchestrator/internal/cloudnuke"
	"github.com/tanyasawarn/skillfyme-hands-on/orchestrator/internal/config"
	"github.com/tanyasawarn/skillfyme-hands-on/orchestrator/internal/credbroker"
)

// CloudLifecycle bundles the Phase 3 Stage 2 components so main() wires
// them with a single call. When cfg.CloudAccountsEnabled is false the
// whole thing is a no-op: no goroutines start, no HTTP routes register,
// and CloudAWS is nil.
type CloudLifecycle struct {
	Enabled     bool
	CloudAWS    cloudaws.Client
	Pool        *accountpool.Manager
	Broker      *credbroker.Registry
	Budget      *cloudbudget.Enforcer
	LaunchCap   *cloudbudget.LaunchCap
	CostPoller  *cloudcost.Poller
	NukeSweeper *cloudnuke.Sweeper
}

// setupCloudLifecycle builds the Stage 2 stack. `terminateT3` is the
// force-terminate hook the budget enforcer calls at 100% — main() passes
// the T3 driver's Destroy-by-attempt path (Stage 3.2); until that exists
// it can pass a stub that logs.
func setupCloudLifecycle(
	ctx context.Context,
	cfg config.Config,
	db *pgxpool.Pool,
	rdb *redis.Client,
	nc *nats.Conn,
	terminateT3 func(ctx context.Context, attemptID string) error,
) *CloudLifecycle {
	if !cfg.CloudAccountsEnabled {
		log.Println("[main] Phase 3 cloud-account lifecycle DISABLED (CLOUD_ACCOUNTS_ENABLED not set) — T3 cloud sandboxes unavailable")
		return &CloudLifecycle{Enabled: false}
	}

	var client cloudaws.Client
	real, err := cloudaws.NewRealClient(ctx, cloudaws.RealClientConfig{
		Region:             cfg.AWSRegion,
		PlatformAccountID:  cfg.PlatformAccountID,
		BaselineModuleDir:  getenvOr("BASELINE_MODULE_DIR", "/opt/practice/account-baseline"),
		NukeConfigTemplate: getenvOr("CLOUD_NUKE_CONFIG_TEMPLATE", "/opt/practice/nuke/aws-nuke.yaml.tmpl"),
		TFStateBucket:      getenvOr("TF_STATE_BUCKET", ""),
		CURBucket:          getenvOr("CUR_BUCKET", ""),
	})
	if err != nil {
		log.Fatalf("[main] CLOUD_ACCOUNTS_ENABLED=true but AWS client init failed: %v", err)
	}
	client = real
	log.Printf("[main] Phase 3 cloud-account lifecycle ENABLED (region=%s, platform-account=%s)", cfg.AWSRegion, cfg.PlatformAccountID)

	events := &natsAccountEvents{nc: nc}
	pool := accountpool.NewManager(db, rdb, client, events)
	if err := pool.SyncRedisFromDB(ctx); err != nil {
		log.Printf("[main] account pool: initial Redis sync failed (will retry via filler): %v", err)
	}

	brokerReg := credbroker.NewRegistry()

	revoke := func(attemptID string) { brokerReg.StopFor(attemptID) }
	emitWarn := func(ctx context.Context, attemptID, accountID string, pct int, amt, budget float64) {
		events.publish("ACCOUNT_BUDGET_WARNING", attemptID, map[string]any{
			"cloud_account_id": accountID, "percent": pct, "amount_usd": amt, "budget_usd": budget,
		})
	}
	budget := cloudbudget.NewEnforcer(db, revoke, terminateT3, emitWarn)
	launchCap := cloudbudget.NewLaunchCap(db, cfg.T3LaunchCap)

	costPoller := cloudcost.NewPoller(db, client)
	sweeper := cloudnuke.NewSweeper(db, client, &webhookPager{url: cfg.PagerWebhookURL})

	// Background loops. loop.RunTicker takes a stop channel; derive one
	// from ctx so a SIGTERM stops them cleanly.
	stop := make(chan struct{})
	go func() { <-ctx.Done(); close(stop) }()

	if targets := parseAccountPoolTargets(cfg.AccountPoolTargets); len(targets) > 0 {
		filler := accountpool.NewFiller(pool, targets)
		log.Printf("[main] account pool filler enabled: %d region target(s), interval=%s", len(targets), cfg.AccountPoolFillInterval)
		go filler.Run(stop, cfg.AccountPoolFillInterval)
	}
	log.Printf("[main] nightly account sweeper enabled, interval=%s", cfg.CloudNukeSweepInterval)
	go sweeper.Run(stop, cfg.CloudNukeSweepInterval)
	log.Printf("[main] cloud cost pollers enabled (hourly=%s, daily=%s)", cfg.CloudCostHourlyInterval, cfg.CloudCostDailyInterval)
	go costPoller.RunHourly(stop, cfg.CloudCostHourlyInterval)
	go costPoller.RunDaily(stop, cfg.CloudCostDailyInterval)

	return &CloudLifecycle{
		Enabled:     true,
		CloudAWS:    client,
		Pool:        pool,
		Broker:      brokerReg,
		Budget:      budget,
		LaunchCap:   launchCap,
		CostPoller:  costPoller,
		NukeSweeper: sweeper,
	}
}

// RegisterHTTP mounts the budget-breach endpoint the AWS Budgets → SNS
// path posts to. Called by main() on the metrics mux (same server the
// /metrics + /healthz endpoints use).
func (c *CloudLifecycle) RegisterHTTP(mux *http.ServeMux) {
	if !c.Enabled {
		return
	}
	mux.HandleFunc("/cloud/budget-breach", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "POST only", http.StatusMethodNotAllowed)
			return
		}
		b, err := parseSNSBudgetBreach(r)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if err := c.Budget.HandleBreach(r.Context(), b); err != nil {
			log.Printf("[cloud] budget-breach handler error: %v", err)
			http.Error(w, "internal", http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusOK)
	})
}

// --- NATS event adapter for accountpool.EventPublisher ----------------

type natsAccountEvents struct{ nc *nats.Conn }

func (e *natsAccountEvents) publish(eventType, attemptID string, payload map[string]any) {
	if e.nc == nil {
		return
	}
	env := map[string]any{"attempt_id": attemptID, "payload": payload}
	data, err := json.Marshal(env)
	if err != nil {
		return
	}
	_ = e.nc.Publish("env.telemetry."+eventType, data)
}

func (e *natsAccountEvents) PublishAccountClaimed(_ context.Context, attemptID, accountID, region string) {
	e.publish("ACCOUNT_CLAIMED", attemptID, map[string]any{"cloud_account_id": accountID, "region": region})
}
func (e *natsAccountEvents) PublishAccountNuked(_ context.Context, attemptID, accountID string, verified bool, rr int) {
	e.publish("ACCOUNT_NUKED", attemptID, map[string]any{
		"cloud_account_id": accountID, "verified": verified, "resources_remaining": rr,
	})
}
func (e *natsAccountEvents) PublishAccountQuarantined(_ context.Context, attemptID, accountID, reason string, rr int) {
	e.publish("ACCOUNT_QUARANTINED", attemptID, map[string]any{
		"cloud_account_id": accountID, "reason": reason, "resources_remaining": rr,
	})
}

// --- pager -----------------------------------------------------------

type webhookPager struct{ url string }

func (p *webhookPager) Page(ctx context.Context, accountID, reason, detail string) {
	log.Printf("[cloud] PAGE: account=%s reason=%s detail=%s", accountID, reason, detail)
	if p.url == "" {
		return
	}
	body, _ := json.Marshal(map[string]string{
		"summary": "T3 sandbox account " + accountID + " quarantined: " + reason,
		"detail":  detail, "account_id": accountID, "reason": reason,
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.url, strings.NewReader(string(body)))
	if err != nil {
		return
	}
	req.Header.Set("content-type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		log.Printf("[cloud] pager webhook failed: %v", err)
		return
	}
	_ = resp.Body.Close()
}

// --- helpers -------------------------------------------------------

// parseAccountPoolTargets parses "region:count,region:count".
func parseAccountPoolTargets(raw string) []accountpool.FillTarget {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	var out []accountpool.FillTarget
	for _, part := range strings.Split(raw, ",") {
		kv := strings.SplitN(strings.TrimSpace(part), ":", 2)
		if len(kv) != 2 {
			continue
		}
		n, err := strconv.Atoi(strings.TrimSpace(kv[1]))
		if err != nil || n < 0 {
			continue
		}
		out = append(out, accountpool.FillTarget{Region: strings.TrimSpace(kv[0]), Count: n})
	}
	return out
}

// parseSNSBudgetBreach extracts a cloudbudget.Breach from an SNS
// notification body. Accepts either a raw AWS Budgets notification or a
// simplified {account_id, percent, amount_usd, budget_usd} shape (what a
// small EventBridge transform can produce).
func parseSNSBudgetBreach(r *http.Request) (cloudbudget.Breach, error) {
	var msg struct {
		AccountID string  `json:"account_id"`
		Percent   int     `json:"percent"`
		AmountUSD float64 `json:"amount_usd"`
		BudgetUSD float64 `json:"budget_usd"`
		// SNS envelope
		Message string `json:"Message"`
	}
	dec := json.NewDecoder(r.Body)
	if err := dec.Decode(&msg); err != nil {
		return cloudbudget.Breach{}, err
	}
	if msg.Message != "" && msg.AccountID == "" {
		// unwrap the SNS envelope
		var inner struct {
			AccountID string  `json:"account_id"`
			Percent   int     `json:"percent"`
			AmountUSD float64 `json:"amount_usd"`
			BudgetUSD float64 `json:"budget_usd"`
		}
		if err := json.Unmarshal([]byte(msg.Message), &inner); err != nil {
			return cloudbudget.Breach{}, err
		}
		return cloudbudget.Breach{
			AccountID: inner.AccountID, Percent: inner.Percent,
			AmountUSD: inner.AmountUSD, BudgetUSD: inner.BudgetUSD,
		}, nil
	}
	return cloudbudget.Breach{
		AccountID: msg.AccountID, Percent: msg.Percent,
		AmountUSD: msg.AmountUSD, BudgetUSD: msg.BudgetUSD,
	}, nil
}

func getenvOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
