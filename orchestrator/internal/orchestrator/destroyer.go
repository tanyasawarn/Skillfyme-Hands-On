package orchestrator

import (
	"context"
	"encoding/json"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/nats-io/nats.go"

	"github.com/tanyasawarn/skillfyme-hands-on/orchestrator/internal/logging"

	"github.com/tanyasawarn/skillfyme-hands-on/orchestrator/internal/audit"
	"github.com/tanyasawarn/skillfyme-hands-on/orchestrator/internal/costmeter"
	"github.com/tanyasawarn/skillfyme-hands-on/orchestrator/internal/envstatus"
	"github.com/tanyasawarn/skillfyme-hands-on/orchestrator/internal/k8s"
	"github.com/tanyasawarn/skillfyme-hands-on/orchestrator/internal/reaper"
)

// RecordingForgetter is the narrow interface Destroyer needs from
// telemetry.S3RecordingSink -- kept as an interface (not a direct
// *telemetry.S3RecordingSink field) so this package doesn't need to
// import internal/telemetry just for this one optional hook, matching
// TokenRegistrar/IdleTracker's existing narrow-interface pattern in
// server.go.
type RecordingForgetter interface {
	Forget(attemptID string)
}

// natsSubjectEnvDestroyed matches contracts/events.md's "env.telemetry.<event_type>"
// subject convention for Orchestrator -> Practice Core events (PLAN.md
// integration point #4).
const natsSubjectEnvDestroyed = "env.telemetry.ENV_DESTROYED"

// envDestroyedPayload matches contracts/events/env_destroyed.schema.json.
type envDestroyedPayload struct {
	EnvironmentID string `json:"environment_id"`
	Reason        string `json:"reason"`
}

// envDestroyedEnvelope wraps the payload with attempt_id, mirroring
// telemetry.NATSEventSink's {attempt_id, payload} shape for
// COMMAND_EXECUTED -- Practice Core's NATS consumer needs attempt_id to
// route the event to the right attempt without a second DB lookup.
type envDestroyedEnvelope struct {
	AttemptID string              `json:"attempt_id"`
	Payload   envDestroyedPayload `json:"payload"`
}

// Destroyer is the single place every environment-teardown path funnels
// through -- doc §4.2 / contracts/events.md rule #3: "ENV_DESTROYED is
// the only way an attempt learns its environment is gone, whether that's
// a clean submit teardown, idle/TTL/budget teardown, or the reaper
// force-destroying past a deadline." Before this existed, only the gRPC
// Destroy() RPC (the "submit" path) did the full teardown (namespace
// delete + meter stop + idle untrack + DB status + reaper unregister +
// notify); idledetect and reaper called k8s.Provisioner.Destroy directly,
// skipping all of that bookkeeping and never publishing ENV_DESTROYED --
// so an idle-timeout or TTL-expired environment left its attempt stuck
// IN_PROGRESS forever, permanently occupying the learner's one
// concurrent-environment slot even though the infrastructure was long
// gone. Constructed once in main.go and handed to every consumer
// (Server, reaper.Reaper, idledetect.Detector, costmeter.Meter) as the
// same underlying logic, so there is exactly one teardown path instead
// of four divergent ones.
type Destroyer struct {
	db          *pgxpool.Pool
	provisioner *k8s.Provisioner
	meter       *costmeter.Meter
	reaper      *reaper.Reaper
	idle        IdleTracker
	recording   RecordingForgetter
	nc          *nats.Conn
	audit       *audit.Logger
}

// NewDestroyer builds a Destroyer. meter/reaper/idle are set via their
// own setters (SetMeter/SetIdleTracker) after construction, since
// costmeter.Meter and idledetect.Detector are themselves constructed
// with a DestroyFunc closing over this Destroyer -- Go closures capture
// by reference, so the closure is safe to hand out before these fields
// are populated as long as nothing invokes it before main.go finishes
// wiring (true here: metering/idle tracking only starts once Provision
// runs, which is well after main.go returns from setup).
func NewDestroyer(db *pgxpool.Pool, provisioner *k8s.Provisioner, rp *reaper.Reaper, nc *nats.Conn) *Destroyer {
	return &Destroyer{db: db, provisioner: provisioner, reaper: rp, nc: nc, audit: audit.NewLogger(db)}
}

func (d *Destroyer) SetMeter(m *costmeter.Meter)       { d.meter = m }
func (d *Destroyer) SetIdleTracker(idle IdleTracker)   { d.idle = idle }
func (d *Destroyer) SetRecording(r RecordingForgetter) { d.recording = r }

// Destroy tears down envID's namespace and every piece of bookkeeping
// that must stay in sync with it, then publishes ENV_DESTROYED so
// Practice Core learns the environment (and therefore the attempt's
// occupancy of the learner's concurrent-environment slot) is gone.
// Idempotent: safe to call on an already-destroyed environment (the
// namespace delete no-ops per k8s.Provisioner.Destroy's own contract),
// which matters because both a clean submit and a slightly-later
// reaper sweep can race to destroy the same environment.
func (d *Destroyer) Destroy(ctx context.Context, envID, reason string) (err error) {
	// PLAN.md M1.14 audit baseline -- see Server.Provision's identical
	// pattern/rationale for why this is a defer over named-return err
	// rather than a call at each return statement.
	defer func() {
		outcome := audit.Success
		errMsg := ""
		if err != nil {
			outcome = audit.Failure
			errMsg = err.Error()
		}
		d.audit.Record(context.Background(), audit.Entry{
			EnvironmentID: envID,
			Action:        audit.ActionDestroy,
			Outcome:       outcome,
			Detail:        map[string]any{"reason": reason},
			ErrorMessage:  errMsg,
		})
	}()

	exists, err := d.provisioner.NamespaceExists(ctx, envID)
	if err != nil {
		return err
	}
	if !exists {
		return nil // already gone -- still fine to fall through and re-publish/reconcile below
	}

	slogger.Info("destroying environment", logging.KeyEnvID, envID, logging.KeyReason, reason)

	if d.meter != nil {
		d.meter.StopMetering(envID)
	}
	if d.idle != nil {
		d.idle.Untrack(envID)
	}

	if err := d.provisioner.Destroy(ctx, envID); err != nil {
		return err
	}

	var attemptID string
	if err := d.db.QueryRow(ctx, `
		UPDATE env.environment SET status = $3, destroyed_at = now(), destroy_reason = $2
		WHERE id = $1
		RETURNING attempt_id
	`, envID, reason, envstatus.Destroyed).Scan(&attemptID); err != nil {
		slogger.Warn("failed to mark environment destroyed in DB", logging.KeyEnvID, envID, logging.KeyReason, reason, logging.KeyError, err)
	}

	if d.reaper != nil {
		if err := d.reaper.Unregister(ctx, envID); err != nil {
			slogger.Warn("failed to unregister environment from reaper", logging.KeyEnvID, envID, logging.KeyError, err)
		}
	}

	if d.recording != nil && attemptID != "" {
		// Forget does one final synchronous flush before dropping the
		// buffer (telemetry.S3RecordingSink's own doc comment) -- without
		// this, up to flushInterval's worth of trailing output (the most
		// recent commands before the environment was torn down, often
		// the most relevant part of a recording) would be silently lost.
		// Best-effort like this method's other post-destroy bookkeeping:
		// a failed flush is logged, not returned as an error, since the
		// environment itself is already gone by this point regardless.
		d.recording.Forget(attemptID)
	}

	d.publishEnvDestroyed(attemptID, envID, reason)
	return nil
}

func (d *Destroyer) publishEnvDestroyed(attemptID, envID, reason string) {
	if d.nc == nil || attemptID == "" {
		// No attempt_id resolved (e.g. env.environment row missing/already
		// cleared) -- nothing for Practice Core to route the event to.
		// Not an error: this can legitimately happen on a re-destroy of an
		// already-torn-down environment.
		return
	}
	data, err := json.Marshal(envDestroyedEnvelope{
		AttemptID: attemptID,
		Payload:   envDestroyedPayload{EnvironmentID: envID, Reason: reason},
	})
	if err != nil {
		slogger.Error("failed to marshal ENV_DESTROYED", logging.KeyEnvID, envID, logging.KeyAttemptID, attemptID, logging.KeyError, err)
		return
	}
	if err := d.nc.Publish(natsSubjectEnvDestroyed, data); err != nil {
		slogger.Error("failed to publish ENV_DESTROYED", logging.KeyEnvID, envID, logging.KeyAttemptID, attemptID, logging.KeyError, err)
	}
}
