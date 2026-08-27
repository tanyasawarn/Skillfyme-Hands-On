// Package sessionbroker implements doc §5.4's Session Broker: "maps
// session -> env endpoint, PTY multiplexing, TELEMETRY TAP <- command
// capture happens HERE, record asciicast to S3." M1.5.
//
// Doc §4.2: "Command capture must happen server-side. Never trust the
// browser to report commands. Capture at the terminal multiplexer (the
// PTY proxy sits between the WebSocket and the container exec)." This
// package is that proxy: it execs a shell into the workspace pod via the
// K8s exec API, wires the resulting stdin/stdout/stderr streams to a
// WebSocket connection, and taps the byte stream in between to detect
// command boundaries and emit COMMAND_EXECUTED events.
package sessionbroker

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"time"

	"github.com/gorilla/websocket"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/remotecommand"

	"github.com/tanyasawarn/skillfyme-hands-on/orchestrator/internal/k8s"
)

// CommandEvent is what the broker emits per detected command -- doc §4.2
// COMMAND_EXECUTED payload: {cmd, cwd, exit_code, duration_ms,
// stdout_hash}. Captured via the PROMPT_COMMAND shell hook (see
// telemetry_tap.go), which gives real Cmd + ExitCode text directly from
// the shell, unlike parsing raw PTY output. Cwd is not captured in this
// Phase 1 slice (the hook script doesn't currently emit $PWD) --
// explicitly left empty rather than fabricated; adding it is a one-line
// change to bashHookScript plus one more \x1f-delimited field.
type CommandEvent struct {
	AttemptID  string
	Cmd        string
	ExitCode   int
	DurationMs int64
	CmdHash    string
	OccurredAt time.Time
}

// EventSink is how the broker publishes CommandEvents without importing
// NATS or Practice Core's event store directly -- kept as a narrow
// interface so this package has no dependency on the transport doc
// §13.2 picks (NATS JetStream) or on Dev B's schema.
type EventSink interface {
	Publish(ctx context.Context, event CommandEvent)
}

// RecordingSink receives raw terminal bytes for asciicast-style session
// recording (doc §5.4: "record asciicast to S3"). Phase 1 scope: the tap
// point exists and is wired; actual S3 upload is a thin adapter left for
// deployment-time configuration (doc's retention table, §4.3, sets
// terminal recordings at 30-90 days -- a lifecycle policy on the bucket,
// not application code).
type RecordingSink interface {
	Write(ctx context.Context, attemptID string, data []byte)
}

// ActivityRecorder mirrors internal/idledetect.Detector's activity-signal
// input -- doc §5.6: "No stdin... " is the first of the idle clock's two
// signals. Optional (nil-checked) so Broker doesn't require an idle
// detector to function.
type ActivityRecorder interface {
	RecordActivity(envID string)
}

type Broker struct {
	restConfig *rest.Config
	clientset  *kubernetes.Clientset
	events     EventSink
	recording  RecordingSink
	activity   ActivityRecorder
	sessions   *registry
}

func New(restConfig *rest.Config, clientset *kubernetes.Clientset, events EventSink, recording RecordingSink, activity ActivityRecorder) *Broker {
	return &Broker{restConfig: restConfig, clientset: clientset, events: events, recording: recording, activity: activity, sessions: newRegistry()}
}

// hookFilePath is where installHook writes bashHookScript inside the
// workspace container, and what --rcfile sources on shell start.
const hookFilePath = "/tmp/.pe_telemetry_hook.sh"

// installHook writes bashHookScript to hookFilePath via a short-lived,
// non-TTY, non-interactive exec -- run once per Attach, before the
// interactive PTY session opens. Doc §4.2: this is the "out-of-band"
// half of getting the hook into the container without it ever appearing
// on the interactive stdin/stdout the learner sees.
func (b *Broker) installHook(ctx context.Context, ns string) error {
	req := b.clientset.CoreV1().RESTClient().Post().
		Resource("pods").
		Namespace(ns).
		Name(k8s.WorkspacePodName).
		SubResource("exec").
		VersionedParams(&corev1.PodExecOptions{
			Container: k8s.WorkspaceContainerName,
			Command:   []string{"/bin/sh", "-c", fmt.Sprintf("cat > %s", hookFilePath)},
			Stdin:     true,
			Stdout:    true,
			Stderr:    true,
			TTY:       false,
		}, scheme.ParameterCodec)

	executor, err := remotecommand.NewSPDYExecutor(b.restConfig, "POST", req.URL())
	if err != nil {
		return fmt.Errorf("building install-hook executor: %w", err)
	}

	var stdout, stderr bytes.Buffer
	err = executor.StreamWithContext(ctx, remotecommand.StreamOptions{
		Stdin:  bytes.NewReader([]byte(bashHookScript)),
		Stdout: &stdout,
		Stderr: &stderr,
	})
	if err != nil {
		return fmt.Errorf("writing hook file (stderr=%q): %w", stderr.String(), err)
	}
	return nil
}

// namespaceForEnv mirrors internal/k8s.namespaceName -- duplicated
// rather than imported to keep sessionbroker decoupled from the
// provisioning package (the only coupling is the naming convention,
// documented in both places).
func namespaceForEnv(envID string) string {
	return fmt.Sprintf("env-%s", envID)
}

// Attach binds ws to envID's long-lived PTY session, creating that
// session (via startSession) if this is the first attach or if a prior
// session already terminated. Doc §5.4: "WebSocket, not SSH from the
// browser... Terminate at the platform, proxy inward over the control
// plane's own channel (K8s exec API for T1/T2)" plus "Broker keeps the
// PTY alive for 120s after socket loss and replays a scrollback buffer
// on reconnect" -- the exec stream itself (session_registry.go's
// startSession) runs independently of any single WS connection; this
// method only manages *this* WS's attachment to it. Blocks until ws
// disconnects or ctx is cancelled, then detaches (not terminates) the
// session, arming the reconnect-grace timer.
func (b *Broker) Attach(ctx context.Context, attemptID, envID string, ws *websocket.Conn) error {
	session, ok := b.sessions.get(envID)
	if !ok {
		var err error
		session, err = b.startSession(ctx, attemptID, envID)
		if err != nil {
			return err
		}
		b.sessions.set(envID, session)
	}

	if err := session.attachWS(ws); err != nil {
		return err
	}

	wsReader := &wsStdinReader{ws: ws, envID: envID, activity: b.activity}
	stdinDone := make(chan struct{})
	go func() {
		defer close(stdinDone)
		_, _ = io.Copy(session.stdin(), wsReader)
	}()

	select {
	case <-ctx.Done():
	case <-stdinDone: // wsReader.Read returned io.EOF: this WS closed or errored
	case <-session.done: // the PTY itself exited (pod gone, unrecoverable stream error) -- nothing left to attach to
		b.sessions.delete(envID)
		return fmt.Errorf("session for env=%s ended", envID)
	}

	session.detachWS(ws, func() {
		session.terminate()
		b.sessions.delete(envID)
	})
	return nil
}

// wsStdinReader adapts a gorilla WebSocket connection to io.Reader for
// the exec stream's Stdin, and reports each read as learner activity
// (doc §5.6 idle clock signal #1: "No stdin").
type wsStdinReader struct {
	ws       *websocket.Conn
	buf      bytes.Buffer
	envID    string
	activity ActivityRecorder
}

func (r *wsStdinReader) Read(p []byte) (int, error) {
	for r.buf.Len() == 0 {
		_, data, err := r.ws.ReadMessage()
		if err != nil {
			return 0, io.EOF
		}
		r.buf.Write(data)
		if r.activity != nil {
			r.activity.RecordActivity(r.envID)
		}
	}
	return r.buf.Read(p)
}

// sessionTappedWriter (session_registry.go) is this package's
// TELEMETRY-TAP dual-write today -- it fans filtered PTY output out to
// whichever WS is currently attached to a ptySession (or just scrollback
// if none is), the reconnect-aware successor to a plain per-WS
// tappedWriter.
