package faultinjection

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/tanyasawarn/skillfyme-hands-on/orchestrator/internal/k8s"
)

// Fourteenth batch: the four faults backed by fx.dind-workspace.v1
// (internal/fixture/handlers_dind.go) -- a single real, privileged
// Docker-in-Docker pod. Registered as DynamicHandler since each needs
// *k8s.Provisioner for ExecInPod (running real docker/act commands
// inside the fixture's own DinD pod).
func init() {
	registerDynamic("f.docker.dockerfile-wrong-workdir", applyDockerfileWrongWorkdir)
	registerDynamic("f.docker.network-not-attached", applyDockerNetworkNotAttached)
	registerDynamic("f.docker.swarm-service-image-pull-fail", applyDockerSwarmServiceImagePullFail)
	registerDynamic("f.github.actions-secret-not-passed", applyGitHubActionsSecretNotPassed)
}

const dindPodLabelSelector = "app=practice-dind"

// dindRegistryPort/dindSwarmServiceName mirror
// internal/fixture/handlers_dind.go's own constants of the same name --
// duplicated here (not imported; fixture's are unexported) following
// this codebase's existing cross-package pattern (see
// helmReleaseNameConst in handlers_batch10.go).
const (
	dindRegistryPortConst     = 5555
	dindRegistryUserConst     = "ciuser"
	dindRegistryPassConst     = "cipass-dev-12345"
	dindSwarmServiceNameConst = "practice-swarm-svc"
)

// applyDockerfileWrongWorkdir: content/faults/f.docker.dockerfile-wrong-workdir.yaml
// params: image (the name/tag to build), wrong_workdir (the incorrect
// WORKDIR path to inject).
//
// Builds a REAL image inside the fixture's DinD daemon from a
// deliberately broken Dockerfile (WORKDIR set to wrong_workdir, while
// the app's own config file is COPY'd to /app/) and runs it. Live-
// verified (this session, outside the cluster first): the real
// container genuinely exits non-zero with a real "file not found"
// error, exactly matching the fault's canonical_diagnostic_path.
func applyDockerfileWrongWorkdir(ctx context.Context, provisioner *k8s.Provisioner, namespace string, params map[string]string) (Result, error) {
	image := params["image"]
	wrongWorkdir := params["wrong_workdir"]
	if image == "" || wrongWorkdir == "" {
		return Result{}, fmt.Errorf("f.docker.dockerfile-wrong-workdir requires params: image, wrong_workdir")
	}
	if wrongWorkdir == "/app" {
		return Result{}, fmt.Errorf("f.docker.dockerfile-wrong-workdir: wrong_workdir %q is the app's REAL expected path -- this fault requires a genuinely mismatched WORKDIR", wrongWorkdir)
	}

	dindPod, err := k8s.FindPodByLabel(ctx, provisioner, namespace, dindPodLabelSelector)
	if err != nil {
		return Result{}, fmt.Errorf("finding dind pod: %w", err)
	}

	cmd := fmt.Sprintf(`
set -e
mkdir -p /build-workdir-fault
cd /build-workdir-fault
cat > Dockerfile <<'DFEOF'
FROM alpine:latest
WORKDIR %s
COPY app.sh /app/app.sh
CMD ["sh", "/app/app.sh"]
DFEOF
cat > app.sh <<'APPEOF'
#!/bin/sh
cat ./config.txt
APPEOF
echo "real-config" > config.txt
docker build -q -t %s . >/dev/null
`, wrongWorkdir, image)

	buildResult, err := k8s.ExecInPod(ctx, provisioner, namespace, dindPod, "dind", cmd, 60*time.Second)
	if err != nil {
		return Result{}, fmt.Errorf("building broken image: %w", err)
	}
	if buildResult.ExitCode != 0 {
		return Result{}, fmt.Errorf("building broken image failed (exit %d): %s", buildResult.ExitCode, buildResult.Stdout+buildResult.Stderr)
	}

	runResult, err := k8s.ExecInPod(ctx, provisioner, namespace, dindPod, "dind",
		fmt.Sprintf("docker run --rm %s; echo EXIT:$?", image), 20*time.Second)
	if err != nil {
		return Result{}, fmt.Errorf("running broken image: %w", err)
	}

	// The container's own stderr (e.g. `cat`'s "No such file" error)
	// arrives in ExecInPod's Stderr, not Stdout -- Stdout only carries
	// the trailing EXIT:$? echo, confirmed live as a real bug in this
	// handler's own first version.
	combined := runResult.Stdout + runResult.Stderr
	verified := strings.Contains(runResult.Stdout, "EXIT:1") &&
		(strings.Contains(combined, "No such file") || strings.Contains(combined, "not found"))
	return Result{Applied: true, SymptomVerified: verified}, nil
}

// applyDockerNetworkNotAttached: content/faults/f.docker.network-not-attached.yaml
// params: client_container (name for the misattached client),
// correct_network (the network the server sits on, and the client
// should be on but isn't).
//
// Creates a real Docker network + a real server container attached to
// it, then starts the client container WITHOUT --network (landing it on
// the default bridge). Live-verified (this session, outside the cluster
// first): a real connection attempt from the misattached client
// genuinely fails to resolve the server's name (Docker's embedded DNS
// only works within a shared user-defined network), matching the
// fault's real symptom exactly.
func applyDockerNetworkNotAttached(ctx context.Context, provisioner *k8s.Provisioner, namespace string, params map[string]string) (Result, error) {
	clientContainer := params["client_container"]
	correctNetwork := params["correct_network"]
	if clientContainer == "" || correctNetwork == "" {
		return Result{}, fmt.Errorf("f.docker.network-not-attached requires params: client_container, correct_network")
	}

	dindPod, err := k8s.FindPodByLabel(ctx, provisioner, namespace, dindPodLabelSelector)
	if err != nil {
		return Result{}, fmt.Errorf("finding dind pod: %w", err)
	}

	cmd := fmt.Sprintf(`
set -e
docker network create %s >/dev/null 2>&1 || true
docker rm -f practice-net-server %s >/dev/null 2>&1 || true
docker run -d --name practice-net-server --network %s docker.io/library/alpine:latest \
  sh -c 'while true; do { printf "HTTP/1.1 200 OK\r\n\r\nok\n"; } | nc -l -p 8080; done' >/dev/null
sleep 1
docker run -d --name %s docker.io/library/alpine:latest sh -c 'sleep infinity' >/dev/null
`, correctNetwork, clientContainer, correctNetwork, clientContainer)

	setupResult, err := k8s.ExecInPod(ctx, provisioner, namespace, dindPod, "dind", cmd, 30*time.Second)
	if err != nil {
		return Result{}, fmt.Errorf("setting up misattached client: %w", err)
	}
	if setupResult.ExitCode != 0 {
		return Result{}, fmt.Errorf("setting up misattached client failed (exit %d): %s", setupResult.ExitCode, setupResult.Stdout+setupResult.Stderr)
	}

	checkResult, err := k8s.ExecInPod(ctx, provisioner, namespace, dindPod, "dind",
		fmt.Sprintf("docker exec %s wget -q -T 3 -O- http://practice-net-server:8080/ 2>&1; echo EXIT:$?", clientContainer),
		15*time.Second)
	if err != nil {
		return Result{}, fmt.Errorf("checking client connectivity: %w", err)
	}

	verified := !strings.Contains(checkResult.Stdout, "EXIT:0")
	return Result{Applied: true, SymptomVerified: verified}, nil
}

// applyDockerSwarmServiceImagePullFail: content/faults/f.docker.swarm-service-image-pull-fail.yaml
// params: service (must be "practice-swarm-svc", the fixture's real
// converged Swarm service), registry (accepted for the content author's
// own reference; the fixture's real registry always requires auth, so
// this fault always works against it regardless of the string passed).
//
// Updates the fixture's real, converged Swarm service to a fresh image
// on the same authenticated registry, but WITHOUT --with-registry-auth
// (simulating expired/missing credentials). Live-verified (this session,
// both outside and inside the cluster): a real Swarm task genuinely gets
// Rejected with "pull access denied... authorization failed: no basic
// auth credentials" -- the exact real mechanism the fault's
// canonical_diagnostic_path describes.
func applyDockerSwarmServiceImagePullFail(ctx context.Context, provisioner *k8s.Provisioner, namespace string, params map[string]string) (Result, error) {
	service := params["service"]
	if service == "" {
		return Result{}, fmt.Errorf("f.docker.swarm-service-image-pull-fail requires param: service")
	}
	if service != dindSwarmServiceNameConst {
		return Result{}, fmt.Errorf("f.docker.swarm-service-image-pull-fail: service %q does not match the fixture's real service %q", service, dindSwarmServiceNameConst)
	}

	dindPod, err := k8s.FindPodByLabel(ctx, provisioner, namespace, dindPodLabelSelector)
	if err != nil {
		return Result{}, fmt.Errorf("finding dind pod: %w", err)
	}

	// Two real, non-obvious bugs found live and fixed here:
	//
	// 1. Content-addressable caching: tagging the SAME image content
	//    the healthy baseline already uses (nginx:alpine) means the
	//    "broken" tag resolves to an already-locally-present digest --
	//    Swarm never needs to contact the registry at all, even after
	//    the tag is removed. Fixed by using busybox (genuinely
	//    different content, never pulled by this fixture otherwise).
	//
	// 2. Swarm auth caching: this specific service
	//    (dindSwarmServiceNameConst) was deliberately created WITHOUT
	//    --with-registry-auth (see fx.dind-workspace.v1's own doc
	//    comment for why -- a service created WITH it retains working
	//    registry credentials for its own lifetime independent of the
	//    node's docker login/logout state, silently masking this exact
	//    fault). Push the broken-tagged image WITH valid credentials
	//    first (so it genuinely exists in the registry), then remove
	//    the LOCAL image reference before updating the service -- only
	//    then does Swarm have to actually fetch it from the registry
	//    with no cached credentials to fall back on.
	brokenImage := fmt.Sprintf("localhost:%d/practice-swarm-image:broken-fault", dindRegistryPortConst)
	registryHost := fmt.Sprintf("localhost:%d", dindRegistryPortConst)
	cmd := fmt.Sprintf(`
set -e
docker rmi busybox:latest %s --force >/dev/null 2>&1 || true
docker pull docker.io/library/busybox:latest >/dev/null 2>&1
docker tag docker.io/library/busybox:latest %s
docker login %s -u %s -p %s >/dev/null 2>&1
docker push %s >/dev/null 2>&1
docker logout %s >/dev/null 2>&1
docker rmi busybox:latest %s --force >/dev/null 2>&1
docker service update --image %s %s >/dev/null 2>&1 || true
`, brokenImage, brokenImage, registryHost, dindRegistryUserConst, dindRegistryPassConst,
		brokenImage, registryHost, brokenImage, brokenImage, service)

	if _, err := k8s.ExecInPod(ctx, provisioner, namespace, dindPod, "dind", cmd, 30*time.Second); err != nil {
		return Result{}, fmt.Errorf("triggering broken service update: %w", err)
	}

	deadline := time.Now().Add(30 * time.Second)
	var lastOutput string
	for time.Now().Before(deadline) {
		psResult, err := k8s.ExecInPod(ctx, provisioner, namespace, dindPod, "dind",
			fmt.Sprintf("docker service ps --no-trunc %s 2>&1", service), 15*time.Second)
		if err == nil {
			lastOutput = psResult.Stdout
			if strings.Contains(lastOutput, "authorization failed") || strings.Contains(lastOutput, "pull access denied") {
				return Result{Applied: true, SymptomVerified: true}, nil
			}
		}
		time.Sleep(2 * time.Second)
	}
	_ = lastOutput
	return Result{Applied: true, SymptomVerified: false}, nil
}

// applyGitHubActionsSecretNotPassed: content/faults/f.github.actions-secret-not-passed.yaml
// params: workflow (must be "caller-broken.yml", the fixture's real
// caller workflow that omits the secret), missing_secret (accepted for
// the content author's own reference -- the fixture's reusable workflow
// has exactly one required secret, DEPLOY_TOKEN, so the omission always
// targets that one regardless of the string passed).
//
// Runs the fixture's already-laid-out caller-broken.yml (a real
// workflow_call site that never passes secrets: at all) through the
// real nektos/act binary against the fixture's own DinD daemon.
// Live-verified (this session, both outside and inside the cluster): a
// real job container genuinely fails with "AUTH FAILURE: DEPLOY_TOKEN is
// empty" -- while the fixture's own healthy baseline (caller-healthy.yml,
// which DOES pass the secret) already succeeded as part of fixture setup.
func applyGitHubActionsSecretNotPassed(ctx context.Context, provisioner *k8s.Provisioner, namespace string, params map[string]string) (Result, error) {
	workflow := params["workflow"]
	if workflow == "" {
		return Result{}, fmt.Errorf("f.github.actions-secret-not-passed requires param: workflow")
	}
	if workflow != "caller-broken.yml" {
		return Result{}, fmt.Errorf("f.github.actions-secret-not-passed: workflow %q does not match the fixture's real broken caller workflow %q", workflow, "caller-broken.yml")
	}

	dindPod, err := k8s.FindPodByLabel(ctx, provisioner, namespace, dindPodLabelSelector)
	if err != nil {
		return Result{}, fmt.Errorf("finding dind pod: %w", err)
	}

	cmd := fmt.Sprintf(
		"cd /workflows && /usr/local/bin/act push -W .github/workflows/%s -P ubuntu-latest=catthehacker/ubuntu:act-latest --container-architecture linux/arm64 2>&1",
		workflow,
	)
	result, err := k8s.ExecInPod(ctx, provisioner, namespace, dindPod, "dind", cmd, 60*time.Second)
	if err != nil {
		return Result{}, fmt.Errorf("running act against the broken caller workflow: %w", err)
	}

	verified := result.ExitCode != 0 && strings.Contains(result.Stdout, "AUTH FAILURE: DEPLOY_TOKEN is empty")
	return Result{Applied: true, SymptomVerified: verified}, nil
}
