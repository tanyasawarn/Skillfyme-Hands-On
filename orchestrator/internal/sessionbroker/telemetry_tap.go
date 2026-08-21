package sessionbroker

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"strconv"
	"strings"
	"time"
)

// telemetryEnvelopeMarker delimits telemetry lines the shell hook prints
// to stdout, so the tap can pull them out of the PTY byte stream without
// confusing them with the learner's actual terminal output. A marker
// this specific is vanishingly unlikely to collide with real program
// output; if it ever does, the affected line is simply not parsed as
// telemetry (fails the prefix check below), it still reaches the
// learner's terminal untouched.
//
// Deliberately printable ASCII, not control-character-delimited: an
// earlier version used \x00 (NUL) as a delimiter (the most
// collision-proof choice on paper), but a real end-to-end test against a
// live pod showed the NUL bytes never arriving at this tap at all --
// confirmed via kubectl exec that bash's printf emits them correctly, so
// the loss happens somewhere in the PTY/TTY layer between the container
// and here (control-byte stripping/translation is a long-standing Unix
// tty line-discipline behavior, not unique to NUL). A fully printable
// marker sidesteps the whole class of "does this byte survive a TTY"
// question. Collision risk against real program output is accepted as
// negligible for this exact 14-character string.
const telemetryEnvelopeMarker = "##PE_TELEMETRY##"

// bashHookScript is piped into the shell's stdin immediately after the
// exec stream opens (see Broker.Attach). It's a PROMPT_COMMAND-style
// hook -- doc §4.2's own recommended capture mechanism ("shell audit
// hook (PROMPT_COMMAND...)") -- rather than trying to reconstruct
// commands from raw PTY output after the fact, which cannot recover the
// command text or exit code at all. This is stronger than the
// output-hashing approach: it captures the actual command, its exit
// code, and a millisecond timestamp, matching doc §4.2's
// COMMAND_EXECUTED payload {cmd, cwd, exit_code, duration_ms} directly.
//
// Runs only for interactive `sh`/`bash`; doc's own two-source design
// (§4.2: "Reconcile both; discrepancies are an integrity signal") exists
// precisely because a hook like this can be defeated by a learner who
// unsets PROMPT_COMMAND or execs a different shell -- that's an
// accepted, documented limitation of shell-hook capture alone, not a
// bug. Phase 2's in-container auditd/eBPF path (doc §4.2) is the
// reconciliation source; not implemented in this Phase 1 slice.
const fieldSep = "@@PE_SEP@@"

const bashHookScript = `
__pe_telemetry() {
  local ec=$?
  printf '` + telemetryEnvelopeMarker + `%s` + fieldSep + `%s` + fieldSep + `%d\n' "$(date +%s%3N)" "$(history 1 | sed -e 's/^[ ]*[0-9]*[ ]*//')" "$ec" >&2
}
export PROMPT_COMMAND=__pe_telemetry
`

// telemetryTap parses bashHookScript's envelope lines out of the exec
// stream's stderr and emits a CommandEvent per real command, while
// leaving all non-telemetry bytes flowing through to the learner's
// terminal untouched.
type telemetryTap struct {
	attemptID string
	events    EventSink
	recording RecordingSink

	// pending holds bytes seen since the last complete '\n'-terminated
	// line -- a PTY exec stream delivers arbitrary byte chunks, so a
	// telemetry envelope line (or the marker itself) can straddle two
	// observe() calls. A real WS-client test against a live pod caught
	// exactly this: the marker rendered as literal text in the
	// learner-visible output because the original per-call bufio.Scanner
	// couldn't see a marker split across chunks. Buffering across calls
	// fixes it.
	pending     strings.Builder
	lastCmdTime time.Time
}

func newTelemetryTap(attemptID string, events EventSink, recording RecordingSink) *telemetryTap {
	return &telemetryTap{attemptID: attemptID, events: events, recording: recording, lastCmdTime: time.Now()}
}

// observe processes one chunk of combined stdout+stderr from the exec
// stream. Non-telemetry bytes are returned for display; telemetry
// envelope lines are parsed, published, and stripped so the learner
// never sees the raw marker. Bytes since the last '\n' are held in
// t.pending across calls, since a chunk boundary can land mid-line
// (including mid-marker) -- see the doc comment on the pending field.
func (t *telemetryTap) observe(p []byte) []byte {
	if t.recording != nil {
		t.recording.Write(context.Background(), t.attemptID, p)
	}

	t.pending.Write(p)
	buffered := t.pending.String()

	lastNewline := strings.LastIndexByte(buffered, '\n')
	if lastNewline == -1 {
		// No complete line yet at all -- nothing safe to emit or parse
		// until more bytes arrive. Keep everything pending. This can
		// briefly delay display of a long line with no newline, but a
		// PTY session's normal output always has frequent newlines (or
		// terminal control sequences), so in practice this is not
		// perceptible.
		return nil
	}

	complete := buffered[:lastNewline+1]
	rest := buffered[lastNewline+1:]
	t.pending.Reset()
	t.pending.WriteString(rest)

	var out strings.Builder
	for _, line := range strings.SplitAfter(complete, "\n") {
		if line == "" {
			continue
		}
		trimmed := strings.TrimSuffix(line, "\n")
		// Search for the marker anywhere in the line, not just as a
		// strict prefix: a real end-to-end test showed the terminal
		// prepending its own control sequences (e.g. bracketed-paste-mode
		// \x1b[?2004l\r) ahead of the hook's printf output on the same
		// PTY line-flush, so "starts with the marker" was too strict and
		// let the raw telemetry line leak into the learner's visible
		// terminal. Anything before the marker on the line (typically
		// just terminal control bytes) is preserved and still displayed;
		// the marker onward is parsed and stripped.
		if idx := strings.Index(trimmed, telemetryEnvelopeMarker); idx != -1 {
			out.WriteString(trimmed[:idx])
			t.parseAndPublish(trimmed[idx:])
			continue // the telemetry portion itself never reaches the learner's terminal
		}
		out.WriteString(line)
	}
	return []byte(out.String())
}

func (t *telemetryTap) parseAndPublish(line string) {
	// PTY output translates \n to \r\n (canonical-mode terminal driver
	// behavior) -- the caller already stripped the trailing \n via
	// TrimSuffix, but a \r remains on the last field. A real end-to-end
	// test caught this exactly: exit_code came through as -1 because
	// strconv.Atoi("0\r") fails to parse. TrimRight covers \r wherever it
	// lands, not just at the very end, in case of other CR noise.
	payload := strings.TrimRight(strings.TrimPrefix(line, telemetryEnvelopeMarker), "\r")
	parts := strings.Split(payload, fieldSep)
	if len(parts) != 3 {
		return // malformed envelope (e.g. truncated mid-chunk) -- drop rather than emit a garbage event
	}

	epochMs, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil {
		return
	}
	cmd := parts[1]
	if cmd == "" {
		return // empty history line (e.g. blank Enter press) -- not a real command
	}
	exitCode, err := strconv.Atoi(parts[2])
	if err != nil {
		exitCode = -1 // unparseable exit code -- surfaced as -1 rather than silently defaulting to 0 ("success")
	}

	occurredAt := time.UnixMilli(epochMs)
	duration := occurredAt.Sub(t.lastCmdTime).Milliseconds()
	t.lastCmdTime = occurredAt

	hash := sha256.Sum256([]byte(cmd))
	t.events.Publish(context.Background(), CommandEvent{
		AttemptID:  t.attemptID,
		Cmd:        cmd,
		ExitCode:   exitCode,
		DurationMs: duration,
		CmdHash:    hex.EncodeToString(hash[:]),
		OccurredAt: occurredAt,
	})
}
