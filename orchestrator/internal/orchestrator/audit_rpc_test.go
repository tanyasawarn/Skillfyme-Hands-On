package orchestrator

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	pb "github.com/tanyasawarn/skillfyme-hands-on/orchestrator/pkg/pb"
)

// PLAN.md Phase 2 closure item: Connect, CaptureBaseline, CheckRegression,
// and ExecValidator previously wrote zero env.audit_log entries. These
// tests reuse setupOwnershipTestServer's real-infra-gated pattern
// (ownership_rpc_test.go) and, rather than inferring from code that an
// audit.Record call was added, query env.audit_log directly after each
// RPC call to prove a real row landed -- the same standard
// PLAN_RPC_AUTHZ.md's own audit tests already hold InjectFault to.

// mostRecentAuditEntry returns the action/outcome/error_message of the
// newest env.audit_log row for the given environment_id, failing the
// test if none exists.
func mostRecentAuditEntry(t *testing.T, ctx context.Context, db *pgxpool.Pool, envID string) (action, outcome, errMsg string) {
	t.Helper()
	err := db.QueryRow(ctx, `
		SELECT action, outcome, COALESCE(error_message, '')
		FROM env.audit_log
		WHERE environment_id = $1
		ORDER BY occurred_at DESC
		LIMIT 1
	`, envID).Scan(&action, &outcome, &errMsg)
	if err != nil {
		t.Fatalf("expected an env.audit_log row for environment_id=%s, query failed: %v", envID, err)
	}
	return action, outcome, errMsg
}

func TestConnect_IsAudited(t *testing.T) {
	server, db := setupOwnershipTestServer(t)
	ctx := context.Background()
	envID, ownerAttemptID := seedOwnedEnvironment(t, ctx, db, server.provisioner.Clientset())

	t.Run("success is audited", func(t *testing.T) {
		_, err := server.Connect(ctx, &pb.ConnectRequest{EnvironmentId: envID, AttemptId: ownerAttemptID})
		if err != nil {
			t.Fatalf("expected success for the owning attempt, got: %v", err)
		}
		action, outcome, _ := mostRecentAuditEntry(t, ctx, db, envID)
		if action != "CONNECT" {
			t.Errorf("expected action=CONNECT, got %q", action)
		}
		if outcome != "SUCCESS" {
			t.Errorf("expected outcome=SUCCESS, got %q", outcome)
		}
	})

	t.Run("ownership rejection is audited as a failure", func(t *testing.T) {
		strangerAttemptID := uuid.New().String()
		_, err := server.Connect(ctx, &pb.ConnectRequest{EnvironmentId: envID, AttemptId: strangerAttemptID})
		if err == nil {
			t.Fatal("expected PermissionDenied for a non-owning attempt_id")
		}
		action, outcome, errMsg := mostRecentAuditEntry(t, ctx, db, envID)
		if action != "CONNECT" {
			t.Errorf("expected action=CONNECT, got %q", action)
		}
		if outcome != "FAILURE" {
			t.Errorf("expected outcome=FAILURE, got %q", outcome)
		}
		if errMsg == "" {
			t.Error("expected a non-empty error_message on a failed Connect")
		}
	})
}

func TestCaptureBaseline_IsAudited(t *testing.T) {
	server, db := setupOwnershipTestServer(t)
	ctx := context.Background()
	envID, _ := seedOwnedEnvironment(t, ctx, db, server.provisioner.Clientset())

	t.Run("success is audited", func(t *testing.T) {
		_, err := server.CaptureBaseline(ctx, &pb.CaptureBaselineRequest{EnvironmentId: envID, SnapshotKey: "baseline.test"})
		if err != nil {
			t.Fatalf("expected success, got: %v", err)
		}
		action, outcome, _ := mostRecentAuditEntry(t, ctx, db, envID)
		if action != "CAPTURE_BASELINE" {
			t.Errorf("expected action=CAPTURE_BASELINE, got %q", action)
		}
		if outcome != "SUCCESS" {
			t.Errorf("expected outcome=SUCCESS, got %q", outcome)
		}
	})

	t.Run("missing snapshot_key is audited as a failure", func(t *testing.T) {
		_, err := server.CaptureBaseline(ctx, &pb.CaptureBaselineRequest{EnvironmentId: envID, SnapshotKey: ""})
		if err == nil {
			t.Fatal("expected InvalidArgument for empty snapshot_key")
		}
		action, outcome, errMsg := mostRecentAuditEntry(t, ctx, db, envID)
		if action != "CAPTURE_BASELINE" {
			t.Errorf("expected action=CAPTURE_BASELINE, got %q", action)
		}
		if outcome != "FAILURE" {
			t.Errorf("expected outcome=FAILURE, got %q", outcome)
		}
		if errMsg == "" {
			t.Error("expected a non-empty error_message")
		}
	})
}

func TestCheckRegression_IsAudited(t *testing.T) {
	server, db := setupOwnershipTestServer(t)
	ctx := context.Background()
	envID, _ := seedOwnedEnvironment(t, ctx, db, server.provisioner.Clientset())

	t.Run("no baseline found is audited as a failure", func(t *testing.T) {
		_, err := server.CheckRegression(ctx, &pb.CheckRegressionRequest{EnvironmentId: envID, SnapshotKey: "does-not-exist"})
		if err == nil {
			t.Fatal("expected NotFound for a snapshot_key with no captured baseline")
		}
		action, outcome, errMsg := mostRecentAuditEntry(t, ctx, db, envID)
		if action != "CHECK_REGRESSION" {
			t.Errorf("expected action=CHECK_REGRESSION, got %q", action)
		}
		if outcome != "FAILURE" {
			t.Errorf("expected outcome=FAILURE, got %q", outcome)
		}
		if errMsg == "" {
			t.Error("expected a non-empty error_message")
		}
	})

	t.Run("success after a real CaptureBaseline is audited", func(t *testing.T) {
		if _, err := server.CaptureBaseline(ctx, &pb.CaptureBaselineRequest{EnvironmentId: envID, SnapshotKey: "baseline.regr-test"}); err != nil {
			t.Fatalf("seeding baseline failed: %v", err)
		}
		_, err := server.CheckRegression(ctx, &pb.CheckRegressionRequest{EnvironmentId: envID, SnapshotKey: "baseline.regr-test"})
		if err != nil {
			t.Fatalf("expected success, got: %v", err)
		}
		action, outcome, _ := mostRecentAuditEntry(t, ctx, db, envID)
		if action != "CHECK_REGRESSION" {
			t.Errorf("expected action=CHECK_REGRESSION, got %q", action)
		}
		if outcome != "SUCCESS" {
			t.Errorf("expected outcome=SUCCESS, got %q", outcome)
		}
	})
}

func TestExecValidator_IsAudited(t *testing.T) {
	server, db := setupOwnershipTestServer(t)
	ctx := context.Background()
	envID, ownerAttemptID := seedOwnedEnvironment(t, ctx, db, server.provisioner.Clientset())

	t.Run("ownership rejection is audited as a failure", func(t *testing.T) {
		strangerAttemptID := uuid.New().String()
		_, err := server.ExecValidator(ctx, &pb.ExecValidatorRequest{
			EnvironmentId: envID,
			ValidatorId:   "v.test",
			ValidatorType: "SHELL_ASSERT",
			Run:           "true",
			AttemptId:     strangerAttemptID,
		})
		if err == nil {
			t.Fatal("expected PermissionDenied for a non-owning attempt_id")
		}
		action, outcome, errMsg := mostRecentAuditEntry(t, ctx, db, envID)
		if action != "EXEC_VALIDATOR" {
			t.Errorf("expected action=EXEC_VALIDATOR, got %q", action)
		}
		if outcome != "FAILURE" {
			t.Errorf("expected outcome=FAILURE, got %q", outcome)
		}
		if errMsg == "" {
			t.Error("expected a non-empty error_message")
		}
	})

	// No real workspace pod is stood up in this test (same documented
	// limitation ownership_rpc_test.go's ExecValidator test carries) --
	// with a matching attempt_id, validation.Exec runs for real against a
	// nonexistent pod and returns a StatusError result with a nil RPC
	// error (doc §6.2's "ERROR is never scored" contract). This is
	// exactly the case the audit defer's resp.Status == StatusError check
	// exists for: a validator that errored must still show up as a
	// FAILURE in the audit log even though the RPC itself succeeded.
	t.Run("validator ERROR result (RPC succeeds, validator itself broke) is audited as a failure", func(t *testing.T) {
		resp, err := server.ExecValidator(ctx, &pb.ExecValidatorRequest{
			EnvironmentId: envID,
			ValidatorId:   "v.test-error",
			ValidatorType: "SHELL_ASSERT",
			Run:           "true",
			AttemptId:     ownerAttemptID,
		})
		if err != nil {
			t.Fatalf("expected a nil RPC error even when the validator itself errors, got: %v", err)
		}
		if resp.Status != "ERROR" {
			t.Fatalf("expected validator Status=ERROR against a nonexistent workspace pod, got %q", resp.Status)
		}
		action, outcome, errMsg := mostRecentAuditEntry(t, ctx, db, envID)
		if action != "EXEC_VALIDATOR" {
			t.Errorf("expected action=EXEC_VALIDATOR, got %q", action)
		}
		if outcome != "FAILURE" {
			t.Errorf("SECURITY/CORRECTNESS REGRESSION: a validator ERROR result must be audited as FAILURE, not SUCCESS (got %q) -- otherwise a validator that silently broke reads as a clean audit trail", outcome)
		}
		if errMsg == "" {
			t.Error("expected a non-empty error_message for an ERROR-status validator")
		}
	})
}
