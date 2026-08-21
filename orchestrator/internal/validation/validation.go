// Package validation implements real execution for the 6 validator
// types every currently-published lab actually uses (SHELL_ASSERT 69x,
// K8S_ASSERT 34x, FILE_EXISTS 29x, FILE_CONTENT 21x, FILE_PARSE 8x,
// SHELL_JSON 6x, per content/activities/*.yaml). Doc §6.2's real design
// has Dev B (practice-core) executing validators itself against minted
// read-only credentials (MintValidatorCredentials) -- that path isn't
// implemented, same gap InjectFault/regression already worked around.
// This package is the same pragmatic shortcut: SHELL_ASSERT/SHELL_JSON/
// FILE_* exec non-interactively inside the workspace pod (no cluster
// credentials needed there, same pattern as sessionbroker's telemetry
// hook install); K8S_ASSERT executes via the orchestrator's own K8s API
// access, since the workspace pod has no kubectl or cluster credentials
// (confirmed live during InjectFault/CaptureBaseline work).
//
// The other 12 validator types in ValidatorSpec's type union
// (HTTP_PROBE, HTTP_SLO, CLOUD_ASSERT, IAC_STATE, DB_QUERY, TEST_SUITE,
// STATIC_ANALYSIS, PERF_BENCH, CHAOS_PROBE, TELEMETRY_ASSERT,
// NO_REGRESSION, AI_RUBRIC) are NOT implemented here -- NO_REGRESSION has
// its own real mechanism in internal/regression (CaptureBaseline/
// CheckRegression, a separate RPC pair, not this one) but practice-core
// doesn't call it yet; the rest have no authored content to execute
// against and are honestly reported as unsupported, matching
// faultinjection.ErrNoHandler's pattern.
package validation

import (
	"bytes"
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/tools/remotecommand"

	"github.com/tanyasawarn/skillfyme-hands-on/orchestrator/internal/k8s"
)

// Result mirrors contracts/orchestrator.proto's ExecValidatorResponse
// fields, so the gRPC handler is a thin pass-through -- same convention
// as faultinjection.Result and regression's return shapes.
type Result struct {
	Status     string // PASS | FAIL | ERROR
	Observed   any
	ErrorMsg   string
	DurationMs int32
}

// ErrUnsupportedType distinguishes "this validator type has no real
// executor yet" from an execution-time failure, so the gRPC handler can
// return Unimplemented rather than Internal -- same distinction
// faultinjection.ErrNoHandler makes for fault ids.
type ErrUnsupportedType struct{ Type string }

func (e ErrUnsupportedType) Error() string {
	return fmt.Sprintf("no real executor implemented for validator type %s", e.Type)
}

// Request is the decoded form of ExecValidatorRequest -- ExpectJSON is
// parsed into a generic map here so the six type-specific execute
// functions work with real Go values, not raw JSON strings.
type Request struct {
	EnvironmentID string
	ValidatorID   string
	ValidatorType string
	Run           string
	Expect        map[string]any
	TimeoutMs     int32
}

// Exec dispatches to the real execution logic for one of the 6 supported
// validator types. All shell/file executors share one exec helper
// (execInPod); K8S_ASSERT does not exec anything inside the pod at all.
func Exec(ctx context.Context, provisioner *k8s.Provisioner, req Request) (Result, error) {
	start := time.Now()
	timeout := time.Duration(req.TimeoutMs) * time.Millisecond
	if timeout <= 0 {
		timeout = 30 * time.Second // doc §6.2 default execution budget per validator
	}
	execCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	var result Result
	var err error

	switch req.ValidatorType {
	case "SHELL_ASSERT":
		result, err = execShellAssert(execCtx, provisioner, req)
	case "SHELL_JSON":
		result, err = execShellJSON(execCtx, provisioner, req)
	case "FILE_EXISTS":
		result, err = execFileExists(execCtx, provisioner, req)
	case "FILE_CONTENT":
		result, err = execFileContent(execCtx, provisioner, req)
	case "FILE_PARSE":
		result, err = execFileParse(execCtx, provisioner, req)
	case "K8S_ASSERT":
		result, err = execK8sAssert(execCtx, provisioner, req)
	default:
		return Result{}, ErrUnsupportedType{Type: req.ValidatorType}
	}

	result.DurationMs = int32(time.Since(start).Milliseconds())
	return result, err
}

// ShellResult is the raw outcome of an ExecShell call -- unlike Result,
// it carries stdout/stderr/exit code directly rather than a PASS/FAIL/ERROR
// verdict, since ExecShell runs arbitrary commands (e.g. a content-CI
// golden-path harness applying a reference_solution script) rather than
// evaluating an assertion.
type ShellResult struct {
	ExitCode   int
	Stdout     string
	Stderr     string
	DurationMs int32
}

// ExecShell runs an arbitrary command inside the workspace pod and
// returns its raw output. Same execInPod mechanism the SHELL_* validator
// types use, exported for the ExecShell RPC.
func ExecShell(ctx context.Context, provisioner *k8s.Provisioner, envID, cmd string, timeoutMs int32) (ShellResult, error) {
	start := time.Now()
	timeout := time.Duration(timeoutMs) * time.Millisecond
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	execCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	out, err := execInPod(execCtx, provisioner, envID, cmd)
	result := ShellResult{
		ExitCode:   out.ExitCode,
		Stdout:     out.Stdout,
		Stderr:     out.Stderr,
		DurationMs: int32(time.Since(start).Milliseconds()),
	}
	return result, err
}

// --- shared exec-in-pod helper -------------------------------------------

// execResult is the raw outcome of running a command inside the
// workspace pod -- shell/file validators build their Result from this.
type execResult struct {
	Stdout   string
	Stderr   string
	ExitCode int
}

// execInPod runs `cmd` non-interactively inside the workspace pod's
// shell container, the same SPDY-exec mechanism sessionbroker uses for
// hook installation (broker.go's installHook) -- not the interactive PTY
// session, so nothing here is ever visible to the learner's terminal.
func execInPod(ctx context.Context, provisioner *k8s.Provisioner, envID, cmd string) (execResult, error) {
	clientset := provisioner.Clientset()
	restConfig := provisioner.RestConfig()
	ns := k8s.NamespaceForEnv(envID)

	// Wrap in a subshell that always echoes the exit code on its own
	// line after stdout/stderr, since remotecommand's StreamWithContext
	// doesn't otherwise surface the remote process's exit code directly
	// to the caller in a way this package can read alongside stdout.
	// Joined with a newline, not ";" -- cmd can be a multi-line script
	// (ExecShell's solution_apply use case) that already ends in "\n",
	// and "\n; echo ..." is a bash syntax error (an empty statement
	// before ";"), confirmed live when the first multi-line ExecShell
	// caller hit exactly this.
	wrapped := fmt.Sprintf("%s\necho \"__EXIT_CODE__:$?\"", cmd)

	req := clientset.CoreV1().RESTClient().Post().
		Resource("pods").
		Namespace(ns).
		Name("workspace").
		SubResource("exec").
		VersionedParams(&corev1.PodExecOptions{
			Container: "shell",
			Command:   []string{"/bin/bash", "-c", wrapped},
			Stdin:     false,
			Stdout:    true,
			Stderr:    true,
			TTY:       false,
		}, scheme.ParameterCodec)

	executor, err := remotecommand.NewSPDYExecutor(restConfig, "POST", req.URL())
	if err != nil {
		return execResult{}, fmt.Errorf("building exec executor: %w", err)
	}

	var stdout, stderr bytes.Buffer
	streamErr := executor.StreamWithContext(ctx, remotecommand.StreamOptions{
		Stdout: &stdout,
		Stderr: &stderr,
	})
	// A nonzero exit code from the wrapped command surfaces as a
	// StreamWithContext error in some client-go versions; the
	// __EXIT_CODE__ marker is the authoritative source, so a stream
	// error here is only fatal if the marker never showed up either.
	out := stdout.String()
	code, foundMarker := parseExitCodeMarker(out)
	if !foundMarker {
		if streamErr != nil {
			return execResult{}, fmt.Errorf("exec stream error (no exit marker found): %w", streamErr)
		}
		return execResult{}, fmt.Errorf("exit code marker not found in output")
	}

	return execResult{Stdout: stripExitCodeMarker(out), Stderr: stderr.String(), ExitCode: code}, nil
}

func parseExitCodeMarker(out string) (int, bool) {
	idx := strings.LastIndex(out, "__EXIT_CODE__:")
	if idx == -1 {
		return 0, false
	}
	rest := strings.TrimSpace(out[idx+len("__EXIT_CODE__:"):])
	code, err := strconv.Atoi(rest)
	if err != nil {
		return 0, false
	}
	return code, true
}

func stripExitCodeMarker(out string) string {
	idx := strings.LastIndex(out, "__EXIT_CODE__:")
	if idx == -1 {
		return out
	}
	return strings.TrimRight(out[:idx], "\n")
}
