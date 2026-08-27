package config

import (
	"testing"
	"time"
)

// PLAN.md Phase 4's K14: Load() must reproduce main.go's own prior
// fallback defaults exactly -- these tests pin every one of them so a
// future edit can't silently drift a default without a test failure,
// same discipline as U7-U12's tests in Phase 3.
func TestLoad_defaults(t *testing.T) {
	clearOrchestratorEnv(t)

	cfg := Load()

	if cfg.GRPCPort != "50051" {
		t.Errorf("GRPCPort = %q, want 50051", cfg.GRPCPort)
	}
	if cfg.WSPort != "8081" {
		t.Errorf("WSPort = %q, want 8081", cfg.WSPort)
	}
	if cfg.MetricsPort != "9090" {
		t.Errorf("MetricsPort = %q, want 9090", cfg.MetricsPort)
	}
	if cfg.WSGatewayBaseURL != "ws://localhost:8081" {
		t.Errorf("WSGatewayBaseURL = %q, want ws://localhost:8081", cfg.WSGatewayBaseURL)
	}
	if cfg.WSGatewayAllowedOrigins != nil {
		t.Errorf("WSGatewayAllowedOrigins = %v, want nil", cfg.WSGatewayAllowedOrigins)
	}
	// PLAN.md K17: no fallback (empty string) -- a real-looking hardcoded
	// secret used to live here. Empty means main.go's NewTokenValidator
	// call panics rather than silently signing with a committed secret;
	// see wsgateway.NewTokenValidator's own empty-string guard.
	if cfg.WSGatewayJWTSecret != "" {
		t.Errorf("WSGatewayJWTSecret = %q, want empty (no committed-secret fallback)", cfg.WSGatewayJWTSecret)
	}
	if cfg.Kubeconfig != "" {
		t.Errorf("Kubeconfig = %q, want empty (no fallback, matches main.go's raw os.Getenv)", cfg.Kubeconfig)
	}
	if cfg.DatabaseURL != "postgres://practice:practice@localhost:5433/practice_engine" {
		t.Errorf("DatabaseURL = %q, unexpected", cfg.DatabaseURL)
	}
	if cfg.RedisURL != "redis://localhost:6379" {
		t.Errorf("RedisURL = %q, want redis://localhost:6379", cfg.RedisURL)
	}
	if cfg.NATSURL != "nats://127.0.0.1:4222" {
		t.Errorf("NATSURL = %q, want nats.DefaultURL", cfg.NATSURL)
	}
	if cfg.DefaultBudgetUSD != 0.08 {
		t.Errorf("DefaultBudgetUSD = %v, want 0.08 (doc §13.1 exit criteria)", cfg.DefaultBudgetUSD)
	}
	if cfg.GVisorEnabled {
		t.Errorf("GVisorEnabled = true, want false by default")
	}
	if cfg.T2Enabled {
		t.Errorf("T2Enabled = true, want false by default")
	}
	if cfg.RecordingS3Bucket != "" {
		t.Errorf("RecordingS3Bucket = %q, want empty", cfg.RecordingS3Bucket)
	}
	if cfg.RecordingFlushInterval != 5*time.Second {
		t.Errorf("RecordingFlushInterval = %v, want 5s", cfg.RecordingFlushInterval)
	}
	if cfg.WarmPoolFillInterval != 20*time.Second {
		t.Errorf("WarmPoolFillInterval = %v, want 20s", cfg.WarmPoolFillInterval)
	}
	if cfg.SharedSecret != "" {
		t.Errorf("SharedSecret = %q, want empty (auth opt-in)", cfg.SharedSecret)
	}
}

func TestLoad_readsRealEnvVars(t *testing.T) {
	clearOrchestratorEnv(t)

	t.Setenv("ORCHESTRATOR_GRPC_PORT", "9999")
	t.Setenv("ORCHESTRATOR_GVISOR_ENABLED", "true")
	t.Setenv("DEFAULT_BUDGET_USD", "0.25")
	t.Setenv("WS_GATEWAY_ALLOWED_ORIGINS", "https://a.example.com, https://b.example.com")
	t.Setenv("RECORDING_FLUSH_INTERVAL", "10s")

	cfg := Load()

	if cfg.GRPCPort != "9999" {
		t.Errorf("GRPCPort = %q, want 9999", cfg.GRPCPort)
	}
	if !cfg.GVisorEnabled {
		t.Errorf("GVisorEnabled = false, want true")
	}
	if cfg.DefaultBudgetUSD != 0.25 {
		t.Errorf("DefaultBudgetUSD = %v, want 0.25", cfg.DefaultBudgetUSD)
	}
	wantOrigins := []string{"https://a.example.com", "https://b.example.com"}
	if len(cfg.WSGatewayAllowedOrigins) != len(wantOrigins) {
		t.Fatalf("WSGatewayAllowedOrigins = %v, want %v", cfg.WSGatewayAllowedOrigins, wantOrigins)
	}
	for i, want := range wantOrigins {
		if cfg.WSGatewayAllowedOrigins[i] != want {
			t.Errorf("WSGatewayAllowedOrigins[%d] = %q, want %q", i, cfg.WSGatewayAllowedOrigins[i], want)
		}
	}
	if cfg.RecordingFlushInterval != 10*time.Second {
		t.Errorf("RecordingFlushInterval = %v, want 10s", cfg.RecordingFlushInterval)
	}
}

// clearOrchestratorEnv unsets every env var Load() reads, so a
// developer's real .env-loaded shell doesn't leak into these tests and
// make the "defaults" assertions pass only by coincidence.
func clearOrchestratorEnv(t *testing.T) {
	t.Helper()
	vars := []string{
		"ORCHESTRATOR_GRPC_PORT", "ORCHESTRATOR_WS_PORT", "ORCHESTRATOR_METRICS_PORT",
		"WS_GATEWAY_BASE_URL",
		"WS_GATEWAY_ALLOWED_ORIGINS", "WS_GATEWAY_JWT_SECRET", "KUBECONFIG",
		"DATABASE_URL", "REDIS_URL", "NATS_URL", "DEFAULT_BUDGET_USD",
		"ORCHESTRATOR_GVISOR_ENABLED", "ORCHESTRATOR_T2_ENABLED",
		"RECORDING_S3_BUCKET", "S3_ENDPOINT_URL", "RECORDING_FLUSH_INTERVAL",
		"WARM_POOL_TARGETS", "WARM_POOL_FILL_INTERVAL", "ORCHESTRATOR_SHARED_SECRET",
	}
	for _, v := range vars {
		t.Setenv(v, "")
	}
}
