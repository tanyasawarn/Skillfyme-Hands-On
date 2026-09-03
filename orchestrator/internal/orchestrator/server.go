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

	"github.com/tanyasawarn/skillfyme-hands-on/orchestrator/internal/audit"
	"github.com/tanyasawarn/skillfyme-hands-on/orchestrator/internal/costmeter"
	"github.com/tanyasawarn/skillfyme-hands-on/orchestrator/internal/envstatus"
	"github.com/tanyasawarn/skillfyme-hands-on/orchestrator/internal/faultinjection"
	"github.com/tanyasawarn/skillfyme-hands-on/orchestrator/internal/fixture"
	"github.com/tanyasawarn/skillfyme-hands-on/orchestrator/internal/k8s"
	"github.com/tanyasawarn/skillfyme-hands-on/orchestrator/internal/logging"
	"github.com/tanyasawarn/skillfyme-hands-on/orchestrator/internal/metrics"
	"github.com/tanyasawarn/skillfyme-hands-on/orchestrator/internal/reaper"
	"github.com/tanyasawarn/skillfyme-hands-on/orchestrator/internal/regression"
	"github.com/tanyasawarn/skillfyme-hands-on/orchestrator/internal/ttl"
	"github.com/tanyasawarn/skillfyme-hands-on/orchestrator/internal/validation"
	"github.com/tanyasawarn/skillfyme-hands-on/orchestrator/internal/warmpool"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// slogger is the structured logger for this package's lifecycle lines
// (PHASE1_MVP_COMPLETION.md §4.2: provision / warm-pool / teardown carry
// env_id + attempt_id + reason). RPC-result diagnostics still use the
// std log package -- those are outside §4.2's lifecycle scope, and the
// logging.Init bridge sends them to the same JSON stream anyway.
var slogger = logging.Component("orchestrator")

// blueprintImage maps a blueprint id to a base image. Doc §5.2 image
// strategy: "a small set of base images (ubuntu-tools, python-ds, node,
// jvm, cloud-cli)... pulled from a regional pull-through cache." M1.11
// (build + publish those images to a real registry, DaemonSet pre-pull
// them onto every node) -- the "build + publish to a real registry" half
// is now real: orchestrator/images/linux-tools/Dockerfile builds a real
// git+kubectl+coreutils image, pushed to a local registry (docker-compose's
// `registry` service, registry:2, see docker-compose.yml + k3s-registries.yaml
// for the insecure-HTTP containerd config that lets k3s pull from it).
// "registry:5000" (not localhost:5001) is deliberate: that's the
// compose-network DNS name/port k3s's own containerd resolves, not the
// host-mapped port `docker push` uses from outside the compose network --
// two different addresses for the same registry, reachable from two
// different places. DaemonSet pre-pull (the other M1.11 half) is still
// not implemented -- Phase 1's provisioning rate doesn't yet justify it,
// and every pull still succeeds today, just not pre-warmed.
var blueprintImage = map[string]string{
	"bp.k8s-single-node.v1": "registry:5000/practiceengine/linux-tools:v1",
	"bp.docker.v1":          "registry:5000/practiceengine/linux-tools:v1",
	"bp.linux.v1":           "registry:5000/practiceengine/linux-tools:v1",
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
	destroyer   *Destroyer

	wsGatewayBaseURL string

	// Doc §5.5: "TTL: Wall-clock since READY exceeds ttl_minutes." Used
	// only when the caller leaves ProvisionRequest.ttl_minutes unset (0).
	defaultTTL time.Duration

	// t2Enabled gates whether Provision() will attempt
	// TIER_T2_ISOLATED_MICROVM requests at all. PLAN.md's Phase 2 T2
	// section carries an explicit sequencing dependency, not just a
	// suggestion: "Dev A should not start T2 until Phase 1's
	// reaper/teardown has been running with zero orphans for a sustained
	// period... this is a real sequencing dependency, not just
	// advisory." That precondition is an operational fact about a real
	// deployment's history, not something a code change can satisfy --
	// so the T2 driver code (k8s.Provisioner's tier branch, the Kata
	// RuntimeClass/node-pool manifests) exists and is real, but stays
	// off by default until an operator who has actually observed that
	// track record explicitly turns it on via ORCHESTRATOR_T2_ENABLED.
	// Same "typed, explicit deferral instead of silent partial behavior"
	// stance internal/faultinjection.ErrUnsupportedMechanism takes for
	// its own not-yet-safe-to-run cases.
	t2Enabled bool

	// credentials holds real minted validator tokens server-side, keyed
	// by the opaque ref returned to callers -- see CredentialStore's own
	// doc comment (credentials.go) for why the raw token never crosses
	// the RPC boundary.
	credentials *CredentialStore

	// fixtures tracks which (environment, fixture, checksum) triples
	// have already been applied -- PLAN.md M1.3, doc §5.5 step 3's
	// "idempotent" half. See internal/fixture.AppliedTracker.
	fixtures fixture.AppliedTracker

	// audit records every security-relevant RPC to env.audit_log --
	// PLAN.md M1.14's "security baseline audit" line item. See
	// internal/audit's own package doc for scope and rationale.
	audit *audit.Logger
}

func NewServer(provisioner *k8s.Provisioner, warmPool *warmpool.Manager, meter *costmeter.Meter, rp *reaper.Reaper, db *pgxpool.Pool, tokens TokenRegistrar, idle IdleTracker, destroyer *Destroyer, wsGatewayBaseURL string, t2Enabled bool) *Server {
	return &Server{
		provisioner:      provisioner,
		credentials:      NewCredentialStore(),
		fixtures:         NewPostgresFixtureTracker(db),
		audit:            audit.NewLogger(db),
		warmPool:         warmPool,
		meter:            meter,
		reaper:           rp,
		db:               db,
		tokens:           tokens,
		idle:             idle,
		destroyer:        destroyer,
		wsGatewayBaseURL: wsGatewayBaseURL,
		defaultTTL:       ttl.EnvironmentDefault,
		t2Enabled:        t2Enabled,
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
// resolveTier maps a wire-level pb.Tier to the K8s driver's Tier,
// enforcing the T2 gate. Pure function (no Server/gRPC/K8s dependencies)
// specifically so this decision -- the one genuinely new piece of
// authorization-adjacent logic T2 introduced -- is unit-testable without
// standing up the rest of Server's dependencies (pgxpool.Pool,
// *k8s.Provisioner, *warmpool.Manager, ...), none of which this package
// has ever had test infrastructure for. Every other RPC in this file
// stays untested by the same historical gap; this one function is
// deliberately carved out because it's new, security-relevant, and small
// enough to isolate cheaply -- see server_test.go.
//
// T2 requests are only honored when explicitly enabled (see
// Server.t2Enabled's doc comment -- PLAN.md's own zero-orphans
// sequencing gate). Rejecting with FailedPrecondition (not Unimplemented:
// the driver exists, it's the operational precondition that's unmet)
// rather than silently downgrading to T1 -- a caller that asked for
// microVM isolation and got a shared gVisor sandbox instead without
// being told would be a real security discrepancy, not a graceful
// degradation.
func resolveTier(requestedTier pb.Tier, t2Enabled bool) (k8s.Tier, error) {
	if requestedTier != pb.Tier_TIER_T2_ISOLATED_MICROVM {
		return k8s.TierT1SharedContainer, nil
	}
	if !t2Enabled {
		return k8s.TierT1SharedContainer, status.Error(codes.FailedPrecondition, "T2 (TIER_T2_ISOLATED_MICROVM) is not enabled on this orchestrator -- PLAN.md's Phase 2 sequencing gate (zero orphans sustained) has not been confirmed for this deployment; set ORCHESTRATOR_T2_ENABLED=true only after that track record is verified")
	}
	return k8s.TierT2IsolatedMicroVM, nil
}

// resolveEnvTTL is the pure ttl-selection decision Provision applies --
// pulled out (same rationale as resolveTier: small, cost-relevant,
// isolate cheaply) so the per-tier default and the caller-override
// precedence are unit-testable without a full Server. Precedence:
// a caller-supplied ttl_minutes (> 0) always wins; otherwise T2 gets
// ttl.EnvironmentDefaultT2 (shorter -- at T2's $0.10-0.35/env-hr band a
// 90-min tail on a walked-away microVM is ~2x the intended per-attempt
// cost, docs/t2-cost-optimization.md §3.1) and every other tier gets
// t1Default (the Server's configured defaultTTL).
func resolveEnvTTL(tier k8s.Tier, ttlMinutes int32, t1Default time.Duration) time.Duration {
	if ttlMinutes > 0 {
		return time.Duration(ttlMinutes) * time.Minute
	}
	if tier == k8s.TierT2IsolatedMicroVM {
		return ttl.EnvironmentDefaultT2
	}
	return t1Default
}

// checkEnvironmentOwnership is the ownership decision shared by every
// RPC that accepts a caller-supplied environment_id alongside an
// attempt_id (InjectFault originally, now also Connect, Destroy,
// MintValidatorCredentials, ExecValidator, ExecShell -- PHASE2_CLOSEOUT.md's
// flagged access-control gap: the service-level shared-secret auth
// (auth.go) proves the CALLER is practice-core's backend, but says
// nothing about which learner's attempt that call is acting on behalf
// of). Pulled out as a pure function (same "small, security-relevant,
// isolate cheaply" rationale as resolveTier above) so it's unit-testable
// without a real *pgxpool.Pool -- each call site does the actual DB
// lookup (env.environment.attempt_id for environmentID) and passes both
// values in here. PermissionDenied, not NotFound: the environment DOES
// exist (the caller already confirmed that via the same query this
// value came from), the caller is just not entitled to act on it --
// conflating the two would leak "this environment_id exists" to an
// unauthorized caller via a different-looking error, information a
// PermissionDenied response withholds by design.
func checkEnvironmentOwnership(ownerAttemptID, callerAttemptID string) error {
	// The empty-string check guards a real edge case, not just belt-and-
	// suspenders: InjectFault's own InvalidArgument check already rejects
	// an empty callerAttemptID before this function is ever called, but
	// ownerAttemptID comes from the DB (env.environment.attempt_id) --
	// defense-in-depth against that column ever being empty (it's `not
	// null` today, but schema constraints are not this function's
	// responsibility to trust blindly on a security-boundary check).
	// Without this, two empty strings would satisfy the comparison below
	// and silently authorize a caller against an environment with no
	// real owner recorded.
	if callerAttemptID == "" {
		return status.Errorf(codes.PermissionDenied, "attempt %s does not own this environment", callerAttemptID)
	}
	// Parsed-UUID comparison, not a raw string ==: caught live during
	// this session's own verification of this exact check --
	// Postgres's uuid column type normalizes to lowercase on read
	// regardless of insert casing, so ownerAttemptID (always lowercase,
	// straight from the DB) failed a byte-for-byte match against a
	// caller-supplied attempt_id in a different casing even though it
	// was genuinely the correct, owning attempt. uuid.Parse normalizes
	// both sides before comparing, so casing differences (or other
	// syntactically-equivalent representations, e.g. missing/extra
	// hyphens) can never cause a false PermissionDenied against a
	// legitimate caller. A parse failure on either side is itself
	// treated as non-ownership (fails closed, not open) -- a malformed
	// attempt_id should never be treated as matching anything.
	ownerUUID, err := uuid.Parse(ownerAttemptID)
	if err != nil {
		return status.Errorf(codes.PermissionDenied, "attempt %s does not own this environment", callerAttemptID)
	}
	callerUUID, err := uuid.Parse(callerAttemptID)
	if err != nil || ownerUUID != callerUUID {
		return status.Errorf(codes.PermissionDenied, "attempt %s does not own this environment", callerAttemptID)
	}
	return nil
}

// requireEnvironmentOwnership is the DB-lookup half that every ownership
// check site (Connect, MintValidatorCredentials, ExecValidator,
// ExecShell -- Destroy has its own variant below, see its own comment)
// needs before calling checkEnvironmentOwnership: resolve
// env.environment.attempt_id for envID, then compare it against the
// caller-supplied attempt_id. Folds the "row not found" case into
// PermissionDenied rather than NotFound too -- by the time this is
// called, s.requireEnvironment(ctx, envID) has already confirmed the
// namespace exists, so a missing DB row here would mean the environment
// exists in the cluster but isn't tracked in env.environment, an
// internal inconsistency rather than a caller error; treating it as
// non-ownership (fail closed) is the same conservative default
// checkEnvironmentOwnership already applies to a malformed attempt_id.
func (s *Server) requireEnvironmentOwnership(ctx context.Context, envID, callerAttemptID string) error {
	if callerAttemptID == "" {
		return status.Error(codes.InvalidArgument, "attempt_id is required")
	}
	var ownerAttemptID string
	if err := s.db.QueryRow(ctx, `SELECT attempt_id FROM env.environment WHERE id = $1`, envID).Scan(&ownerAttemptID); err != nil {
		return status.Errorf(codes.PermissionDenied, "attempt %s does not own this environment", callerAttemptID)
	}
	return checkEnvironmentOwnership(ownerAttemptID, callerAttemptID)
}

// requireEnvironment is PLAN.md Phase 3's U8: the
// "NamespaceExists -> err check -> !exists -> NotFound" block was
// verbatim-duplicated across 6 RPC handlers (Connect,
// MintValidatorCredentials, InjectFault, CaptureBaseline, ExecValidator,
// ExecShell) before this extraction. Destroy is deliberately NOT one of
// those call sites -- its own !exists case returns
// DestroyResponse{AlreadyDestroyed: true}, not a NotFound error (a
// double-Destroy on an already-gone environment is success, not a
// caller error), so it keeps its own inline check rather than being
// forced through a helper whose contract doesn't fit it.
//
// Returns a *status.Status error directly (not a bool+error pair) so
// every call site collapses to the same one-line
// `if err := s.requireEnvironment(...); err != nil { return nil, err }`
// -- the two error CASES (Internal on a real check failure, NotFound on
// a genuine miss) are exactly what was duplicated before, so folding
// them into a single returned error is what actually removes the
// duplication, not just hiding it one level deeper.
func (s *Server) requireEnvironment(ctx context.Context, envID string) error {
	exists, err := s.provisioner.NamespaceExists(ctx, envID)
	if err != nil {
		return status.Errorf(codes.Internal, "checking environment: %v", err)
	}
	if !exists {
		return status.Errorf(codes.NotFound, "environment %s not found", envID)
	}
	return nil
}

func (s *Server) Provision(ctx context.Context, req *pb.ProvisionRequest) (resp *pb.ProvisionResponse, err error) {
	// PLAN.md M1.14 audit baseline: records every Provision call
	// regardless of which return path is taken (named returns + defer,
	// so a future early return added later doesn't silently skip
	// auditing the way a scattered per-return-statement call would risk).
	// envID isn't known yet at defer-registration time -- captured by
	// reference via the closure once it's assigned below.
	var envID string
	// provisionSource is set to "warm" or "cold" once the pool-match
	// decision is made below; the deferred metrics recording reads it.
	provisionSource := "cold"
	start := time.Now()
	defer func() {
		outcome := audit.Success
		errMsg := ""
		result := "success"
		if err != nil {
			outcome = audit.Failure
			errMsg = err.Error()
			result = "failed"
		}
		s.audit.Record(context.Background(), audit.Entry{
			EnvironmentID: envID,
			AttemptID:     req.AttemptId,
			Action:        audit.ActionProvision,
			Outcome:       outcome,
			Detail:        map[string]any{"blueprint_id": req.BlueprintId, "tier": req.Tier.String()},
			ErrorMessage:  errMsg,
		})

		// doc §13.1 exit criteria: provision success rate (≥99%) and
		// time-to-ready p95 (≤20s) are computed from these two metrics.
		// Duration is only meaningful for the success path (a failure's
		// timing is dominated by whichever step errored), so the
		// histogram is only observed on success.
		tierLabel := req.Tier.String()
		metrics.ProvisionTotal.WithLabelValues(tierLabel, result).Inc()
		if err == nil {
			metrics.ProvisionDuration.WithLabelValues(tierLabel, provisionSource).Observe(time.Since(start).Seconds())
		}
	}()

	if req.AttemptId == "" {
		return nil, status.Error(codes.InvalidArgument, "attempt_id is required")
	}

	// Doc §5.1's tier-selection rule: the caller (Dev B's Attempt
	// Service, resolving an activity's environment.tier) declares which
	// tier it needs; this RPC provisions it, it doesn't choose it.
	tier, err := resolveTier(req.Tier, s.t2Enabled)
	if err != nil {
		return nil, err
	}

	// Doc §5.5 step 1: POOL MATCH. Attempt a warm-pool claim first; on
	// miss, cold-provision. Phase 1 warm pool is keyed by blueprint only
	// (tier is always T1 in Phase 1, so it's dropped from the key here) --
	// T2 requests always cold-provision (Claim is never even attempted
	// for them below), since Phase 2's warm pool has no T2 fill logic
	// yet and a wrong-tier warm-pool hit would be a correctness bug, not
	// just a missed optimisation.
	// envID declared in the defer block above; assigned (not
	// re-declared) here so the audit closure captures the real value.
	var claimed bool
	if tier == k8s.TierT1SharedContainer {
		envID, claimed = s.warmPool.Claim(ctx, req.BlueprintId)
	}
	if claimed {
		provisionSource = "warm"
	}
	if !claimed {
		envID = uuid.New().String()
		slogger.Info("cold-provisioning environment",
			logging.KeyEnvID, envID, logging.KeyAttemptID, req.AttemptId,
			"blueprint", req.BlueprintId, "tier", req.Tier.String(), "source", "cold")

		resources := k8s.DefaultT1Resources
		if tier == k8s.TierT2IsolatedMicroVM {
			resources = k8s.DefaultT2Resources // matches applyLimitRange's T2 ceiling (internal/k8s/provision.go)
		}

		if err := s.provisioner.Provision(ctx, k8s.ProvisionRequest{
			AttemptID: req.AttemptId,
			EnvID:     envID,
			Tier:      tier,
			Image:     imageForBlueprint(req.BlueprintId),
			Resources: resources,
		}); err != nil {
			return nil, status.Errorf(codes.Internal, "provisioning failed: %v", err)
		}
	} else {
		slogger.Info("warm-pool hit",
			logging.KeyEnvID, envID, logging.KeyAttemptID, req.AttemptId, "source", "warm")
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
		VALUES ($1, $2, $3, $4, $6, $5, now())
		ON CONFLICT (id) DO UPDATE SET attempt_id = $2, status = $6, ready_at = now()
	`, envID, req.AttemptId, req.Tier.String(), req.BlueprintId, "env-"+envID, envstatus.Ready); err != nil {
		slogger.Warn("failed to write env.environment row",
			logging.KeyEnvID, envID, logging.KeyAttemptID, req.AttemptId, logging.KeyError, err)
	}

	// Doc §13.5 #2: register with the reaper *before* returning to the
	// caller, so a crash on the very next line still leaves a hard
	// deadline on record.
	envTTL := resolveEnvTTL(tier, req.TtlMinutes, s.defaultTTL)
	if err := s.reaper.Register(ctx, envID, "env-"+envID, envTTL); err != nil {
		slogger.Warn("failed to register environment with reaper (may leak if not destroyed cleanly)",
			logging.KeyEnvID, envID, logging.KeyAttemptID, req.AttemptId, logging.KeyError, err)
	}

	// Doc §5.5 step ordering: "create ns -> quota/netpol -> create pods ->
	// fixture apply -> health gate." WaitForPodReady here is the
	// prerequisite for fixture-apply (fixture handlers exec into the pod
	// or mint credentials against the namespace, both of which need a
	// running pod), not the full "health gate" concept -- Phase 1's
	// richer blueprint self-check (HTTP probes, custom readiness
	// commands) is a fixture/blueprint-declared concept not yet modelled
	// in the T1 driver, so pod-Ready doubles as the health gate today,
	// same as before fixture-apply existed.
	if err := s.provisioner.WaitForPodReady(ctx, envID); err != nil {
		// Doc §5.5: "fail => discard & retry (max 2)" -- Phase 1 surfaces
		// the failure directly rather than auto-retrying; the caller
		// (AttemptService) already has its own PROVISION_FAILED path.
		return nil, status.Errorf(codes.DeadlineExceeded, "health gate failed: %v", err)
	}

	// Doc §5.5 step 3, PLAN.md M1.3: idempotent, ordered, checksummed
	// fixture application. Runs for BOTH cold-provisioned and
	// warm-pool-claimed environments -- a warm-pooled pod was
	// provisioned ahead of time against a generic blueprint, not this
	// specific activity's seed data, so fixtures still need to apply
	// regardless of which path produced envID.
	ns := "env-" + envID
	fixtureIDs := make([]string, 0, len(req.Fixtures))
	for _, f := range req.Fixtures {
		fixtureIDs = append(fixtureIDs, f.FixtureId)
	}
	if err := fixture.ApplyAll(ctx, s.provisioner, s.fixtures, envID, ns, fixtureIDs); err != nil {
		var noHandler fixture.ErrNoHandler
		if errors.As(err, &noHandler) {
			// Same stance as InjectFault's ErrNoHandler mapping: a
			// content-authored fixture id with no real handler yet is a
			// content/platform gap, not a reason to fail the whole
			// provision -- most of content/activities/*.yaml's seed:
			// blocks reference fixture ids that don't have handlers
			// (internal/fixture) yet, matching the same "content authored
			// ahead of the fixture/blueprint work" gap
			// internal/faultinjection's own package doc already
			// documents for faults. Log and continue rather than block
			// every activity that references an unimplemented fixture.
			slogger.Warn("fixture-apply skipped unimplemented fixture(s)",
				logging.KeyEnvID, envID, logging.KeyAttemptID, req.AttemptId, logging.KeyError, err)
		} else {
			return nil, status.Errorf(codes.Internal, "fixture apply failed: %v", err)
		}
	}

	// PLAN.md M1.4/M1.14: doc §3.2/§7.3's richer blueprint self-check,
	// declared per-activity as health_gate_json (JSON-encoded
	// []HealthGateCheck). Runs after fixtures (which establish whatever
	// baseline state a check probes) and before the environment is
	// considered READY -- doc §5.5's own step ordering: "fixture apply ->
	// health gate". Empty health_gate_json (every GUIDED_LAB activity
	// today, and any PRODUCTION_SIM that hasn't declared one) means no
	// richer check runs -- pod-Ready (already confirmed by
	// WaitForPodReady above) remains the whole gate, exactly the Phase 1
	// behavior this doesn't change for content that doesn't opt in.
	if req.HealthGateJson != "" {
		var rawChecks []map[string]any
		if err := json.Unmarshal([]byte(req.HealthGateJson), &rawChecks); err != nil {
			return nil, status.Errorf(codes.InvalidArgument, "invalid health_gate_json: %v", err)
		}
		checks, err := validation.ParseHealthGateJSON(rawChecks)
		if err != nil {
			return nil, status.Errorf(codes.InvalidArgument, "invalid health_gate_json: %v", err)
		}
		if err := validation.RunHealthGate(ctx, s.provisioner, envID, checks); err != nil {
			// Doc §5.5: "fail => discard & retry (max 2)" -- same
			// DeadlineExceeded mapping WaitForPodReady's own health-gate
			// failure already uses above, since this IS the richer half
			// of that same gate, not a separate concept.
			return nil, status.Errorf(codes.DeadlineExceeded, "health gate failed: %v", err)
		}
	}

	s.meter.StartMetering(envID, req.AttemptId)

	// Doc §5.6 M1.8: two-signal idle clock, started at READY (matching
	// the TTL clock's own start point).
	if s.idle != nil {
		idleTimeout := ttl.IdleTimeoutDefault // doc §5.6 default
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

func (s *Server) Connect(ctx context.Context, req *pb.ConnectRequest) (resp *pb.ConnectResponse, err error) {
	// PLAN.md Phase 2 closure item: Connect previously wrote zero audit
	// entries despite minting a session token -- same named-return +
	// defer pattern as Provision/InjectFault/MintValidatorCredentials so
	// every return path (including the ownership-check rejection below)
	// is covered without a per-return-statement call.
	defer func() {
		outcome := audit.Success
		errMsg := ""
		if err != nil {
			outcome = audit.Failure
			errMsg = err.Error()
		}
		s.audit.Record(context.Background(), audit.Entry{
			EnvironmentID: req.EnvironmentId,
			AttemptID:     req.AttemptId,
			Action:        audit.ActionConnect,
			Outcome:       outcome,
			ErrorMessage:  errMsg,
		})
	}()

	if err = s.requireEnvironment(ctx, req.EnvironmentId); err != nil {
		return nil, err
	}

	// ConnectRequest's attempt_id (contracts/orchestrator.proto) closes
	// PHASE2_CLOSEOUT.md's flagged access-control gap for this RPC: the
	// service-level shared-secret auth (auth.go) proves the CALLER is
	// practice-core's backend, but says nothing about which learner's
	// attempt the connection is being minted for -- without this check,
	// any Connect call could mint a session token for any environment_id
	// regardless of which attempt owns it. The same DB row this check
	// reads also satisfies the pre-existing "attempt_id for token-record
	// purposes" lookup below, so this replaces that lookup rather than
	// adding a second query.
	var attemptID string
	if qerr := s.db.QueryRow(ctx, `SELECT attempt_id FROM env.environment WHERE id = $1`, req.EnvironmentId).Scan(&attemptID); qerr != nil {
		log.Printf("[orchestrator] WARNING: could not resolve attempt_id for env=%s on Connect: %v", req.EnvironmentId, qerr)
	}
	if err = checkEnvironmentOwnership(attemptID, req.AttemptId); err != nil {
		return nil, err
	}

	resp, err = s.connectionEndpoints(attemptID, req.EnvironmentId)
	return resp, err
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
//
// Mints a REAL K8s ServiceAccount token via TokenRequest (mintValidatorCredential,
// credentials.go), scoped by a namespace-only Role/RoleBinding -- not a
// fake UUID with nothing behind it. Honest caveat, stated plainly rather
// than left implicit: ExecValidator (this orchestrator's actual
// validator-execution path) does not consume this credential -- it runs
// validators using the orchestrator's own cluster-wide client instead,
// an intentional architectural shortcut around the doc's original
// two-sided design. This RPC's minting half is now real and internally
// consistent regardless.
func (s *Server) MintValidatorCredentials(ctx context.Context, req *pb.MintCredentialsRequest) (resp *pb.MintCredentialsResponse, err error) {
	// PLAN.md M1.14 audit baseline. Detail carries scopes/ttl only --
	// never the minted token itself (this RPC's own doc/proto comment:
	// "opaque handle, never the raw secret in logs").
	defer func() {
		outcome := audit.Success
		errMsg := ""
		if err != nil {
			outcome = audit.Failure
			errMsg = err.Error()
		}
		s.audit.Record(context.Background(), audit.Entry{
			EnvironmentID: req.EnvironmentId,
			Action:        audit.ActionMintCredentials,
			Outcome:       outcome,
			Detail:        map[string]any{"scopes": req.Scopes, "ttl_seconds": req.TtlSeconds},
			ErrorMessage:  errMsg,
		})
	}()

	if err := s.requireEnvironment(ctx, req.EnvironmentId); err != nil {
		return nil, err
	}

	// PHASE2_CLOSEOUT.md's flagged access-control gap, closed for this
	// RPC same as InjectFault/Connect: minting a validator credential
	// scoped to environment_id must be limited to the attempt that owns
	// it, not any caller holding the shared secret.
	if err := s.requireEnvironmentOwnership(ctx, req.EnvironmentId, req.AttemptId); err != nil {
		return nil, err
	}

	ttlSeconds := req.TtlSeconds
	if ttlSeconds <= 0 {
		ttlSeconds = int32(ttl.ValidatorCredential.Seconds()) // doc §6.2 default: 5-minute lifetime
	}

	ns := k8s.NamespaceForEnv(req.EnvironmentId)
	token, err := mintValidatorCredential(ctx, s.provisioner.Clientset(), ns, req.EnvironmentId, int64(ttlSeconds), req.Scopes)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "minting validator credential: %v", err)
	}

	ref := uuid.New().String()
	expiresAt := time.Now().Add(time.Duration(ttlSeconds) * time.Second)
	s.credentials.Put(ref, token, expiresAt)

	// Doc/proto's own field comment: "opaque handle, never the raw
	// secret in logs" -- log the ref only, never `token`.
	log.Printf("[orchestrator] minted validator credential ref=%s env=%s ttl=%ds scopes=%v", ref, req.EnvironmentId, ttlSeconds, req.Scopes)

	return &pb.MintCredentialsResponse{
		CredentialRef: ref,
		ExpiresAt:     expiresAtRFC3339(ttlSeconds),
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

	// PHASE2_CLOSEOUT.md's flagged access-control gap, closed for this
	// RPC same as InjectFault/Connect/MintValidatorCredentials -- but
	// deliberately placed AFTER the !exists short-circuit above, not
	// before it: once the namespace is already gone there is no
	// env.environment owner row left to check against, and a
	// double-Destroy of an already-gone environment is success, not a
	// caller error (DestroyResponse's own contract). Requiring
	// attempt_id on that path would turn an idempotent no-op into a
	// spurious rejection.
	if err := s.requireEnvironmentOwnership(ctx, req.EnvironmentId, req.AttemptId); err != nil {
		return nil, err
	}

	// Doc §4.2 / contracts/events.md rule #3: every teardown path --
	// clean submit, idle/TTL/budget, reaper force-destroy -- funnels
	// through the same Destroyer so ENV_DESTROYED always gets published
	// and the bookkeeping (meter stop, idle untrack, DB status, reaper
	// unregister) never drifts between call sites.
	if err := s.destroyer.Destroy(ctx, req.EnvironmentId, req.Reason); err != nil {
		return nil, status.Errorf(codes.Internal, "destroy failed: %v", err)
	}
	return &pb.DestroyResponse{AlreadyDestroyed: false}, nil
}

// InjectFault is Phase 2 scope (Production Simulations, PLAN.md Phase 2
// integration points). Doc §3.3 contract: "Environment provisioned to a
// broken state via a fault injection manifest applied after a healthy
// baseline is confirmed." internal/faultinjection owns the actual K8s
// mutation per fault (a small registry of Go handlers, since fault
// application needs real cluster credentials the workspace pod itself
// doesn't have -- see that package's doc comment).
//
// Three distinct outcomes map to three distinct gRPC codes, not just
// "success or Unimplemented":
//   - ErrNoHandler: fault_id isn't in the registry at all -- genuinely
//     un-triaged. Unimplemented, same as before.
//   - ErrUnsupportedMechanism: fault_id IS registered, and has been
//     triaged as needing infrastructure this deployment doesn't have
//     (no baseline fixture, tier unavailable, or a specific pending
//     contract like the HPA-metrics case) -- FailedPrecondition, with
//     the stable reason tag (faultinjection.Reason*) embedded in the
//     message so a caller can branch on *why* without string-matching
//     prose. Chose message-embedding over a new response field because
//     InjectFaultResponse is a shared contract (contracts/) -- adding a
//     field there is a joint-review change PLAN.md's cross-cutting
//     ownership rules gate on both devs approving, out of scope for a
//     handler-coverage pass.
//   - any other error: a real handler ran and failed -- Internal, as
//     before.
func (s *Server) InjectFault(ctx context.Context, req *pb.InjectFaultRequest) (resp *pb.InjectFaultResponse, err error) {
	// PLAN.md M1.14 audit baseline -- InjectFault deliberately breaks
	// environment state, making it exactly the kind of operation an
	// audit trail exists to record.
	defer func() {
		outcome := audit.Success
		errMsg := ""
		if err != nil {
			outcome = audit.Failure
			errMsg = err.Error()
		}
		s.audit.Record(context.Background(), audit.Entry{
			EnvironmentID: req.EnvironmentId,
			AttemptID:     req.AttemptId,
			Action:        audit.ActionInjectFault,
			Outcome:       outcome,
			Detail:        map[string]any{"fault_id": req.FaultId},
			ErrorMessage:  errMsg,
		})
	}()

	if req.EnvironmentId == "" || req.FaultId == "" {
		return nil, status.Error(codes.InvalidArgument, "environment_id and fault_id are required")
	}
	if req.AttemptId == "" {
		return nil, status.Error(codes.InvalidArgument, "attempt_id is required")
	}

	// PHASE2_CLOSEOUT.md's flagged access-control gap, closed: the
	// service-level shared-secret auth (auth.go) proves the CALLER is
	// practice-core's backend, but says nothing about which learner's
	// attempt that call is acting on behalf of -- without this check,
	// any InjectFault call could target any environment_id regardless
	// of which attempt owns it. env.environment.attempt_id is the
	// existing durable record of that ownership (written by Provision,
	// see this file's INSERT INTO env.environment above) -- a mismatch
	// here means either a client bug (attempt A's code accidentally
	// passed attempt B's environment_id) or an attempted cross-attempt
	// fault injection, and PermissionDenied is correct for both: the
	// caller is authenticated but not authorized for THIS resource.
	var ownerAttemptID, envTier string
	if err := s.db.QueryRow(ctx, `SELECT attempt_id, tier FROM env.environment WHERE id = $1`, req.EnvironmentId).Scan(&ownerAttemptID, &envTier); err != nil {
		return nil, status.Errorf(codes.NotFound, "environment %s not found", req.EnvironmentId)
	}
	if err := checkEnvironmentOwnership(ownerAttemptID, req.AttemptId); err != nil {
		return nil, err
	}

	if err := s.requireEnvironment(ctx, req.EnvironmentId); err != nil {
		return nil, err
	}

	// M3's own explicit instruction: T2-only faults (Istio/ArgoCD CRD
	// mutations that don't exist in a T1 namespace) must fail closed
	// with FailedPrecondition against a non-T2 environment, checked
	// BEFORE Apply runs any mutation -- don't assume the caller's own
	// tier bookkeeping (practice-core's activity/environment.tier
	// resolution) is consistent with what this environment was actually
	// provisioned as.
	if faultinjection.RequiresT2(req.FaultId) && envTier != pb.Tier_TIER_T2_ISOLATED_MICROVM.String() {
		return nil, status.Errorf(codes.FailedPrecondition,
			"fault %s requires a T2 (isolated microVM) environment, but environment %s is tier %s", req.FaultId, req.EnvironmentId, envTier)
	}

	result, err := faultinjection.Apply(ctx, s.provisioner, req.EnvironmentId, req.FaultId, req.Params)
	if err != nil {
		var noHandler faultinjection.ErrNoHandler
		if errors.As(err, &noHandler) {
			return nil, status.Errorf(codes.Unimplemented, "no fault-injection handler implemented for %s yet", req.FaultId)
		}
		var unsupported faultinjection.ErrUnsupportedMechanism
		if errors.As(err, &unsupported) {
			return nil, status.Errorf(codes.FailedPrecondition, "fault %s deferred: reason=%s", req.FaultId, unsupported.Reason)
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
func (s *Server) CaptureBaseline(ctx context.Context, req *pb.CaptureBaselineRequest) (resp *pb.CaptureBaselineResponse, err error) {
	// PLAN.md Phase 2 closure item: CaptureBaseline previously wrote zero
	// audit entries. Note (also called out in the Phase 2 closeout
	// report): this RPC still has no ownership check -- unlike
	// Connect/InjectFault/ExecValidator/ExecShell/Destroy, there is no
	// attempt_id field on CaptureBaselineRequest to check against
	// (PLAN_RPC_AUTHZ.md's own documented non-goal). Adding audit logging
	// here does not close that gap; it only makes the existing exposure
	// observable after the fact.
	var resourcesCaptured int
	defer func() {
		outcome := audit.Success
		errMsg := ""
		if err != nil {
			outcome = audit.Failure
			errMsg = err.Error()
		}
		s.audit.Record(context.Background(), audit.Entry{
			EnvironmentID: req.EnvironmentId,
			Action:        audit.ActionCaptureBaseline,
			Outcome:       outcome,
			Detail:        map[string]any{"snapshot_key": req.SnapshotKey, "resources_captured": resourcesCaptured},
			ErrorMessage:  errMsg,
		})
	}()

	if req.EnvironmentId == "" || req.SnapshotKey == "" {
		err = status.Error(codes.InvalidArgument, "environment_id and snapshot_key are required")
		return nil, err
	}

	if err = s.requireEnvironment(ctx, req.EnvironmentId); err != nil {
		return nil, err
	}

	matrix, capErr := regression.Capture(ctx, s.provisioner, req.EnvironmentId)
	if capErr != nil {
		err = status.Errorf(codes.Internal, "capturing baseline: %v", capErr)
		return nil, err
	}

	matrixJSON, jsonErr := json.Marshal(matrix)
	if jsonErr != nil {
		err = status.Errorf(codes.Internal, "encoding health matrix: %v", jsonErr)
		return nil, err
	}

	capturedAt := time.Now().UTC()
	_, dbErr := s.db.Exec(ctx, `
		INSERT INTO env.regression_baseline (environment_id, snapshot_key, health_matrix, captured_at)
		VALUES ($1, $2, $3, $4)
		ON CONFLICT (environment_id, snapshot_key)
		DO UPDATE SET health_matrix = $3, captured_at = $4
	`, req.EnvironmentId, req.SnapshotKey, matrixJSON, capturedAt)
	if dbErr != nil {
		err = status.Errorf(codes.Internal, "persisting baseline: %v", dbErr)
		return nil, err
	}

	resourcesCaptured = len(matrix.Deployments) + len(matrix.Services)
	log.Printf("[orchestrator] baseline captured: env=%s snapshot_key=%s resources=%d", req.EnvironmentId, req.SnapshotKey, resourcesCaptured)
	resp = &pb.CaptureBaselineResponse{
		SnapshotKey:       req.SnapshotKey,
		CapturedAt:        capturedAt.Format(time.RFC3339),
		ResourcesCaptured: int32(resourcesCaptured),
	}
	return resp, nil
}

// CheckRegression backs the NO_REGRESSION validator type's actual pass/
// fail check: diff the environment's current state against a previously
// captured baseline. Doc §3.3's own worked example: "didn't fix by
// breaking something else."
func (s *Server) CheckRegression(ctx context.Context, req *pb.CheckRegressionRequest) (resp *pb.CheckRegressionResponse, err error) {
	// PLAN.md Phase 2 closure item: CheckRegression previously wrote zero
	// audit entries. Same ownership-check caveat as CaptureBaseline above
	// (no attempt_id field on this RPC either) -- audit logging alone
	// does not close that gap.
	var regressionFound bool
	defer func() {
		outcome := audit.Success
		errMsg := ""
		if err != nil {
			outcome = audit.Failure
			errMsg = err.Error()
		}
		s.audit.Record(context.Background(), audit.Entry{
			EnvironmentID: req.EnvironmentId,
			Action:        audit.ActionCheckRegression,
			Outcome:       outcome,
			Detail:        map[string]any{"snapshot_key": req.SnapshotKey, "regression_found": regressionFound},
			ErrorMessage:  errMsg,
		})
	}()

	if req.EnvironmentId == "" || req.SnapshotKey == "" {
		err = status.Error(codes.InvalidArgument, "environment_id and snapshot_key are required")
		return nil, err
	}

	var matrixJSON []byte
	qerr := s.db.QueryRow(ctx, `
		SELECT health_matrix FROM env.regression_baseline
		WHERE environment_id = $1 AND snapshot_key = $2
	`, req.EnvironmentId, req.SnapshotKey).Scan(&matrixJSON)
	if qerr != nil {
		err = status.Errorf(codes.NotFound, "no baseline found for environment=%s snapshot_key=%s: %v", req.EnvironmentId, req.SnapshotKey, qerr)
		return nil, err
	}

	var baseline regression.HealthMatrix
	if uerr := json.Unmarshal(matrixJSON, &baseline); uerr != nil {
		err = status.Errorf(codes.Internal, "decoding stored baseline: %v", uerr)
		return nil, err
	}

	regressions, derr := regression.Diff(ctx, s.provisioner, req.EnvironmentId, baseline)
	if derr != nil {
		err = status.Errorf(codes.Internal, "checking regression: %v", derr)
		return nil, err
	}

	resourceNames := make([]string, 0, len(regressions))
	for _, r := range regressions {
		resourceNames = append(resourceNames, fmt.Sprintf("%s: %s", r.Resource, r.Detail))
	}

	regressionFound = len(regressions) > 0
	log.Printf("[orchestrator] regression check: env=%s snapshot_key=%s found=%d", req.EnvironmentId, req.SnapshotKey, len(regressions))
	resp = &pb.CheckRegressionResponse{
		RegressionFound:    regressionFound,
		RegressedResources: resourceNames,
	}
	return resp, nil
}

// ExecValidator replaces practice-core's FakeValidatorExecutor for the 6
// validator types every published lab actually uses. See
// internal/validation's package doc for the full architecture rationale
// (workspace pod has no cluster credentials for K8S_ASSERT; SHELL_*/
// FILE_* exec inside the pod non-interactively).
func (s *Server) ExecValidator(ctx context.Context, req *pb.ExecValidatorRequest) (resp *pb.ExecValidatorResponse, err error) {
	// PLAN.md Phase 2 closure item: ExecValidator previously wrote zero
	// audit entries. A validator that ERRORs (validation.StatusError) is
	// still a nil-error RPC response by design (doc §6.2: "ERROR is never
	// scored against the learner" needs a normal response, not an RPC
	// failure) -- so this audits on resp.Status == StatusError in
	// addition to err != nil, otherwise a validator that silently broke
	// would be recorded as a SUCCESS, which would defeat the point of
	// having an audit trail for exactly this kind of "didn't work as
	// expected" event.
	defer func() {
		outcome := audit.Success
		errMsg := ""
		if err != nil {
			outcome = audit.Failure
			errMsg = err.Error()
		} else if resp != nil && resp.Status == validation.StatusError {
			outcome = audit.Failure
			errMsg = resp.ErrorMessage
		}
		s.audit.Record(context.Background(), audit.Entry{
			EnvironmentID: req.EnvironmentId,
			AttemptID:     req.AttemptId,
			Action:        audit.ActionExecValidator,
			Outcome:       outcome,
			Detail:        map[string]any{"validator_id": req.ValidatorId, "validator_type": req.ValidatorType},
			ErrorMessage:  errMsg,
		})
	}()

	if req.EnvironmentId == "" || req.ValidatorId == "" || req.ValidatorType == "" {
		err = status.Error(codes.InvalidArgument, "environment_id, validator_id, and validator_type are required")
		return nil, err
	}

	if err = s.requireEnvironment(ctx, req.EnvironmentId); err != nil {
		return nil, err
	}

	// PHASE2_CLOSEOUT.md's flagged access-control gap, closed for this
	// RPC same as InjectFault/Connect/MintValidatorCredentials/Destroy --
	// a validator must only be run against the attempt's own environment.
	if err = s.requireEnvironmentOwnership(ctx, req.EnvironmentId, req.AttemptId); err != nil {
		return nil, err
	}

	var expect map[string]any
	if req.ExpectJson != "" {
		if uerr := json.Unmarshal([]byte(req.ExpectJson), &expect); uerr != nil {
			err = status.Errorf(codes.InvalidArgument, "decoding expect_json: %v", uerr)
			return nil, err
		}
	}

	result, execErr := validation.Exec(ctx, s.provisioner, validation.Request{
		EnvironmentID: req.EnvironmentId,
		ValidatorID:   req.ValidatorId,
		ValidatorType: req.ValidatorType,
		Run:           req.Run,
		Expect:        expect,
		TimeoutMs:     req.TimeoutMs,
	})
	if execErr != nil {
		var unsupported validation.ErrUnsupportedType
		if errors.As(execErr, &unsupported) {
			err = status.Errorf(codes.Unimplemented, "no real executor implemented for validator type %s yet", req.ValidatorType)
			return nil, err
		}
		// A validator that ERRORs (couldn't run at all) is still a
		// successful RPC response, not a gRPC error -- doc §6.2: "ERROR
		// (validator itself broke) is never scored against the learner,"
		// which means the caller needs a normal response to record that,
		// not an RPC failure to handle separately. (Still recorded as an
		// audit failure via the resp.Status check in the defer above.)
		log.Printf("[orchestrator] ExecValidator errored: env=%s validator=%s type=%s: %v", req.EnvironmentId, req.ValidatorId, req.ValidatorType, execErr)
		resp = &pb.ExecValidatorResponse{Status: validation.StatusError, ErrorMessage: execErr.Error(), DurationMs: result.DurationMs}
		return resp, nil
	}

	observedJSON, jsonErr := json.Marshal(result.Observed)
	if jsonErr != nil {
		observedJSON = []byte("null")
	}

	log.Printf("[orchestrator] validator executed: env=%s validator=%s type=%s status=%s", req.EnvironmentId, req.ValidatorId, req.ValidatorType, result.Status)
	resp = &pb.ExecValidatorResponse{
		Status:       result.Status,
		ObservedJson: string(observedJSON),
		DurationMs:   result.DurationMs,
	}
	return resp, nil
}

func (s *Server) ExecShell(ctx context.Context, req *pb.ExecShellRequest) (*pb.ExecShellResponse, error) {
	if req.EnvironmentId == "" || req.Command == "" {
		return nil, status.Error(codes.InvalidArgument, "environment_id and command are required")
	}

	if err := s.requireEnvironment(ctx, req.EnvironmentId); err != nil {
		return nil, err
	}

	// PHASE2_CLOSEOUT.md's flagged access-control gap, closed for this
	// RPC same as InjectFault/Connect/MintValidatorCredentials/Destroy/
	// ExecValidator -- arbitrary shell execution inside a learner's
	// workspace pod is exactly the kind of operation an ownership check
	// exists to gate. Audited explicitly here (rather than falling
	// through to the audit block below) because a rejection at this
	// point means validation.ExecShell never ran, so there is no `result`
	// yet for that block to record against.
	if err := s.requireEnvironmentOwnership(ctx, req.EnvironmentId, req.AttemptId); err != nil {
		s.audit.Record(context.Background(), audit.Entry{
			EnvironmentID: req.EnvironmentId,
			Action:        audit.ActionExecShell,
			Outcome:       audit.Failure,
			Detail:        map[string]any{"command": req.Command},
			ErrorMessage:  err.Error(),
		})
		return nil, err
	}

	result, err := validation.ExecShell(ctx, s.provisioner, req.EnvironmentId, req.Command, req.TimeoutMs)
	// PLAN.md M1.14 audit baseline. Detail records the command text and
	// exit code (needed to make the audit trail useful at all: "a shell
	// command ran" with no idea which one is not an audit log) but
	// deliberately NEVER stdout/stderr, which routinely contain secrets
	// a learner's environment might hold (doc §9.3: "any secret that
	// appears in a learner's environment must be assumed compromised" --
	// an audit log is exactly the kind of place a leaked secret would
	// have outsized blast radius if captured here).
	//
	// Logging the command text itself is a smaller, but real, residual
	// risk worth stating rather than assuming away: in principle a
	// command string COULD embed a secret (e.g. a curl call with an
	// inline auth header). Checked this RPC's actual callers
	// (practice-core/src/modules/attempt/workspace-file.service.ts) --
	// ExecShell backs a file-management API (cat/mkdir/ls-shaped
	// commands over learner-workspace paths), not free-form learner-typed
	// shell text, so the command strings that reach this RPC are
	// code-constructed with path arguments, not arbitrary learner input.
	// That materially bounds the risk without eliminating it -- revisit
	// if a future caller starts passing genuinely free-form command text.
	auditOutcome := audit.Success
	auditErrMsg := ""
	if err != nil {
		auditOutcome = audit.Failure
		auditErrMsg = err.Error()
	}
	s.audit.Record(context.Background(), audit.Entry{
		EnvironmentID: req.EnvironmentId,
		Action:        audit.ActionExecShell,
		Outcome:       auditOutcome,
		Detail:        map[string]any{"command": req.Command, "exit_code": result.ExitCode},
		ErrorMessage:  auditErrMsg,
	})

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
