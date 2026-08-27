package fixture

import (
	"context"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/tanyasawarn/skillfyme-hands-on/orchestrator/internal/k8s"
)

func waitForJenkinsAgentOnline(t *testing.T, ctx context.Context, provisioner *k8s.Provisioner, namespace, jenkinsPod string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		result, err := k8s.ExecInPod(ctx, provisioner, namespace, jenkinsPod, "jenkins",
			"curl -s http://localhost:8080/computer/practice-agent/api/json", 10*time.Second)
		if err == nil && strings.Contains(result.Stdout, `"offline":false`) {
			return
		}
		time.Sleep(5 * time.Second)
	}
	t.Fatalf("jenkins agent did not come online within %s", timeout)
}

func triggerJenkinsBuild(t *testing.T, ctx context.Context, provisioner *k8s.Provisioner, namespace, jenkinsPod, job string) {
	t.Helper()
	cmd := `
COOKIES=/tmp/test-build-cookies.txt
CRUMB_JSON=$(curl -s -c $COOKIES http://localhost:8080/crumbIssuer/api/json)
CRUMB=$(echo "$CRUMB_JSON" | grep -o '"crumb":"[^"]*"' | cut -d'"' -f4)
curl -s -b $COOKIES -X POST http://localhost:8080/job/` + job + `/build -H "Jenkins-Crumb: $CRUMB" -o /dev/null -w "%{http_code}"
`
	result, err := k8s.ExecInPod(ctx, provisioner, namespace, jenkinsPod, "jenkins", cmd, 15*time.Second)
	if err != nil || result.Stdout != "201" {
		t.Fatalf("triggering build: err=%v stdout=%q stderr=%s", err, result.Stdout, result.Stderr)
	}
}

func waitForJenkinsBuildConsole(t *testing.T, ctx context.Context, provisioner *k8s.Provisioner, namespace, jenkinsPod, job string, buildNumber int, timeout time.Duration) string {
	t.Helper()
	deadline := time.Now().Add(timeout)
	cmd := `curl -s http://localhost:8080/job/` + job + `/` + strconv.Itoa(buildNumber) + `/consoleText`
	for time.Now().Before(deadline) {
		result, err := k8s.ExecInPod(ctx, provisioner, namespace, jenkinsPod, "jenkins", cmd, 10*time.Second)
		if err == nil && (strings.Contains(result.Stdout, "Finished: SUCCESS") || strings.Contains(result.Stdout, "Finished: FAILURE")) {
			return result.Stdout
		}
		time.Sleep(3 * time.Second)
	}
	t.Fatalf("build #%d of %s did not finish within %s", buildNumber, job, timeout)
	return ""
}

func jenkinsQueueHasJob(t *testing.T, ctx context.Context, provisioner *k8s.Provisioner, namespace, jenkinsPod string) bool {
	t.Helper()
	result, err := k8s.ExecInPod(ctx, provisioner, namespace, jenkinsPod, "jenkins", "curl -s http://localhost:8080/queue/api/json", 10*time.Second)
	if err != nil {
		t.Fatalf("querying queue: %v", err)
	}
	return strings.Contains(result.Stdout, `"buildable":false`) && strings.Contains(result.Stdout, jenkinsJobName)
}

func TestJenkinsFixtureAndFaults_LiveIntegration(t *testing.T) {
	provisioner := setupLiveProvisioner(t)
	ctx, cancel := context.WithTimeout(context.Background(), 280*time.Second)
	defer cancel()

	envID := uuid.New().String()
	ns := "fx-jenkins-test-" + envID[:8]

	clientset := provisioner.Clientset()
	if _, err := clientset.CoreV1().Namespaces().Create(ctx, &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name:   ns,
			Labels: map[string]string{"pod-security.kubernetes.io/enforce": "restricted"},
		},
	}, metav1.CreateOptions{}); err != nil {
		t.Fatalf("creating test namespace: %v", err)
	}
	t.Cleanup(func() {
		_ = clientset.CoreV1().Namespaces().Delete(context.Background(), ns, metav1.DeleteOptions{})
	})
	applyRealT1NetworkBaseline(t, ctx, provisioner, ns)

	if err := applyJenkinsBasic(ctx, provisioner, envID, ns); err != nil {
		t.Fatalf("applyJenkinsBasic failed: %v", err)
	}

	jenkinsPod, err := findJenkinsPodName(ctx, provisioner, ns, jenkinsDeploymentName)
	if err != nil {
		t.Fatalf("finding jenkins controller pod: %v", err)
	}
	waitForJenkinsAgentOnline(t, ctx, provisioner, ns, jenkinsPod, 150*time.Second)

	t.Run("healthy baseline: a real build runs on the agent and resolves the seeded dependency version", func(t *testing.T) {
		triggerJenkinsBuild(t, ctx, provisioner, ns, jenkinsPod, jenkinsJobName)
		console := waitForJenkinsBuildConsole(t, ctx, provisioner, ns, jenkinsPod, jenkinsJobName, 1, 60*time.Second)
		if !strings.Contains(console, "Building remotely on practice-agent") {
			t.Fatalf("expected the build to run on practice-agent, console:\n%s", console)
		}
		if !strings.Contains(console, "resolved dependency version: 1.0.0") {
			t.Fatalf("expected the healthy baseline to resolve version 1.0.0, console:\n%s", console)
		}
		if !strings.Contains(console, "Finished: SUCCESS") {
			t.Fatalf("expected the healthy baseline build to succeed, console:\n%s", console)
		}
	})

	t.Run("f.jenkins.agent-offline: a build genuinely queues instead of starting", func(t *testing.T) {
		toggleCmd := `
COOKIES=/tmp/test-offline-cookies.txt
CRUMB_JSON=$(curl -s -c $COOKIES http://localhost:8080/crumbIssuer/api/json)
CRUMB=$(echo "$CRUMB_JSON" | grep -o '"crumb":"[^"]*"' | cut -d'"' -f4)
curl -s -b $COOKIES -X POST http://localhost:8080/computer/practice-agent/toggleOffline \
  -H "Jenkins-Crumb: $CRUMB" \
  --data-urlencode "offlineMessage=test fault" \
  -o /dev/null -w "%{http_code}"
`
		result, err := k8s.ExecInPod(ctx, provisioner, ns, jenkinsPod, "jenkins", toggleCmd, 15*time.Second)
		if err != nil || (result.Stdout != "302" && result.Stdout != "200") {
			t.Fatalf("toggling agent offline: err=%v stdout=%q", err, result.Stdout)
		}

		statusResult, err := k8s.ExecInPod(ctx, provisioner, ns, jenkinsPod, "jenkins", "curl -s http://localhost:8080/computer/practice-agent/api/json", 10*time.Second)
		if err != nil || !strings.Contains(statusResult.Stdout, `"offline":true`) {
			t.Fatalf("expected agent to show offline=true, got: %s (err=%v)", statusResult.Stdout, err)
		}

		triggerJenkinsBuild(t, ctx, provisioner, ns, jenkinsPod, jenkinsJobName)

		deadline := time.Now().Add(20 * time.Second)
		queued := false
		for time.Now().Before(deadline) {
			if jenkinsQueueHasJob(t, ctx, provisioner, ns, jenkinsPod) {
				queued = true
				break
			}
			time.Sleep(2 * time.Second)
		}
		if !queued {
			t.Fatal("expected the build to be stuck in the queue (buildable=false) while the agent is offline")
		}

		// Bring the agent back online so it doesn't leak a permanently
		// broken fixture state into whatever runs next in this same
		// namespace, and to unblock the queued build for the next subtest.
		result2, err := k8s.ExecInPod(ctx, provisioner, ns, jenkinsPod, "jenkins", toggleCmd, 15*time.Second)
		if err != nil || (result2.Stdout != "302" && result2.Stdout != "200") {
			t.Fatalf("toggling agent back online: err=%v stdout=%q", err, result2.Stdout)
		}
		waitForJenkinsAgentOnline(t, ctx, provisioner, ns, jenkinsPod, 30*time.Second)
	})

	t.Run("f.jenkins.stale-cached-dependency: a build genuinely resolves the stale cached version", func(t *testing.T) {
		agentPods, err := clientset.CoreV1().Pods(ns).List(ctx, metav1.ListOptions{LabelSelector: "app=" + jenkinsAgentDeployment})
		if err != nil || len(agentPods.Items) == 0 {
			t.Fatalf("finding jenkins agent pod: %v", err)
		}
		agentPod := agentPods.Items[0].Name

		const staleVersion = "0.9.0-stale"
		writeCmd := "mkdir -p /tmp/practice-cache && printf '" + staleVersion + "' > /tmp/practice-cache/dependency-version.txt && cat /tmp/practice-cache/dependency-version.txt"
		result, err := k8s.ExecInPod(ctx, provisioner, ns, agentPod, "agent", writeCmd, 15*time.Second)
		if err != nil || result.Stdout != staleVersion {
			t.Fatalf("seeding stale cache: err=%v stdout=%q", err, result.Stdout)
		}

		// This subtest may run after M2's own queued build from the
		// offline subtest already consumed build #2 -- resolve the next
		// build number dynamically rather than assuming a fixed number.
		jobInfo, err := k8s.ExecInPod(ctx, provisioner, ns, jenkinsPod, "jenkins", "curl -s http://localhost:8080/job/"+jenkinsJobName+"/api/json", 10*time.Second)
		if err != nil {
			t.Fatalf("querying job info: %v", err)
		}
		nextBuildMarker := `"nextBuildNumber":`
		idx := strings.Index(jobInfo.Stdout, nextBuildMarker)
		if idx == -1 {
			t.Fatalf("could not find nextBuildNumber in job info: %s", jobInfo.Stdout)
		}
		rest := jobInfo.Stdout[idx+len(nextBuildMarker):]
		end := strings.IndexAny(rest, ",}")
		nextBuild, err := strconv.Atoi(rest[:end])
		if err != nil {
			t.Fatalf("parsing nextBuildNumber %q: %v", rest[:end], err)
		}

		triggerJenkinsBuild(t, ctx, provisioner, ns, jenkinsPod, jenkinsJobName)
		console := waitForJenkinsBuildConsole(t, ctx, provisioner, ns, jenkinsPod, jenkinsJobName, nextBuild, 60*time.Second)
		if !strings.Contains(console, "resolved dependency version: "+staleVersion) {
			t.Fatalf("expected the build to resolve the STALE cached version %s, console:\n%s", staleVersion, console)
		}
	})
}
