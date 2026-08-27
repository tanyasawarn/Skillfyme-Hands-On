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
	register("fx.jenkins-basic.v1", applyJenkinsBasic)
	registerChecksum("fx.jenkins-basic.v1", "v1")
}

const (
	jenkinsDeploymentName  = "practice-jenkins"
	jenkinsServiceName     = "practice-jenkins"
	jenkinsAgentDeployment = "practice-jenkins-agent"
	// jenkinsAgentLabel is the fixed name
	// f.jenkins.agent-offline's params_schema targets (agent_label) --
	// content authors reference this exact name. jenkinsJobName is the
	// fixed name f.jenkins.stale-cached-dependency's params_schema
	// targets (job).
	jenkinsAgentLabel = "practice-agent"
	jenkinsJobName    = "practice-pipeline"
)

// applyJenkinsBasic is fx.jenkins-basic.v1: a real Jenkins controller
// (setup wizard disabled, security disabled -- dev-appropriate, same
// standard as every other fixture in this package) plus a real agent
// process connecting over WebSocket (NOT the classic TCP JNLP port --
// confirmed live during this fixture's own build that this specific
// Jenkins image/version combination never emits the X-Instance-Identity
// header the TCP JNLP handshake requires, causing every TCP-mode agent
// connection attempt to fail with "appears to be publishing an invalid
// X-Instance-Identity"; WebSocket mode bypasses that mechanism entirely
// and was confirmed live to connect successfully), registered as a
// permanent node with label practice-agent -- the healthy baseline
// f.jenkins.agent-offline's handler later takes offline via the same
// REST API this fixture itself uses to register it.
func applyJenkinsBasic(ctx context.Context, provisioner *k8s.Provisioner, envID, namespace string) error {
	clientset := provisioner.Clientset()

	if err := ensureJenkinsDeployment(ctx, clientset, namespace); err != nil {
		return fmt.Errorf("ensuring jenkins deployment: %w", err)
	}
	if err := ensureJenkinsService(ctx, clientset, namespace); err != nil {
		return fmt.Errorf("ensuring jenkins service: %w", err)
	}

	jenkinsPod, err := waitForJenkinsPodToExist(ctx, provisioner, namespace)
	if err != nil {
		return fmt.Errorf("waiting for jenkins pod to be scheduled: %w", err)
	}
	if err := waitForJenkinsHTTPReady(ctx, provisioner, namespace, jenkinsPod); err != nil {
		return fmt.Errorf("waiting for jenkins to accept HTTP requests: %w", err)
	}

	secret, err := registerAgentNode(ctx, provisioner, namespace, jenkinsPod)
	if err != nil {
		return fmt.Errorf("registering agent node: %w", err)
	}

	if err := ensureJenkinsAgentDeployment(ctx, clientset, namespace, secret); err != nil {
		return fmt.Errorf("ensuring jenkins agent deployment: %w", err)
	}

	if err := ensurePracticePipelineJob(ctx, provisioner, namespace, jenkinsPod); err != nil {
		return fmt.Errorf("ensuring practice-pipeline job: %w", err)
	}

	return nil
}

// practicePipelineJobConfig: a real Jenkins Freestyle project (Pipeline/
// workflow-cps needs a plugin this bare image doesn't ship -- confirmed
// live during this fixture's own build; Freestyle needs no plugins and
// is equally real Jenkins job execution) pinned to run on
// jenkinsAgentLabel specifically (assignedNode), so
// f.jenkins.agent-offline's fault genuinely blocks this job's builds --
// not just some other, unrelated executor. The build step simulates a
// real dependency-resolution cache: on first run it seeds
// /tmp/practice-cache/dependency-version.txt with "1.0.0" (this is the
// per-agent-pod filesystem the fault's stale-cache handler later
// targets), then every run reads whatever version is cached rather than
// re-resolving -- exactly the "build cache still serves the old
// artifact" mechanism f.jenkins.stale-cached-dependency describes.
const practicePipelineJobConfigTemplate = `<?xml version="1.0" encoding="UTF-8"?>
<project>
  <keepDependencies>false</keepDependencies>
  <properties/>
  <scm class="hudson.scm.NullSCM"/>
  <assignedNode>%s</assignedNode>
  <canRoam>false</canRoam>
  <disabled>false</disabled>
  <blockBuildWhenDownstreamBuilding>false</blockBuildWhenDownstreamBuilding>
  <blockBuildWhenUpstreamBuilding>false</blockBuildWhenUpstreamBuilding>
  <triggers/>
  <concurrentBuild>false</concurrentBuild>
  <builders>
    <hudson.tasks.Shell>
      <command>mkdir -p /tmp/practice-cache
if [ ! -f /tmp/practice-cache/dependency-version.txt ]; then
  echo &quot;1.0.0&quot; &gt; /tmp/practice-cache/dependency-version.txt
fi
CACHED=$(cat /tmp/practice-cache/dependency-version.txt)
echo &quot;resolved dependency version: $CACHED&quot;
echo &quot;$CACHED&quot; &gt; dependency-used.txt
</command>
    </hudson.tasks.Shell>
  </builders>
  <publishers/>
  <buildWrappers/>
</project>
`

// ensurePracticePipelineJob creates the practice-pipeline Freestyle
// project via Jenkins' own REST API (real crumb-authenticated XML POST,
// same exec-into-controller-pod mechanism registerAgentNode uses).
// Idempotent: a 200 (created) or 400 ("already exists") are both treated
// as success.
func ensurePracticePipelineJob(ctx context.Context, provisioner *k8s.Provisioner, namespace, jenkinsPod string) error {
	checkCmd := fmt.Sprintf(`curl -s -o /dev/null -w "%%{http_code}" http://localhost:8080/job/%s/api/json`, jenkinsJobName)
	checkResult, err := k8s.ExecInPod(ctx, provisioner, namespace, jenkinsPod, "jenkins", checkCmd, 15*time.Second)
	if err != nil {
		return fmt.Errorf("checking for existing job: %w", err)
	}
	if checkResult.Stdout == "200" {
		return nil
	}

	configXML := fmt.Sprintf(practicePipelineJobConfigTemplate, jenkinsAgentLabel)
	createCmd := fmt.Sprintf(`
COOKIES=/tmp/fixture-job-cookies.txt
cat > /tmp/practice-pipeline-config.xml <<'JOBCONFIGEOF'
%s
JOBCONFIGEOF
CRUMB_JSON=$(curl -s -c $COOKIES http://localhost:8080/crumbIssuer/api/json)
CRUMB=$(echo "$CRUMB_JSON" | grep -o '"crumb":"[^"]*"' | cut -d'"' -f4)
curl -s -b $COOKIES -X POST "http://localhost:8080/createItem?name=%s" \
  -H "Jenkins-Crumb: $CRUMB" \
  -H "Content-Type: application/xml" \
  --data-binary @/tmp/practice-pipeline-config.xml \
  -o /dev/null -w "%%{http_code}"
`, configXML, jenkinsJobName)
	createResult, err := k8s.ExecInPod(ctx, provisioner, namespace, jenkinsPod, "jenkins", createCmd, 20*time.Second)
	if err != nil {
		return fmt.Errorf("creating practice-pipeline job: %w", err)
	}
	if createResult.Stdout != "200" {
		return fmt.Errorf("creating practice-pipeline job: unexpected HTTP status %q (stderr: %s)", createResult.Stdout, createResult.Stderr)
	}
	return nil
}

// waitForJenkinsPodToExist polls until the Deployment's ReplicaSet has
// created at least one pod, then returns its name -- the pod name itself
// isn't known in advance (a ReplicaSet-generated suffix), so this can't
// be resolved synchronously right after Create.
func waitForJenkinsPodToExist(ctx context.Context, provisioner *k8s.Provisioner, namespace string) (string, error) {
	deadline := time.Now().Add(60 * time.Second)
	for time.Now().Before(deadline) {
		name, err := findJenkinsPodName(ctx, provisioner, namespace, jenkinsDeploymentName)
		if err == nil {
			return name, nil
		}
		time.Sleep(2 * time.Second)
	}
	return "", fmt.Errorf("no jenkins pod appeared within timeout")
}

func findJenkinsPodName(ctx context.Context, provisioner *k8s.Provisioner, namespace, appLabel string) (string, error) {
	pods, err := provisioner.Clientset().CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{LabelSelector: "app=" + appLabel})
	if err != nil {
		return "", err
	}
	for _, p := range pods.Items {
		if p.Status.Phase == corev1.PodRunning {
			return p.Name, nil
		}
	}
	if len(pods.Items) > 0 {
		return pods.Items[0].Name, nil
	}
	return "", fmt.Errorf("no pod found with app=%s in namespace %s", appLabel, namespace)
}

// waitForJenkinsHTTPReady polls Jenkins' own /login endpoint from inside
// the controller pod until it responds -- confirmed live this is the
// real signal Jenkins is done initializing (setup wizard skipped, ready
// to serve requests), distinct from the pod merely being Running.
func waitForJenkinsHTTPReady(ctx context.Context, provisioner *k8s.Provisioner, namespace, pod string) error {
	deadline := time.Now().Add(120 * time.Second)
	for time.Now().Before(deadline) {
		result, err := k8s.ExecInPod(ctx, provisioner, namespace, pod, "jenkins",
			`curl -s -o /dev/null -w "%{http_code}" http://localhost:8080/login`, 10*time.Second)
		if err == nil && (result.Stdout == "200" || result.Stdout == "403") {
			return nil
		}
		time.Sleep(3 * time.Second)
	}
	return fmt.Errorf("jenkins did not become HTTP-ready within timeout")
}

// registerAgentNode creates the practice-agent permanent node via
// Jenkins' own REST API (real crumb-authenticated POST, executed from
// inside the controller pod itself against its own localhost -- the
// exact mechanism confirmed live during this fixture's own build) and
// returns the agent's real JNLP connection secret. Idempotent: if the
// node already exists, this re-fetches its existing secret rather than
// erroring.
func registerAgentNode(ctx context.Context, provisioner *k8s.Provisioner, namespace, jenkinsPod string) (string, error) {
	checkCmd := `curl -s -o /dev/null -w "%{http_code}" http://localhost:8080/computer/practice-agent/api/json`
	checkResult, err := k8s.ExecInPod(ctx, provisioner, namespace, jenkinsPod, "jenkins", checkCmd, 15*time.Second)
	if err != nil {
		return "", fmt.Errorf("checking for existing agent node: %w", err)
	}
	if checkResult.Stdout != "200" {
		// remoteFS=/tmp/agent-work, NOT /home/jenkins/agent -- confirmed
		// live during this fixture's own build: the official jenkins
		// image's /home directory is root-owned with no jenkins
		// subdirectory, so the agent process (running as UID 1000, per
		// this fixture's own PodSecurity-required SecurityContext) hits
		// AccessDeniedException trying to create a workspace there. /tmp
		// is world-writable in this image and matches the agent
		// container's own -workDir flag (ensureJenkinsAgentDeployment),
		// so the node config and the actual running process agree on
		// where the workspace lives.
		createCmd := `
COOKIES=/tmp/fixture-cookies.txt
CRUMB_JSON=$(curl -s -c $COOKIES http://localhost:8080/crumbIssuer/api/json)
CRUMB=$(echo "$CRUMB_JSON" | grep -o '"crumb":"[^"]*"' | cut -d'"' -f4)
curl -s -b $COOKIES -X POST http://localhost:8080/computer/doCreateItem \
  -H "Jenkins-Crumb: $CRUMB" \
  --data-urlencode "name=practice-agent" \
  --data-urlencode "type=hudson.slaves.DumbSlave\$DescriptorImpl" \
  --data-urlencode 'json={"name":"practice-agent","nodeDescription":"","numExecutors":"1","remoteFS":"/tmp/agent-work","labelString":"practice-agent","mode":"EXCLUSIVE","type":"hudson.slaves.DumbSlave","retentionStrategy":{"stapler-class":"hudson.slaves.RetentionStrategy$Always"},"nodeProperties":{"stapler-class-bag":"true"},"launcher":{"stapler-class":"hudson.slaves.JNLPLauncher","workDirSettings":{"disabled":false,"workspaceDir":"","internalDir":"remoting","failIfWorkDirIsMissing":false}}}' \
  -o /dev/null -w "%{http_code}"
`
		createResult, err := k8s.ExecInPod(ctx, provisioner, namespace, jenkinsPod, "jenkins", createCmd, 20*time.Second)
		if err != nil {
			return "", fmt.Errorf("creating agent node: %w", err)
		}
		if createResult.Stdout != "302" && createResult.Stdout != "200" {
			return "", fmt.Errorf("creating agent node: unexpected HTTP status %q (stderr: %s)", createResult.Stdout, createResult.Stderr)
		}
	}

	secretCmd := `curl -s http://localhost:8080/computer/practice-agent/jenkins-agent.jnlp | grep -oE '<argument>[a-f0-9]{64}</argument>' | head -1 | sed 's/<[^>]*>//g'`
	secretResult, err := k8s.ExecInPod(ctx, provisioner, namespace, jenkinsPod, "jenkins", secretCmd, 15*time.Second)
	if err != nil {
		return "", fmt.Errorf("fetching agent secret: %w", err)
	}
	secret := secretResult.Stdout
	if len(secret) != 64 {
		return "", fmt.Errorf("unexpected agent secret format (want 64 hex chars, got %d): %q", len(secret), secret)
	}
	return secret, nil
}

func ensureJenkinsDeployment(ctx context.Context, clientset *kubernetes.Clientset, namespace string) error {
	runAsNonRoot := true
	runAsUser := int64(1000) // the official jenkins image's own baked-in non-root UID
	allowPrivilegeEscalation := false
	replicas := int32(1)

	deployment := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: jenkinsDeploymentName, Namespace: namespace},
		Spec: appsv1.DeploymentSpec{
			Replicas: &replicas,
			Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": jenkinsDeploymentName}},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"app": jenkinsDeploymentName}},
				Spec: corev1.PodSpec{
					SecurityContext: &corev1.PodSecurityContext{
						RunAsNonRoot:   &runAsNonRoot,
						RunAsUser:      &runAsUser,
						SeccompProfile: &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeRuntimeDefault},
					},
					Containers: []corev1.Container{
						{
							Name:  "jenkins",
							Image: "docker.io/jenkins/jenkins:2.492.3-lts-jdk17",
							Env: []corev1.EnvVar{
								{Name: "JAVA_OPTS", Value: "-Djenkins.install.runSetupWizard=false -Xms256m -Xmx512m"},
							},
							Ports: []corev1.ContainerPort{{ContainerPort: 8080}, {ContainerPort: 50000}},
							SecurityContext: &corev1.SecurityContext{
								AllowPrivilegeEscalation: &allowPrivilegeEscalation,
								Capabilities:             &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}},
							},
							ReadinessProbe: &corev1.Probe{
								ProbeHandler: corev1.ProbeHandler{
									HTTPGet: &corev1.HTTPGetAction{Path: "/login", Port: intstr.FromInt32(8080)},
								},
								InitialDelaySeconds: 20,
								PeriodSeconds:       5,
								FailureThreshold:    30,
							},
							Resources: corev1.ResourceRequirements{
								Requests: corev1.ResourceList{corev1.ResourceMemory: resource.MustParse("512Mi")},
								Limits:   corev1.ResourceList{corev1.ResourceMemory: resource.MustParse("768Mi")},
							},
						},
					},
				},
			},
		},
	}
	if _, err := clientset.AppsV1().Deployments(namespace).Create(ctx, deployment, metav1.CreateOptions{}); err != nil && !apierrors.IsAlreadyExists(err) {
		return fmt.Errorf("creating jenkins deployment: %w", err)
	}
	return nil
}

func ensureJenkinsService(ctx context.Context, clientset *kubernetes.Clientset, namespace string) error {
	svc := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: jenkinsServiceName, Namespace: namespace},
		Spec: corev1.ServiceSpec{
			Selector: map[string]string{"app": jenkinsDeploymentName},
			Ports: []corev1.ServicePort{
				{Name: "http", Port: 8080, TargetPort: intstr.FromInt32(8080)},
				{Name: "jnlp", Port: 50000, TargetPort: intstr.FromInt32(50000)},
			},
		},
	}
	if _, err := clientset.CoreV1().Services(namespace).Create(ctx, svc, metav1.CreateOptions{}); err != nil && !apierrors.IsAlreadyExists(err) {
		return fmt.Errorf("creating jenkins service: %w", err)
	}
	return nil
}

// ensureJenkinsAgentDeployment runs a real Jenkins agent process
// connecting to the controller over WebSocket (see applyJenkinsBasic's
// own doc comment for why WebSocket, not TCP JNLP) using the exact
// secret registerAgentNode obtained from the controller's own API.
func ensureJenkinsAgentDeployment(ctx context.Context, clientset *kubernetes.Clientset, namespace, secret string) error {
	runAsNonRoot := true
	runAsUser := int64(1000)
	allowPrivilegeEscalation := false
	replicas := int32(1)

	agentCmd := fmt.Sprintf(
		`curl -sL http://%s:8080/jnlpJars/agent.jar -o /tmp/agent.jar && exec java -jar /tmp/agent.jar -url http://%s:8080/ -name %s -secret %s -workDir /tmp/agent-work -webSocket`,
		jenkinsServiceName, jenkinsServiceName, jenkinsAgentLabel, secret,
	)

	deployment := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: jenkinsAgentDeployment, Namespace: namespace},
		Spec: appsv1.DeploymentSpec{
			Replicas: &replicas,
			Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": jenkinsAgentDeployment}},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"app": jenkinsAgentDeployment}},
				Spec: corev1.PodSpec{
					SecurityContext: &corev1.PodSecurityContext{
						RunAsNonRoot:   &runAsNonRoot,
						RunAsUser:      &runAsUser,
						SeccompProfile: &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeRuntimeDefault},
					},
					Containers: []corev1.Container{
						{
							Name:    "agent",
							Image:   "docker.io/jenkins/jenkins:2.492.3-lts-jdk17",
							Command: []string{"sh", "-c", agentCmd},
							SecurityContext: &corev1.SecurityContext{
								AllowPrivilegeEscalation: &allowPrivilegeEscalation,
								Capabilities:             &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}},
							},
							Resources: corev1.ResourceRequirements{
								Requests: corev1.ResourceList{corev1.ResourceMemory: resource.MustParse("256Mi")},
								Limits:   corev1.ResourceList{corev1.ResourceMemory: resource.MustParse("384Mi")},
							},
						},
					},
				},
			},
		},
	}
	if _, err := clientset.AppsV1().Deployments(namespace).Create(ctx, deployment, metav1.CreateOptions{}); err != nil && !apierrors.IsAlreadyExists(err) {
		return fmt.Errorf("creating jenkins agent deployment: %w", err)
	}
	return nil
}
