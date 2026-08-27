package fixture

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/tanyasawarn/skillfyme-hands-on/orchestrator/internal/k8s"
)

// TestDinDFixtureAndFaults_LiveIntegration is real-infra-gated AND
// heavier than this package's other live tests -- a privileged DinD
// daemon booting, real image pulls (act's own job-runner image,
// catthehacker/ubuntu:act-latest, is large), and 4 real fault mutations
// all happen inside ONE pod, sequentially. Confirmed live (this
// session, outside the cluster first) that privileged pods, nested
// `docker run`, `docker swarm`/`docker service`, and the real
// nektos/act binary all genuinely work on this cluster before this test
// or the fixture itself were written.
func TestDinDFixtureAndFaults_LiveIntegration(t *testing.T) {
	provisioner := setupLiveProvisioner(t)
	ctx, cancel := context.WithTimeout(context.Background(), 400*time.Second)
	defer cancel()

	envID := uuid.New().String()
	ns := "fx-dind-test-" + envID[:8]

	clientset := provisioner.Clientset()
	// No PodSecurity "restricted" enforcement on this namespace -- this
	// fixture's DinD container needs `privileged: true` (real nested
	// Docker capability, not just root), which "restricted" categorically
	// forbids at the admission-controller layer. Same documented,
	// deliberate exception as T2's own privileged pod shape
	// (internal/k8s/provision.go's createNamespace doc comment).
	if _, err := clientset.CoreV1().Namespaces().Create(ctx, &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{Name: ns},
	}, metav1.CreateOptions{}); err != nil {
		t.Fatalf("creating test namespace: %v", err)
	}
	t.Cleanup(func() {
		_ = clientset.CoreV1().Namespaces().Delete(context.Background(), ns, metav1.DeleteOptions{})
	})
	applyRealT1NetworkBaseline(t, ctx, provisioner, ns)

	if err := applyDinDWorkspace(ctx, provisioner, envID, ns); err != nil {
		t.Fatalf("applyDinDWorkspace failed: %v", err)
	}

	dindPod, err := k8s.FindPodByLabel(ctx, provisioner, ns, "app="+dindDeployment)
	if err != nil {
		t.Fatalf("finding dind pod: %v", err)
	}

	// The four subtests below directly reproduce
	// faultinjection.applyDockerfileWrongWorkdir /
	// applyDockerNetworkNotAttached /
	// applyDockerSwarmServiceImagePullFail /
	// applyGitHubActionsSecretNotPassed's own real mutations rather than
	// cross-importing faultinjection (import cycle -- same convention as
	// TestTektonFaultInjection_LiveIntegration).

	t.Run("f.docker.dockerfile-wrong-workdir: a real build+run genuinely fails on a wrong WORKDIR", func(t *testing.T) {
		cmd := `
set -e
mkdir -p /build-workdir-fault-test
cd /build-workdir-fault-test
cat > Dockerfile <<'DFEOF'
FROM alpine:latest
WORKDIR /wrong-path
COPY app.sh /app/app.sh
CMD ["sh", "/app/app.sh"]
DFEOF
cat > app.sh <<'APPEOF'
#!/bin/sh
cat ./config.txt
APPEOF
echo "real-config" > config.txt
docker build -q -t practice-workdir-fault-test:latest . >/dev/null
`
		buildResult, err := k8s.ExecInPod(ctx, provisioner, ns, dindPod, "dind", cmd, 60*time.Second)
		if err != nil || buildResult.ExitCode != 0 {
			t.Fatalf("building broken image: err=%v result=%+v", err, buildResult)
		}

		runResult, err := k8s.ExecInPod(ctx, provisioner, ns, dindPod, "dind",
			"docker run --rm practice-workdir-fault-test:latest; echo EXIT:$?", 20*time.Second)
		if err != nil {
			t.Fatalf("running broken image: %v", err)
		}
		if !strings.Contains(runResult.Stdout, "EXIT:1") {
			t.Fatalf("expected the container to exit non-zero, got: %s", runResult.Stdout)
		}
		// The container's own stderr (`cat`'s "No such file" error)
		// arrives in ExecInPod's Stderr, not Stdout.
		combined := runResult.Stdout + runResult.Stderr
		if !strings.Contains(combined, "No such file") && !strings.Contains(combined, "not found") {
			t.Fatalf("expected a real file-not-found error, got stdout=%q stderr=%q", runResult.Stdout, runResult.Stderr)
		}
	})

	t.Run("f.docker.network-not-attached: a client on the default bridge genuinely can't reach the server", func(t *testing.T) {
		cmd := `
set -e
docker network create practice-net-test >/dev/null 2>&1 || true
docker rm -f practice-net-test-server practice-net-test-client >/dev/null 2>&1 || true
docker run -d --name practice-net-test-server --network practice-net-test alpine:latest \
  sh -c 'while true; do { printf "HTTP/1.1 200 OK\r\n\r\nok\n"; } | nc -l -p 8080; done' >/dev/null
sleep 1
docker run -d --name practice-net-test-client alpine:latest sh -c 'sleep infinity' >/dev/null
`
		setupResult, err := k8s.ExecInPod(ctx, provisioner, ns, dindPod, "dind", cmd, 30*time.Second)
		if err != nil || setupResult.ExitCode != 0 {
			t.Fatalf("setting up misattached client: err=%v result=%+v", err, setupResult)
		}

		checkResult, err := k8s.ExecInPod(ctx, provisioner, ns, dindPod, "dind",
			"docker exec practice-net-test-client wget -q -T 3 -O- http://practice-net-test-server:8080/ 2>&1; echo EXIT:$?",
			15*time.Second)
		if err != nil {
			t.Fatalf("checking client connectivity: %v", err)
		}
		if strings.Contains(checkResult.Stdout, "EXIT:0") {
			t.Fatal("REGRESSION: expected the misattached client to fail reaching the server, but it succeeded")
		}
	})

	t.Run("f.docker.swarm-service-image-pull-fail: an unauthenticated pull genuinely gets rejected", func(t *testing.T) {
		// Two real, non-obvious bugs found live and fixed here (see
		// fx.dind-workspace.v1's and applyDockerSwarmServiceImagePullFail's
		// own doc comments): (1) tagging the SAME content the healthy
		// baseline already uses (nginx:alpine) resolves to an
		// already-locally-present digest, so use busybox instead; (2) a
		// service created WITH --with-registry-auth retains working
		// credentials independent of docker login/logout -- this fixture's
		// service is deliberately created WITHOUT it, so a real
		// login/push/logout/rmi cycle against a genuinely different image
		// is needed to force a real unauthenticated pull attempt.
		cmd := `
set -e
docker rmi busybox:latest localhost:5555/practice-swarm-image:broken-fault-test --force >/dev/null 2>&1 || true
docker pull docker.io/library/busybox:latest >/dev/null 2>&1
docker tag docker.io/library/busybox:latest localhost:5555/practice-swarm-image:broken-fault-test
docker login localhost:5555 -u ciuser -p cipass-dev-12345 >/dev/null 2>&1
docker push localhost:5555/practice-swarm-image:broken-fault-test >/dev/null 2>&1
docker logout localhost:5555 >/dev/null 2>&1
docker rmi busybox:latest localhost:5555/practice-swarm-image:broken-fault-test --force >/dev/null 2>&1
docker service update --image localhost:5555/practice-swarm-image:broken-fault-test practice-swarm-svc >/dev/null 2>&1 || true
`
		if _, err := k8s.ExecInPod(ctx, provisioner, ns, dindPod, "dind", cmd, 30*time.Second); err != nil {
			t.Fatalf("triggering broken service update: %v", err)
		}

		deadline := time.Now().Add(30 * time.Second)
		found := false
		for time.Now().Before(deadline) {
			psResult, err := k8s.ExecInPod(ctx, provisioner, ns, dindPod, "dind",
				"docker service ps --no-trunc practice-swarm-svc 2>&1", 15*time.Second)
			if err == nil && (strings.Contains(psResult.Stdout, "authorization failed") || strings.Contains(psResult.Stdout, "pull access denied")) {
				found = true
				break
			}
			time.Sleep(2 * time.Second)
		}
		if !found {
			t.Fatal("REGRESSION: expected a real pull-access-denied rejection on the Swarm service, but none appeared within 30s")
		}
	})

	t.Run("f.github.actions-secret-not-passed: a real act run genuinely fails without the secret", func(t *testing.T) {
		cmd := "cd /workflows && /usr/local/bin/act push -W .github/workflows/caller-broken.yml -P ubuntu-latest=catthehacker/ubuntu:act-latest --container-architecture linux/arm64 2>&1"
		result, err := k8s.ExecInPod(ctx, provisioner, ns, dindPod, "dind", cmd, 60*time.Second)
		if err != nil {
			t.Fatalf("running act against the broken caller workflow: %v", err)
		}
		if result.ExitCode == 0 {
			t.Fatal("REGRESSION: expected the broken caller workflow to fail, but act reported success")
		}
		if !strings.Contains(result.Stdout, "AUTH FAILURE: DEPLOY_TOKEN is empty") {
			t.Fatalf("expected the real auth-failure message from the job container, got: %s", result.Stdout)
		}
	})
}
