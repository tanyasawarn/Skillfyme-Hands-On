package validation

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"k8s.io/client-go/util/jsonpath"
	"sigs.k8s.io/yaml"

	"github.com/tanyasawarn/skillfyme-hands-on/orchestrator/internal/k8s"
)

// --- SHELL_ASSERT ----------------------------------------------------------
// expect: {exit_code: number, stdout_contains?: string}. Content examples:
// "docker image inspect node-app:v1" / expect.exit_code: 0.
func execShellAssert(ctx context.Context, provisioner *k8s.Provisioner, req Request) (Result, error) {
	out, err := execInPod(ctx, provisioner, req.EnvironmentID, req.Run)
	if err != nil {
		return Result{Status: StatusError}, err
	}

	wantCode, _ := req.Expect["exit_code"].(float64) // JSON numbers decode as float64
	pass := out.ExitCode == int(wantCode)

	if pass {
		if contains, ok := req.Expect["stdout_contains"].(string); ok {
			pass = strings.Contains(out.Stdout, contains)
		}
	}

	status := StatusFail
	if pass {
		status = StatusPass
	}
	return Result{Status: status, Observed: map[string]any{"exit_code": out.ExitCode, "stdout": truncate(out.Stdout, 2000)}}, nil
}

// --- SHELL_JSON --------------------------------------------------------
// expect: {jsonpath, op, value}. Content examples:
// "cat ~/infra/template.json" / expect.jsonpath: "$.Resources...Versioning".
func execShellJSON(ctx context.Context, provisioner *k8s.Provisioner, req Request) (Result, error) {
	out, err := execInPod(ctx, provisioner, req.EnvironmentID, req.Run)
	if err != nil {
		return Result{Status: StatusError}, err
	}
	if out.ExitCode != 0 {
		return Result{Status: StatusFail, Observed: map[string]any{"exit_code": out.ExitCode, "stdout": truncate(out.Stdout, 2000)}}, nil
	}

	var doc any
	if err := json.Unmarshal([]byte(out.Stdout), &doc); err != nil {
		// Content examples also feed raw scalar output (e.g. a bare
		// byte-count string from `docker image inspect --format
		// '{{.Size}}'`) through jsonpath "$" -- not valid JSON on its
		// own, so fall back to treating the trimmed stdout as the value.
		doc = strings.TrimSpace(out.Stdout)
	}

	observed, jpErr := evalJSONPath(doc, req.Expect["jsonpath"])
	if jpErr != nil {
		return Result{Status: StatusError}, jpErr
	}

	pass, err := compareOp(observed, req.Expect["op"], req.Expect["value"])
	if err != nil {
		return Result{Status: StatusError}, err
	}

	status := StatusFail
	if pass {
		status = StatusPass
	}
	return Result{Status: status, Observed: observed}, nil
}

// --- FILE_EXISTS -------------------------------------------------------
// expect: {exists: bool}. run is a file path (may use ~ for the pod's home dir).
func execFileExists(ctx context.Context, provisioner *k8s.Provisioner, req Request) (Result, error) {
	out, err := execInPod(ctx, provisioner, req.EnvironmentID, fmt.Sprintf("test -e %s", shellQuote(req.Run)))
	if err != nil {
		return Result{Status: StatusError}, err
	}
	exists := out.ExitCode == 0
	wantExists, _ := req.Expect["exists"].(bool)

	status := StatusFail
	if exists == wantExists {
		status = StatusPass
	}
	return Result{Status: status, Observed: map[string]any{"exists": exists}}, nil
}

// --- FILE_CONTENT --------------------------------------------------------
// expect: {contains?: string, contains_all?: []string}. run is a file path.
func execFileContent(ctx context.Context, provisioner *k8s.Provisioner, req Request) (Result, error) {
	out, err := execInPod(ctx, provisioner, req.EnvironmentID, fmt.Sprintf("cat %s", shellQuote(req.Run)))
	if err != nil {
		return Result{Status: StatusError}, err
	}
	if out.ExitCode != 0 {
		return Result{Status: StatusFail, Observed: map[string]any{"error": "file not readable", "stderr": truncate(out.Stderr, 500)}}, nil
	}

	pass := true
	if want, ok := req.Expect["contains"].(string); ok {
		pass = strings.Contains(out.Stdout, want)
	}
	if wantAll, ok := req.Expect["contains_all"].([]any); ok {
		for _, w := range wantAll {
			s, _ := w.(string)
			if !strings.Contains(out.Stdout, s) {
				pass = false
				break
			}
		}
	}

	status := StatusFail
	if pass {
		status = StatusPass
	}
	return Result{Status: status, Observed: map[string]any{"content_preview": truncate(out.Stdout, 500)}}, nil
}

// --- FILE_PARSE ----------------------------------------------------------
// expect: {format: "yaml"|"json"}. run is a file path.
func execFileParse(ctx context.Context, provisioner *k8s.Provisioner, req Request) (Result, error) {
	out, err := execInPod(ctx, provisioner, req.EnvironmentID, fmt.Sprintf("cat %s", shellQuote(req.Run)))
	if err != nil {
		return Result{Status: StatusError}, err
	}
	if out.ExitCode != 0 {
		return Result{Status: StatusFail, Observed: map[string]any{"error": "file not readable"}}, nil
	}

	format, _ := req.Expect["format"].(string)
	var parseErr error
	switch format {
	case "json":
		var v any
		parseErr = json.Unmarshal([]byte(out.Stdout), &v)
	case "yaml":
		var v any
		// sigs.k8s.io/yaml round-trips through JSON, so this validates
		// real YAML syntax rather than just "did json.Unmarshal accept it".
		parseErr = yaml.Unmarshal([]byte(out.Stdout), &v)
	default:
		return Result{Status: StatusError}, fmt.Errorf("unsupported FILE_PARSE format %q", format)
	}

	if parseErr != nil {
		return Result{Status: StatusFail, Observed: map[string]any{"parse_error": parseErr.Error()}}, nil
	}
	return Result{Status: StatusPass, Observed: map[string]any{"format": format, "valid": true}}, nil
}

// --- HTTP_SLO --------------------------------------------------------------
// run: target URL. expect: {success_rate_min: number, window_s?: number,
// sample_interval_s?: number}. Doc §3.3 worked example / §11.3 table:
// "success rate over N seconds under load" -- the "under load" half (a
// dedicated load generator hitting the service concurrently, doc's
// "Load generator" execution source) is a separate mechanism this
// package doesn't implement; what's here is the real, honest subset:
// repeated in-network sampling of the URL from inside the learner's own
// pod (same NetworkPolicy the learner's own curl would be bound by, so
// this measures what the learner's environment can actually reach, not
// an orchestrator-side bypass of their network boundary), computing a
// real success rate over the window. A learner debugging a genuinely
// intermittent 502 (doc's own worked incident) gets a real signal from
// this; validating behavior specifically under concurrent load traffic
// is the scoped-out half.
const (
	httpSLODefaultWindowSeconds  = 30 // capped well under window_s:180's doc example -- see doc comment above
	httpSLOMaxWindowSeconds      = 60
	httpSLOSampleIntervalSeconds = 2
)

func execHTTPSLO(ctx context.Context, provisioner *k8s.Provisioner, req Request) (Result, error) {
	url := req.Run
	if url == "" {
		return Result{Status: StatusError}, fmt.Errorf("HTTP_SLO requires run to be the target URL")
	}
	successRateMin, err := toFloat(req.Expect["success_rate_min"])
	if err != nil {
		return Result{Status: StatusError}, fmt.Errorf("HTTP_SLO requires expect.success_rate_min: %w", err)
	}

	requestedWindowSeconds := httpSLODefaultWindowSeconds
	if w, ok := req.Expect["window_s"]; ok {
		wf, err := toFloat(w)
		if err != nil {
			return Result{Status: StatusError}, fmt.Errorf("invalid expect.window_s: %w", err)
		}
		requestedWindowSeconds = int(wf)
	}

	var ctxRemainingSeconds int
	hasDeadline := false
	if deadline, ok := ctx.Deadline(); ok {
		ctxRemainingSeconds = int(time.Until(deadline).Seconds())
		hasDeadline = true
	}
	windowSeconds := clampHTTPSLOWindow(requestedWindowSeconds, ctxRemainingSeconds, hasDeadline)
	sampleCount := windowSeconds / httpSLOSampleIntervalSeconds
	if sampleCount < 1 {
		sampleCount = 1
	}

	// One exec call samples the whole window internally (a loop inside
	// the pod), not N separate execInPod calls -- each execInPod is its
	// own SPDY exec session, expensive enough that N of them would both
	// be slow and make "every 2s" timing unreliable (session setup
	// latency would dominate). curl -s -o /dev/null -w '%{http_code}'
	// prints just the status code (000 on a connection failure, which
	// correctly fails the >=200 <400 success check below rather than
	// crashing the script).
	script := fmt.Sprintf(
		`for i in $(seq 1 %d); do curl -s -o /dev/null -w '%%{http_code}\n' --max-time 5 %s; sleep %d; done`,
		sampleCount, shellQuote(url), httpSLOSampleIntervalSeconds,
	)

	out, err := execInPod(ctx, provisioner, req.EnvironmentID, script)
	if err != nil {
		return Result{Status: StatusError}, err
	}

	total, success := parseHTTPSLOSamples(out.Stdout)
	if total == 0 {
		return Result{Status: StatusError, Observed: map[string]any{"error": "no samples collected"}}, nil
	}

	observedRate := float64(success) / float64(total)
	status := StatusFail
	if observedRate >= successRateMin {
		status = StatusPass
	}
	return Result{
		Status: status,
		Observed: map[string]any{
			"success_rate":       observedRate,
			"samples":            total,
			"successes":          success,
			"window_s_requested": requestedWindowSeconds,
			"window_s_used":      windowSeconds,
		},
	}, nil
}

// clampHTTPSLOWindow bounds a requested HTTP_SLO window to what's
// actually safe to run in one exec call: never more than
// httpSLOMaxWindowSeconds (doc's own window_s:180 example blocking a
// single gRPC call for 3 minutes is a bad shape for this RPC -- see
// execHTTPSLO's doc comment), and never more than the caller's real
// remaining context deadline minus a small setup/teardown headroom (so
// the sampling script finishes before the outer context cancels it
// mid-curl, rather than the whole exec failing with a generic timeout
// error instead of a clean, explained result). Always at least one full
// sample interval.
func clampHTTPSLOWindow(requestedSeconds, ctxRemainingSeconds int, hasDeadline bool) int {
	window := requestedSeconds
	if window > httpSLOMaxWindowSeconds {
		window = httpSLOMaxWindowSeconds
	}
	if hasDeadline {
		const teardownHeadroomSeconds = 3
		budget := ctxRemainingSeconds - teardownHeadroomSeconds
		if budget < window {
			window = budget
		}
	}
	if window < httpSLOSampleIntervalSeconds {
		window = httpSLOSampleIntervalSeconds
	}
	return window
}

// parseHTTPSLOSamples parses execHTTPSLO's sampling script output: one
// HTTP status code per line ("000" on a connection failure/timeout).
// Blank lines are ignored (trailing newline, or curl producing no output
// on some early-abort paths); a line that isn't a parseable status code
// counts toward total but not success, same treatment as an explicit
// failure code -- a corrupted sample line is honest evidence of
// something going wrong, not grounds to silently drop it from the ratio.
func parseHTTPSLOSamples(stdout string) (total, success int) {
	for _, line := range strings.Split(strings.TrimSpace(stdout), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		total++
		if code, err := strconv.Atoi(line); err == nil && code >= 200 && code < 400 {
			success++
		}
	}
	return total, success
}

// --- K8S_ASSERT ----------------------------------------------------------
// run is a literal `kubectl get <resource> -o jsonpath='...'` command
// string (see content/activities' authored shape). Parsed and executed
// against the K8s API directly from the orchestrator, NOT via the
// learner's pod, since the workspace pod has no kubectl or cluster
// credentials (confirmed gap, same one InjectFault/regression hit).
// expect: {jsonpath, op, value} -- jsonpath here is informational only
// (the real jsonpath already ran server-side via `kubectl -o jsonpath`);
// this function re-derives the equivalent via direct API + our own
// jsonpath eval so it doesn't depend on a kubectl binary existing anywhere.
func execK8sAssert(ctx context.Context, provisioner *k8s.Provisioner, req Request) (Result, error) {
	// `kubectl exec <pod> [-c <container>] -- <cmd...>` is a distinct
	// shape from `kubectl get`: several authored K8S_ASSERT validators
	// check in-pod state that has no API-object equivalent (env vars a
	// pod received via envFrom, a file the app wrote). Run the command in
	// the pod via the exec API and compare its trimmed stdout to
	// expect.value.
	if pod, container, cmd, ok := parseKubectlExec(req.Run); ok {
		return execK8sAssertExec(ctx, provisioner, req, pod, container, cmd)
	}

	parsed, err := parseKubectlCommand(req.Run)
	if err != nil {
		return Result{Status: StatusError}, err
	}

	ns := k8s.NamespaceForEnv(req.EnvironmentID)
	obj, err := fetchResourceAsMap(ctx, provisioner, ns, parsed)
	if err != nil {
		return Result{Status: StatusFail, Observed: map[string]any{"error": err.Error()}}, nil
	}

	// Which jsonpath to evaluate against the fetched object:
	//   - expect.jsonpath if the author set a real one ($.status.phase, ...)
	//   - otherwise the `-o jsonpath='{...}'` expression the author wrote
	//     into the `run` command itself. A lot of authored K8S_ASSERT
	//     validators put the real selector only in `-o jsonpath=` and
	//     leave expect.jsonpath as the default "$", which would otherwise
	//     compare the *entire* resource object against a scalar `value`
	//     and always FAIL. Honouring the run-command selector matches
	//     what the author plainly intended and what a shell `kubectl get
	//     ... -o jsonpath=...` would actually print.
	jsonPathExpr := asString(req.Expect["jsonpath"])
	if jsonPathExpr == "" || jsonPathExpr == "$" {
		if fromRun := jsonpathFromRunCommand(req.Run); fromRun != "" {
			jsonPathExpr = fromRun
		}
	}

	observed, jpErr := evalJSONPath(obj, jsonPathExpr)
	if jpErr != nil {
		return Result{Status: StatusError}, jpErr
	}

	pass, err := compareOp(observed, req.Expect["op"], req.Expect["value"])
	if err != nil {
		return Result{Status: StatusError}, err
	}

	status := StatusFail
	if pass {
		status = StatusPass
	}
	return Result{Status: status, Observed: observed}, nil
}

// --- shared helpers --------------------------------------------------------

func asString(v any) string {
	s, _ := v.(string)
	return s
}

// kubectlExecRe matches `kubectl exec <pod> [-c <container>|--container <container>]
// [-it|-i|-t] -- <command...>`. The `--` separator is required (matches
// how the authored validators are written and how kubectl itself
// disambiguates its flags from the remote command).
var kubectlExecRe = regexp.MustCompile(
	`^kubectl\s+exec\s+(?:(?:-i|-t|-it|--stdin|--tty)\s+)*([a-zA-Z0-9._-]+)` +
		`(?:\s+(?:-c|--container)[=\s]+([a-zA-Z0-9._-]+))?` +
		`(?:\s+(?:-i|-t|-it|--stdin|--tty))*\s+--\s+(.+)$`,
)

// parseKubectlExec pulls (pod, container, argv) out of a `kubectl exec`
// command string. ok is false for anything that isn't a `kubectl exec
// ... -- ...` command (callers fall back to the `kubectl get` path).
func parseKubectlExec(cmd string) (pod, container string, argv []string, ok bool) {
	m := kubectlExecRe.FindStringSubmatch(strings.TrimSpace(cmd))
	if m == nil {
		return "", "", nil, false
	}
	fields := strings.Fields(m[3])
	if len(fields) == 0 {
		return "", "", nil, false
	}
	return m[1], m[2], fields, true
}

// execK8sAssertExec runs a `kubectl exec` K8S_ASSERT's remote command in
// the target pod and compares its trimmed stdout to expect.value via the
// same compareOp the `kubectl get` path uses. A non-zero exit from the
// remote command is a FAIL (the asserted state isn't there), not an
// executor ERROR.
func execK8sAssertExec(ctx context.Context, provisioner *k8s.Provisioner, req Request, pod, container string, argv []string) (Result, error) {
	ns := k8s.NamespaceForEnv(req.EnvironmentID)
	out, err := execInNamedPod(ctx, provisioner, ns, pod, container, argv)
	if err != nil {
		return Result{Status: StatusFail, Observed: map[string]any{"error": err.Error()}}, nil
	}
	if out.ExitCode != 0 {
		return Result{Status: StatusFail, Observed: map[string]any{
			"exit_code": out.ExitCode, "stderr": strings.TrimSpace(out.Stderr),
		}}, nil
	}
	observed := strings.TrimSpace(out.Stdout)
	pass, cmpErr := compareOp(observed, req.Expect["op"], req.Expect["value"])
	if cmpErr != nil {
		return Result{Status: StatusError}, cmpErr
	}
	status := StatusFail
	if pass {
		status = StatusPass
	}
	return Result{Status: status, Observed: observed}, nil
}

// kubectlOJsonpathRe pulls the expression out of a `-o jsonpath='{...}'`
// / `-o=jsonpath={...}` / `--output jsonpath="..."` flag in a kubectl
// command. Tolerates single quotes, double quotes, or no quotes.
var kubectlOJsonpathRe = regexp.MustCompile(
	`(?:-o|--output)[=\s]+(?:jsonpath|go-template)=(?:'([^']*)'|"([^"]*)"|(\S+))`,
)

// jsonpathFromRunCommand returns the `{...}` template found in a kubectl
// command's -o jsonpath flag, normalised to the "$.foo.bar" form
// evalJSONPath expects (leading "$", no surrounding braces). Empty string
// if the command has no such flag.
func jsonpathFromRunCommand(cmd string) string {
	m := kubectlOJsonpathRe.FindStringSubmatch(cmd)
	if m == nil {
		return ""
	}
	expr := m[1]
	if expr == "" {
		expr = m[2]
	}
	if expr == "" {
		expr = m[3]
	}
	expr = strings.TrimSpace(expr)
	expr = strings.TrimPrefix(expr, "{")
	expr = strings.TrimSuffix(expr, "}")
	if expr == "" {
		return ""
	}
	if !strings.HasPrefix(expr, "$") {
		expr = "$" + expr
	}
	return expr
}

func evalJSONPath(doc any, exprRaw any) (any, error) {
	expr, _ := exprRaw.(string)
	if expr == "" || expr == "$" {
		return doc, nil
	}

	jp := jsonpath.New("validator")
	// k8s.io/apimachinery's jsonpath wants relaxed/JSONPath template
	// syntax with the leading "$" stripped and wrapped in braces (its
	// own convention, matching how kubectl -o jsonpath='{...}' works,
	// which is exactly the syntax the authored content already uses).
	template := "{" + strings.TrimPrefix(expr, "$") + "}"
	if err := jp.Parse(template); err != nil {
		return nil, fmt.Errorf("parsing jsonpath %q: %w", expr, err)
	}

	var buf strings.Builder
	if err := jp.Execute(&buf, doc); err != nil {
		return nil, fmt.Errorf("evaluating jsonpath %q: %w", expr, err)
	}
	return buf.String(), nil
}

func compareOp(observed any, opRaw any, wantRaw any) (bool, error) {
	op, _ := opRaw.(string)
	switch op {
	case "exists", "":
		return observed != nil && observed != "", nil
	case "eq":
		return looseEquals(observed, wantRaw), nil
	case "ne":
		return !looseEquals(observed, wantRaw), nil
	case "lt", "gte", "gt", "lte":
		obsNum, err1 := toFloat(observed)
		wantNum, err2 := toFloat(wantRaw)
		if err1 != nil || err2 != nil {
			return false, fmt.Errorf("op %q requires numeric values, got observed=%v want=%v", op, observed, wantRaw)
		}
		switch op {
		case "lt":
			return obsNum < wantNum, nil
		case "lte":
			return obsNum <= wantNum, nil
		case "gt":
			return obsNum > wantNum, nil
		case "gte":
			return obsNum >= wantNum, nil
		}
	}
	return false, fmt.Errorf("unsupported comparison op %q", op)
}

func looseEquals(a, b any) bool {
	// Observed values come back as strings (jsonpath template execution
	// always yields text); expected values come from authored YAML/JSON
	// and may be typed (bool/number/string) -- compare as strings so
	// `value: true` matches an observed "true" the same way `value: "web"`
	// matches an observed "web".
	return fmt.Sprintf("%v", a) == fmt.Sprintf("%v", b)
}

func toFloat(v any) (float64, error) {
	switch t := v.(type) {
	case float64:
		return t, nil
	case string:
		return strconv.ParseFloat(strings.TrimSpace(t), 64)
	default:
		return 0, fmt.Errorf("cannot convert %T to number", v)
	}
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "...(truncated)"
}

// shellQuote is a minimal quote wrapper for paths used in exec commands
// this package builds itself (not learner-influenced) -- sufficient for
// the ~/path-style values every authored validator uses. A leading "~"
// is expanded to $HOME *before* quoting, not left inside
// the quotes -- single quotes suppress ALL shell expansion including
// tilde expansion, so a naively-quoted '~/toolchain.txt' resolves to a
// literal directory named "~" instead of the home directory (confirmed
// live: a real file at /home/ubuntu/toolchain.txt made `test -e
// '~/toolchain.txt'` report NOT FOUND while the unquoted/expanded form
// correctly reported FOUND). $HOME itself is safe to leave unquoted-then-
// requoted since it's read from the process environment, not learner input.
func shellQuote(s string) string {
	if strings.HasPrefix(s, "~/") {
		s = "$HOME/" + strings.TrimPrefix(s, "~/")
	} else if s == "~" {
		s = "$HOME"
	}
	return "\"" + strings.ReplaceAll(s, `"`, `\"`) + "\""
}
