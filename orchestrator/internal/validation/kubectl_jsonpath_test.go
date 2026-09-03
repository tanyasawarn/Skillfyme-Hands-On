package validation

import (
	"reflect"
	"testing"
)

func TestParseKubectlExec(t *testing.T) {
	cases := []struct {
		name          string
		cmd           string
		wantPod       string
		wantContainer string
		wantArgv      []string
		wantOK        bool
	}{
		{
			name:     "simple printenv",
			cmd:      "kubectl exec config-demo -- printenv LOG_LEVEL",
			wantPod:  "config-demo",
			wantArgv: []string{"printenv", "LOG_LEVEL"},
			wantOK:   true,
		},
		{
			name:          "with container flag",
			cmd:           "kubectl exec mypod -c app -- test -f /data/marker.txt",
			wantPod:       "mypod",
			wantContainer: "app",
			wantArgv:      []string{"test", "-f", "/data/marker.txt"},
			wantOK:        true,
		},
		{
			name:     "with -it flags",
			cmd:      "kubectl exec -it storage-demo -- cat /data/x",
			wantPod:  "storage-demo",
			wantArgv: []string{"cat", "/data/x"},
			wantOK:   true,
		},
		{
			name:   "kubectl get is not exec",
			cmd:    "kubectl get pod writer-reader -o jsonpath='{.status.phase}'",
			wantOK: false,
		},
		{
			name:   "exec without -- separator is rejected",
			cmd:    "kubectl exec config-demo printenv LOG_LEVEL",
			wantOK: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			pod, container, argv, ok := parseKubectlExec(tc.cmd)
			if ok != tc.wantOK {
				t.Fatalf("ok = %v, want %v", ok, tc.wantOK)
			}
			if !ok {
				return
			}
			if pod != tc.wantPod || container != tc.wantContainer || !reflect.DeepEqual(argv, tc.wantArgv) {
				t.Fatalf("got (%q, %q, %v), want (%q, %q, %v)",
					pod, container, argv, tc.wantPod, tc.wantContainer, tc.wantArgv)
			}
		})
	}
}

func TestJSONPathFromRunCommand(t *testing.T) {
	cases := []struct {
		name string
		cmd  string
		want string
	}{
		{
			name: "single-quoted -o jsonpath",
			cmd:  "kubectl get pod writer-reader -o jsonpath='{.status.phase}'",
			want: "$.status.phase",
		},
		{
			name: "-o=jsonpath form",
			cmd:  "kubectl get svc web-svc -o=jsonpath={.spec.type}",
			want: "$.spec.type",
		},
		{
			name: "double-quoted --output jsonpath",
			cmd:  `kubectl get statefulset db --output jsonpath="{.spec.replicas}"`,
			want: "$.spec.replicas",
		},
		{
			name: "already has leading $",
			cmd:  "kubectl get pod p -o jsonpath='{$.status.phase}'",
			want: "$.status.phase",
		},
		{
			name: "no jsonpath flag",
			cmd:  "kubectl get pod writer-reader",
			want: "",
		},
		{
			name: "jsonpath flag with -n before it",
			cmd:  "kubectl get pod p -n env-x -o jsonpath='{.status.phase}'",
			want: "$.status.phase",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := jsonpathFromRunCommand(tc.cmd); got != tc.want {
				t.Fatalf("jsonpathFromRunCommand(%q) = %q, want %q", tc.cmd, got, tc.want)
			}
		})
	}
}

// The bug this guards against: a validator whose `run` carries the real
// selector in `-o jsonpath=` while `expect.jsonpath` is the default "$"
// used to compare the ENTIRE fetched object against a scalar value and
// always FAIL. evalJSONPath must now receive the run-command selector.
func TestEvalJSONPath_UsesRunCommandSelectorFallback(t *testing.T) {
	podObj := map[string]any{
		"status": map[string]any{"phase": "Running"},
	}

	// Simulate execK8sAssert's selection logic.
	expectJSONPath := "$" // author left the default
	sel := expectJSONPath
	if sel == "" || sel == "$" {
		if fromRun := jsonpathFromRunCommand(
			"kubectl get pod writer-reader -o jsonpath='{.status.phase}'",
		); fromRun != "" {
			sel = fromRun
		}
	}

	observed, err := evalJSONPath(podObj, sel)
	if err != nil {
		t.Fatalf("evalJSONPath: %v", err)
	}
	if observed != "Running" {
		t.Fatalf("observed = %#v, want \"Running\"", observed)
	}
}
