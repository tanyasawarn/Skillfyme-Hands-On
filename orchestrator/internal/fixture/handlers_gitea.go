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
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/client-go/kubernetes"

	"github.com/tanyasawarn/skillfyme-hands-on/orchestrator/internal/k8s"
)

func init() {
	register("fx.gitea-repo.v1", applyGiteaRepo)
	registerChecksum("fx.gitea-repo.v1", "v1")
}

const (
	giteaDeployment       = "practice-gitea"
	giteaRunnerDeployment = "practice-gitea-runner"
	giteaAdminUser        = "admin"
	giteaAdminPass        = "admin-dev-pw-12345" // dev-only, same "not a real secret" stance as this codebase's other fixed fixture credentials
	giteaRepoOwner        = giteaAdminUser
	giteaRepoName         = "practice-repo"
	// giteaProtectedBranch is the fixed branch name
	// f.gitlab.protected-branch-blocks-push's params_schema targets
	// (branch) -- content authors reference this exact name.
	giteaProtectedBranch = "main"
	// giteaLegitCIUser/giteaBlockedCIUser: a real, GitLab-substitute
	// (documented re-scope, same precedent as nektos/act standing in for
	// GitHub Actions) demonstration of the fault's real mechanism --
	// giteaLegitCIUser is on the branch's real push whitelist (the
	// "grant push exception" fix class already applied), giteaBlockedCIUser
	// is a write-collaborator but NOT whitelisted, matching the fault's
	// "forgot to grant the CI token's role an exception" framing.
	giteaLegitCIUser   = "ci-legit"
	giteaLegitCIPass   = "ci-legit-dev-pw-12345"
	giteaBlockedCIUser = "ci-blocked"
	giteaBlockedCIPass = "ci-blocked-dev-pw-12345"
)

// applyGiteaRepo is fx.gitea-repo.v1: a real Gitea instance (documented
// substitute for GitLab -- Gitea's branch-protection API/enforcement
// mechanism is a real, faithful analog of GitLab's "protected branches"
// feature, same "API-compatible substitute, documented as a re-scope"
// precedent as fx.ansible-target.v1 standing in for the plan's original
// generic CI-runner framing) plus a real runner pod (git + wget) that
// drives Gitea's real REST API and real `git push` operations.
//
// Sets up: an admin user + a "practice-repo" with a real initial commit
// on main, TWO real non-admin users with identical repo-level "write"
// collaborator access (giteaLegitCIUser, giteaBlockedCIUser), and a real
// branch-protection rule on main with enable_push_whitelist covering
// ONLY giteaLegitCIUser. Live-verified (this session, outside the
// cluster first): a write-collaborator who is NOT on the push whitelist
// gets genuinely rejected with Gitea's real "Not allowed to push to
// protected branch main" pre-receive-hook error; the whitelisted user's
// identical push succeeds -- this is the exact real mechanism
// f.gitlab.protected-branch-blocks-push's canonical_diagnostic_path
// describes, not a synthetic stand-in.
func applyGiteaRepo(ctx context.Context, provisioner *k8s.Provisioner, envID, namespace string) error {
	clientset := provisioner.Clientset()

	if err := ensureGiteaDeployment(ctx, clientset, namespace); err != nil {
		return fmt.Errorf("ensuring gitea deployment: %w", err)
	}
	if err := ensureGiteaRunnerDeployment(ctx, clientset, namespace); err != nil {
		return fmt.Errorf("ensuring gitea runner deployment: %w", err)
	}

	giteaPod, err := k8s.WaitForPodByLabel(ctx, provisioner, namespace, "app="+giteaDeployment, 120*time.Second)
	if err != nil {
		return fmt.Errorf("waiting for gitea pod: %w", err)
	}
	runnerPod, err := k8s.WaitForPodByLabel(ctx, provisioner, namespace, "app="+giteaRunnerDeployment, 90*time.Second)
	if err != nil {
		return fmt.Errorf("waiting for gitea runner pod: %w", err)
	}

	if err := ensureGiteaUsersAndRepo(ctx, provisioner, namespace, giteaPod); err != nil {
		return fmt.Errorf("ensuring gitea users/repo: %w", err)
	}
	if err := ensureGiteaRunnerAPITokens(ctx, provisioner, namespace, runnerPod); err != nil {
		return fmt.Errorf("ensuring gitea API tokens: %w", err)
	}
	if err := ensureGiteaBranchProtection(ctx, provisioner, namespace, runnerPod); err != nil {
		return fmt.Errorf("ensuring gitea branch protection: %w", err)
	}
	if err := ensureGiteaHealthyBaselinePush(ctx, provisioner, namespace, runnerPod); err != nil {
		return fmt.Errorf("verifying healthy baseline push: %w", err)
	}

	return nil
}

func ensureGiteaDeployment(ctx context.Context, clientset *kubernetes.Clientset, namespace string) error {
	replicas := int32(1)

	deployment := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: giteaDeployment, Namespace: namespace},
		Spec: appsv1.DeploymentSpec{
			Replicas: &replicas,
			Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": giteaDeployment}},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"app": giteaDeployment}},
				Spec: corev1.PodSpec{
					// Gitea's own s6-overlay init needs to start as root to
					// manage its data directory ownership/permissions --
					// same documented, scoped PodSecurity "restricted"
					// exception as fx.ansible-target.v1's openssh-server
					// image and fx.jenkins-basic.v1's controller.
					Volumes: []corev1.Volume{
						{Name: "data", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}},
					},
					Containers: []corev1.Container{
						{
							Name:  "gitea",
							Image: "docker.io/gitea/gitea:latest",
							Env: []corev1.EnvVar{
								{Name: "GITEA__security__INSTALL_LOCK", Value: "true"},
								{Name: "GITEA__database__DB_TYPE", Value: "sqlite3"},
								{Name: "GITEA__server__DISABLE_SSH", Value: "true"},
								{Name: "GITEA__server__ROOT_URL", Value: fmt.Sprintf("http://%s:3000/", giteaDeployment)},
							},
							Ports: []corev1.ContainerPort{{ContainerPort: 3000}},
							VolumeMounts: []corev1.VolumeMount{
								{Name: "data", MountPath: "/data"},
							},
							Resources: corev1.ResourceRequirements{
								Requests: corev1.ResourceList{corev1.ResourceMemory: resource.MustParse("256Mi")},
								Limits:   corev1.ResourceList{corev1.ResourceMemory: resource.MustParse("512Mi")},
							},
							ReadinessProbe: &corev1.Probe{
								ProbeHandler: corev1.ProbeHandler{
									HTTPGet: &corev1.HTTPGetAction{Path: "/api/healthz", Port: intstr.FromInt32(3000)},
								},
								InitialDelaySeconds: 5,
								PeriodSeconds:       3,
								FailureThreshold:    30,
							},
						},
					},
				},
			},
		},
	}
	if _, err := clientset.AppsV1().Deployments(namespace).Create(ctx, deployment, metav1.CreateOptions{}); err != nil && !apierrors.IsAlreadyExists(err) {
		return fmt.Errorf("creating gitea deployment: %w", err)
	}

	svc := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: giteaDeployment, Namespace: namespace},
		Spec: corev1.ServiceSpec{
			Selector: map[string]string{"app": giteaDeployment},
			Ports:    []corev1.ServicePort{{Port: 3000, TargetPort: intstr.FromInt32(3000)}},
		},
	}
	if _, err := clientset.CoreV1().Services(namespace).Create(ctx, svc, metav1.CreateOptions{}); err != nil && !apierrors.IsAlreadyExists(err) {
		return fmt.Errorf("creating gitea service: %w", err)
	}
	return nil
}

// ensureGiteaRunnerDeployment: alpine/git ships git + busybox wget, but
// busybox wget has no PUT-method support at all (confirmed live this
// session, same gap the Terraform fixture's MinIO bucket creation hit) --
// Gitea's collaborator-add endpoint is PUT-only, so this fixture needs a
// real curl. `apk add curl` needs root (confirmed live: apk's own lock/
// log files are root-owned even for a non-root UID), so this container
// runs as root at container-start ONLY to install curl synchronously
// (same documented, scoped PodSecurity "restricted" exception and same
// synchronous-not-backgrounded install pattern as
// fx.ansible-target.v1's `apk add python3`, avoiding the exact same
// install-vs-readiness race already found and fixed there) -- the actual
// git/curl operations this fixture and its fault handler run afterward
// don't themselves need root, but the container's own SecurityContext
// applies for its whole lifetime, so this is accepted the same way
// Jenkins' controller and the Ansible SSH target already are.
func ensureGiteaRunnerDeployment(ctx context.Context, clientset *kubernetes.Clientset, namespace string) error {
	replicas := int32(1)

	deployment := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: giteaRunnerDeployment, Namespace: namespace},
		Spec: appsv1.DeploymentSpec{
			Replicas: &replicas,
			Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": giteaRunnerDeployment}},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"app": giteaRunnerDeployment}},
				Spec: corev1.PodSpec{
					Volumes: []corev1.Volume{
						{Name: "work", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}},
					},
					Containers: []corev1.Container{
						{
							Name:    "git",
							Image:   "docker.io/alpine/git:latest",
							Command: []string{"sh", "-c", "apk add --no-cache curl >/tmp/apk-install.log 2>&1 && sleep infinity"},
							Env: []corev1.EnvVar{
								// apk needs dl-cdn.alpinelinux.org through
								// the real T1/T2 egress-proxy allowlist --
								// same fix as the Ansible fixture's apk
								// access, confirmed live this session that
								// omitting it here left this container
								// crash-looping (apk add silently unable to
								// resolve DNS under the real default-deny
								// policy, readiness probe never passing).
								{Name: "HTTP_PROXY", Value: k8s.EgressProxyURL},
								{Name: "HTTPS_PROXY", Value: k8s.EgressProxyURL},
								{Name: "http_proxy", Value: k8s.EgressProxyURL},
								{Name: "https_proxy", Value: k8s.EgressProxyURL},
								// NO_PROXY is REQUIRED, not optional -- same
								// real bug already found and fixed in the
								// Terraform fixture's runner pod: without
								// it, curl/git route traffic to this
								// fixture's OWN in-namespace Gitea service
								// through Squid instead of directly, and
								// Squid's real deny-all-by-default response
								// surfaces as a confusing 403, not a clean
								// connection.
								{Name: "NO_PROXY", Value: giteaDeployment + ",.svc.cluster.local,.svc"},
								{Name: "no_proxy", Value: giteaDeployment + ",.svc.cluster.local,.svc"},
							},
							VolumeMounts: []corev1.VolumeMount{
								{Name: "work", MountPath: "/work"},
							},
							Resources: corev1.ResourceRequirements{
								Requests: corev1.ResourceList{corev1.ResourceMemory: resource.MustParse("64Mi")},
								Limits:   corev1.ResourceList{corev1.ResourceMemory: resource.MustParse("128Mi")},
							},
							ReadinessProbe: &corev1.Probe{
								ProbeHandler: corev1.ProbeHandler{
									Exec: &corev1.ExecAction{Command: []string{"sh", "-c", "which curl"}},
								},
								InitialDelaySeconds: 2,
								PeriodSeconds:       2,
								FailureThreshold:    30,
							},
						},
					},
				},
			},
		},
	}
	if _, err := clientset.AppsV1().Deployments(namespace).Create(ctx, deployment, metav1.CreateOptions{}); err != nil && !apierrors.IsAlreadyExists(err) {
		return fmt.Errorf("creating gitea runner deployment: %w", err)
	}
	return nil
}

// ensureGiteaUsersAndRepo runs Gitea's own `gitea admin` CLI inside the
// Gitea pod itself, via `su-exec git` -- confirmed live this session
// that Gitea's binary hard-refuses to run ANY subcommand as root
// ("Gitea is not supposed to be run as root"), and k8s exec's own
// default user for this pod is root (0) regardless of the image's
// internal entrypoint eventually dropping the SERVER process to git via
// s6, so every exec into this pod must explicitly su-exec to git itself
// -- to create the admin user, the two CI users
// (--must-change-password=false is REQUIRED, confirmed live: without
// it, Gitea forces a password-change flow on first API/git use that
// would surface as a confusing, unrelated 401 rather than the fault's
// real branch-protection rejection), and the initial repo with an
// auto-initialized main branch.
func ensureGiteaUsersAndRepo(ctx context.Context, provisioner *k8s.Provisioner, namespace, giteaPod string) error {
	checkCmd := "su-exec git gitea admin user list 2>/dev/null | grep -q " + giteaAdminUser + " && echo EXISTS || echo MISSING"
	checkResult, err := k8s.ExecInPod(ctx, provisioner, namespace, giteaPod, "gitea", checkCmd, 15*time.Second)
	if err == nil && checkResult.Stdout == "EXISTS" {
		return nil
	}

	users := []struct {
		name, pass string
		admin      bool
	}{
		{giteaAdminUser, giteaAdminPass, true},
		{giteaLegitCIUser, giteaLegitCIPass, false},
		{giteaBlockedCIUser, giteaBlockedCIPass, false},
	}
	for _, u := range users {
		adminFlag := ""
		if u.admin {
			adminFlag = "--admin"
		}
		cmd := fmt.Sprintf(
			"su-exec git gitea admin user create --username %s --password %s --email %s@example.com --must-change-password=false %s",
			u.name, u.pass, u.name, adminFlag,
		)
		result, err := k8s.ExecInPod(ctx, provisioner, namespace, giteaPod, "gitea", cmd, 20*time.Second)
		if err != nil {
			return fmt.Errorf("creating user %s: %w", u.name, err)
		}
		if result.ExitCode != 0 {
			return fmt.Errorf("creating user %s failed (exit %d): %s", u.name, result.ExitCode, result.Stdout+result.Stderr)
		}
	}
	return nil
}

// ensureGiteaRunnerAPITokens/ensureGiteaBranchProtection/
// ensureGiteaHealthyBaselinePush all run from the runner pod against
// Gitea's real REST API (via curl -- busybox wget has no PUT support at
// all, needed for the collaborator-add endpoint, confirmed live this
// session) and real `git` operations, establishing exactly the setup
// live-verified outside the cluster: admin creates the repo, both CI
// users get "write" collaborator access, a branch-protection rule on
// main whitelists ONLY giteaLegitCIUser for push, and a real git push
// from giteaLegitCIUser succeeds as the healthy baseline
// (giteaBlockedCIUser's push is the fault itself, in faultinjection's
// own handler).
func ensureGiteaRunnerAPITokens(ctx context.Context, provisioner *k8s.Provisioner, namespace, runnerPod string) error {
	giteaURL := fmt.Sprintf("http://%s:3000", giteaDeployment)

	checkCmd := "test -f /work/.repo-created && echo EXISTS || echo MISSING"
	checkResult, err := k8s.ExecInPod(ctx, provisioner, namespace, runnerPod, "git", checkCmd, 10*time.Second)
	if err == nil && checkResult.Stdout == "EXISTS" {
		return nil
	}

	createRepoCmd := fmt.Sprintf(
		`curl -sf -u %s:%s -H "Content-Type: application/json" -d '{"name":"%s","auto_init":true,"default_branch":"%s"}' %s/api/v1/user/repos && touch /work/.repo-created`,
		giteaAdminUser, giteaAdminPass, giteaRepoName, giteaProtectedBranch, giteaURL,
	)
	result, err := k8s.ExecInPod(ctx, provisioner, namespace, runnerPod, "git", createRepoCmd, 20*time.Second)
	if err != nil {
		return fmt.Errorf("creating repo via API: %w", err)
	}
	if result.ExitCode != 0 {
		return fmt.Errorf("creating repo via API failed (exit %d): %s", result.ExitCode, result.Stdout+result.Stderr)
	}

	for _, ciUser := range []string{giteaLegitCIUser, giteaBlockedCIUser} {
		collabCmd := fmt.Sprintf(
			`curl -sf -u %s:%s -X PUT -H "Content-Type: application/json" -d '{"permission":"write"}' %s/api/v1/repos/%s/%s/collaborators/%s`,
			giteaAdminUser, giteaAdminPass, giteaURL, giteaRepoOwner, giteaRepoName, ciUser,
		)
		if result, err := k8s.ExecInPod(ctx, provisioner, namespace, runnerPod, "git", collabCmd, 15*time.Second); err != nil || result.ExitCode != 0 {
			return fmt.Errorf("adding collaborator %s failed: err=%v result=%+v", ciUser, err, result)
		}
	}
	return nil
}

func ensureGiteaBranchProtection(ctx context.Context, provisioner *k8s.Provisioner, namespace, runnerPod string) error {
	giteaURL := fmt.Sprintf("http://%s:3000", giteaDeployment)

	checkCmd := fmt.Sprintf(`curl -sf -u %s:%s %s/api/v1/repos/%s/%s/branch_protections/%s -o /dev/null -w '%%{http_code}'`,
		giteaAdminUser, giteaAdminPass, giteaURL, giteaRepoOwner, giteaRepoName, giteaProtectedBranch)
	checkResult, err := k8s.ExecInPod(ctx, provisioner, namespace, runnerPod, "git", checkCmd, 15*time.Second)
	if err == nil && checkResult.Stdout == "200" {
		return nil
	}

	protectCmd := fmt.Sprintf(
		`curl -sf -u %s:%s -H "Content-Type: application/json" -d '{"branch_name":"%s","enable_push":true,"enable_push_whitelist":true,"push_whitelist_usernames":["%s"]}' %s/api/v1/repos/%s/%s/branch_protections`,
		giteaAdminUser, giteaAdminPass, giteaProtectedBranch, giteaLegitCIUser, giteaURL, giteaRepoOwner, giteaRepoName,
	)
	result, err := k8s.ExecInPod(ctx, provisioner, namespace, runnerPod, "git", protectCmd, 20*time.Second)
	if err != nil {
		return fmt.Errorf("creating branch protection: %w", err)
	}
	if result.ExitCode != 0 {
		return fmt.Errorf("creating branch protection failed (exit %d): %s", result.ExitCode, result.Stdout+result.Stderr)
	}
	return nil
}

func ensureGiteaHealthyBaselinePush(ctx context.Context, provisioner *k8s.Provisioner, namespace, runnerPod string) error {
	cmd := fmt.Sprintf(`
set -e
rm -rf /work/baseline-check
mkdir -p /work/baseline-check
cd /work/baseline-check
git clone -q http://%s:%s@%s:3000/%s/%s.git . 2>&1
git config user.email "%s@example.com"
git config user.name "%s"
echo "baseline-check-$(date +%%s)" >> baseline.txt
git add baseline.txt
git commit -q -m "chore: baseline push check"
git push origin %s 2>&1
`, giteaLegitCIUser, giteaLegitCIPass, giteaDeployment, giteaRepoOwner, giteaRepoName,
		giteaLegitCIUser, giteaLegitCIUser, giteaProtectedBranch)

	result, err := k8s.ExecInPod(ctx, provisioner, namespace, runnerPod, "git", cmd, 30*time.Second)
	if err != nil {
		return fmt.Errorf("healthy-baseline push: %w", err)
	}
	if result.ExitCode != 0 {
		return fmt.Errorf("healthy-baseline push (whitelisted user) failed (exit %d): %s -- expected the whitelisted ci-legit user to push successfully", result.ExitCode, result.Stdout+result.Stderr)
	}
	return nil
}
