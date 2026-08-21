package sessionbroker

import (
	"context"
	"sync"
	"testing"
)

type fakeSink struct {
	mu     sync.Mutex
	events []CommandEvent
}

func (f *fakeSink) Publish(ctx context.Context, e CommandEvent) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.events = append(f.events, e)
}

func envelopeLine(epochMs int64, cmd string, exitCode int) string {
	return telemetryEnvelopeMarker +
		itoa(epochMs) + fieldSep + cmd + fieldSep + itoa(int64(exitCode)) + "\n"
}

func itoa(n int64) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}

func TestTelemetryTap_SingleChunkLine(t *testing.T) {
	sink := &fakeSink{}
	tap := newTelemetryTap("attempt-1", sink, nil)

	line := envelopeLine(1000, "ls -la", 0)
	out := tap.observe([]byte(line))

	if len(out) != 0 {
		t.Fatalf("expected telemetry line to be fully stripped, got %q", out)
	}
	if len(sink.events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(sink.events))
	}
	if sink.events[0].Cmd != "ls -la" {
		t.Fatalf("expected cmd 'ls -la', got %q", sink.events[0].Cmd)
	}
	if sink.events[0].ExitCode != 0 {
		t.Fatalf("expected exit code 0, got %d", sink.events[0].ExitCode)
	}
}

// This is the exact bug a real WS-client test against a live pod caught:
// the marker (or the rest of the envelope) can arrive split across two
// separate observe() calls, since a PTY exec stream delivers arbitrary
// byte chunks, not line-aligned ones.
func TestTelemetryTap_MarkerSplitAcrossChunks(t *testing.T) {
	sink := &fakeSink{}
	tap := newTelemetryTap("attempt-1", sink, nil)

	full := envelopeLine(2000, "echo hi", 0)
	mid := len(full) / 2
	chunk1, chunk2 := full[:mid], full[mid:]

	out1 := tap.observe([]byte(chunk1))
	if len(out1) != 0 {
		t.Fatalf("expected no output while marker is still incomplete, got %q", out1)
	}

	out2 := tap.observe([]byte(chunk2))
	if len(out2) != 0 {
		t.Fatalf("expected telemetry line to be fully stripped once complete, got %q", out2)
	}

	if len(sink.events) != 1 {
		t.Fatalf("expected 1 event after the split line completes, got %d", len(sink.events))
	}
	if sink.events[0].Cmd != "echo hi" {
		t.Fatalf("expected cmd 'echo hi', got %q", sink.events[0].Cmd)
	}
}

func TestTelemetryTap_LearnerOutputPassesThroughUnmodified(t *testing.T) {
	sink := &fakeSink{}
	tap := newTelemetryTap("attempt-1", sink, nil)

	out := tap.observe([]byte("hello world\n"))
	if string(out) != "hello world\n" {
		t.Fatalf("expected learner output unchanged, got %q", out)
	}
	if len(sink.events) != 0 {
		t.Fatalf("expected no telemetry events for plain output, got %d", len(sink.events))
	}
}

func TestTelemetryTap_MixedLearnerOutputAndTelemetryInOneChunk(t *testing.T) {
	sink := &fakeSink{}
	tap := newTelemetryTap("attempt-1", sink, nil)

	chunk := "hello-from-real-shell\r\n" + envelopeLine(3000, "echo hello-from-real-shell", 0)
	out := tap.observe([]byte(chunk))

	if string(out) != "hello-from-real-shell\r\n" {
		t.Fatalf("expected learner output preserved with telemetry stripped, got %q", out)
	}
	if len(sink.events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(sink.events))
	}
	if sink.events[0].Cmd != "echo hello-from-real-shell" {
		t.Fatalf("unexpected cmd: %q", sink.events[0].Cmd)
	}
}

func TestTelemetryTap_IncompleteTrailingLineIsHeldNotLost(t *testing.T) {
	sink := &fakeSink{}
	tap := newTelemetryTap("attempt-1", sink, nil)

	out1 := tap.observe([]byte("partial output no newline yet"))
	if len(out1) != 0 {
		t.Fatalf("expected nothing emitted for a line with no trailing newline yet, got %q", out1)
	}

	out2 := tap.observe([]byte(" and now it ends\n"))
	if string(out2) != "partial output no newline yet and now it ends\n" {
		t.Fatalf("expected the held-back partial line to be completed and emitted, got %q", out2)
	}
}

// A live end-to-end test against a real pod caught this exact bug: PTY
// output translates \n to \r\n, leaving a trailing \r on the exit-code
// field that strconv.Atoi rejected, silently turning every real exit
// code into -1.
func TestTelemetryTap_TrailingCarriageReturnFromPTYIsStripped(t *testing.T) {
	sink := &fakeSink{}
	tap := newTelemetryTap("attempt-1", sink, nil)

	// \r\n, not \n -- the actual line ending a PTY in canonical mode produces.
	line := telemetryEnvelopeMarker + "4000" + fieldSep + "ls -la" + fieldSep + "0\r\n"
	tap.observe([]byte(line))

	if len(sink.events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(sink.events))
	}
	if sink.events[0].ExitCode != 0 {
		t.Fatalf("expected exit code 0, got %d (the \\r-stripping regression)", sink.events[0].ExitCode)
	}
}

// A live end-to-end test against a real pod caught this exact bug: the
// terminal prepends its own control sequences (bracketed-paste-mode
// \x1b[?2004l\r) ahead of the hook's printf output on the same PTY
// line-flush, so a strict HasPrefix check on the marker missed it
// entirely and the raw telemetry text leaked into the learner's visible
// terminal.
func TestTelemetryTap_MarkerNotAtLineStartIsStillStripped(t *testing.T) {
	sink := &fakeSink{}
	tap := newTelemetryTap("attempt-1", sink, nil)

	prefix := "\x1b[?2004l\r"
	line := prefix + telemetryEnvelopeMarker + "5000" + fieldSep + "false" + fieldSep + "1\r\n"
	out := tap.observe([]byte(line))

	if string(out) != prefix {
		t.Fatalf("expected only the terminal control prefix to remain visible, got %q", out)
	}
	if len(sink.events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(sink.events))
	}
	if sink.events[0].Cmd != "false" || sink.events[0].ExitCode != 1 {
		t.Fatalf("unexpected event: %+v", sink.events[0])
	}
}

func TestTelemetryTap_MalformedEnvelopeIsDroppedNotCrashed(t *testing.T) {
	sink := &fakeSink{}
	tap := newTelemetryTap("attempt-1", sink, nil)

	// Marker present but payload doesn't have the expected 3 \x1f-delimited fields.
	out := tap.observe([]byte(telemetryEnvelopeMarker + "garbage\n"))
	if len(out) != 0 {
		t.Fatalf("expected malformed telemetry line to still be stripped from display, got %q", out)
	}
	if len(sink.events) != 0 {
		t.Fatalf("expected no event published for a malformed envelope, got %d", len(sink.events))
	}
}
