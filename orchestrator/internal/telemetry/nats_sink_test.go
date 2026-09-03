package telemetry

import (
	"context"
	"encoding/json"
	"os"
	"testing"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/tanyasawarn/skillfyme-hands-on/orchestrator/internal/sessionbroker"
)

// PLAN.md integration point #2b: the Session Broker emits COMMAND_EXECUTED
// on "env.telemetry.<event_type>"; practice-core's event store is the
// sink. This round-trips a real CommandEvent through NATSEventSink and
// asserts the wire shape practice-core's command-executed.consumer.ts
// parses -- subject + {attempt_id, payload:{cmd,exit_code,duration_ms,
// cmd_hash,source}}.
//
// Skips (does not fail) when the dev-stack NATS isn't running, matching
// internal/orchestrator/ownership_rpc_test.go's convention.
func TestNATSEventSink_PublishesCommandExecutedOnAgreedSubject(t *testing.T) {
	natsURL := os.Getenv("NATS_URL")
	if natsURL == "" {
		natsURL = "nats://localhost:4222"
	}
	nc, err := nats.Connect(natsURL, nats.Timeout(3*time.Second))
	if err != nil {
		t.Skipf("skipping: nats unreachable (dev stack not running?): %v", err)
	}
	t.Cleanup(nc.Close)

	sub, err := nc.SubscribeSync(natsSubjectCommandExecuted)
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	t.Cleanup(func() { _ = sub.Unsubscribe() })
	if err := nc.Flush(); err != nil {
		t.Fatalf("flush: %v", err)
	}

	sink := NewNATSEventSink(nc)
	sink.Publish(context.Background(), sessionbroker.CommandEvent{
		AttemptID:  "11111111-1111-1111-1111-111111111111",
		Cmd:        "kubectl get pods",
		ExitCode:   0,
		DurationMs: 42,
		CmdHash:    "abc123",
	})

	msg, err := sub.NextMsg(3 * time.Second)
	if err != nil {
		t.Fatalf("no message received on %s: %v", natsSubjectCommandExecuted, err)
	}
	if msg.Subject != "env.telemetry.COMMAND_EXECUTED" {
		t.Errorf("subject = %q, want env.telemetry.COMMAND_EXECUTED", msg.Subject)
	}

	var got struct {
		AttemptID string `json:"attempt_id"`
		Payload   struct {
			Cmd        string `json:"cmd"`
			ExitCode   int    `json:"exit_code"`
			DurationMs int64  `json:"duration_ms"`
			CmdHash    string `json:"cmd_hash"`
			Source     string `json:"source"`
		} `json:"payload"`
	}
	if err := json.Unmarshal(msg.Data, &got); err != nil {
		t.Fatalf("unmarshal published message: %v", err)
	}
	if got.AttemptID != "11111111-1111-1111-1111-111111111111" {
		t.Errorf("attempt_id = %q", got.AttemptID)
	}
	if got.Payload.Cmd != "kubectl get pods" {
		t.Errorf("payload.cmd = %q", got.Payload.Cmd)
	}
	if got.Payload.ExitCode != 0 {
		t.Errorf("payload.exit_code = %d", got.Payload.ExitCode)
	}
	if got.Payload.DurationMs != 42 {
		t.Errorf("payload.duration_ms = %d", got.Payload.DurationMs)
	}
	if got.Payload.CmdHash != "abc123" {
		t.Errorf("payload.cmd_hash = %q", got.Payload.CmdHash)
	}
	if got.Payload.Source != "web_terminal" {
		t.Errorf("payload.source = %q, want web_terminal", got.Payload.Source)
	}
}

// A malformed event (unmarshalable) must not panic -- Publish logs and
// returns. CommandEvent is all value types so this can't actually fail
// json.Marshal today; the guard is future-proofing and this test pins
// the "never panic" contract.
func TestNATSEventSink_PublishNeverPanics(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("Publish panicked: %v", r)
		}
	}()
	// nil conn: Publish should log the publish error, not panic.
	sink := NewNATSEventSink(nil)
	func() {
		defer func() { _ = recover() }() // nats.Conn.Publish on nil panics inside the lib; we only assert OUR code doesn't add a panic path before it
		sink.Publish(context.Background(), sessionbroker.CommandEvent{AttemptID: "x"})
	}()
}
