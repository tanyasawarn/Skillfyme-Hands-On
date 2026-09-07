package orchestrator

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/nats-io/nats.go"
	"github.com/redis/go-redis/v9"

	"github.com/tanyasawarn/skillfyme-hands-on/orchestrator/internal/accountpool"
	"github.com/tanyasawarn/skillfyme-hands-on/orchestrator/internal/cloudaws"
	"github.com/tanyasawarn/skillfyme-hands-on/orchestrator/internal/credbroker"
	"github.com/tanyasawarn/skillfyme-hands-on/orchestrator/internal/k8s"
	"github.com/tanyasawarn/skillfyme-hands-on/orchestrator/internal/reaper"
	"github.com/tanyasawarn/skillfyme-hands-on/orchestrator/internal/snapshotstate"
	"github.com/tanyasawarn/skillfyme-hands-on/orchestrator/internal/t3driver"
	"github.com/tanyasawarn/skillfyme-hands-on/orchestrator/internal/t3local"
	"github.com/tanyasawarn/skillfyme-hands-on/orchestrator/internal/wsgateway"
	pb "github.com/tanyasawarn/skillfyme-hands-on/orchestrator/pkg/pb"
)

// TestT3Lifecycle_LocalReal is the PLAN.md Phase 3 end-to-end proof for
// the "wire the T3 driver into the gRPC server" P0 item, running fully
// LOCAL-REAL:
//
//   - REAL k3s workspace pod (the compose dev cluster)
//   - REAL MinIO for the snapshot manifests (the compose minio)
//   - REAL in-pod ExecShell (SPDY exec against the real pod)
//   - FAKE AWS control plane (cloudaws.FakeClient) for STS / aws-nuke /
//     Budgets / Cost Explorer -- the one boundary that needs a real AWS
//     Organizations account.
//
// Flow: seed a fake AVAILABLE sandbox account -> Provision(TIER_T3_CLOUD_ACCOUNT)
// -> assert a real pod is Ready with a brokered creds file on disk ->
// ExecShell inside it -> Snapshot (manifest lands in MinIO, compute torn
// down) -> Restore (fresh pod, account re-claimed) -> Destroy.
//
// Skips (never fails) when the dev stack isn't reachable, same
// convention as ownership_rpc_test.go / provision_t2_live_test.go.
func TestT3Lifecycle_LocalReal(t *testing.T) {
	if os.Getenv("PE_T3_LOCAL_REAL") != "1" {
		t.Skip("set PE_T3_LOCAL_REAL=1 to run the T3 local-real lifecycle test (needs the compose dev stack: k3s + MinIO + Postgres + NATS + the t3-tools image)")
	}

	dbURL := envOr("DATABASE_URL", "postgres://practice:practice@localhost:5433/practice_engine")
	kubeconfig := envOr("KUBECONFIG", "../../../.local/k3s-output/kubeconfig.yaml")
	natsURL := envOr("NATS_URL", "nats://localhost:4222")
	minioEndpoint := envOr("S3_ENDPOINT_URL", "http://localhost:9000")
	// Defaults to the always-present linux-tools image (has a shell +
	// coreutils, which is all this RPC-path proof needs). Set
	// T3_EDITOR_IMAGE=registry:5000/practiceengine/t3-tools:v1 to exercise
	// a real in-pod `terraform` instead (that image is built from
	// orchestrator/images/t3-tools/Dockerfile).
	editorImage := envOr("T3_EDITOR_IMAGE", "registry:5000/practiceengine/linux-tools:v1")
	haveTerraform := strings.Contains(editorImage, "t3-tools")
	region := "us-east-1"

	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Minute)
	defer cancel()

	db, err := pgxpool.New(ctx, dbURL)
	if err != nil {
		t.Skipf("skipping: postgres pool: %v", err)
	}
	t.Cleanup(db.Close)
	if err := db.Ping(ctx); err != nil {
		t.Skipf("skipping: postgres unreachable: %v", err)
	}

	restConfig, err := k8s.NewRestConfig(kubeconfig)
	if err != nil {
		t.Skipf("skipping: k8s rest config: %v", err)
	}
	clientset, err := k8s.NewClientsetFromConfig(restConfig)
	if err != nil {
		t.Skipf("skipping: k8s clientset: %v", err)
	}
	if _, err := clientset.Discovery().ServerVersion(); err != nil {
		t.Skipf("skipping: k8s cluster unreachable: %v", err)
	}
	nc, err := nats.Connect(natsURL, nats.Timeout(3*time.Second))
	if err != nil {
		t.Skipf("skipping: nats unreachable: %v", err)
	}
	t.Cleanup(nc.Close)

	// --- MinIO S3 client ------------------------------------------------
	awsCfg, err := awsconfig.LoadDefaultConfig(ctx,
		awsconfig.WithRegion(region),
		awsconfig.WithCredentialsProvider(credentials.NewStaticCredentialsProvider("practice", "practice-dev-only", "")),
	)
	if err != nil {
		t.Fatalf("aws config for minio: %v", err)
	}
	s3c := s3.NewFromConfig(awsCfg, func(o *s3.Options) {
		o.BaseEndpoint = aws.String(minioEndpoint)
		o.UsePathStyle = true
	})
	bucket := "practice-snapshots-test"
	if _, err := s3c.CreateBucket(ctx, &s3.CreateBucketInput{Bucket: aws.String(bucket)}); err != nil &&
		!strings.Contains(err.Error(), "BucketAlreadyOwnedByYou") && !strings.Contains(err.Error(), "BucketAlreadyExists") {
		t.Skipf("skipping: cannot reach MinIO to create test bucket: %v", err)
	}

	// --- Server + T3 wiring (mirrors cmd/orchestrator) -----------------
	provisioner := k8s.NewProvisioner(clientset, restConfig, k8s.ProvisionerConfig{})
	rp := reaper.New(db, provisioner)
	destroyer := NewDestroyer(db, provisioner, rp, nc)
	tokens := wsgateway.NewTokenValidator("t3-test-secret")
	server := NewServer(provisioner, nil, nil, rp, db, tokens, noopIdleTracker{}, destroyer, "ws://localhost:8081", false)

	fakeAWS := cloudaws.NewFakeClient()
	// A real Redis is part of the compose dev stack; reuse it for the
	// account pool's fast-path CAS.
	rdb := redis.NewClient(&redis.Options{Addr: envOr("REDIS_ADDR", "localhost:6379")})
	t.Cleanup(func() { _ = rdb.Close() })
	if err := rdb.Ping(ctx).Err(); err != nil {
		t.Skipf("skipping: redis unreachable: %v", err)
	}
	pool := accountpool.NewManager(db, rdb, fakeAWS, noopAccountEvents{})
	brokerReg := credbroker.NewRegistry()

	acctID := "999000000001"
	if err := pool.RegisterAvailableAccount(ctx, acctID, region); err != nil {
		t.Fatalf("seed fake account: %v", err)
	}
	t.Cleanup(func() {
		_, _ = db.Exec(context.Background(), `DELETE FROM env.cloud_account WHERE aws_account_id = $1`, acctID)
	})

	attemptEnv := t3local.NewAttemptEnvMap()
	credsPath := k8s.T3DefaultCredsMountPath
	podMgr := t3local.NewLocalPodManager(provisioner, editorImage, credsPath, region, minioEndpoint, true)
	driver := t3driver.NewDriver(
		t3driver.Config{
			EditorImage:         editorImage,
			Region:              region,
			WSGatewayBaseURL:    "ws://localhost:8081",
			CredsMountPath:      credsPath,
			CredBrokerTTL:       time.Hour,
			CredRefreshFraction: 0.5,
		},
		pool, brokerReg, podMgr, tokens, fakeAWS,
		t3local.NewStaticTokenSource("", "test-client", time.Hour),
		t3local.NewFileCredsWriter(provisioner, credsPath, attemptEnv),
	)
	snapMgr := snapshotstate.NewManager(
		snapshotstate.Config{SnapshotBucketPrefix: "s3://" + bucket, TFBackendURI: "s3://" + bucket, Region: region},
		t3local.NewMinioBlobStore(s3c, bucket, "s3://"+bucket),
		t3local.NewExecShellRunner(provisioner),
		podMgr,
		t3local.NewPoolReclaimer(pool),
	)
	server.EnableT3(driver, snapMgr, attemptEnv, region, 5.0, "/workspace")

	attemptID := "bbbbbbbb-0000-0000-0000-0000000b3333"
	t.Cleanup(func() {
		_, _ = db.Exec(context.Background(), `DELETE FROM env.environment WHERE attempt_id = $1`, attemptID)
	})

	// --- 1. Provision(T3) --------------------------------------------
	presp, err := server.Provision(ctx, &pb.ProvisionRequest{
		AttemptId:     attemptID,
		BlueprintId:   "bp.project.default",
		Tier:          pb.Tier_TIER_T3_CLOUD_ACCOUNT,
		NetworkPolicy: "region=us-east-1;budget=4.50",
	})
	if err != nil {
		t.Fatalf("Provision(T3): %v", err)
	}
	if presp.Status != pb.EnvironmentStatus_ENVIRONMENT_STATUS_READY {
		t.Fatalf("Provision(T3) status = %v, want READY", presp.Status)
	}
	envID := presp.EnvironmentId
	t.Logf("T3 provisioned: env=%s", envID)

	// account is now IN_USE
	var st string
	_ = db.QueryRow(ctx, `SELECT state FROM env.cloud_account WHERE aws_account_id = $1`, acctID).Scan(&st)
	if st != "IN_USE" {
		t.Fatalf("cloud account state = %s, want IN_USE", st)
	}
	// brokered creds file exists in the pod
	credCheck, err := k8s.ExecInPod(ctx, provisioner, "env-"+envID, k8s.WorkspacePodName, k8s.WorkspaceContainerName,
		"test -f "+credsPath+"/credentials && head -1 "+credsPath+"/credentials", 20*time.Second)
	if err != nil || credCheck.ExitCode != 0 {
		t.Fatalf("brokered creds file missing in pod (exit %d, err %v): %s", credCheck.ExitCode, err, credCheck.Stderr)
	}
	if !strings.Contains(credCheck.Stdout, "[default]") {
		t.Errorf("creds file first line = %q, want the [default] profile header", credCheck.Stdout)
	}

	// --- 2. ExecShell inside the T3 pod -----------------------------
	// Proves the real ExecShell SPDY path reaches a running T3 workspace
	// pod and the brokered AWS_* env is in scope. When the image carries
	// terraform (t3-tools), also run a real `terraform init` so the
	// Snapshot step's `terraform state pull` has a state to read.
	cmd := "mkdir -p /workspace && echo 'built on T3' > /workspace/NOTES.md && " +
		"printenv AWS_SHARED_CREDENTIALS_FILE && test -f \"$AWS_SHARED_CREDENTIALS_FILE\" && echo OK-T3-EXEC"
	if haveTerraform {
		cmd = "mkdir -p /workspace && printf 'terraform {\\n  backend \"local\" {}\\n}\\n' > /workspace/main.tf && " +
			"terraform -chdir=/workspace init -input=false && echo OK-T3-EXEC"
	}
	sh, err := server.ExecShell(ctx, &pb.ExecShellRequest{
		EnvironmentId: envID,
		AttemptId:     attemptID,
		Command:       cmd,
		TimeoutMs:     120_000,
	})
	if err != nil {
		t.Fatalf("ExecShell in T3 pod: %v", err)
	}
	if !strings.Contains(sh.Stdout, "OK-T3-EXEC") {
		t.Fatalf("ExecShell in T3 pod did not report OK-T3-EXEC:\nstdout=%s\nstderr=%s", sh.Stdout, sh.Stderr)
	}

	// --- 3. Snapshot (suspend) ------------------------------------
	snap, err := server.Snapshot(ctx, &pb.SnapshotRequest{
		EnvironmentId: envID,
		AttemptId:     attemptID,
		Reason:        "suspend",
	})
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if snap.SnapshotId == "" || snap.StorageUri == "" {
		t.Fatalf("Snapshot returned empty id/uri: %+v", snap)
	}
	if snap.Manifest == nil || snap.Manifest.SandboxAccountId != acctID {
		t.Fatalf("Snapshot manifest missing/wrong account: %+v", snap.Manifest)
	}
	// compute torn down: the account is released immediately; the
	// namespace deletion is async (graceful termination), so give it a
	// bounded window to fully clear.
	_ = db.QueryRow(ctx, `SELECT state FROM env.cloud_account WHERE aws_account_id = $1`, acctID).Scan(&st)
	if st == "IN_USE" {
		t.Errorf("after suspend Snapshot the account should not be IN_USE, got %s", st)
	}
	if err := provisioner.WaitForNamespaceGone(ctx, envID, 90*time.Second); err != nil {
		t.Fatalf("waiting for suspended env namespace to clear: %v", err)
	}
	if exists, _ := provisioner.NamespaceExists(ctx, envID); exists {
		t.Errorf("after suspend Snapshot (+90s) the T3 namespace should be gone, still present")
	}
	// manifest really is in MinIO
	if _, gerr := s3c.GetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(bucket),
		Key:    aws.String(strings.TrimPrefix(snap.StorageUri, "s3://"+bucket+"/")),
	}); gerr != nil {
		t.Fatalf("snapshot manifest not readable from MinIO at %s: %v", snap.StorageUri, gerr)
	}

	// --- 4. Restore --------------------------------------------------
	rresp, err := server.Restore(ctx, &pb.RestoreRequest{
		AttemptId:        attemptID,
		SnapshotId:       snap.SnapshotId,
		BlueprintId:      "bp.project.default",
		Tier:             pb.Tier_TIER_T3_CLOUD_ACCOUNT,
		CloudAccountHint: acctID,
	})
	if err != nil {
		t.Fatalf("Restore: %v", err)
	}
	if rresp.Status != pb.EnvironmentStatus_ENVIRONMENT_STATUS_READY {
		t.Fatalf("Restore status = %v, want READY", rresp.Status)
	}
	restoredEnv := rresp.EnvironmentId
	if ok, _ := provisioner.NamespaceExists(ctx, restoredEnv); !ok {
		t.Fatalf("after Restore the workspace namespace should exist for env %s", restoredEnv)
	}

	// --- 5. Destroy -----------------------------------------------
	if _, err := server.Destroy(ctx, &pb.DestroyRequest{
		EnvironmentId: restoredEnv,
		AttemptId:     attemptID,
		Reason:        "submit",
	}); err != nil {
		t.Fatalf("Destroy: %v", err)
	}
	// namespace deletion is async — give it the bounded window to clear.
	if err := provisioner.WaitForNamespaceGone(ctx, restoredEnv, 90*time.Second); err != nil {
		t.Fatalf("waiting for destroyed env namespace to clear: %v", err)
	}
	if ok, _ := provisioner.NamespaceExists(ctx, restoredEnv); ok {
		t.Errorf("after Destroy (+90s) the workspace namespace should be gone for env %s", restoredEnv)
	}
	// account is back in the pool
	var finalState string
	_ = db.QueryRow(ctx, `SELECT state FROM env.cloud_account WHERE aws_account_id = $1`, acctID).Scan(&finalState)
	if finalState != "AVAILABLE" {
		t.Errorf("after Destroy the sandbox account should be AVAILABLE again, got %s", finalState)
	}
}

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

// noopAccountEvents satisfies accountpool.EventPublisher without a NATS
// dependency -- this test asserts DB/pod/MinIO state directly, not the
// emitted ACCOUNT_* taxonomy (that's covered in internal/accountpool).
type noopAccountEvents struct{}

func (noopAccountEvents) PublishAccountClaimed(context.Context, string, string, string)  {}
func (noopAccountEvents) PublishAccountNuked(context.Context, string, string, bool, int) {}
func (noopAccountEvents) PublishAccountQuarantined(context.Context, string, string, string, int) {
}
