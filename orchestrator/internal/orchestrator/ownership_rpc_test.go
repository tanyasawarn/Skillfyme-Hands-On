package orchestrator

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/nats-io/nats.go"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"

	"github.com/tanyasawarn/skillfyme-hands-on/orchestrator/internal/k8s"
	"github.com/tanyasawarn/skillfyme-hands-on/orchestrator/internal/reaper"
	"github.com/tanyasawarn/skillfyme-hands-on/orchestrator/internal/wsgateway"
	pb "github.com/tanyasawarn/skillfyme-hands-on/orchestrator/pkg/pb"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// This file is the RPC-handler half of Section 3 (PLAN_RPC_AUTHZ.md)'s
// coverage for the 5 newly ownership-checked RPCs (Connect, Destroy,
// MintValidatorCredentials, ExecValidator, ExecShell). Every other test
// in this package tests pure functions only (see resolveTier's and
// checkEnvironmentOwnership's own doc comments) because *Server has
// never had test infrastructure -- it genuinely requires a live
// Postgres, a live K8s cluster (Provisioner is hardwired to a concrete
// *kubernetes.Clientset, not the kubernetes.Interface a fake clientset
// satisfies -- confirmed by reading provision.go before writing this),
// and live Redis/NATS for the full server wiring main.go does.
//
// Rather than mock any of that (this package has never had a mocking
// library and introducing one for a single test file was rejected in
// favor of testing the real thing), this file builds a real *Server
// wired almost identically to cmd/orchestrator/main.go, against
// whatever DATABASE_URL/KUBECONFIG/REDIS_URL/NATS_URL this environment
// already has running (the project's own docker-compose dev stack).
// Skips gracefully (t.Skip, not t.Fatal) if that infra isn't reachable,
// so `go test ./...` still passes in an environment without the dev
// stack up -- this is deliberately the first test in this repo with
// that requirement, so it must not become a silent CI trap.
//
// Scope: these tests prove the OWNERSHIP CHECK fires correctly for each
// RPC (mismatch -> PermissionDenied, empty attempt_id -> rejected,
// matching attempt_id -> ownership check passes and the RPC proceeds
// past it). They do not assert full RPC functional correctness beyond
// that boundary -- ExecValidator/ExecShell's "matching attempt" case in
// particular only proves the call reaches real pod-exec logic (no
// workspace pod is stood up here, so a downstream Internal error from
// that layer is expected and asserted to NOT be PermissionDenied,
// rather than asserting success).

func setupOwnershipTestServer(t *testing.T) (*Server, *pgxpool.Pool) {
	t.Helper()

	dbURL := os.Getenv("DATABASE_URL")
	if dbURL == "" {
		dbURL = "postgres://practice:practice@localhost:5433/practice_engine"
	}
	kubeconfig := os.Getenv("KUBECONFIG")
	if kubeconfig == "" {
		// go test's working directory is this package
		// (orchestrator/internal/orchestrator), so the repo-root-relative
		// path from orchestrator/.env.example needs one extra ../ here.
		kubeconfig = "../../../.local/k3s-output/kubeconfig.yaml"
	}
	natsURL := os.Getenv("NATS_URL")
	if natsURL == "" {
		natsURL = "nats://localhost:4222"
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	db, err := pgxpool.New(ctx, dbURL)
	if err != nil {
		t.Skipf("skipping: postgres pool: %v", err)
	}
	if err := db.Ping(ctx); err != nil {
		db.Close()
		t.Skipf("skipping: postgres unreachable (dev stack not running?): %v", err)
	}

	restConfig, err := k8s.NewRestConfig(kubeconfig)
	if err != nil {
		db.Close()
		t.Skipf("skipping: k8s rest config: %v", err)
	}
	clientset, err := k8s.NewClientsetFromConfig(restConfig)
	if err != nil {
		db.Close()
		t.Skipf("skipping: k8s clientset: %v", err)
	}
	if _, err := clientset.Discovery().ServerVersion(); err != nil {
		db.Close()
		t.Skipf("skipping: k8s cluster unreachable (dev stack not running?): %v", err)
	}

	nc, err := nats.Connect(natsURL, nats.Timeout(3*time.Second))
	if err != nil {
		db.Close()
		t.Skipf("skipping: nats unreachable (dev stack not running?): %v", err)
	}
	t.Cleanup(nc.Close)

	provisioner := k8s.NewProvisioner(clientset, restConfig, k8s.ProvisionerConfig{})
	rp := reaper.New(db, provisioner)
	destroyer := NewDestroyer(db, provisioner, rp, nc)
	tokens := wsgateway.NewTokenValidator("test-only-secret-not-used-for-real-auth")

	server := NewServer(provisioner, nil, nil, rp, db, tokens, noopIdleTracker{}, destroyer, "ws://localhost:8081", false)

	t.Cleanup(func() { db.Close() })
	return server, db
}

type noopIdleTracker struct{}

func (noopIdleTracker) Track(envID string, idleTimeout time.Duration, cpuLimitMilli int64) {}
func (noopIdleTracker) Untrack(envID string)                                               {}

// seedOwnedEnvironment creates a real K8s namespace (so requireEnvironment's
// NamespaceExists passes) and a matching env.environment row (so the
// ownership check's DB lookup finds a real owner) for a fresh envID/
// ownerAttemptID pair, and registers cleanup for both.
func seedOwnedEnvironment(t *testing.T, ctx context.Context, db *pgxpool.Pool, clientset *kubernetes.Clientset) (envID, ownerAttemptID string) {
	t.Helper()
	envID = uuid.New().String()
	ownerAttemptID = uuid.New().String()
	ns := k8s.NamespaceForEnv(envID)

	_, err := clientset.CoreV1().Namespaces().Create(ctx, &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{Name: ns},
	}, metav1.CreateOptions{})
	if err != nil {
		t.Fatalf("creating test namespace %s: %v", ns, err)
	}
	t.Cleanup(func() {
		_ = clientset.CoreV1().Namespaces().Delete(context.Background(), ns, metav1.DeleteOptions{})
	})

	_, err = db.Exec(ctx, `
		INSERT INTO env.environment (id, attempt_id, status, tier, blueprint_id, namespace)
		VALUES ($1, $2, 'READY', 'T1_SHARED_CONTAINER', 'test-blueprint', $3)
	`, envID, ownerAttemptID, ns)
	if err != nil {
		t.Fatalf("seeding env.environment row for %s: %v", envID, err)
	}
	t.Cleanup(func() {
		_, _ = db.Exec(context.Background(), `DELETE FROM env.environment WHERE id = $1`, envID)
	})

	return envID, ownerAttemptID
}

func TestConnect_OwnershipEnforced(t *testing.T) {
	server, db := setupOwnershipTestServer(t)
	ctx := context.Background()
	envID, ownerAttemptID := seedOwnedEnvironment(t, ctx, db, server.provisioner.Clientset())
	strangerAttemptID := uuid.New().String()

	t.Run("mismatched attempt denied", func(t *testing.T) {
		_, err := server.Connect(ctx, &pb.ConnectRequest{EnvironmentId: envID, AttemptId: strangerAttemptID})
		if err == nil {
			t.Fatal("SECURITY REGRESSION: expected PermissionDenied for a non-owning attempt_id, got success")
		}
		if status.Code(err) != codes.PermissionDenied {
			t.Errorf("expected PermissionDenied, got %v (%v)", status.Code(err), err)
		}
	})

	t.Run("empty attempt_id rejected", func(t *testing.T) {
		_, err := server.Connect(ctx, &pb.ConnectRequest{EnvironmentId: envID, AttemptId: ""})
		if err == nil {
			t.Fatal("SECURITY REGRESSION: expected rejection for empty attempt_id, got success")
		}
		if status.Code(err) != codes.PermissionDenied && status.Code(err) != codes.InvalidArgument {
			t.Errorf("expected PermissionDenied or InvalidArgument for empty attempt_id, got %v (%v)", status.Code(err), err)
		}
	})

	t.Run("matching attempt allowed", func(t *testing.T) {
		resp, err := server.Connect(ctx, &pb.ConnectRequest{EnvironmentId: envID, AttemptId: ownerAttemptID})
		if err != nil {
			t.Fatalf("expected success for the owning attempt, got: %v", err)
		}
		if resp.TerminalWsUrl == "" {
			t.Error("expected a non-empty terminal WS URL for a successful Connect")
		}
	})
}

func TestDestroy_OwnershipEnforced(t *testing.T) {
	server, db := setupOwnershipTestServer(t)
	ctx := context.Background()
	strangerAttemptID := uuid.New().String()

	t.Run("mismatched attempt denied, environment left intact", func(t *testing.T) {
		envID, _ := seedOwnedEnvironment(t, ctx, db, server.provisioner.Clientset())
		_, err := server.Destroy(ctx, &pb.DestroyRequest{EnvironmentId: envID, AttemptId: strangerAttemptID, Reason: "submit"})
		if err == nil {
			t.Fatal("SECURITY REGRESSION: expected PermissionDenied for a non-owning attempt_id, got success")
		}
		if status.Code(err) != codes.PermissionDenied {
			t.Errorf("expected PermissionDenied, got %v (%v)", status.Code(err), err)
		}
		exists, existsErr := server.provisioner.NamespaceExists(ctx, envID)
		if existsErr != nil {
			t.Fatalf("checking namespace still exists: %v", existsErr)
		}
		if !exists {
			t.Error("SECURITY REGRESSION: namespace was destroyed despite a PermissionDenied ownership check")
		}
	})

	t.Run("empty attempt_id rejected", func(t *testing.T) {
		envID, _ := seedOwnedEnvironment(t, ctx, db, server.provisioner.Clientset())
		_, err := server.Destroy(ctx, &pb.DestroyRequest{EnvironmentId: envID, AttemptId: "", Reason: "submit"})
		if err == nil {
			t.Fatal("SECURITY REGRESSION: expected rejection for empty attempt_id, got success")
		}
		if status.Code(err) != codes.PermissionDenied && status.Code(err) != codes.InvalidArgument {
			t.Errorf("expected PermissionDenied or InvalidArgument for empty attempt_id, got %v (%v)", status.Code(err), err)
		}
	})

	t.Run("matching attempt allowed and environment actually destroyed", func(t *testing.T) {
		envID, ownerAttemptID := seedOwnedEnvironment(t, ctx, db, server.provisioner.Clientset())
		resp, err := server.Destroy(ctx, &pb.DestroyRequest{EnvironmentId: envID, AttemptId: ownerAttemptID, Reason: "submit"})
		if err != nil {
			t.Fatalf("expected success for the owning attempt, got: %v", err)
		}
		if resp.AlreadyDestroyed {
			t.Error("expected AlreadyDestroyed=false for a live environment's first Destroy call")
		}
		// Namespace deletion in real K8s is asynchronous -- Delete()
		// returns as soon as the namespace is marked Terminating, not
		// once it's actually gone (the namespace controller finalizes
		// removal over the following seconds). NamespaceExists only
		// checks Get() success, which is still true while Terminating,
		// so asserting full removal here would be flaky/slow and isn't
		// what this test exists to prove -- proving the delete call
		// actually reached the API server (namespace phase is
		// Terminating) is enough evidence Destroy() did real work here,
		// not just return success without acting.
		ns := k8s.NamespaceForEnv(envID)
		nsObj, getErr := server.provisioner.Clientset().CoreV1().Namespaces().Get(ctx, ns, metav1.GetOptions{})
		if getErr != nil {
			t.Fatalf("expected the namespace to still be gettable (Terminating) right after Destroy, got error: %v", getErr)
		}
		if nsObj.Status.Phase != corev1.NamespaceTerminating && nsObj.DeletionTimestamp == nil {
			t.Errorf("expected the namespace to be Terminating (or have a DeletionTimestamp) after a successful Destroy, got phase=%v deletionTimestamp=%v", nsObj.Status.Phase, nsObj.DeletionTimestamp)
		}
	})

	t.Run("already-destroyed environment is a no-op success without requiring attempt_id", func(t *testing.T) {
		neverProvisionedEnvID := uuid.New().String()
		resp, err := server.Destroy(ctx, &pb.DestroyRequest{EnvironmentId: neverProvisionedEnvID, AttemptId: "", Reason: "submit"})
		if err != nil {
			t.Fatalf("expected AlreadyDestroyed success with no attempt_id for a namespace that never existed, got: %v", err)
		}
		if !resp.AlreadyDestroyed {
			t.Error("expected AlreadyDestroyed=true for a namespace that never existed")
		}
	})
}

func TestMintValidatorCredentials_OwnershipEnforced(t *testing.T) {
	server, db := setupOwnershipTestServer(t)
	ctx := context.Background()
	envID, ownerAttemptID := seedOwnedEnvironment(t, ctx, db, server.provisioner.Clientset())
	strangerAttemptID := uuid.New().String()

	t.Run("mismatched attempt denied", func(t *testing.T) {
		_, err := server.MintValidatorCredentials(ctx, &pb.MintCredentialsRequest{EnvironmentId: envID, AttemptId: strangerAttemptID})
		if err == nil {
			t.Fatal("SECURITY REGRESSION: expected PermissionDenied for a non-owning attempt_id, got success")
		}
		if status.Code(err) != codes.PermissionDenied {
			t.Errorf("expected PermissionDenied, got %v (%v)", status.Code(err), err)
		}
	})

	t.Run("empty attempt_id rejected", func(t *testing.T) {
		_, err := server.MintValidatorCredentials(ctx, &pb.MintCredentialsRequest{EnvironmentId: envID, AttemptId: ""})
		if err == nil {
			t.Fatal("SECURITY REGRESSION: expected rejection for empty attempt_id, got success")
		}
		if status.Code(err) != codes.PermissionDenied && status.Code(err) != codes.InvalidArgument {
			t.Errorf("expected PermissionDenied or InvalidArgument for empty attempt_id, got %v (%v)", status.Code(err), err)
		}
	})

	t.Run("matching attempt allowed", func(t *testing.T) {
		// TtlSeconds explicitly set above the RPC's own 5-minute default
		// (ttl.ValidatorCredential) -- this test cluster's K8s API server
		// enforces a real minimum TokenRequest duration of 10 minutes
		// (confirmed live: the 300s default was rejected with "may not
		// specify a duration less than 10 minutes"), unrelated to the
		// ownership check this test exists to verify.
		resp, err := server.MintValidatorCredentials(ctx, &pb.MintCredentialsRequest{EnvironmentId: envID, AttemptId: ownerAttemptID, TtlSeconds: 600})
		if err != nil {
			t.Fatalf("expected success for the owning attempt, got: %v", err)
		}
		if resp.CredentialRef == "" {
			t.Error("expected a non-empty credential ref for a successful mint")
		}
	})
}

func TestExecValidator_OwnershipEnforced(t *testing.T) {
	server, db := setupOwnershipTestServer(t)
	ctx := context.Background()
	envID, ownerAttemptID := seedOwnedEnvironment(t, ctx, db, server.provisioner.Clientset())
	strangerAttemptID := uuid.New().String()

	baseReq := func(attemptID string) *pb.ExecValidatorRequest {
		return &pb.ExecValidatorRequest{
			EnvironmentId: envID,
			ValidatorId:   "v-ownership-test",
			ValidatorType: "SHELL_ASSERT",
			Run:           "true",
			AttemptId:     attemptID,
		}
	}

	t.Run("mismatched attempt denied", func(t *testing.T) {
		_, err := server.ExecValidator(ctx, baseReq(strangerAttemptID))
		if err == nil {
			t.Fatal("SECURITY REGRESSION: expected PermissionDenied for a non-owning attempt_id, got success")
		}
		if status.Code(err) != codes.PermissionDenied {
			t.Errorf("expected PermissionDenied, got %v (%v)", status.Code(err), err)
		}
	})

	t.Run("empty attempt_id rejected", func(t *testing.T) {
		_, err := server.ExecValidator(ctx, baseReq(""))
		if err == nil {
			t.Fatal("SECURITY REGRESSION: expected rejection for empty attempt_id, got success")
		}
		if status.Code(err) != codes.PermissionDenied && status.Code(err) != codes.InvalidArgument {
			t.Errorf("expected PermissionDenied or InvalidArgument for empty attempt_id, got %v (%v)", status.Code(err), err)
		}
	})

	t.Run("matching attempt passes ownership check (no workspace pod, so a downstream error is expected but must not be PermissionDenied)", func(t *testing.T) {
		_, err := server.ExecValidator(ctx, baseReq(ownerAttemptID))
		if err != nil && status.Code(err) == codes.PermissionDenied {
			t.Fatalf("ownership check should have passed for the owning attempt, but got PermissionDenied: %v", err)
		}
	})
}

func TestExecShell_OwnershipEnforced(t *testing.T) {
	server, db := setupOwnershipTestServer(t)
	ctx := context.Background()
	envID, ownerAttemptID := seedOwnedEnvironment(t, ctx, db, server.provisioner.Clientset())
	strangerAttemptID := uuid.New().String()

	t.Run("mismatched attempt denied", func(t *testing.T) {
		_, err := server.ExecShell(ctx, &pb.ExecShellRequest{EnvironmentId: envID, Command: "true", AttemptId: strangerAttemptID})
		if err == nil {
			t.Fatal("SECURITY REGRESSION: expected PermissionDenied for a non-owning attempt_id, got success")
		}
		if status.Code(err) != codes.PermissionDenied {
			t.Errorf("expected PermissionDenied, got %v (%v)", status.Code(err), err)
		}
	})

	t.Run("empty attempt_id rejected", func(t *testing.T) {
		_, err := server.ExecShell(ctx, &pb.ExecShellRequest{EnvironmentId: envID, Command: "true", AttemptId: ""})
		if err == nil {
			t.Fatal("SECURITY REGRESSION: expected rejection for empty attempt_id, got success")
		}
		if status.Code(err) != codes.PermissionDenied && status.Code(err) != codes.InvalidArgument {
			t.Errorf("expected PermissionDenied or InvalidArgument for empty attempt_id, got %v (%v)", status.Code(err), err)
		}
	})

	t.Run("matching attempt passes ownership check (no workspace pod, so a downstream error is expected but must not be PermissionDenied)", func(t *testing.T) {
		_, err := server.ExecShell(ctx, &pb.ExecShellRequest{EnvironmentId: envID, Command: "true", AttemptId: ownerAttemptID})
		if err != nil && status.Code(err) == codes.PermissionDenied {
			t.Fatalf("ownership check should have passed for the owning attempt, but got PermissionDenied: %v", err)
		}
	})
}

// TestOwnershipRejection_IsAuditedForExecShell verifies Section 2's
// claim (server.go's new ExecShell ownership-check block) that a
// PermissionDenied rejection is actually written to env.audit_log, not
// just returned to the caller -- the checklist explicitly calls this
// out as something to verify against a real audit table, not assume
// from reading the code.
func TestOwnershipRejection_IsAuditedForExecShell(t *testing.T) {
	server, db := setupOwnershipTestServer(t)
	ctx := context.Background()
	envID, _ := seedOwnedEnvironment(t, ctx, db, server.provisioner.Clientset())
	strangerAttemptID := uuid.New().String()
	marker := fmt.Sprintf("ownership-audit-marker-%s", uuid.New().String())

	_, err := server.ExecShell(ctx, &pb.ExecShellRequest{EnvironmentId: envID, Command: marker, AttemptId: strangerAttemptID})
	if err == nil || status.Code(err) != codes.PermissionDenied {
		t.Fatalf("expected PermissionDenied to set up this audit assertion, got: %v", err)
	}

	var outcome, detail string
	scanErr := db.QueryRow(ctx, `
		SELECT outcome, detail::text FROM env.audit_log
		WHERE environment_id = $1 AND action = 'EXEC_SHELL'
		ORDER BY occurred_at DESC LIMIT 1
	`, envID).Scan(&outcome, &detail)
	if scanErr != nil {
		t.Fatalf("expected an audit_log row for the rejected ExecShell call, query failed: %v", scanErr)
	}
	if outcome != "FAILURE" {
		t.Errorf("expected audit outcome FAILURE for a PermissionDenied rejection, got %q", outcome)
	}
}
