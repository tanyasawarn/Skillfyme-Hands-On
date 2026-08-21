package sessionbroker

import (
	"context"
	"fmt"
	"io"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/tools/remotecommand"
)

// reconnectGrace is how long a PTY session survives after its WebSocket
// disconnects before being torn down -- doc §5.4: "Broker keeps the PTY
// alive for 120s after socket loss and replays a scrollback buffer on
// reconnect." A var, not a const, only so tests can shrink it rather
// than waiting out a real 120s timer -- production always runs with the
// doc's literal value.
var reconnectGrace = 120 * time.Second

// scrollbackLimit bounds the ring buffer of recent PTY output kept for
// replay on reconnect. Not time-bounded (a session could sit idle for
// most of reconnectGrace with no new output) -- a fixed byte cap is
// simpler and still comfortably covers a typical terminal's visible
// history; 64KB is enough for tens of thousands of lines of shell output.
const scrollbackLimit = 64 * 1024

// ptySession is one long-lived K8s exec stream, decoupled from any
// single WebSocket connection -- the structural change the doc's
// reconnection-gap comment (formerly at the end of broker.go) called
// for: "requires decoupling the K8s exec stream's lifetime from any
// single WebSocket connection (a session registry keyed by session
// token, not by ws *websocket.Conn)." Keyed by envID here rather than
// session token specifically, since Phase 1 mints at most one live
// terminal session per environment at a time (doc §5.4's terminal route
// is per-environment, not per-token) -- a session token identifies which
// caller is allowed to attach, not a distinct PTY.
type ptySession struct {
	envID     string
	attemptID string

	mu         sync.Mutex
	ws         *websocket.Conn // nil when no client is currently attached
	scrollback *ringBuffer
	closed     bool
	graceTimer *time.Timer

	stdinW *io.PipeWriter // exec stream's Stdin reads from the paired PipeReader; wsStdinReader writes here
	cancel context.CancelFunc

	done chan struct{} // closed once the exec stream itself has exited (pod gone, unrecoverable error)
}

// registry tracks the one live ptySession per envID. A package-level
// singleton (not a Broker field) would work equally well given there's
// only ever one Broker in this process, but threading it through Broker
// keeps sessionbroker's exported surface explicit about what state it
// owns, matching costmeter/idledetect's own "owned by the constructing
// struct" convention elsewhere in this codebase.
type registry struct {
	mu       sync.Mutex
	sessions map[string]*ptySession
}

func newRegistry() *registry {
	return &registry{sessions: make(map[string]*ptySession)}
}

func (r *registry) get(envID string) (*ptySession, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	s, ok := r.sessions[envID]
	return s, ok
}

func (r *registry) set(envID string, s *ptySession) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.sessions[envID] = s
}

func (r *registry) delete(envID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.sessions, envID)
}

// ringBuffer is a fixed-capacity byte buffer that keeps only the most
// recently written bytes once full -- simplest possible scrollback
// store, no allocation churn per write beyond the occasional slice
// truncation.
type ringBuffer struct {
	mu  sync.Mutex
	buf []byte
	cap int
}

func newRingBuffer(capacity int) *ringBuffer {
	return &ringBuffer{cap: capacity}
}

func (rb *ringBuffer) Write(p []byte) {
	rb.mu.Lock()
	defer rb.mu.Unlock()
	rb.buf = append(rb.buf, p...)
	if excess := len(rb.buf) - rb.cap; excess > 0 {
		rb.buf = rb.buf[excess:]
	}
}

func (rb *ringBuffer) Snapshot() []byte {
	rb.mu.Lock()
	defer rb.mu.Unlock()
	out := make([]byte, len(rb.buf))
	copy(out, rb.buf)
	return out
}

// attachWS binds ws as this session's active output target, replaying
// scrollback first so a reconnecting client sees what it missed. Any
// previously-attached ws (e.g. a stale connection that hasn't yet
// noticed it's dead) is displaced, not closed -- closing here could race
// with that connection's own read loop tearing itself down; leaving it
// alone and simply no longer writing to it is enough, its read loop will
// error out and exit on its own.
func (s *ptySession) attachWS(ws *websocket.Conn) error {
	s.mu.Lock()
	if s.graceTimer != nil {
		s.graceTimer.Stop()
		s.graceTimer = nil
	}
	scrollback := s.scrollback.Snapshot()
	s.ws = ws
	s.mu.Unlock()

	if len(scrollback) > 0 {
		if err := ws.WriteMessage(websocket.BinaryMessage, scrollback); err != nil {
			return fmt.Errorf("replaying scrollback: %w", err)
		}
	}
	return nil
}

// detachWS clears ws as the active output target if it's still the
// current one (a newer attachWS call may have already replaced it, in
// which case this is a no-op -- don't let a stale connection's teardown
// clobber a fresher one) and arms the reconnect-grace timer, which tears
// the whole session down via onGrace if nothing reattaches in time.
func (s *ptySession) detachWS(ws *websocket.Conn, onGrace func()) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.ws != ws {
		return
	}
	s.ws = nil
	if s.closed {
		return
	}
	s.graceTimer = time.AfterFunc(reconnectGrace, onGrace)
}

// write sends filtered PTY output to whichever WS is currently attached
// (silently dropped if none is, since the session survives disconnects)
// and always records it to scrollback regardless of attachment state --
// a learner who reconnects mid-grace-window must see everything that
// happened while they were gone, not just what happened before they
// first disconnected.
func (s *ptySession) write(p []byte) {
	s.scrollback.Write(p)

	s.mu.Lock()
	ws := s.ws
	s.mu.Unlock()

	if ws != nil {
		_ = ws.WriteMessage(websocket.BinaryMessage, p) // best-effort: a write failure here just means this WS is already dead, its own read loop will notice and detach
	}
}

// stdin returns the writer end of the pipe the exec stream reads Stdin
// from -- wsStdinReader for the currently-attached WS writes here so
// keystrokes reach the same long-lived PTY across a reconnect.
func (s *ptySession) stdin() io.Writer {
	return s.stdinW
}

// terminate tears down the exec stream itself (pod exec context
// cancelled) and marks the session closed so no future grace timer fires
// against it. Called either by the grace timer expiring with nobody
// reattached, or by the exec stream exiting on its own (pod deleted,
// unrecoverable stream error).
func (s *ptySession) terminate() {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	s.closed = true
	if s.graceTimer != nil {
		s.graceTimer.Stop()
		s.graceTimer = nil
	}
	s.mu.Unlock()

	s.cancel()
	_ = s.stdinW.Close()
}

// startSession execs the long-lived PTY inside the workspace pod and
// runs the stream in a background goroutine, returning immediately with
// the new ptySession once the exec has been issued (not once it exits --
// this mirrors Attach's old behavior of blocking the *caller* until
// stream end, just now the caller is this session's own goroutine
// instead of the original HTTP request).
func (b *Broker) startSession(parentCtx context.Context, attemptID, envID string) (*ptySession, error) {
	ns := namespaceForEnv(envID)

	if err := b.installHook(parentCtx, ns); err != nil {
		return nil, fmt.Errorf("installing telemetry hook: %w", err)
	}

	req := b.clientset.CoreV1().RESTClient().Post().
		Resource("pods").
		Namespace(ns).
		Name("workspace").
		SubResource("exec").
		VersionedParams(&corev1.PodExecOptions{
			Container: "shell",
			Command:   []string{"/bin/bash", "--rcfile", hookFilePath, "-i"},
			Stdin:     true,
			Stdout:    true,
			Stderr:    true,
			TTY:       true,
		}, scheme.ParameterCodec)

	executor, err := remotecommand.NewSPDYExecutor(b.restConfig, "POST", req.URL())
	if err != nil {
		return nil, fmt.Errorf("building SPDY executor: %w", err)
	}

	// Session outlives any single HTTP request/WS connection, so it gets
	// its own cancellable context rather than inheriting parentCtx
	// directly -- parentCtx (the WS handler's request context) is
	// cancelled the moment that particular HTTP request ends, which is
	// exactly the lifetime this change is decoupling the PTY from.
	sessionCtx, cancel := context.WithCancel(context.Background())

	stdinR, stdinW := io.Pipe()
	session := &ptySession{
		envID:      envID,
		attemptID:  attemptID,
		scrollback: newRingBuffer(scrollbackLimit),
		stdinW:     stdinW,
		cancel:     cancel,
		done:       make(chan struct{}),
	}

	tap := newTelemetryTap(attemptID, b.events, b.recording)
	sessionWriter := &sessionTappedWriter{session: session, tap: tap}

	go func() {
		defer close(session.done)
		streamErr := executor.StreamWithContext(sessionCtx, remotecommand.StreamOptions{
			Stdin:  stdinR,
			Stdout: sessionWriter,
			Stderr: sessionWriter,
			Tty:    true,
		})
		if streamErr != nil && sessionCtx.Err() == nil {
			// Unrecoverable stream error while a WS may still be attached
			// (e.g. pod was deleted out from under a live session) --
			// nothing to retry into, so tear the session down rather than
			// leaving a dead entry in the registry that every future
			// reconnect attempt would keep hitting.
			session.terminate()
		}
		b.sessions.delete(envID)
	}()

	return session, nil
}

// sessionTappedWriter mirrors tappedWriter but writes into a ptySession
// (which fans out to whichever WS is attached, or just scrollback if
// none is) instead of directly into one *websocket.Conn.
type sessionTappedWriter struct {
	session *ptySession
	tap     *telemetryTap
}

func (w *sessionTappedWriter) Write(p []byte) (int, error) {
	filtered := w.tap.observe(p)
	if len(filtered) > 0 {
		w.session.write(filtered)
	}
	return len(p), nil
}
