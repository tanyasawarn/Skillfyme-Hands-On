package fixture

import (
	"context"
	"fmt"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"

	"github.com/tanyasawarn/skillfyme-hands-on/orchestrator/internal/k8s"
)

func init() {
	register("fx.dind-workspace.v1", applyDinDWorkspace)
	registerChecksum("fx.dind-workspace.v1", "v1")
}

const (
	dindDeployment = "practice-dind"
	// dindRegistryContainerName/dindRegistryPort/dindRegistryUser/
	// dindRegistryPass: an htpasswd-authenticated real `registry:2`
	// instance run AS A CONTAINER INSIDE the DinD daemon itself (not a
	// separate K8s pod) -- f.docker.swarm-service-image-pull-fail needs
	// a registry that genuinely REQUIRES credentials to reproduce a real
	// auth-denial pull failure, live-verified (this session, outside the
	// cluster first) with the real error "pull access denied... no
	// basic auth credentials", not a synthetic stand-in.
	dindRegistryContainerName = "practice-swarm-registry"
	dindRegistryPort          = 5555
	dindRegistryUser          = "ciuser"
	dindRegistryPass          = "cipass-dev-12345"
	// dindSwarmServiceName/dindSwarmImage are the fixed names
	// f.docker.swarm-service-image-pull-fail's params_schema targets
	// (service) -- content authors reference this exact name.
	dindSwarmServiceName = "practice-swarm-svc"
)

// applyDinDWorkspace is fx.dind-workspace.v1: a single real Docker-in-
// Docker daemon (docker:dind, confirmed live this session to work on
// this cluster: privileged pods, nested `docker run`, `docker swarm`,
// and `docker service` all function correctly) backing FOUR real Docker/
// Swarm faults that all need genuine Docker Engine behavior no fake
// client can reproduce: f.docker.dockerfile-wrong-workdir (a real
// `docker build`+`docker run`), f.docker.network-not-attached (real
// Docker bridge-network DNS isolation), f.docker.swarm-service-image-
// pull-fail (a real single-node Swarm + an htpasswd-authenticated
// registry), and f.github.actions-secret-not-passed (nektos/act running
// real job containers via this same daemon -- act has no official
// container image, only raw platform binaries, downloaded at
// fixture-apply time; it needs Docker socket access to run job
// containers at all, so it structurally requires the same privileged
// DinD mechanism as the Docker faults, not the lower-risk plain-tool-
// binary pattern this codebase's other fixtures use).
//
// Privileged, matching T2's own documented threat-model reasoning
// (internal/k8s/provision.go's createNamespace doc comment): a
// container's capability set is the wrong control surface once genuine
// nested-Docker capability is required, same as T2's own DinD/systemd
// pods. This fixture's own namespace does NOT get the "restricted" PSS
// label for the same reason T2 namespaces don't -- deliberate, scoped,
// documented, not an oversight.
func applyDinDWorkspace(ctx context.Context, provisioner *k8s.Provisioner, envID, namespace string) error {
	clientset := provisioner.Clientset()

	if err := ensureDinDDeployment(ctx, clientset, namespace); err != nil {
		return fmt.Errorf("ensuring dind deployment: %w", err)
	}

	dindPod, err := k8s.WaitForPodByLabel(ctx, provisioner, namespace, "app="+dindDeployment, 120*time.Second)
	if err != nil {
		return fmt.Errorf("waiting for dind pod: %w", err)
	}

	if err := ensureDockerDaemonReady(ctx, provisioner, namespace, dindPod); err != nil {
		return fmt.Errorf("waiting for docker daemon: %w", err)
	}
	if err := ensureSwarmRegistryAndService(ctx, provisioner, namespace, dindPod); err != nil {
		return fmt.Errorf("ensuring swarm registry/service baseline: %w", err)
	}
	if err := ensureActBinary(ctx, provisioner, namespace, dindPod); err != nil {
		return fmt.Errorf("ensuring act binary: %w", err)
	}

	return nil
}

func ensureDinDDeployment(ctx context.Context, clientset *kubernetes.Clientset, namespace string) error {
	replicas := int32(1)
	privileged := true

	deployment := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: dindDeployment, Namespace: namespace},
		Spec: appsv1.DeploymentSpec{
			Replicas: &replicas,
			Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": dindDeployment}},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"app": dindDeployment}},
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{
						{
							Name:  "dind",
							Image: "docker.io/library/docker:dind",
							Env: []corev1.EnvVar{
								// TLS disabled: this pod is not
								// externally reachable (no Service
								// exposing it beyond the runner exec
								// itself), matching this codebase's
								// existing "dev-only, in-namespace only"
								// posture for other fixtures' internal
								// plumbing.
								{Name: "DOCKER_TLS_CERTDIR", Value: ""},
								// The NESTED dockerd genuinely reads these
								// container env vars itself (confirmed
								// live this session: an invalid proxy
								// value changed docker pull's own error
								// from "connection refused" to a real
								// proxyconnect DNS-lookup failure,
								// proving dockerd picked it up) -- without
								// this, every docker pull/build AND apk
								// add (registry-1.docker.io,
								// dl-cdn.alpinelinux.org) is blocked
								// outright by the real default-deny
								// policy, same class of bug already found
								// and fixed in every other internet-
								// needing fixture this session.
								{Name: "HTTP_PROXY", Value: k8s.EgressProxyURL},
								{Name: "HTTPS_PROXY", Value: k8s.EgressProxyURL},
								{Name: "http_proxy", Value: k8s.EgressProxyURL},
								{Name: "https_proxy", Value: k8s.EgressProxyURL},
								// NO_PROXY excludes this fixture's OWN
								// in-pod registry (localhost:5555) and
								// localhost generally -- without it,
								// docker push/pull against the fixture's
								// own registry would incorrectly route
								// through Squid too (same bug class as
								// the Terraform/Gitea fixtures' NO_PROXY
								// fix).
								{Name: "NO_PROXY", Value: "localhost,127.0.0.1"},
								{Name: "no_proxy", Value: "localhost,127.0.0.1"},
							},
							SecurityContext: &corev1.SecurityContext{
								Privileged: &privileged,
							},
							Resources: corev1.ResourceRequirements{
								Requests: corev1.ResourceList{corev1.ResourceMemory: resource.MustParse("512Mi")},
								Limits:   corev1.ResourceList{corev1.ResourceMemory: resource.MustParse("2Gi")},
							},
						},
					},
				},
			},
		},
	}
	if _, err := clientset.AppsV1().Deployments(namespace).Create(ctx, deployment, metav1.CreateOptions{}); err != nil && !apierrors.IsAlreadyExists(err) {
		return fmt.Errorf("creating dind deployment: %w", err)
	}
	return nil
}

// ensureDockerDaemonReady polls `docker version` inside the DinD pod --
// no built-in readiness probe is used here (docker:dind's own startup
// takes a variable amount of time to actually accept connections after
// the pod reports Ready at the K8s level, confirmed live), so this
// polls the real daemon directly rather than trusting pod Ready alone.
func ensureDockerDaemonReady(ctx context.Context, provisioner *k8s.Provisioner, namespace, dindPod string) error {
	deadline := time.Now().Add(90 * time.Second)
	for time.Now().Before(deadline) {
		result, err := k8s.ExecInPod(ctx, provisioner, namespace, dindPod, "dind", "docker version >/dev/null 2>&1", 10*time.Second)
		if err == nil && result.ExitCode == 0 {
			return nil
		}
		time.Sleep(3 * time.Second)
	}
	return fmt.Errorf("docker daemon did not become ready within 90s")
}

// ensureSwarmRegistryAndService: real `docker swarm init`, a real
// htpasswd-authenticated `registry:2` container run INSIDE the DinD
// daemon (not a separate K8s pod -- this fixture's whole footprint is
// one privileged pod, matching the "single privileged workspace"
// threat-model framing rather than growing a second privileged
// surface), and a real Swarm service seeded from an image ALREADY
// PULLED LOCALLY (via a real, credentialed docker pull -- NOT via
// `docker service create --with-registry-auth`) -- establishing the
// healthy, converged baseline f.docker.swarm-service-image-pull-fail's
// handler later breaks by updating to the same registry's image WITHOUT
// credentials.
//
// Deliberately avoids --with-registry-auth here: confirmed live, as a
// real and non-obvious bug, that Swarm's manager retains/caches the
// registry credentials a service was ORIGINALLY created with for that
// service's whole lifetime, independent of the node's own `docker
// login`/`docker logout` state -- so a service created with
// --with-registry-auth kept successfully pulling on every LATER
// `docker service update`, even after a real `docker logout`, silently
// masking the fault this fixture exists to support. A brand-new service
// (never given --with-registry-auth) does NOT have this problem and
// genuinely re-checks credentials on every image change, confirmed live
// with the real "pull access denied... no basic auth credentials"
// error. Creating the service against an image ALREADY resolved
// locally (this pull happened with real credentials moments earlier)
// sidesteps needing --with-registry-auth for the healthy baseline at
// all.
func ensureSwarmRegistryAndService(ctx context.Context, provisioner *k8s.Provisioner, namespace, dindPod string) error {
	checkCmd := "docker service inspect " + dindSwarmServiceName + " >/dev/null 2>&1 && echo EXISTS || echo MISSING"
	checkResult, err := k8s.ExecInPod(ctx, provisioner, namespace, dindPod, "dind", checkCmd, 15*time.Second)
	if err == nil && checkResult.Stdout == "EXISTS" {
		return nil
	}

	setupCmd := fmt.Sprintf(`
set -e
docker swarm init >/dev/null 2>&1 || true
apk add --no-cache apache2-utils >/dev/null 2>&1
mkdir -p /auth
htpasswd -Bbn %s %s > /auth/htpasswd
docker run -d --name %s -p %d:5000 \
  -v /auth:/auth \
  -e REGISTRY_AUTH=htpasswd \
  -e REGISTRY_AUTH_HTPASSWD_REALM="Registry Realm" \
  -e REGISTRY_AUTH_HTPASSWD_PATH=/auth/htpasswd \
  docker.io/library/registry:2 >/dev/null 2>&1
for i in $(seq 1 20); do
  wget -q -O /dev/null http://localhost:%d/v2/ 2>/dev/null && break
  sleep 1
done
docker pull docker.io/library/nginx:alpine >/dev/null 2>&1
docker tag docker.io/library/nginx:alpine localhost:%d/practice-swarm-image:latest
docker login localhost:%d -u %s -p %s >/dev/null 2>&1
docker push localhost:%d/practice-swarm-image:latest >/dev/null 2>&1
docker logout localhost:%d >/dev/null 2>&1
docker service create --name %s --replicas 2 localhost:%d/practice-swarm-image:latest >/dev/null 2>&1
`, dindRegistryUser, dindRegistryPass, dindRegistryContainerName, dindRegistryPort,
		dindRegistryPort, dindRegistryPort, dindRegistryPort, dindRegistryUser, dindRegistryPass, dindRegistryPort,
		dindRegistryPort, dindSwarmServiceName, dindRegistryPort)

	result, err := k8s.ExecInPod(ctx, provisioner, namespace, dindPod, "dind", setupCmd, 120*time.Second)
	if err != nil {
		return fmt.Errorf("setting up swarm registry/service: %w", err)
	}
	if result.ExitCode != 0 {
		return fmt.Errorf("setting up swarm registry/service failed (exit %d): %s", result.ExitCode, result.Stdout+result.Stderr)
	}

	deadline := time.Now().Add(60 * time.Second)
	for time.Now().Before(deadline) {
		psResult, err := k8s.ExecInPod(ctx, provisioner, namespace, dindPod, "dind",
			fmt.Sprintf("docker service ps %s --filter desired-state=running --format '{{.CurrentState}}' 2>&1 | grep -c Running", dindSwarmServiceName),
			10*time.Second)
		if err == nil && psResult.Stdout == "2" {
			return nil
		}
		time.Sleep(3 * time.Second)
	}
	return fmt.Errorf("swarm service did not converge to 2 running replicas within 60s")
}

// ensureActBinary downloads the real nektos/act Linux arm64 binary
// (confirmed live this session to work: v0.2.89, no official container
// image exists for act itself -- only raw GitHub Releases platform
// binaries) into the DinD pod, alongside a real reusable-workflow +
// caller-workflow pair matching f.github.actions-secret-not-passed's
// exact mechanism (a workflow_call job requiring a secret; the caller
// either passes it or doesn't). Runs the healthy-baseline case (secret
// passed) to confirm act itself genuinely works end-to-end against this
// daemon before the fault handler ever runs the broken variant.
//
// Uses curl (installed here), not busybox wget -- confirmed live this
// session that GitHub Releases downloads now 302-redirect to
// release-assets.githubusercontent.com (a newer domain than
// objects.githubusercontent.com, already allow-listed for other
// purposes; added to the Squid allowlist as part of this fixture's own
// build) and busybox wget's handling of that HTTPS-redirect-through-
// proxy combination is unreliable ("Resource temporarily unavailable"/
// "Invalid seek" with no useful diagnostic), where curl handles the same
// request cleanly.
func ensureActBinary(ctx context.Context, provisioner *k8s.Provisioner, namespace, dindPod string) error {
	checkCmd := "test -x /usr/local/bin/act && echo EXISTS || echo MISSING"
	checkResult, err := k8s.ExecInPod(ctx, provisioner, namespace, dindPod, "dind", checkCmd, 10*time.Second)
	if err == nil && checkResult.Stdout == "EXISTS" {
		return nil
	}

	setupCmd := `
set -e
apk add --no-cache curl >/dev/null 2>&1
curl -sL https://github.com/nektos/act/releases/download/v0.2.89/act_Linux_arm64.tar.gz -o /tmp/act.tar.gz
mkdir -p /tmp/act-extract
tar -xzf /tmp/act.tar.gz -C /tmp/act-extract
mv /tmp/act-extract/act /usr/local/bin/act
chmod +x /usr/local/bin/act
mkdir -p /workflows/.github/workflows
cat > /workflows/.github/workflows/reusable.yml <<'WFEOF'
name: reusable
on:
  workflow_call:
    secrets:
      DEPLOY_TOKEN:
        required: true
jobs:
  deploy:
    runs-on: ubuntu-latest
    steps:
      - name: Use secret
        run: |
          if [ -z "${{ secrets.DEPLOY_TOKEN }}" ]; then
            echo "AUTH FAILURE: DEPLOY_TOKEN is empty" >&2
            exit 1
          fi
          echo "deploying with token"
WFEOF
cat > /workflows/.github/workflows/caller-healthy.yml <<'WFEOF'
name: caller-healthy
on: push
jobs:
  call-reusable:
    uses: ./.github/workflows/reusable.yml
    secrets:
      DEPLOY_TOKEN: real-token-value
WFEOF
cat > /workflows/.github/workflows/caller-broken.yml <<'WFEOF'
name: caller-broken
on: push
jobs:
  call-reusable:
    uses: ./.github/workflows/reusable.yml
WFEOF
cd /workflows && /usr/local/bin/act push -W .github/workflows/caller-healthy.yml -P ubuntu-latest=catthehacker/ubuntu:act-latest --container-architecture linux/arm64 2>&1
`
	result, err := k8s.ExecInPod(ctx, provisioner, namespace, dindPod, "dind", setupCmd, 180*time.Second)
	if err != nil {
		return fmt.Errorf("setting up act/workflows: %w", err)
	}
	if result.ExitCode != 0 {
		return fmt.Errorf("act healthy-baseline run failed (exit %d): %s", result.ExitCode, result.Stdout+result.Stderr)
	}
	return nil
}
