// Package config centralizes the orchestrator's environment-variable
// configuration. PLAN.md Phase 4's K14: main.go previously read ~18 raw
// env vars via 6 ad-hoc getEnv*/os.Getenv calls scattered across its own
// body, threading each one positionally into whichever constructor
// needed it -- a genuine risk in a function this size (over a dozen
// constructor calls), since a reordered parameter list at any one call
// site silently compiles with values swapped, caught only by runtime
// misbehavior, not the compiler. One Load() call at the top of main(),
// one struct, named fields at every call site instead of positional
// primitives.
package config

import (
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/nats-io/nats.go"
)

// Config holds every environment-derived setting main.go needs. Field
// names and doc comments mirror the real env var each one comes from —
// this is a direct transcription of what main.go already read, not a
// redesign of what's configurable.
type Config struct {
	// gRPC server + WS Gateway
	GRPCPort string
	WSPort   string
	// MetricsPort serves Prometheus /metrics + /healthz (doc §11
	// observability, PLAN.md Phase 1 exit-criteria measurement:
	// time-to-ready p95, provision success rate, cost/attempt all read
	// from here). Empty disables the endpoint entirely.
	MetricsPort             string
	WSGatewayBaseURL        string
	WSGatewayAllowedOrigins []string
	WSGatewayJWTSecret      string

	// Cluster / storage
	Kubeconfig  string
	DatabaseURL string
	RedisURL    string
	NATSURL     string

	// Cost / budget (doc §13.1 exit criteria: cost/attempt < $0.08)
	DefaultBudgetUSD float64

	// Feature flags
	GVisorEnabled bool
	T2Enabled     bool

	// T2RuntimeClass is the RuntimeClass a T2 (isolated-workload) pod is
	// scheduled under. Default "sysbox-runc" (Sysbox on the shared T1
	// node pool -- real DinD/systemd/nested-k3s without a dedicated metal
	// pool or a microVM, the ₹100/user cost decision). Set to "kata" plus
	// the infra/practice-cluster/t2-nodepool-kata/ pool for hardware-VM
	// isolation once concurrent T2 volume makes that economical. Empty
	// string = no runtimeClassName set (local dev with no Sysbox degrades
	// to a plain container instead of failing to schedule).
	T2RuntimeClass string

	// Session recording (doc §5.4 asciicast-to-S3)
	RecordingS3Bucket      string
	S3EndpointURL          string
	RecordingFlushInterval time.Duration

	// Warm pool (doc §5.5)
	WarmPoolTargets      string
	WarmPoolFillInterval time.Duration

	// Phase 3 — T3 cloud account lifecycle (Stage 2.x). Everything here
	// is opt-in: when CloudAccountsEnabled is false (the default) the
	// orchestrator wires cloudaws.FakeClient and does NOT start the
	// account-pool filler / nightly sweeper / cost pollers, so the
	// service runs unchanged without a Platform AWS account. Setting
	// CLOUD_ACCOUNTS_ENABLED=true + the AWS_* / PLATFORM_* vars switches
	// every Stage 2 component to the real AWS path with no code change.
	CloudAccountsEnabled bool
	AWSRegion            string
	// PlatformAccountID is the Platform (payer-side) account that assumes
	// PlatformNukeRole in each sandbox and hosts the OIDC IdP.
	PlatformAccountID string
	// PlatformIdPURL / PlatformIdPClientID identify the platform OIDC
	// issuer the STS broker exchanges tokens against (Stage 2.1 / 1.3).
	PlatformIdPURL      string
	PlatformIdPClientID string
	// AccountPoolTargets: "region:count,region:count" warm-pool depth
	// per SCP-allowed region (D-P3-5). Empty ⇒ no filler.
	AccountPoolTargets      string
	AccountPoolFillInterval time.Duration
	CloudNukeSweepInterval  time.Duration
	CloudCostHourlyInterval time.Duration
	CloudCostDailyInterval  time.Duration
	// T3LaunchCap bounds concurrent IN_USE sandbox accounts (Stage 2.3).
	// 0 = uncapped.
	T3LaunchCap int
	// CredBrokerTTL / CredBrokerRefreshFraction control the STS broker
	// (Stage 2.1). Defaults: 1h / 0.5.
	CredBrokerTTL             time.Duration
	CredBrokerRefreshFraction float64
	// SnapshotS3Bucket is where Stage 3.3's IaC-state manifests land.
	SnapshotS3Bucket string
	// PagerWebhookURL is the PagerDuty/Opsgenie webhook the quarantine
	// pager posts to (Stage 2.2). Empty ⇒ log-only.
	PagerWebhookURL string

	// Auth (closes the access-control gap PHASE2_CLOSEOUT.md flagged)
	SharedSecret string

	// mTLS (PLAN.md Phase 2 closure item: "plaintext gRPC, protected only
	// by a shared-secret interceptor" was a named security gap). When
	// TLSEnabled is true, main.go must fail to start rather than fall
	// back to plaintext if cert loading fails -- TLSEnabled is a
	// deliberate separate flag, not "cert path set", so a deployment
	// that means to run with mTLS never silently serves plaintext because
	// of a typo'd path that happened to resolve to empty.
	TLSEnabled  bool
	TLSCertFile string
	TLSKeyFile  string
	TLSCAFile   string
}

// Load reads every setting from the process environment (via
// os.Getenv), applying the same fallback defaults main.go's own
// getEnv*/inline literals used before this. Does not call godotenv.Load
// itself -- main.go's own call to that stays first, so this only ever
// reads what's already in the process environment by the time Load()
// runs, same ordering as before.
func Load() Config {
	return Config{
		GRPCPort:                getEnv("ORCHESTRATOR_GRPC_PORT", "50051"),
		WSPort:                  getEnv("ORCHESTRATOR_WS_PORT", "8081"),
		MetricsPort:             getEnv("ORCHESTRATOR_METRICS_PORT", "9090"),
		WSGatewayBaseURL:        getEnv("WS_GATEWAY_BASE_URL", "ws://localhost:8081"),
		WSGatewayAllowedOrigins: getEnvList("WS_GATEWAY_ALLOWED_ORIGINS", nil),
		// Doc §5.4 "JWT/session auth per socket": must match Practice
		// Core's JWT_SECRET (practice-core/.env) for tokens to be
		// meaningfully signed rather than merely opaque. PLAN.md K17: this
		// previously fell back to a real-looking, committed 64-char hex
		// literal when the env var was unset -- a secret-shaped value
		// living in source control regardless of whether it was ever
		// actually "secret" in practice. No-fallback (empty string)
		// matches practice-core's own AuthModule, which already throws on
		// a missing JWT_SECRET rather than silently degrading to a shared
		// default -- and NewTokenValidator (internal/wsgateway/gateway.go)
		// already panics on an empty secret, a safety check this fallback
		// was quietly preventing from ever firing. Trade-off: local dev
		// now requires WS_GATEWAY_JWT_SECRET to be set explicitly in
		// .env (documented there, value must match practice-core/.env's
		// JWT_SECRET) instead of working with zero config.
		WSGatewayJWTSecret: getEnv("WS_GATEWAY_JWT_SECRET", ""),

		Kubeconfig:  os.Getenv("KUBECONFIG"),
		DatabaseURL: getEnv("DATABASE_URL", "postgres://practice:practice@localhost:5433/practice_engine"),
		RedisURL:    getEnv("REDIS_URL", "redis://localhost:6379"),
		NATSURL:     getEnv("NATS_URL", nats.DefaultURL),

		DefaultBudgetUSD: getEnvFloat("DEFAULT_BUDGET_USD", 0.08),

		// PLAN.md M1.1/M1.14: T1 pods should run under the gVisor
		// RuntimeClass, but hardcoding it makes every Provision() call
		// fail scheduling on a cluster without gVisor installed --
		// default false, opt in only after confirming the T1 node pool
		// actually has it.
		GVisorEnabled: getEnvBool("ORCHESTRATOR_GVISOR_ENABLED", false),
		// PLAN.md Phase 2: "Dev A should not start T2 until Phase 1's
		// reaper/teardown has been running with zero orphans for a
		// sustained period." Off by default.
		T2Enabled:      getEnvBool("ORCHESTRATOR_T2_ENABLED", false),
		T2RuntimeClass: getEnv("ORCHESTRATOR_T2_RUNTIME_CLASS", "sysbox-runc"),

		RecordingS3Bucket:      getEnv("RECORDING_S3_BUCKET", ""),
		S3EndpointURL:          getEnv("S3_ENDPOINT_URL", ""),
		RecordingFlushInterval: getEnvDuration("RECORDING_FLUSH_INTERVAL", 5*time.Second),

		WarmPoolTargets:      getEnv("WARM_POOL_TARGETS", ""),
		WarmPoolFillInterval: getEnvDuration("WARM_POOL_FILL_INTERVAL", 20*time.Second),

		// Phase 3 T3 cloud account lifecycle (Stage 2.x). Opt-in.
		CloudAccountsEnabled:      getEnvBool("CLOUD_ACCOUNTS_ENABLED", false),
		AWSRegion:                 getEnv("AWS_REGION", ""),
		PlatformAccountID:         getEnv("PLATFORM_ACCOUNT_ID", ""),
		PlatformIdPURL:            getEnv("PLATFORM_IDP_URL", ""),
		PlatformIdPClientID:       getEnv("PLATFORM_IDP_CLIENT_ID", ""),
		AccountPoolTargets:        getEnv("ACCOUNT_POOL_TARGETS", ""),
		AccountPoolFillInterval:   getEnvDuration("ACCOUNT_POOL_FILL_INTERVAL", 5*time.Minute),
		CloudNukeSweepInterval:    getEnvDuration("CLOUD_NUKE_SWEEP_INTERVAL", 24*time.Hour),
		CloudCostHourlyInterval:   getEnvDuration("CLOUD_COST_HOURLY_INTERVAL", time.Hour),
		CloudCostDailyInterval:    getEnvDuration("CLOUD_COST_DAILY_INTERVAL", 24*time.Hour),
		T3LaunchCap:               getEnvInt("T3_LAUNCH_CAP", 0),
		CredBrokerTTL:             getEnvDuration("CRED_BROKER_TTL", time.Hour),
		CredBrokerRefreshFraction: getEnvFloat("CRED_BROKER_REFRESH_FRACTION", 0.5),
		SnapshotS3Bucket:          getEnv("SNAPSHOT_S3_BUCKET", ""),
		PagerWebhookURL:           getEnv("PAGER_WEBHOOK_URL", ""),

		// Closes the access-control gap PHASE2_CLOSEOUT.md flagged:
		// every RPC previously had no caller-identity check at all.
		// Deliberately opt-in (empty secret = disabled) rather than a
		// hard requirement.
		SharedSecret: getEnv("ORCHESTRATOR_SHARED_SECRET", ""),

		// mTLS. Off by default like every other security/scale knob in
		// this codebase -- but unlike SharedSecret, once TLSEnabled=true
		// main.go treats any cert-loading failure as fatal (log.Fatalf),
		// never a silent fallback to plaintext.
		TLSEnabled:  getEnvBool("ORCHESTRATOR_TLS_ENABLED", false),
		TLSCertFile: getEnv("ORCHESTRATOR_TLS_CERT", ""),
		TLSKeyFile:  getEnv("ORCHESTRATOR_TLS_KEY", ""),
		TLSCAFile:   getEnv("ORCHESTRATOR_TLS_CA", ""),
	}
}

func getEnv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

// getEnvList parses a comma-separated env var (e.g.
// WS_GATEWAY_ALLOWED_ORIGINS=https://app.example.com,https://staging.example.com)
// into a slice, trimming whitespace and dropping empty entries.
func getEnvList(key string, fallback []string) []string {
	v := os.Getenv(key)
	if v == "" {
		return fallback
	}
	parts := strings.Split(v, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

func getEnvFloat(key string, fallback float64) float64 {
	if v := os.Getenv(key); v != "" {
		if f, err := strconv.ParseFloat(v, 64); err == nil {
			return f
		}
	}
	return fallback
}

func getEnvInt(key string, fallback int) int {
	if v := os.Getenv(key); v != "" {
		if i, err := strconv.Atoi(v); err == nil {
			return i
		}
	}
	return fallback
}

func getEnvDuration(key string, fallback time.Duration) time.Duration {
	if v := os.Getenv(key); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			return d
		}
	}
	return fallback
}

func getEnvBool(key string, fallback bool) bool {
	if v := os.Getenv(key); v != "" {
		if b, err := strconv.ParseBool(v); err == nil {
			return b
		}
	}
	return fallback
}
