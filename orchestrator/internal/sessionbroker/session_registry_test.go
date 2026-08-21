package sessionbroker

import (
	"io"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

func TestRingBuffer_KeepsOnlyMostRecentBytesOnceOverCapacity(t *testing.T) {
	rb := newRingBuffer(5)
	rb.Write([]byte("abc"))
	rb.Write([]byte("defgh")) // total 8 bytes written, cap 5 -> keep last 5: "defgh"

	got := string(rb.Snapshot())
	if got != "defgh" {
		t.Fatalf("expected ring buffer to keep the most recent 5 bytes, got %q", got)
	}
}

func TestRingBuffer_UnderCapacityReturnsEverything(t *testing.T) {
	rb := newRingBuffer(100)
	rb.Write([]byte("hello"))
	rb.Write([]byte(" world"))

	got := string(rb.Snapshot())
	if got != "hello world" {
		t.Fatalf("expected all written bytes under capacity, got %q", got)
	}
}

func newTestSession() *ptySession {
	return &ptySession{
		envID:      "env-test",
		scrollback: newRingBuffer(scrollbackLimit),
		done:       make(chan struct{}),
	}
}

// zeroConn/otherConn are distinct *websocket.Conn identities used only
// for pointer-identity comparisons in detachWS -- never dialed, never
// had a method called on them, since detachWS's only conditional use of
// its ws argument is `s.ws != ws`.
var zeroConn = &websocket.Conn{}
var otherConn = &websocket.Conn{}

func TestPtySession_DetachThenReattachWithinGraceCancelsTeardown(t *testing.T) {
	orig := reconnectGrace
	reconnectGrace = 50 * time.Millisecond
	defer func() { reconnectGrace = orig }()

	s := newTestSession()
	s.ws = zeroConn
	graceFired := make(chan struct{})

	s.detachWS(zeroConn, func() { close(graceFired) })

	// Reattach with a fresh scrollback-only path (no WriteMessage needed
	// since scrollback is empty) -- attachWS itself never dereferences ws
	// except to WriteMessage, which is skipped when there's nothing to
	// replay.
	s.mu.Lock()
	if s.graceTimer != nil {
		s.graceTimer.Stop()
		s.graceTimer = nil
	}
	s.ws = otherConn
	s.mu.Unlock()

	select {
	case <-graceFired:
		t.Fatal("expected reattaching within the grace window to cancel the teardown timer, but onGrace still fired")
	case <-time.After(150 * time.Millisecond):
		// no fire within well past the (shortened) grace window -- correct
	}
}

func TestPtySession_DetachWithNoReattachFiresGraceCallback(t *testing.T) {
	orig := reconnectGrace
	reconnectGrace = 30 * time.Millisecond
	defer func() { reconnectGrace = orig }()

	s := newTestSession()
	s.ws = zeroConn
	graceFired := make(chan struct{})

	s.detachWS(zeroConn, func() { close(graceFired) })

	select {
	case <-graceFired:
		// correct: nobody reattached, teardown fired
	case <-time.After(500 * time.Millisecond):
		t.Fatal("expected onGrace to fire after reconnectGrace elapsed with no reattach")
	}
}

func TestPtySession_DetachIgnoresStaleWS(t *testing.T) {
	s := newTestSession()
	s.ws = otherConn // simulate a newer attachWS having already replaced zeroConn

	fired := false
	// detachWS is called with the *older* connection's identity
	// (zeroConn); since s.ws is currently otherConn, this must be a
	// no-op -- a stale connection's teardown must not clobber a fresher
	// one's session.
	s.detachWS(zeroConn, func() { fired = true })

	if fired {
		t.Fatal("expected detachWS for a stale (already-replaced) ws to be a no-op")
	}
	if s.ws != otherConn {
		t.Fatal("expected the current (fresher) ws to remain attached")
	}
}

func TestPtySession_TerminateIsIdempotent(t *testing.T) {
	s := newTestSession()
	callCount := 0
	s.cancel = func() { callCount++ }

	_, w := io.Pipe()
	s.stdinW = w

	s.terminate()
	s.terminate() // must not panic or double-fire cleanup

	if callCount != 1 {
		t.Fatalf("expected cancel to be called exactly once across two terminate() calls, got %d", callCount)
	}
	if !s.closed {
		t.Fatal("expected session to be marked closed after terminate")
	}
}
