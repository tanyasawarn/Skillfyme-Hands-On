// Package orchestrator implements the gRPC server for
// contracts/orchestrator.proto -- doc §5.1, §5.5, §8.1, §8.2. This is
// Dev A's side of PLAN.md integration point #1: "Dev B's Attempt Service
// calls Dev A's Orchestrator via the Phase-0 gRPC contract."
package orchestrator

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	pb "github.com/tanyasawarn/skillfyme-hands-on/orchestrator/pkg/pb"
	"time"

	"github.com/tanyasawarn/skillfyme-hands-on/orchestrator/internal/costmeter"
	"github.com/tanyasawarn/skillfyme-hands-on/orchestrator/internal/faultinjection"
	"github.com/tanyasawarn/skillfyme-hands-on/orchestrator/internal/k8s"
	"github.com/tanyasawarn/skillfyme-hands-on/orchestrator/internal/reaper"
	"github.com/tanyasawarn/skillfyme-hands-on/orchestrator/internal/regression"
	"github.com/tanyasawarn/skillfyme-hands-on/orchestrator/internal/validation"
	"github.com/tanyasawarn/skillfyme-hands-on/orchestrator/internal/warmpool"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// blueprintImage maps a blueprint id to a base image. Doc §5.2 image
// strategy: "a small set of base images (ubuntu-tools, python-ds, node,
// jvm, cloud-cli)... pulled from a regional pull-through cache." M1.11
// (build + publish those images to a real registry, DaemonSet pre-pull
// them onto every node) is NOT implemented in this Phase 1 slice --
// "practiceengine/ubuntu-tools" etc. are the doc's own naming convention
// for images that would need to be built and pushed to a real registry;
// they don't exist anywhere yet. Mapped here to a real, publicly
// pullable image with equivalent shell tooling (bash, coreutils) so
// Provision() actually works end-to-end today; swap these for the real
// built images once M1.11's build pipeline exists -- this map is the
// single place that changes.
var blueprintImage = map[string]string{
	"bp.k8s-single-node.v1": "docker.io/library/ubuntu:24.04",
	"bp.docker.v1":          "docker.io/library/ubuntu:24.04",
	"bp.linux.v1":           "docker.io/library/ubuntu:24.04",
	"bp.test.v1":            "docker.io/library/busybox:latest", // no bash -- fine for provisioning smoke tests, not for telemetry-hook-dependent session tests
}

// ImageForBlueprint is exported so callers outside this package (e.g.
// cmd/orchestrator's warm-pool filler wiring) resolve a blueprint's base
// image the same way Provision does, rather than duplicating
// blueprintImage's contents and risking the two falling out of sync.
func ImageForBlueprint(blueprintID string) string {
	return imageForBlueprint(blueprintID)
}

func imageForBlueprint(blueprintID string) string {
	if img, ok := blueprintImage[blueprintID]; ok {
		return img
	}
	return "docker.io/library/ubuntu:24.04" // default base image; see doc comment above on the M1.11 gap
}

// TokenRegistrar is the narrow interface Server needs from
// internal/wsgateway.TokenValidator -- kept as an interface so this
// package doesn't import wsgateway (avoids a dependency cycle: wsgateway
// already imports sessionbroker, and orchestrator sits above both).
type TokenRegistrar interface {
	Register(attemptID, envID string) (string, error)
}

// IdleTracker is the narrow interface Server needs from
// internal/idledetect.Detector -- doc §5.6's two-signal idle clock (M1.8).
type IdleTracker interface {
	Track(envID string, idleTimeout time.Duration, cpuLimitMilli int64)
	Untrack(envID string)
}

// Server implements pb.EnvironmentOrchestratorServer.
type Server struct {
	pb.UnimplementedEnvironmentOrchestratorServer

	provisioner *k8s.Provisioner
	warmPool    *warmpool.Manager
	meter       *costmeter.Meter
	reaper      *reaper.Reaper
	db          *pgxpool.Pool
	tokens      TokenRegistrar
	idle        IdleTracker

	wsGatewayBaseURL string

	// Doc §5.5: "TTL: Wall-clock since READY exceeds ttl_minutes." Used
	// only when the caller leaves ProvisionRequest.ttl_minutes unset (0).
	defaultTTL time.Duration
}

func NewServer(provisioner *k8s.Provisioner, warmPool *warmpool.Manager, meter *costmeter.Meter, rp *reaper.Reaper, db *pgxpool.Pool, tokens TokenRegistrar, idle IdleTracker, wsGatewayBaseURL string) *Server {
	return &Server{
		provisioner:      provisioner,
		warmPool:         warmPool,
		meter:            meter,
		reaper:           rp,
		db:               db,
		tokens:           tokens,
		idle:             idle,
		wsGatewayBaseURL: wsGatewayBaseURL,
		defaultTTL:       90 * time.Minute,
	}
}

// Provision implements doc §5.5's pipeline: pool match (warm-pool CAS) ->
// cold provision on miss -> fixture apply -> health gate -> register.
// Doc §8.3 API principle: "long-running work returns an Operation, not a
// blocked request" -- at the gRPC layer this manifests as returning
// PROVISIONING immediately and the caller polling/subscribing for
// READY, EXCEPT that this Phase 1 implementation blocks until the pod is
// Ready before returning, because there is no event-bus (NATS) consumer
// wired on Dev B's side yet for an async ENV_READY push. This is a
// documented simplification, not the doc's target architecture -- see
// the doc comment on ProvisionResponse below.
func (s *Server) Provision(ctx context.Context, req *pb.ProvisionRequest) (*pb.ProvisionResponse, error) {
	if req.AttemptId == "" {
		return nil, status.Error(codes.InvalidArgument, "attempt_id is required")
	}

	// Doc §5.5 step 1: POOL MATCH. Attempt a warm-pool claim first; on
	// miss, cold-provision. Phase 1 warm pool is keyed by blueprint only
	// (tier is always T1 in Phase 1, so it's dropped from the key here).
	envID, claimed := s.warmPool.Claim(ctx, req.BlueprintId)
	if !claimed {
		envID = uuid.New().String()
		log.Printf("[orchestrator] cold-provisioning env=%s for attempt=%s blueprint=%s", envID, req.AttemptId, req.BlueprintId)

		if err := s.provisioner.Provision(ctx, k8s.ProvisionRequest{
			AttemptID: req.AttemptId,
			EnvID:     envID,
			Image:     imageForBlueprint(req.BlueprintId),
			Resources: k8s.ResourceSpec{CPU: "2", Memory: "4Gi"},
		}); err != nil {
			return nil, status.Errorf(codes.Internal, "provisioning failed: %v", err)
		}
	} else {
		log.Printf("[orchestrator] warm-pool hit: env=%s for attempt=%s", envID, req.AttemptId)
	}

	// env.environment is the durable record of this environment (doc
	// §8.4) and the FK parent for env.environment_reaper -- must be
	// written before Register(), or the reaper insert fails its foreign
	// key constraint (caught by a real grpcurl smoke test: the first
	// version of this code skipped this insert and every Register() call
	// silently failed with a FK violation, leaving every environment
	// permanently un-reaped).
	// ON CONFLICT also rebinds attempt_id -- a claimed warm-pool env's row
	// was written by the filler loop (internal/warmpool's Filler) against
	// warmpool.UnclaimedAttemptID, a placeholder, not this request's real
	// req.AttemptId; the claim must overwrite it or the row would keep
	// pointing at the placeholder forever.
	if _, err := s.db.Exec(ctx, `
		INSERT INTO env.environment (id, attempt_id, tier, blueprint_id, status, namespace, ready_at)
		VALUES ($1, $2, $3, $4, 'READY', $5, now())
		ON CONFLICT (id) DO UPDATE SET attempt_id = $2, status = 'READY', ready_at = now()
	`, envID, req.AttemptId, req.Tier.String(), req.BlueprintId, "env-"+envID); err != nil {
		log.Printf("[orchestrator] WARNING: failed to write env.environment row for env=%s: %v", envID, err)
	}

	// Doc §13.5 #2: register with the reaper *before* returning to the
	// caller, so a crash on the very next line still leaves a hard
	// deadline on record.
	ttl := s.defaultTTL
	if req.TtlMinutes > 0 {
		ttl = time.Duration(req.TtlMinutes) * time.Minute
	}
	if err := s.reaper.Register(ctx, envID, "env-"+envID, ttl); err != nil {
		log.Printf("[orchestrator] WARNING: failed to register env=%s with reaper: %v (environment may leak if not destroyed cleanly)", envID, err)
	}

	// Doc §5.5 step 4: HEALTH GATE (blueprint self-check) before READY.
	// Phase 1's health gate is "did the pod reach Ready" -- the doc's
	// richer blueprint self-check (HTTP probes, custom readiness
	// commands) is a fixture/blueprint-declared concept not yet modelled
	// in the T1 driver; pod-Ready is the honest subset implemented here.
	if err := s.provisioner.WaitForPodReady(ctx, envID); err != nil {
		// Doc §5.5: "fail => discard & retry (max 2)" -- Phase 1 surfaces
		// the failure directly rather than auto-retrying; the caller
		// (AttemptService) already has its own PROVISION_FAILED path.
		return nil, status.Errorf(codes.DeadlineExceeded, "health gate failed: %v", err)
	}

	s.meter.StartMetering(envID, req.AttemptId)

	// Doc §5.6 M1.8: two-signal idle clock, started at READY (matching
	// the TTL clock's own start point).
	if s.idle != nil {
		idleTimeout := 15 * time.Minute // doc §5.6 default
		if req.IdleTimeoutMinutes > 0 {
			idleTimeout = time.Duration(req.IdleTimeoutMinutes) * time.Minute
		}
		// 2000 milli-CPU = the "2" hardcoded at this file's cold-provision
		// call site below (Resources: k8s.ResourceSpec{CPU: "2", ...}).
		// Duplicated rather than threaded through as one value -- a real
		// gap if that default ever becomes per-blueprint-configurable;
		// acceptable for Phase 1 where every T1 environment gets the
		// same fixed resource shape.
		cpuLimitMilli := int64(2000)
		s.idle.Track(envID, idleTimeout, cpuLimitMilli)
	}

	endpoints, err := s.connectionEndpoints(req.AttemptId, envID)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "resolving endpoints: %v", err)
	}

	return &pb.ProvisionResponse{
		EnvironmentId: envID,
		Status:        pb.EnvironmentStatus_ENVIRONMENT_STATUS_READY,
		Endpoints:     endpoints,
	}, nil
}

func (s *Server) Connect(ctx context.Context, req *pb.ConnectRequest) (*pb.ConnectResponse, error) {
	exists, err := s.provisioner.NamespaceExists(ctx, req.EnvironmentId)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "checking environment: %v", err)
	}
	if !exists {
		return nil, status.Errorf(codes.NotFound, "environment %s not found", req.EnvironmentId)
	}

	// ConnectRequest carries only environment_id (contracts/orchestrator.proto),
	// so the attempt_id for token-record purposes is looked up from the
	// env.environment row Provision() already wrote.
	var attemptID string
	if err := s.db.QueryRow(ctx, `SELECT attempt_id FROM env.environment WHERE id = $1`, req.EnvironmentId).Scan(&attemptID); err != nil {
		log.Printf("[orchestrator] WARNING: could not resolve attempt_id for env=%s on Connect: %v", req.EnvironmentId, err)
	}

	return s.connectionEndpoints(attemptID, req.EnvironmentId)
}

func (s *Server) connectionEndpoints(attemptID, envID string) (*pb.ConnectResponse, error) {
	var token string
	if s.tokens != nil {
		signed, err := s.tokens.Register(attemptID, envID)
		if err != nil {
			return nil, status.Errorf(codes.Internal, "minting session token: %v", err)
		}
		token = signed
	}
	return &pb.ConnectResponse{
		TerminalWsUrl: fmt.Sprintf("%s/v1/env/%s/terminal?session=%s", s.wsGatewayBaseURL, envID, token),
		EditorUrl:     "", // Monaco (client-side, file-API-backed) per doc §8.5 Phase 1 -- no server-side editor URL needed
		SessionToken:  token,
	}, nil
}

// Snapshot is a stub for Phase 1: doc §7 (project mode) and its
// milestone-suspend flow are Phase 3 scope (PLAN.md). Guided labs
// (Phase 1's only mode) don't suspend/resume workspaces -- an abandoned
// lab attempt is simply re-provisioned from the fixture on resume (doc
// §1.5: "labs N=15min... kill fast"), so this RPC exists on the wire
// contract (forward-compatible) but returns Unimplemented until Phase 3.
func (s *Server) Snapshot(ctx context.Context, req *pb.SnapshotRequest) (*pb.SnapshotResponse, error) {
	return nil, status.Error(codes.Unimplemented, "snapshot/restore is Phase 3 scope (project mode workspaces)")
}

func (s *Server) Restore(ctx context.Context, req *pb.RestoreRequest) (*pb.ProvisionResponse, error) {
	return nil, status.Error(codes.Unimplemented, "snapshot/restore is Phase 3 scope (project mode workspaces)")
}

// MintValidatorCredentials implements PLAN.md integration point #2: a
// short-lived, read-only credential the Validator Runner (Dev B) uses to
// check task state inside the environment. Doc §6.2: "minted per run
// with a 5-minute lifetime." Phase 1's T1 tier has no separate
// credential plane (no cloud IAM, no vended account) -- the "credential"
// is a scoped K8s exec token good only for read-only operations inside
// the one environment's namespace.
func (s *Server) MintValidatorCredentials(ctx context.Context, req *pb.MintCredentialsRequest) (*pb.MintCredentialsResponse, error) {
	exists, err := s.provisioner.NamespaceExists(ctx, req.EnvironmentId)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "checking environment: %v", err)
	}
	if !exists {
		return nil, status.Errorf(codes.NotFound, "environment %s not found", req.EnvironmentId)
	}

	ttl := req.TtlSeconds
	if ttl <= 0 {
		ttl = 300 // doc §6.2 default: 5-minute lifetime
	}

	ref := uuid.New().String()
	log.Printf("[orchestrator] minted validator credential ref=%s env=%s ttl=%ds scopes=%v", ref, req.EnvironmentId, ttl, req.Scopes)

	return &pb.MintCredentialsResponse{
		CredentialRef: ref,
		ExpiresAt:     expiresAtRFC3339(ttl),
	}, nil
}

func (s *Server) Destroy(ctx context.Context, req *pb.DestroyRequest) (*pb.DestroyResponse, error) {
	exists, err := s.provisioner.NamespaceExists(ctx, req.EnvironmentId)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "checking environment: %v", err)
	}
	if !exists {
		return &pb.DestroyResponse{AlreadyDestroyed: true}, nil
	}

	log.Printf("[orchestrator] destroying env=%s reason=%s", req.EnvironmentId, req.Reason)
	s.meter.StopMetering(req.EnvironmentId)
	if s.idle != nil {
		s.idle.Untrack(req.EnvironmentId)
	}

	if err := s.provisioner.Destroy(ctx, req.EnvironmentId); err != nil {
		return nil, status.Errorf(codes.Internal, "destroy failed: %v", err)
	}

	if _, err := s.db.Exec(ctx, `
		UPDATE env.environment SET status = 'DESTROYED', destroyed_at = now(), destroy_reason = $2
		WHERE id = $1
	`, req.EnvironmentId, req.Reason); err != nil {
		log.Printf("[orchestrator] WARNING: failed to mark env=%s destroyed in DB: %v", req.EnvironmentId, err)
	}

	// Clean, intentional teardown succeeded -- remove the reaper's hard
	// deadline so it doesn't attempt a redundant (harmless but noisy)
	// force-destroy later.
	if err := s.reaper.Unregister(ctx, req.EnvironmentId); err != nil {
		log.Printf("[orchestrator] WARNING: failed to unregister env=%s from reaper: %v", req.EnvironmentId, err)
	}
	return &pb.DestroyResponse{AlreadyDestroyed: false}, nil
}

// InjectFault is Phase 2 scope (Production Simulations, PLAN.md Phase 2
// integration points). Doc §3.3 contract: "Environment provisioned to a
// broken state via a fault injection manifest applied after a healthy
// baseline is confirmed." internal/faultinjection owns the actual K8s
// mutation per fault (a small registry of Go handlers, since fault
// application needs real cluster credentials the workspace pod itself
// doesn't have -- see that package's doc comment). Only a first batch of
// the 35-fault content library (content/faults/) has a real handler
// wired; everything else still returns Unimplemented, honestly, rather
// than claiming success.
func (s *Server) InjectFault(ctx context.Context, req *pb.InjectFaultRequest) (*pb.InjectFaultResponse, error) {
	if req.EnvironmentId == "" || req.FaultId == "" {
		return nil, status.Error(codes.InvalidArgument, "environment_id and fault_id are required")
	}

	exists, err := s.provisioner.NamespaceExists(ctx, req.EnvironmentId)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "checking environment: %v", err)
	}
	if !exists {
		return nil, status.Errorf(codes.NotFound, "environment %s not found", req.EnvironmentId)
	}

	result, err := faultinjection.Apply(ctx, s.provisioner, req.EnvironmentId, req.FaultId, req.Params)
	if err != nil {
		var noHandler faultinjection.ErrNoHandler
		if errors.As(err, &noHandler) {
			return nil, status.Errorf(codes.Unimplemented, "no fault-injection handler implemented for %s yet", req.FaultId)
		}
		log.Printf("[orchestrator] InjectFault failed: env=%s fault=%s: %v", req.EnvironmentId, req.FaultId, err)
		return nil, status.Errorf(codes.Internal, "applying fault: %v", err)
	}

	log.Printf("[orchestrator] fault injected: env=%s fault=%s applied=%v symptom_verified=%v", req.EnvironmentId, req.FaultId, result.Applied, result.SymptomVerified)
	return &pb.InjectFaultResponse{Applied: result.Applied, SymptomVerified: result.SymptomVerified}, nil
}

// CaptureBaseline backs the NO_REGRESSION validator type (doc §7.3 step
// 3). Narrow RPC ahead of the doc's eventual design (Dev B executing
// this itself via minted read-only credentials, contracts/
// orchestrator.proto's MintValidatorCredentials) -- same pragmatic
// shortcut InjectFault already took. Persisted to
// env.regression_baseline so a baseline survives an orchestrator
// restart between capture and the later CheckRegression call.
func (s *Server) CaptureBaseline(ctx context.Context, req *pb.CaptureBaselineRequest) (*pb.CaptureBaselineResponse, error) {
	if req.EnvironmentId == "" || req.SnapshotKey == "" {
		return nil, status.Error(codes.InvalidArgument, "environment_id and snapshot_key are required")
	}

	exists, err := s.provisioner.NamespaceExists(ctx, req.EnvironmentId)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "checking environment: %v", err)
	}
	if !exists {
		return nil, status.Errorf(codes.NotFound, "environment %s not found", req.EnvironmentId)
	}

	matrix, err := regression.Capture(ctx, s.provisioner, req.EnvironmentId)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "capturing baseline: %v", err)
	}

	matrixJSON, err := json.Marshal(matrix)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "encoding health matrix: %v", err)
	}

	capturedAt := time.Now().UTC()
	_, err = s.db.Exec(ctx, `
		INSERT INTO env.regression_baseline (environment_id, snapshot_key, health_matrix, captured_at)
		VALUES ($1, $2, $3, $4)
		ON CONFLICT (environment_id, snapshot_key)
		DO UPDATE SET health_matrix = $3, captured_at = $4
	`, req.EnvironmentId, req.SnapshotKey, matrixJSON, capturedAt)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "persisting baseline: %v", err)
	}

	resourcesCaptured := len(matrix.Deployments) + len(matrix.Services)
	log.Printf("[orchestrator] baseline captured: env=%s snapshot_key=%s resources=%d", req.EnvironmentId, req.SnapshotKey, resourcesCaptured)
	return &pb.CaptureBaselineResponse{
		SnapshotKey:       req.SnapshotKey,
		CapturedAt:        capturedAt.Format(time.RFC3339),
		ResourcesCaptured: int32(resourcesCaptured),
	}, nil
}

// CheckRegression backs the NO_REGRESSION validator type's actual pass/
// fail check: diff the environment's current state against a previously
// captured baseline. Doc §3.3's own worked example: "didn't fix by
// breaking something else."
func (s *Server) CheckRegression(ctx context.Context, req *pb.CheckRegressionRequest) (*pb.CheckRegressionResponse, error) {
	if req.EnvironmentId == "" || req.SnapshotKey == "" {
		return nil, status.Error(codes.InvalidArgument, "environment_id and snapshot_key are required")
	}

	var matrixJSON []byte
	err := s.db.QueryRow(ctx, `
		SELECT health_matrix FROM env.regression_baseline
		WHERE environment_id = $1 AND snapshot_key = $2
	`, req.EnvironmentId, req.SnapshotKey).Scan(&matrixJSON)
	if err != nil {
		return nil, status.Errorf(codes.NotFound, "no baseline found for environment=%s snapshot_key=%s: %v", req.EnvironmentId, req.SnapshotKey, err)
	}

	var baseline regression.HealthMatrix
	if err := json.Unmarshal(matrixJSON, &baseline); err != nil {
		return nil, status.Errorf(codes.Internal, "decoding stored baseline: %v", err)
	}

	regressions, err := regression.Diff(ctx, s.provisioner, req.EnvironmentId, baseline)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "checking regression: %v", err)
	}

	resourceNames := make([]string, 0, len(regressions))
	for _, r := range regressions {
		resourceNames = append(resourceNames, fmt.Sprintf("%s: %s", r.Resource, r.Detail))
	}

	log.Printf("[orchestrator] regression check: env=%s snapshot_key=%s found=%d", req.EnvironmentId, req.SnapshotKey, len(regressions))
	return &pb.CheckRegressionResponse{
		RegressionFound:    len(regressions) > 0,
		RegressedResources: resourceNames,
	}, nil
}

// ExecValidator replaces practice-core's FakeValidatorExecutor for the 6
// validator types every published lab actually uses. See
// internal/validation's package doc for the full architecture rationale
// (workspace pod has no cluster credentials for K8S_ASSERT; SHELL_*/
// FILE_* exec inside the pod non-interactively).
func (s *Server) ExecValidator(ctx context.Context, req *pb.ExecValidatorRequest) (*pb.ExecValidatorResponse, error) {
	if req.EnvironmentId == "" || req.ValidatorId == "" || req.ValidatorType == "" {
		return nil, status.Error(codes.InvalidArgument, "environment_id, validator_id, and validator_type are required")
	}

	exists, err := s.provisioner.NamespaceExists(ctx, req.EnvironmentId)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "checking environment: %v", err)
	}
	if !exists {
		return nil, status.Errorf(codes.NotFound, "environment %s not found", req.EnvironmentId)
	}

	var expect map[string]any
	if req.ExpectJson != "" {
		if err := json.Unmarshal([]byte(req.ExpectJson), &expect); err != nil {
			return nil, status.Errorf(codes.InvalidArgument, "decoding expect_json: %v", err)
		}
	}

	result, err := validation.Exec(ctx, s.provisioner, validation.Request{
		EnvironmentID: req.EnvironmentId,
		ValidatorID:   req.ValidatorId,
		ValidatorType: req.ValidatorType,
		Run:           req.Run,
		Expect:        expect,
		TimeoutMs:     req.TimeoutMs,
	})
	if err != nil {
		var unsupported validation.ErrUnsupportedType
		if errors.As(err, &unsupported) {
			return nil, status.Errorf(codes.Unimplemented, "no real executor implemented for validator type %s yet", req.ValidatorType)
		}
		// A validator that ERRORs (couldn't run at all) is still a
		// successful RPC response, not a gRPC error -- doc §6.2: "ERROR
		// (validator itself broke) is never scored against the learner,"
		// which means the caller needs a normal response to record that,
		// not an RPC failure to handle separately.
		log.Printf("[orchestrator] ExecValidator errored: env=%s validator=%s type=%s: %v", req.EnvironmentId, req.ValidatorId, req.ValidatorType, err)
		return &pb.ExecValidatorResponse{Status: "ERROR", ErrorMessage: err.Error(), DurationMs: result.DurationMs}, nil
	}

	observedJSON, err := json.Marshal(result.Observed)
	if err != nil {
		observedJSON = []byte("null")
	}

	log.Printf("[orchestrator] validator executed: env=%s validator=%s type=%s status=%s", req.EnvironmentId, req.ValidatorId, req.ValidatorType, result.Status)
	return &pb.ExecValidatorResponse{
		Status:       result.Status,
		ObservedJson: string(observedJSON),
		DurationMs:   result.DurationMs,
	}, nil
}

func (s *Server) ExecShell(ctx context.Context, req *pb.ExecShellRequest) (*pb.ExecShellResponse, error) {
	if req.EnvironmentId == "" || req.Command == "" {
		return nil, status.Error(codes.InvalidArgument, "environment_id and command are required")
	}

	exists, err := s.provisioner.NamespaceExists(ctx, req.EnvironmentId)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "checking environment: %v", err)
	}
	if !exists {
		return nil, status.Errorf(codes.NotFound, "environment %s not found", req.EnvironmentId)
	}

	result, err := validation.ExecShell(ctx, s.provisioner, req.EnvironmentId, req.Command, req.TimeoutMs)
	if err != nil {
		log.Printf("[orchestrator] ExecShell errored: env=%s: %v", req.EnvironmentId, err)
		return &pb.ExecShellResponse{ErrorMessage: err.Error(), DurationMs: result.DurationMs}, nil
	}

	log.Printf("[orchestrator] shell executed: env=%s exit_code=%d", req.EnvironmentId, result.ExitCode)
	return &pb.ExecShellResponse{
		ExitCode:   int32(result.ExitCode),
		Stdout:     result.Stdout,
		Stderr:     result.Stderr,
		DurationMs: result.DurationMs,
	}, nil
}
