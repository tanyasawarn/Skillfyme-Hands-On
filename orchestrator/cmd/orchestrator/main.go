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
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/joho/godotenv"
	"github.com/nats-io/nats.go"
	"github.com/redis/go-redis/v9"
	"google.golang.org/grpc"
	"google.golang.org/grpc/health"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/reflection"
	metricsclient "k8s.io/metrics/pkg/client/clientset/versioned"

	"github.com/tanyasawarn/skillfyme-hands-on/orchestrator/internal/config"
	"github.com/tanyasawarn/skillfyme-hands-on/orchestrator/internal/costmeter"
	"github.com/tanyasawarn/skillfyme-hands-on/orchestrator/internal/destroyreason"
	"github.com/tanyasawarn/skillfyme-hands-on/orchestrator/internal/idledetect"
	"github.com/tanyasawarn/skillfyme-hands-on/orchestrator/internal/k8s"
	"github.com/tanyasawarn/skillfyme-hands-on/orchestrator/internal/logging"
	"github.com/tanyasawarn/skillfyme-hands-on/orchestrator/internal/metrics"
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

	// PHASE1_MVP_COMPLETION.md §4.2: structured (JSON) logging. Installs a
	// slog JSON handler as the process default and bridges the std log
	// package onto it, so both the converted lifecycle/reaper/idle/budget
	// paths and any not-yet-converted log.Printf land as JSON on stderr.
	logging.Init(slog.LevelInfo)

	// PLAN.md Phase 4's K14: every setting below used to be read via its
	// own getEnv*/os.Getenv call directly in this function, then
	// threaded positionally into whichever constructor needed it -- a
	// real risk in a function with this many constructor calls, since a
	// reordered parameter list at any one call site silently compiles
	// with values swapped. One Load() call, one struct, named fields at
	// every call site below instead.
	cfg := config.Load()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	restConfig, err := k8s.NewRestConfig(cfg.Kubeconfig)
	if err != nil {
		log.Fatalf("k8s rest config: %v", err)
	}
	clientset, err := k8s.NewClientsetFromConfig(restConfig)
	if err != nil {
		log.Fatalf("k8s client: %v", err)
	}
	// PLAN.md M1.1/M1.14: T1 pods should run under the gVisor RuntimeClass
	// (manifests/t1/runtimeclass-gvisor.yaml), but hardcoding it makes
	// every Provision() call fail scheduling on a cluster without gVisor
	// installed -- default false, opt in only after confirming the T1
	// node pool actually has it (see k8s.Provisioner's gVisorEnabled doc
	// comment).
	if !cfg.GVisorEnabled {
		log.Println("[main] WARNING: ORCHESTRATOR_GVISOR_ENABLED is not set -- T1 workspace pods will NOT run under the gVisor RuntimeClass (manifests/t1/runtimeclass-gvisor.yaml). Set it to true only after confirming gVisor is actually installed on this cluster's T1 node pool.")
	}
	log.Printf("[main] T2 workloads will use RuntimeClass %q (ORCHESTRATOR_T2_RUNTIME_CLASS). Default \"sysbox-runc\" = Sysbox on the shared T1 pool; \"kata\" = dedicated microVM pool; \"\" = node default (local dev only).", cfg.T2RuntimeClass)
	provisioner := k8s.NewProvisioner(clientset, restConfig, k8s.ProvisionerConfig{
		GVisorEnabled:  cfg.GVisorEnabled,
		T2RuntimeClass: cfg.T2RuntimeClass,
	})

	db, err := pgxpool.New(ctx, cfg.DatabaseURL)
	if err != nil {
		log.Fatalf("postgres pool: %v", err)
	}
	defer db.Close()
	if err := db.Ping(ctx); err != nil {
		log.Fatalf("postgres ping: %v", err)
	}

	redisOpts, err := redis.ParseURL(cfg.RedisURL)
	if err != nil {
		log.Fatalf("redis url: %v", err)
	}
	rdb := redis.NewClient(redisOpts)
	if err := rdb.Ping(ctx).Err(); err != nil {
		log.Fatalf("redis ping: %v", err)
	}
	defer rdb.Close()

	nc, err := nats.Connect(cfg.NATSURL)
	if err != nil {
		log.Fatalf("nats connect: %v", err)
	}
	defer nc.Close()

	warmPool := warmpool.NewManager(rdb)
	rp := reaper.New(db, provisioner)

	// Doc §4.2 / contracts/events.md rule #3: every teardown path (clean
	// submit, idle/TTL, budget hard-stop, reaper force-destroy) must
	// funnel through the same logic and publish ENV_DESTROYED -- see
	// destroyer.go's doc comment for why this used to be four divergent
	// call sites instead of one. Meter/idle are attached below via
	// setters once they exist; the closures passed to them right after
	// capture destroyer by reference, so this ordering is safe (nothing
	// invokes those closures until Provision() runs, well after main's
	// setup finishes).
	destroyer := orchsvc.NewDestroyer(db, provisioner, rp, nc)
	rp.SetDestroyFunc(destroyer.Destroy)

	// Doc §5.6 Budget clock: "Immediate credential revoke, snapshot,
	// destroy, notify" at the 120% hard-stop.
	budgetDestroyFn := func(ctx context.Context, envID string) {
		if err := destroyer.Destroy(ctx, envID, destroyreason.Budget); err != nil {
			log.Printf("[main] budget-triggered destroy failed for env=%s: %v", envID, err)
		}
	}
	meter := costmeter.NewMeter(db, budgetDestroyFn, cfg.DefaultBudgetUSD)
	defer meter.Close()
	destroyer.SetMeter(meter)

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
		if err := destroyer.Destroy(ctx, envID, reason); err != nil {
			log.Printf("[main] idle-triggered destroy failed for env=%s: %v", envID, err)
		}
	}
	idleDetector := idledetect.New(clientset, metricsClient, idleDestroyFn)
	go idleDetector.Run(ctx)
	destroyer.SetIdleTracker(idleDetector)

	tokenValidator := wsgateway.NewTokenValidator(cfg.WSGatewayJWTSecret)
	// PLAN.md Phase 2: "Dev A should not start T2 until Phase 1's
	// reaper/teardown has been running with zero orphans for a sustained
	// period... a real sequencing dependency, not just advisory." Off by
	// default (fallback false) -- an operator sets this only after
	// confirming that track record for this specific deployment; see
	// Server.t2Enabled's doc comment (internal/orchestrator/server.go).
	server := orchsvc.NewServer(provisioner, warmPool, meter, rp, db, tokenValidator, idleDetector, destroyer, cfg.WSGatewayBaseURL, cfg.T2Enabled)

	// Doc §5.4 "record asciicast to S3", PLAN.md M1.5. Previously a
	// documented no-op (LogRecordingSink discarded every byte) -- now a
	// real S3-compatible client. S3_ENDPOINT_URL lets this point at MinIO
	// for local dev/docker-compose (S3-compatible, same client code) or
	// stay unset for real AWS S3. Credentials come from the standard AWS
	// credential chain (env vars, shared config file, IAM role) via
	// config.LoadDefaultConfig -- never hardcoded here.
	var recordingSink *telemetry.S3RecordingSink
	if cfg.RecordingS3Bucket == "" {
		log.Println("[main] WARNING: RECORDING_S3_BUCKET is not set -- session recordings (doc §5.4 asciicast-to-S3) will be silently dropped, same as before this feature existed. Set RECORDING_S3_BUCKET (and S3_ENDPOINT_URL for local MinIO) to enable real recording.")
	} else {
		awsCfg, err := awsconfig.LoadDefaultConfig(ctx)
		if err != nil {
			log.Fatalf("loading AWS config for session recording: %v", err)
		}
		s3Client := s3.NewFromConfig(awsCfg, func(o *s3.Options) {
			if cfg.S3EndpointURL != "" {
				o.BaseEndpoint = aws.String(cfg.S3EndpointURL)
				o.UsePathStyle = true // MinIO and most S3-compatible stores need path-style, not virtual-hosted-style, addressing
			}
		})
		recordingSink = telemetry.NewS3RecordingSink(s3Client, cfg.RecordingS3Bucket, cfg.RecordingFlushInterval)
		go recordingSink.Run(ctx)
		destroyer.SetRecording(recordingSink)
		log.Printf("[main] session recording enabled: bucket=%s flush_interval=%s", cfg.RecordingS3Bucket, cfg.RecordingFlushInterval)
	}

	// Session Broker (M1.5) + WS Gateway (M1.6): the PTY proxy and its
	// stateless front door. Doc §5.4: "WS Gateway (stateless)... Session
	// Broker (stateful)... TELEMETRY TAP lives in the Session Broker."
	eventSink := telemetry.NewNATSEventSink(nc)
	var recording sessionbroker.RecordingSink = telemetry.LogRecordingSink{}
	if recordingSink != nil {
		recording = recordingSink
	}
	broker := sessionbroker.New(restConfig, clientset, eventSink, recording, idleDetector)
	gateway := wsgateway.New(tokenValidator, broker, cfg.WSGatewayAllowedOrigins)

	wsMux := http.NewServeMux()
	wsMux.HandleFunc("/v1/env/{envID}/terminal", gateway.HandleTerminal)
	wsServer := &http.Server{Addr: ":" + cfg.WSPort, Handler: wsMux}
	go func() {
		log.Printf("[main] WS Gateway listening on :%s", cfg.WSPort)
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

	// Prometheus /metrics + a liveness /healthz, on their own port
	// (ORCHESTRATOR_METRICS_PORT, default 9090) so scraping never
	// contends with the WS data plane or the gRPC port. doc §11 /
	// PLAN.md Phase 1 exit-criteria measurement: time-to-ready p95,
	// provision success rate, cost/attempt, and the zero-orphan gate
	// are all read from here. Empty port disables the endpoint.
	// Phase 3 Stage 2: T3 cloud-account lifecycle (account pool, STS
	// broker, budget enforcement, nuke sweeper, cost pollers). No-op
	// unless CLOUD_ACCOUNTS_ENABLED=true — see internal/config for the
	// full env-var set. `terminateT3` is a stub until the T3 driver's
	// Destroy-by-attempt path lands (Stage 3.2); it logs the intent so a
	// budget breach is still visible.
	cloudLife := setupCloudLifecycle(ctx, cfg, db, rdb, nc, func(_ context.Context, attemptID string) error {
		log.Printf("[cloud] budget breach: would force-terminate T3 for attempt %s (T3 driver lands in Stage 3.2)", attemptID)
		return nil
	})
	_ = cloudLife // Provision-path wiring (LaunchCap, Pool.Claim) is Stage 3.2

	if cfg.MetricsPort != "" {
		metricsMux := http.NewServeMux()
		metricsMux.Handle("/metrics", metrics.Handler())
		metricsMux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("ok"))
		})
		cloudLife.RegisterHTTP(metricsMux) // /cloud/budget-breach (no-op when disabled)
		metricsServer := &http.Server{Addr: ":" + cfg.MetricsPort, Handler: metricsMux}
		go func() {
			log.Printf("[main] metrics + healthz listening on :%s", cfg.MetricsPort)
			if err := metricsServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
				log.Printf("[main] metrics server error: %v", err)
			}
		}()
		go func() {
			<-ctx.Done()
			shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_ = metricsServer.Shutdown(shutdownCtx)
		}()
	}

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
	if targets := parseWarmPoolTargets(cfg.WarmPoolTargets); len(targets) > 0 {
		filler := warmpool.NewFiller(warmPool, provisioner, rp, db, targets, cfg.WarmPoolFillInterval)
		log.Printf("[main] warm pool filler enabled: %d target(s), interval=%s", len(targets), cfg.WarmPoolFillInterval)
		go filler.Run(ctx)
	}

	lis, err := net.Listen("tcp", ":"+cfg.GRPCPort)
	if err != nil {
		log.Fatalf("listen: %v", err)
	}

	// Closes the access-control gap PHASE2_CLOSEOUT.md flagged and left
	// deliberately unfixed: every RPC previously had no caller-identity
	// check at all. See internal/orchestrator/auth.go's doc comment for
	// the design (shared-secret bearer token, service-level auth, not
	// per-resource authorization). Deliberately opt-in (empty secret =
	// disabled) rather than a hard requirement, matching every other
	// security/scale knob in this codebase -- but a production
	// deployment running with auth disabled is a real, visible
	// misconfiguration, not a silent one, hence the loud warning log.
	authInterceptor := orchsvc.NewAuthInterceptor(cfg.SharedSecret)
	if !authInterceptor.Enabled() {
		log.Println("[main] WARNING: ORCHESTRATOR_SHARED_SECRET is not set -- gRPC authentication is DISABLED, every RPC is reachable by any network peer that can dial this port. Set ORCHESTRATOR_SHARED_SECRET before deploying anywhere the network boundary isn't fully trusted.")
	} else {
		log.Println("[main] gRPC authentication enabled (ORCHESTRATOR_SHARED_SECRET set)")
	}

	// mTLS (PLAN.md Phase 2 closure item: plaintext gRPC protected only
	// by the shared-secret interceptor was a named security gap). Off by
	// default like every other security knob here -- but once
	// ORCHESTRATOR_TLS_ENABLED=true is set, a cert-loading failure is
	// fatal, never a silent fallback to the plaintext listener below.
	serverOpts := []grpc.ServerOption{grpc.UnaryInterceptor(authInterceptor.Unary())}
	if cfg.TLSEnabled {
		tlsCreds, err := orchsvc.ServerTLSCredentials(cfg.TLSCertFile, cfg.TLSKeyFile, cfg.TLSCAFile)
		if err != nil {
			log.Fatalf("mTLS enabled (ORCHESTRATOR_TLS_ENABLED=true) but credentials failed to load: %v", err)
		}
		serverOpts = append(serverOpts, grpc.Creds(tlsCreds))
		log.Println("[main] mTLS enabled: server requires and verifies client certificates")
	} else {
		log.Println("[main] WARNING: ORCHESTRATOR_TLS_ENABLED is not set -- gRPC transport is PLAINTEXT, protected only by the shared-secret interceptor (if enabled). Set ORCHESTRATOR_TLS_ENABLED=true with ORCHESTRATOR_TLS_CERT/_KEY/_CA before deploying anywhere the network boundary isn't fully trusted.")
	}

	grpcServer := grpc.NewServer(serverOpts...)
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

	log.Printf("[main] Environment Orchestrator listening on :%s (T1 driver only, doc §5.1/§13.1 Phase 1 scope)", cfg.GRPCPort)
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
