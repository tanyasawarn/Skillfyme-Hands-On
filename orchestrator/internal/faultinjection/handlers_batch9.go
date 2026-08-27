package faultinjection

import (
	"context"
	"fmt"
	"strings"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/tanyasawarn/skillfyme-hands-on/orchestrator/internal/k8s"
)

// Ninth batch: the two Jenkins faults, backed by fx.jenkins-basic.v1
// (internal/fixture/handlers_jenkins.go) provisioning a real Jenkins
// controller + a real WebSocket-connected agent + a real Freestyle job
// pinned to that agent's label. Both handlers exec real crumb-
// authenticated REST calls against the controller's own localhost API --
// the exact mechanism a real operator/script uses, not a shortcut. See
// the fixture's own doc comment for why the agent connects over
// WebSocket rather than classic TCP JNLP (this exact Jenkins image never
// emits the X-Instance-Identity header the TCP handshake needs,
// confirmed live).
func init() {
	registerDynamic("f.jenkins.agent-offline", applyJenkinsAgentOffline)
	registerDynamic("f.jenkins.stale-cached-dependency", applyJenkinsStaleCachedDependency)
}

const (
	jenkinsPodLabelSelector      = "app=practice-jenkins"
	jenkinsAgentPodLabelSelector = "app=practice-jenkins-agent"
	jenkinsAgentLabelConst       = "practice-agent"
	jenkinsJobNameConst          = "practice-pipeline"
)

func findJenkinsControllerPod(ctx context.Context, provisioner *k8s.Provisioner, namespace string) (string, error) {
	pods, err := provisioner.Clientset().CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{LabelSelector: jenkinsPodLabelSelector})
	if err != nil {
		return "", fmt.Errorf("listing jenkins controller pods: %w", err)
	}
	if len(pods.Items) == 0 {
		return "", fmt.Errorf("no jenkins controller pod found in namespace %s -- has fx.jenkins-basic.v1 been applied?", namespace)
	}
	return pods.Items[0].Name, nil
}

// applyJenkinsAgentOffline: content/faults/f.jenkins.agent-offline.yaml
// params: agent_label (must match the fixture's real agent label,
// "practice-agent" -- validated below rather than trusted blindly).
//
// Uses Jenkins' own /computer/<name>/toggleOffline REST endpoint -- the
// real "temporarily offline" mechanism (distinct from doDisconnect,
// which this handler's own development found does NOT reliably keep a
// still-connected WebSocket agent offline: Jenkins reconnects it almost
// immediately since the underlying process is still alive and pinging.
// toggleOffline sets a durable temporarilyOffline flag Jenkins honors
// regardless of the agent process's own connection state -- confirmed
// live: the agent shows offline=true, temporarilyOffline=true, and stays
// that way, exactly matching the fault's own canonical_diagnostic_path
// ("Jenkins > Nodes → confirm the labeled agent shows Offline").
func applyJenkinsAgentOffline(ctx context.Context, provisioner *k8s.Provisioner, namespace string, params map[string]string) (Result, error) {
	agentLabel := params["agent_label"]
	if agentLabel == "" {
		return Result{}, fmt.Errorf("f.jenkins.agent-offline requires param: agent_label")
	}
	if agentLabel != jenkinsAgentLabelConst {
		return Result{}, fmt.Errorf("f.jenkins.agent-offline: agent_label %q does not match the fixture's real agent %q", agentLabel, jenkinsAgentLabelConst)
	}

	jenkinsPod, err := findJenkinsControllerPod(ctx, provisioner, namespace)
	if err != nil {
		return Result{}, err
	}

	toggleCmd := fmt.Sprintf(`
COOKIES=/tmp/fault-offline-cookies.txt
CRUMB_JSON=$(curl -s -c $COOKIES http://localhost:8080/crumbIssuer/api/json)
CRUMB=$(echo "$CRUMB_JSON" | grep -o '"crumb":"[^"]*"' | cut -d'"' -f4)
curl -s -b $COOKIES -X POST http://localhost:8080/computer/%s/toggleOffline \
  -H "Jenkins-Crumb: $CRUMB" \
  --data-urlencode "offlineMessage=fault: agent taken offline by f.jenkins.agent-offline" \
  -o /dev/null -w "%%{http_code}"
`, agentLabel)
	result, err := k8s.ExecInPod(ctx, provisioner, namespace, jenkinsPod, "jenkins", toggleCmd, 20*time.Second)
	if err != nil {
		return Result{}, fmt.Errorf("toggling agent %s offline: %w", agentLabel, err)
	}
	if result.Stdout != "302" && result.Stdout != "200" {
		return Result{}, fmt.Errorf("toggling agent %s offline: unexpected HTTP status %q (stderr: %s)", agentLabel, result.Stdout, result.Stderr)
	}

	statusCmd := fmt.Sprintf(`curl -s http://localhost:8080/computer/%s/api/json`, agentLabel)
	statusResult, err := k8s.ExecInPod(ctx, provisioner, namespace, jenkinsPod, "jenkins", statusCmd, 15*time.Second)
	if err != nil {
		return Result{Applied: true, SymptomVerified: false}, nil
	}
	verified := strings.Contains(statusResult.Stdout, `"offline":true`)
	return Result{Applied: true, SymptomVerified: verified}, nil
}

// applyJenkinsStaleCachedDependency: content/faults/f.jenkins.stale-cached-dependency.yaml
// params: job (must match the fixture's real job, "practice-pipeline"),
// stale_dependency (the version string to force the cache to serve --
// content-authored, e.g. an old semver like "1.0.0", contrasted against
// whatever "source" would resolve to on a clean build).
//
// Overwrites the real cache file
// (/tmp/practice-cache/dependency-version.txt) the fixture's own build
// step reads from -- on the AGENT pod specifically (not the controller),
// since that's where the Freestyle job's shell step actually executes
// (assignedNode pins it there). The next build genuinely reads this
// stale value and writes it to dependency-used.txt, a real, observable
// mismatch against whatever the job's own "source" step would resolve
// -- not a simulated log line.
func applyJenkinsStaleCachedDependency(ctx context.Context, provisioner *k8s.Provisioner, namespace string, params map[string]string) (Result, error) {
	job := params["job"]
	staleDependency := params["stale_dependency"]
	if job == "" || staleDependency == "" {
		return Result{}, fmt.Errorf("f.jenkins.stale-cached-dependency requires params: job, stale_dependency")
	}
	if job != jenkinsJobNameConst {
		return Result{}, fmt.Errorf("f.jenkins.stale-cached-dependency: job %q does not match the fixture's real job %q", job, jenkinsJobNameConst)
	}

	pods, err := provisioner.Clientset().CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{LabelSelector: jenkinsAgentPodLabelSelector})
	if err != nil {
		return Result{}, fmt.Errorf("listing jenkins agent pods: %w", err)
	}
	if len(pods.Items) == 0 {
		return Result{}, fmt.Errorf("no jenkins agent pod found in namespace %s -- has fx.jenkins-basic.v1 been applied?", namespace)
	}
	agentPod := pods.Items[0].Name

	// Ensure the cache directory/file exists first -- a fresh
	// environment where the job has never run yet has no cache to make
	// stale; this handler creates it directly rather than requiring a
	// prior build, matching the same "verify prerequisites, don't
	// silently no-op" stance other handlers in this codebase take.
	writeCmd := fmt.Sprintf(`mkdir -p /tmp/practice-cache && printf '%%s' '%s' > /tmp/practice-cache/dependency-version.txt && cat /tmp/practice-cache/dependency-version.txt`, staleDependency)
	result, err := k8s.ExecInPod(ctx, provisioner, namespace, agentPod, "agent", writeCmd, 15*time.Second)
	if err != nil {
		return Result{}, fmt.Errorf("writing stale cache value on agent pod: %w", err)
	}
	verified := result.Stdout == staleDependency
	return Result{Applied: true, SymptomVerified: verified}, nil
}
