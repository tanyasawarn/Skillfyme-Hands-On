package validation

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

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
		return Result{Status: "ERROR"}, err
	}

	wantCode, _ := req.Expect["exit_code"].(float64) // JSON numbers decode as float64
	pass := out.ExitCode == int(wantCode)

	if pass {
		if contains, ok := req.Expect["stdout_contains"].(string); ok {
			pass = strings.Contains(out.Stdout, contains)
		}
	}

	status := "FAIL"
	if pass {
		status = "PASS"
	}
	return Result{Status: status, Observed: map[string]any{"exit_code": out.ExitCode, "stdout": truncate(out.Stdout, 2000)}}, nil
}

// --- SHELL_JSON --------------------------------------------------------
// expect: {jsonpath, op, value}. Content examples:
// "cat ~/infra/template.json" / expect.jsonpath: "$.Resources...Versioning".
func execShellJSON(ctx context.Context, provisioner *k8s.Provisioner, req Request) (Result, error) {
	out, err := execInPod(ctx, provisioner, req.EnvironmentID, req.Run)
	if err != nil {
		return Result{Status: "ERROR"}, err
	}
	if out.ExitCode != 0 {
		return Result{Status: "FAIL", Observed: map[string]any{"exit_code": out.ExitCode, "stdout": truncate(out.Stdout, 2000)}}, nil
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
		return Result{Status: "ERROR"}, jpErr
	}

	pass, err := compareOp(observed, req.Expect["op"], req.Expect["value"])
	if err != nil {
		return Result{Status: "ERROR"}, err
	}

	status := "FAIL"
	if pass {
		status = "PASS"
	}
	return Result{Status: status, Observed: observed}, nil
}

// --- FILE_EXISTS -------------------------------------------------------
// expect: {exists: bool}. run is a file path (may use ~ for the pod's home dir).
func execFileExists(ctx context.Context, provisioner *k8s.Provisioner, req Request) (Result, error) {
	out, err := execInPod(ctx, provisioner, req.EnvironmentID, fmt.Sprintf("test -e %s", shellQuote(req.Run)))
	if err != nil {
		return Result{Status: "ERROR"}, err
	}
	exists := out.ExitCode == 0
	wantExists, _ := req.Expect["exists"].(bool)

	status := "FAIL"
	if exists == wantExists {
		status = "PASS"
	}
	return Result{Status: status, Observed: map[string]any{"exists": exists}}, nil
}

// --- FILE_CONTENT --------------------------------------------------------
// expect: {contains?: string, contains_all?: []string}. run is a file path.
func execFileContent(ctx context.Context, provisioner *k8s.Provisioner, req Request) (Result, error) {
	out, err := execInPod(ctx, provisioner, req.EnvironmentID, fmt.Sprintf("cat %s", shellQuote(req.Run)))
	if err != nil {
		return Result{Status: "ERROR"}, err
	}
	if out.ExitCode != 0 {
		return Result{Status: "FAIL", Observed: map[string]any{"error": "file not readable", "stderr": truncate(out.Stderr, 500)}}, nil
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

	status := "FAIL"
	if pass {
		status = "PASS"
	}
	return Result{Status: status, Observed: map[string]any{"content_preview": truncate(out.Stdout, 500)}}, nil
}

// --- FILE_PARSE ----------------------------------------------------------
// expect: {format: "yaml"|"json"}. run is a file path.
func execFileParse(ctx context.Context, provisioner *k8s.Provisioner, req Request) (Result, error) {
	out, err := execInPod(ctx, provisioner, req.EnvironmentID, fmt.Sprintf("cat %s", shellQuote(req.Run)))
	if err != nil {
		return Result{Status: "ERROR"}, err
	}
	if out.ExitCode != 0 {
		return Result{Status: "FAIL", Observed: map[string]any{"error": "file not readable"}}, nil
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
		return Result{Status: "ERROR"}, fmt.Errorf("unsupported FILE_PARSE format %q", format)
	}

	if parseErr != nil {
		return Result{Status: "FAIL", Observed: map[string]any{"parse_error": parseErr.Error()}}, nil
	}
	return Result{Status: "PASS", Observed: map[string]any{"format": format, "valid": true}}, nil
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
	parsed, err := parseKubectlCommand(req.Run)
	if err != nil {
		return Result{Status: "ERROR"}, err
	}

	ns := k8s.NamespaceForEnv(req.EnvironmentID)
	obj, err := fetchResourceAsMap(ctx, provisioner, ns, parsed)
	if err != nil {
		return Result{Status: "FAIL", Observed: map[string]any{"error": err.Error()}}, nil
	}

	observed, jpErr := evalJSONPath(obj, req.Expect["jsonpath"])
	if jpErr != nil {
		return Result{Status: "ERROR"}, jpErr
	}

	pass, err := compareOp(observed, req.Expect["op"], req.Expect["value"])
	if err != nil {
		return Result{Status: "ERROR"}, err
	}

	status := "FAIL"
	if pass {
		status = "PASS"
	}
	return Result{Status: status, Observed: observed}, nil
}

// --- shared helpers --------------------------------------------------------

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
