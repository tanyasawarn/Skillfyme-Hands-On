package main

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/tanyasawarn/skillfyme-hands-on/orchestrator/internal/config"
	"github.com/tanyasawarn/skillfyme-hands-on/orchestrator/internal/k8s"
	orchsvc "github.com/tanyasawarn/skillfyme-hands-on/orchestrator/internal/orchestrator"
	"github.com/tanyasawarn/skillfyme-hands-on/orchestrator/internal/snapshotstate"
	"github.com/tanyasawarn/skillfyme-hands-on/orchestrator/internal/t3driver"
	"github.com/tanyasawarn/skillfyme-hands-on/orchestrator/internal/t3local"
)

// setupT3 builds the PLAN.md Phase 3 T3 tier (TIER_T3_CLOUD_ACCOUNT):
// the t3driver.Driver (account claim + STS broker + workspace pod) and
// the snapshotstate.Manager (IaC-state suspend/resume), and enables them
// on the gRPC server. No-op unless cfg.T3Enabled().
//
// In `fake` (local-real) mode every adapter is backed by REAL local
// infra: LocalPodManager -> real k3s, MinioBlobStore -> the compose
// MinIO, ExecShellRunner -> the real in-pod ExecShell path. Only the
// cloudLife.CloudAWS boundary (STS / aws-nuke / Budgets / Cost Explorer)
// is the fake, which is exactly the boundary that needs a real AWS
// Organizations account.
// terminateT3Fn is the real "force-terminate the T3 environment for this
// attempt" path, populated by setupT3 once the driver exists. main()'s
// budget-breach closure dispatches through it (late binding: the cloud
// lifecycle is wired before the driver is built).
var terminateT3Fn func(ctx context.Context, attemptID string) error

func setupT3(
	ctx context.Context,
	cfg config.Config,
	server *orchsvc.Server,
	provisioner *k8s.Provisioner,
	tokenMinter t3driver.SessionTokenMinter,
	cloudLife *CloudLifecycle,
	db *pgxpool.Pool,
	seedAccounts int,
) {
	if !cfg.T3Enabled() {
		return
	}
	if cloudLife == nil || !cloudLife.Enabled || cloudLife.Pool == nil || cloudLife.Broker == nil || cloudLife.CloudAWS == nil {
		log.Println("[main] T3 requested but the cloud-account lifecycle stack is not up — T3 will report FailedPrecondition")
		return
	}

	region := cfg.AWSRegion
	if region == "" {
		region = "us-east-1" // local-real default; the fake AWS client ignores it beyond echoing
	}

	// Seed N fake AVAILABLE accounts so a T3 Provision has something to
	// claim. Real mode never does this — accounts come from
	// `aws organizations create-account` ahead of a cohort (accountpool
	// package doc). Fake mode has no such source, so we mint placeholder
	// 12-digit ids here.
	if cfg.T3Fake() && seedAccounts > 0 {
		for i := 0; i < seedAccounts; i++ {
			acctID := fakeAccountID(i)
			if err := cloudLife.Pool.RegisterAvailableAccount(ctx, acctID, region); err != nil {
				log.Printf("[main] T3 fake account seed %s failed: %v", acctID, err)
				continue
			}
		}
		log.Printf("[main] T3 (fake): seeded %d AVAILABLE sandbox account(s) in region %s", seedAccounts, region)
	}

	// MinIO client for the snapshot blob store (reuse the same
	// S3_ENDPOINT_URL the session-recording sink uses).
	var s3Client *s3.Client
	if cfg.T3Fake() {
		awsCfg, err := awsconfig.LoadDefaultConfig(ctx,
			awsconfig.WithRegion(region),
			awsconfig.WithCredentialsProvider(credentials.NewStaticCredentialsProvider("practice", "practice-dev-only", "")),
		)
		if err != nil {
			log.Fatalf("[main] T3 (fake): loading AWS config for MinIO snapshot store: %v", err)
		}
		s3Client = s3.NewFromConfig(awsCfg, func(o *s3.Options) {
			if cfg.S3EndpointURL != "" {
				o.BaseEndpoint = aws.String(cfg.S3EndpointURL)
				o.UsePathStyle = true
			}
		})
	} else {
		awsCfg, err := awsconfig.LoadDefaultConfig(ctx, awsconfig.WithRegion(region))
		if err != nil {
			log.Fatalf("[main] T3: loading AWS config for snapshot store: %v", err)
		}
		s3Client = s3.NewFromConfig(awsCfg)
	}

	snapshotBucket := cfg.SnapshotS3Bucket
	if snapshotBucket == "" {
		snapshotBucket = "practice-snapshots"
	}
	if cfg.T3Fake() {
		ensureBucket(ctx, s3Client, snapshotBucket)
	}

	credsPath := k8s.T3DefaultCredsMountPath

	// attempt -> env map shared between the driver (writes it) and the
	// credential broker's CredsWriter (reads it).
	attemptEnv := t3local.NewAttemptEnvMap()

	podMgr := t3local.NewLocalPodManager(
		provisioner,
		cfg.T3EditorImage,
		credsPath,
		region,
		cfg.S3EndpointURL,
		cfg.T3Fake(),
	)
	credsWriter := t3local.NewFileCredsWriter(provisioner, credsPath, attemptEnv)
	tokenSource := t3local.NewStaticTokenSource("", cfg.PlatformIdPClientID, cfg.CredBrokerTTL)
	blobStore := t3local.NewMinioBlobStore(s3Client, snapshotBucket, "s3://"+snapshotBucket)
	shellRunner := t3local.NewExecShellRunner(provisioner)
	reclaimer := t3local.NewPoolReclaimer(cloudLife.Pool)

	driver := t3driver.NewDriver(
		t3driver.Config{
			EditorImage:         cfg.T3EditorImage,
			Region:              region,
			WSGatewayBaseURL:    cfg.WSGatewayBaseURL,
			CredsMountPath:      credsPath,
			CredBrokerTTL:       durOr(cfg.CredBrokerTTL, time.Hour),
			CredRefreshFraction: fracOr(cfg.CredBrokerRefreshFraction, 0.5),
		},
		cloudLife.Pool,
		cloudLife.Broker,
		podMgr,
		tokenMinter, // wsgateway.TokenValidator: Register(attemptID, envID) -> scoped session token
		cloudLife.CloudAWS,
		tokenSource,
		credsWriter,
	)

	snapMgr := snapshotstate.NewManager(
		snapshotstate.Config{
			SnapshotBucketPrefix: "s3://" + snapshotBucket,
			TFBackendURI:         "s3://" + snapshotBucket, // local-real: TF state also lands in MinIO
			Region:               region,
		},
		blobStore,
		shellRunner,
		podMgr,
		reclaimer,
	)

	server.EnableT3(driver, snapMgr, attemptEnv, region, cfg.DefaultBudgetUSD, "/workspace")

	// Wire the real budget-breach force-terminate path (main()'s closure
	// dispatches through this). Look the env row up by attempt_id and run
	// the driver's Destroy (stop broker -> release account -> delete pod).
	terminateT3Fn = func(c context.Context, attemptID string) error {
		var envID, accountID, namespace string
		err := db.QueryRow(c, `
			SELECT id, COALESCE(cloud_account_id, ''), namespace
			  FROM env.environment
			 WHERE attempt_id = $1 AND tier = 'TIER_T3_CLOUD_ACCOUNT'
			 ORDER BY ready_at DESC NULLS LAST
			 LIMIT 1`, attemptID).Scan(&envID, &accountID, &namespace)
		if err != nil {
			return fmt.Errorf("terminateT3: no T3 env for attempt %s: %w", attemptID, err)
		}
		if derr := driver.Destroy(c, attemptID, envID, accountID, namespace); derr != nil {
			return fmt.Errorf("terminateT3: driver.Destroy(attempt=%s env=%s): %w", attemptID, envID, derr)
		}
		attemptEnv.Delete(attemptID)
		_, _ = db.Exec(c, `UPDATE env.environment SET status = 'DESTROYED' WHERE id = $1`, envID)
		log.Printf("[cloud] budget breach: force-terminated T3 env=%s for attempt=%s", envID, attemptID)
		return nil
	}

	log.Printf("[main] T3 tier wired (mode=%s, region=%s, editor-image=%s, snapshot-bucket=%s)",
		cfg.CloudAccountsMode, region, cfg.T3EditorImage, snapshotBucket)
}

func fakeAccountID(i int) string {
	// 12-digit synthetic account id, deterministic per index.
	base := int64(100000000000) + int64(i)*111
	return itoa12(base)
}

func itoa12(n int64) string {
	b := []byte("000000000000")
	for i := len(b) - 1; i >= 0 && n > 0; i-- {
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b)
}

func durOr(d, def time.Duration) time.Duration {
	if d <= 0 {
		return def
	}
	return d
}

func fracOr(f, def float64) float64 {
	if f <= 0 || f >= 1 {
		return def
	}
	return f
}

// ensureBucket creates the snapshot bucket in MinIO if it doesn't exist
// (local-real convenience; the compose minio-init only makes a couple of
// fixed buckets).
func ensureBucket(ctx context.Context, cl *s3.Client, bucket string) {
	_, err := cl.HeadBucket(ctx, &s3.HeadBucketInput{Bucket: aws.String(bucket)})
	if err == nil {
		return
	}
	if _, cerr := cl.CreateBucket(ctx, &s3.CreateBucketInput{Bucket: aws.String(bucket)}); cerr != nil {
		log.Printf("[main] T3 (fake): could not create snapshot bucket %q (may already exist): %v", bucket, cerr)
	}
}
