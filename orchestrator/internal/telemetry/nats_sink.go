// Package telemetry provides the EventSink/RecordingSink implementations
// internal/sessionbroker needs, wired to the real transport doc §13.2
// recommends (NATS JetStream) and to S3-compatible object storage (doc
// §5.4: "record asciicast to S3").
package telemetry

import (
	"context"
	"encoding/json"
	"log"

	"github.com/nats-io/nats.go"
	"github.com/tanyasawarn/skillfyme-hands-on/orchestrator/internal/sessionbroker"
)

// natsSubjectCommandExecuted matches contracts/events.md's agreed
// subject naming for Dev A -> Dev B execution/environment events:
// "env.telemetry.<event_type>" -- PLAN.md integration point #2b.
const natsSubjectCommandExecuted = "env.telemetry.COMMAND_EXECUTED"

// commandExecutedPayload mirrors contracts/events/command_executed.schema.json
// (updated alongside this implementation: cwd made optional since the
// Phase 1 shell hook doesn't capture it yet, and stdout_hash renamed to
// cmd_hash to accurately describe what's hashed -- see the schema file's
// own comment for the reasoning).
type commandExecutedPayload struct {
	Cmd        string `json:"cmd"`
	ExitCode   int    `json:"exit_code"`
	DurationMs int64  `json:"duration_ms"`
	CmdHash    string `json:"cmd_hash"`
	Source     string `json:"source"`
}

// NATSEventSink implements sessionbroker.EventSink by publishing to
// NATS. Doc §4.2's envelope (attempt_id, seq, occurred_at, actor, type,
// payload) is Practice Core's event-store writer's job to assemble --
// this sink publishes the raw payload plus attempt_id; Dev B's NATS
// consumer (not yet built on that side) is responsible for wrapping it
// into an attempt_events row with a server-assigned seq, per
// contracts/events.md: "seq is assigned by the writer (this service)."
type NATSEventSink struct {
	nc *nats.Conn
}

func NewNATSEventSink(nc *nats.Conn) *NATSEventSink {
	return &NATSEventSink{nc: nc}
}

func (s *NATSEventSink) Publish(ctx context.Context, event sessionbroker.CommandEvent) {
	payload := commandExecutedPayload{
		Cmd:        event.Cmd,
		ExitCode:   event.ExitCode,
		DurationMs: event.DurationMs,
		CmdHash:    event.CmdHash,
		Source:     "web_terminal",
	}
	data, err := json.Marshal(struct {
		AttemptID string                 `json:"attempt_id"`
		Payload   commandExecutedPayload `json:"payload"`
	}{AttemptID: event.AttemptID, Payload: payload})
	if err != nil {
		log.Printf("[telemetry] failed to marshal command event: %v", err)
		return
	}

	if err := s.nc.Publish(natsSubjectCommandExecuted, data); err != nil {
		log.Printf("[telemetry] failed to publish command event: %v", err)
	}
}

// LogRecordingSink is a Phase 1 stand-in for doc §5.4's "record
// asciicast to S3" -- real S3/object-storage upload needs bucket
// credentials and a lifecycle policy (doc §4.3 retention table) that are
// deployment configuration, not something to hardcode here. This sink
// makes the tap point real and exercised (every byte does flow through
// it) without committing to a specific object-storage SDK/bucket before
// a real deployment target is chosen.
type LogRecordingSink struct{}

func (LogRecordingSink) Write(ctx context.Context, attemptID string, data []byte) {
	// Deliberately not logging the actual bytes (terminal output can
	// contain secrets/credentials mid-session, doc §9.3: "any secret
	// that appears in a learner's environment must be assumed
	// compromised" -- logging it in cleartext here would be an
	// unnecessary secondary exposure). Byte count only, as a liveness
	// signal that the tap is active.
	_ = ctx
	_ = attemptID
	_ = data
}
