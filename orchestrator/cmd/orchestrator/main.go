// Command orchestrator is Dev A's Environment Orchestrator service (doc
// §5.1, §5.5, §8.1, §8.2, PLAN.md M1.2). It serves the gRPC contract
// defined in contracts/orchestrator.proto and runs the reaper loop
// (M1.7) as a background goroutine in the same process -- doc §5.6 does
// not require the reaper to be a separate deployable, only that it runs
// independently of the request path, which an in-process ticker
// satisfies.
package main

import (
	"context"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/joho/godotenv"
	"github.com/nats-io/nats.go"
	"github.com/redis/go-redis/v9"
	"google.golang.org/grpc"
	"google.golang.org/grpc/health"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/reflection"
	metricsclient "k8s.io/metrics/pkg/client/clientset/versioned"

	"github.com/tanyasawarn/skillfyme-hands-on/orchestrator/internal/costmeter"
	"github.com/tanyasawarn/skillfyme-hands-on/orchestrator/internal/idledetect"
	"github.com/tanyasawarn/skillfyme-hands-on/orchestrator/internal/k8s"
	orchsvc "github.com/tanyasawarn/skillfyme-hands-on/orchestrator/internal/orchestrator"
	"github.com/tanyasawarn/skillfyme-hands-on/orchestrator/internal/reaper"
	"github.com/tanyasawarn/skillfyme-hands-on/orchestrator/internal/sessionbroker"
	"github.com/tanyasawarn/skillfyme-hands-on/orchestrator/internal/telemetry"
	"github.com/tanyasawarn/skillfyme-hands-on/orchestrator/internal/warmpool"
	"github.com/tanyasawarn/skillfyme-hands-on/orchestrator/internal/wsgateway"
	pb "github.com/tanyasawarn/skillfyme-hands-on/orchestrator/pkg/pb"
)

func main() {
	// Loads orchestrator/.env into the process environment if present --
	// mirrors practice-core's own .env convention (that one's read by
	// @nestjs/config automatically; Go has no built-in equivalent, hence
	// godotenv). Deliberately silent on a missing file (Err ignored, not
	// Fatal): a real deployment sets these as real env vars/secrets and
	// has no .env file at all, which must not be treated as an error.
	// Explicit `export FOO=bar` in the calling shell always wins over
	// .env either way (godotenv.Load never overwrites an already-set var).
	_ = godotenv.Load()

	grpcPort := getEnv("ORCHESTRATOR_GRPC_PORT", "50051")
	wsPort := getEnv("ORCHESTRATOR_WS_PORT", "8081")
	wsGatewayBaseURL := getEnv("WS_GATEWAY_BASE_URL", "ws://localhost:8081")
	wsGatewayAllowedOrigins := getEnvList("WS_GATEWAY_ALLOWED_ORIGINS", nil)
	kubeconfig := os.Getenv("KUBECONFIG")
	databaseURL := getEnv("DATABASE_URL", "postgres://practice:practice@localhost:5433/practice_engine")
	redisURL := getEnv("REDIS_URL", "redis://localhost:6379")
	natsURL := getEnv("NATS_URL", nats.DefaultURL)
	defaultBudgetUSD := getEnvFloat("DEFAULT_BUDGET_USD", 0.08) // doc §13.1 exit criteria: cost/attempt < $0.08
	// Doc §5.4 "JWT/session auth per socket": must match Practice Core's
	// JWT_SECRET (practice-core/.env) for tokens to be meaningfully
	// signed rather than merely opaque -- no cross-service verification
	// happens yet (only this orchestrator's own TokenValidator checks
	// its own tokens today), but sharing the secret now means Practice
	// Core can start verifying WS Gateway-issued tokens without a token
	// format change later. The literal fallback matches practice-core/
	// .env's dev value so local dev works with zero extra config; a real
	// deployment MUST set this explicitly and MUST NOT rely on the
	// fallback (same posture as JWT_SECRET's own must-be-set enforcement
	// on the Practice Core side).
	wsGatewayJWTSecret := getEnv("WS_GATEWAY_JWT_SECRET", "774454b8ca78dfe9bd1423450f26eee21c4e539460bd4393c45de387d7d3e03b")

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	restConfig, err := k8s.NewRestConfig(kubeconfig)
	if err != nil {
		log.Fatalf("k8s rest config: %v", err)
	}
	clientset, err := k8s.NewClientsetFromConfig(restConfig)
	if err != nil {
		log.Fatalf("k8s client: %v", err)
	}
	provisioner := k8s.NewProvisioner(clientset, restConfig)

	db, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		log.Fatalf("postgres pool: %v", err)
	}
	defer db.Close()
	if err := db.Ping(ctx); err != nil {
		log.Fatalf("postgres ping: %v", err)
	}

	redisOpts, err := redis.ParseURL(redisURL)
	if err != nil {
		log.Fatalf("redis url: %v", err)
	}
	rdb := redis.NewClient(redisOpts)
	if err := rdb.Ping(ctx).Err(); err != nil {
		log.Fatalf("redis ping: %v", err)
	}
	defer rdb.Close()

	nc, err := nats.Connect(natsURL)
	if err != nil {
		log.Fatalf("nats connect: %v", err)
	}
	defer nc.Close()

	warmPool := warmpool.NewManager(rdb)
	rp := reaper.New(db, provisioner)

	// Doc §5.6 Budget clock: "Immediate credential revoke, snapshot,
	// destroy, notify" at the 120% hard-stop. Wired as a plain function so
	// costmeter has no import-time dependency on the k8s package.
	destroyFn := func(ctx context.Context, envID string) {
		if err := provisioner.Destroy(ctx, envID); err != nil {
			log.Printf("[main] budget-triggered destroy failed for env=%s: %v", envID, err)
		}
	}
	meter := costmeter.NewMeter(db, destroyFn, defaultBudgetUSD)
	defer meter.Close()

	// Doc §5.6 M1.8: two-signal idle clock. metrics-server ships with k3s
	// by default (confirmed live: `kubectl get pods -A` shows it running
	// in kube-system on this project's dev cluster), so this works
	// out-of-the-box locally; a real deployment needs metrics-server (or
	// an equivalent metrics API implementation) installed on the target
	// cluster.
	metricsClient, err := metricsclient.NewForConfig(restConfig)
	if err != nil {
		log.Fatalf("metrics client: %v", err)
	}
	idleDestroyFn := func(ctx context.Context, envID, reason string) {
		if err := provisioner.Destroy(ctx, envID); err != nil {
			log.Printf("[main] idle-triggered destroy failed for env=%s: %v", envID, err)
		}
	}
	idleDetector := idledetect.New(clientset, metricsClient, idleDestroyFn)
	go idleDetector.Run(ctx)

	tokenValidator := wsgateway.NewTokenValidator(wsGatewayJWTSecret)
	server := orchsvc.NewServer(provisioner, warmPool, meter, rp, db, tokenValidator, idleDetector, wsGatewayBaseURL)

	// Session Broker (M1.5) + WS Gateway (M1.6): the PTY proxy and its
	// stateless front door. Doc §5.4: "WS Gateway (stateless)... Session
	// Broker (stateful)... TELEMETRY TAP lives in the Session Broker."
	eventSink := telemetry.NewNATSEventSink(nc)
	broker := sessionbroker.New(restConfig, clientset, eventSink, telemetry.LogRecordingSink{}, idleDetector)
	gateway := wsgateway.New(tokenValidator, broker, wsGatewayAllowedOrigins)

	wsMux := http.NewServeMux()
	wsMux.HandleFunc("/v1/env/{envID}/terminal", gateway.HandleTerminal)
	wsServer := &http.Server{Addr: ":" + wsPort, Handler: wsMux}
	go func() {
		log.Printf("[main] WS Gateway listening on :%s", wsPort)
		if err := wsServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Printf("[main] WS Gateway error: %v", err)
		}
	}()
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = wsServer.Shutdown(shutdownCtx)
	}()

	// Reaper runs for the life of the process, independent of the gRPC
	// request path (doc §5.6).
	go rp.Run(ctx)

	// Orphan sweep: doc §5.6 "hourly" cadence, run here every 10 minutes
	// for Phase 1's lower attempt volume -- tight enough to catch a
	// crashed-mid-provision namespace quickly without adding meaningful
	// K8s API load on a single-node dev cluster.
	go runOrphanSweep(ctx, rp, provisioner)

	// Warm pool filler (doc §5.5 "predicted_demand" sizing, honest fixed-
	// target subset -- see internal/warmpool's package doc). Opt-in via
	// WARM_POOL_TARGETS so running the orchestrator locally doesn't
	// silently start background-provisioning real pods unless asked;
	// WARM_POOL_FILL_INTERVAL controls how often it tops up. Format:
	// "blueprint_id:count,blueprint_id:count" e.g. "bp.linux.v1:2".
	if targets := parseWarmPoolTargets(getEnv("WARM_POOL_TARGETS", "")); len(targets) > 0 {
		fillInterval := getEnvDuration("WARM_POOL_FILL_INTERVAL", 20*time.Second)
		filler := warmpool.NewFiller(warmPool, provisioner, rp, db, targets, fillInterval)
		log.Printf("[main] warm pool filler enabled: %d target(s), interval=%s", len(targets), fillInterval)
		go filler.Run(ctx)
	}

	lis, err := net.Listen("tcp", ":"+grpcPort)
	if err != nil {
		log.Fatalf("listen: %v", err)
	}

	grpcServer := grpc.NewServer()
	pb.RegisterEnvironmentOrchestratorServer(grpcServer, server)

	healthServer := health.NewServer()
	healthpb.RegisterHealthServer(grpcServer, healthServer)
	healthServer.SetServingStatus("", healthpb.HealthCheckResponse_SERVING)

	reflection.Register(grpcServer) // grpcurl-friendly for local dev/debugging

	go func() {
		<-ctx.Done()
		log.Println("[main] shutting down gRPC server...")
		grpcServer.GracefulStop()
	}()

	log.Printf("[main] Environment Orchestrator listening on :%s (T1 driver only, doc §5.1/§13.1 Phase 1 scope)", grpcPort)
	if err := grpcServer.Serve(lis); err != nil {
		log.Fatalf("serve: %v", err)
	}
}

// Doc §5.6: "Orphan detection sweeps... hourly." Phase 1 runs it every
// 10 minutes (see the comment at the call site for why) -- and once
// immediately on startup, since a crash-restart shouldn't wait a full
// interval to catch orphans left behind by the crash.
func runOrphanSweep(ctx context.Context, rp *reaper.Reaper, provisioner *k8s.Provisioner) {
	rp.OrphanSweep(ctx, provisioner.ListManagedNamespaces)

	ticker := time.NewTicker(10 * time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			rp.OrphanSweep(ctx, provisioner.ListManagedNamespaces)
		}
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

func getEnvDuration(key string, fallback time.Duration) time.Duration {
	if v := os.Getenv(key); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			return d
		}
	}
	return fallback
}

// parseWarmPoolTargets parses WARM_POOL_TARGETS's
// "blueprint_id:count,blueprint_id:count" format into warmpool.Target
// values, resolving each blueprint's image via orchsvc.ImageForBlueprint
// so the filler provisions the exact same image Server.Provision's own
// cold-provision path would use for that blueprint. Malformed entries
// are logged and skipped rather than failing startup -- a typo in this
// env var shouldn't take down the whole orchestrator process.
func parseWarmPoolTargets(raw string) []warmpool.Target {
	if raw == "" {
		return nil
	}
	var targets []warmpool.Target
	for _, entry := range strings.Split(raw, ",") {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		parts := strings.SplitN(entry, ":", 2)
		if len(parts) != 2 {
			log.Printf("[main] WARNING: skipping malformed WARM_POOL_TARGETS entry %q (want blueprint_id:count)", entry)
			continue
		}
		blueprintID := strings.TrimSpace(parts[0])
		count, err := strconv.Atoi(strings.TrimSpace(parts[1]))
		if err != nil || count <= 0 {
			log.Printf("[main] WARNING: skipping malformed WARM_POOL_TARGETS entry %q (count must be a positive integer)", entry)
			continue
		}
		targets = append(targets, warmpool.Target{
			BlueprintID: blueprintID,
			Image:       orchsvc.ImageForBlueprint(blueprintID),
			Count:       count,
		})
	}
	return targets
}
