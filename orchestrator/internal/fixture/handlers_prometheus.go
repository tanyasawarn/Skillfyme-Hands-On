package fixture

import (
	"context"
	"fmt"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/client-go/kubernetes"

	"github.com/tanyasawarn/skillfyme-hands-on/orchestrator/internal/k8s"
)

func init() {
	register("fx.prometheus-minimal.v1", applyPrometheusMinimal)
	registerChecksum("fx.prometheus-minimal.v1", "v1")
}

const (
	prometheusConfigMapName  = "practice-prometheus-config"
	prometheusDeploymentName = "practice-prometheus"
	prometheusServiceName    = "practice-prometheus"
	// prometheusScrapeJobName/prometheusRulesFileName are the fixed names
	// the two Prometheus faults' params_schema target (job_name,
	// rules_file) -- content authors reference these exact names.
	prometheusScrapeJobName = "practice-target"
	prometheusRulesFileName = "practice-rules.yml"
	scrapeTargetPodName     = "practice-scrape-target"
)

// prometheusYML seeds one working scrape job (self-scrape, always
// reachable so the fixture is healthy immediately) plus a second job
// named prometheusScrapeJobName targeting a real, always-up target pod
// in this same namespace -- f.prometheus.scrape-target-down's handler
// later adds a metric_relabel_configs drop rule that makes this SECOND
// job's data vanish without touching the first, so Prometheus's own
// health (self-scrape) stays observably fine throughout, matching the
// fault's own detectability: low framing ("no obvious error in
// Prometheus itself").
const prometheusYML = `global:
  scrape_interval: 5s
scrape_configs:
  - job_name: prometheus
    static_configs:
      - targets: ['localhost:9090']
  - job_name: practice-target
    static_configs:
      - targets: ['practice-scrape-target:8080']
rule_files:
  - /etc/prometheus/practice-rules.yml
`

// prometheusRulesYML: one real, syntactically-valid alert rule group
// f.prometheus.alert-rule-syntax-silent-fail's handler later corrupts.
const prometheusRulesYML = `groups:
  - name: practice-alerts
    rules:
      - alert: ScrapeTargetDown
        expr: up{job="practice-target"} == 0
        for: 1m
        labels:
          severity: warning
        annotations:
          summary: "practice-target is down"
`

// applyPrometheusMinimal is fx.prometheus-minimal.v1: a real Prometheus
// Deployment (--web.enable-lifecycle, so fault handlers can trigger a
// live config reload the same way an operator fixing a real
// misconfiguration would) plus a real always-up scrape target pod, both
// namespace-scoped -- no cluster-wide install needed (unlike Tekton),
// Prometheus itself is just a Deployment + ConfigMap + Service.
func applyPrometheusMinimal(ctx context.Context, provisioner *k8s.Provisioner, envID, namespace string) error {
	clientset := provisioner.Clientset()

	if err := ensurePrometheusConfigMap(ctx, clientset, namespace, prometheusYML, prometheusRulesYML); err != nil {
		return fmt.Errorf("ensuring prometheus ConfigMap: %w", err)
	}
	if err := ensureScrapeTargetPod(ctx, clientset, namespace); err != nil {
		return fmt.Errorf("ensuring scrape target pod: %w", err)
	}
	if err := ensurePrometheusDeployment(ctx, clientset, namespace); err != nil {
		return fmt.Errorf("ensuring prometheus deployment: %w", err)
	}
	if err := ensurePrometheusService(ctx, clientset, namespace); err != nil {
		return fmt.Errorf("ensuring prometheus service: %w", err)
	}
	return nil
}

func ensurePrometheusConfigMap(ctx context.Context, clientset *kubernetes.Clientset, namespace, mainYML, rulesYML string) error {
	cms := clientset.CoreV1().ConfigMaps(namespace)
	data := map[string]string{"prometheus.yml": mainYML, prometheusRulesFileName: rulesYML}
	existing, err := cms.Get(ctx, prometheusConfigMapName, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		_, err := cms.Create(ctx, &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{Name: prometheusConfigMapName, Namespace: namespace},
			Data:       data,
		}, metav1.CreateOptions{})
		return err
	}
	if err != nil {
		return err
	}
	existing.Data = data
	_, err = cms.Update(ctx, existing, metav1.UpdateOptions{})
	return err
}

// ensureScrapeTargetPod seeds a tiny, always-up HTTP server exposing
// /metrics -- a real Prometheus-format response, so the "practice-target"
// scrape job is genuinely healthy (up{job="practice-target"} == 1) before
// any fault runs.
func ensureScrapeTargetPod(ctx context.Context, clientset *kubernetes.Clientset, namespace string) error {
	runAsNonRoot := true
	runAsUser := int64(1000)
	allowPrivilegeEscalation := false
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: scrapeTargetPodName, Namespace: namespace, Labels: map[string]string{"app": scrapeTargetPodName}},
		Spec: corev1.PodSpec{
			SecurityContext: &corev1.PodSecurityContext{
				RunAsNonRoot:   &runAsNonRoot,
				RunAsUser:      &runAsUser,
				SeccompProfile: &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeRuntimeDefault},
			},
			Containers: []corev1.Container{
				{
					Name:  "metrics",
					Image: "docker.io/library/busybox:latest",
					// busybox's nc has no -q (per-connection quit-after-idle)
					// flag -- confirmed live against the exact image this
					// fixture pulls (BusyBox v1.38.0's nc only supports -l/-p/
					// -w/-i/-n/-u/-b/-v/-o/-z, no -q), so a naive `nc -l -p
					// 8080 -q 1` either rejects the flag or exits after the
					// first connection depending on busybox build, breaking
					// the loop entirely -- caught by a real live scrape test
					// against this exact command before landing it.
					Command: []string{"sh", "-c", `while true; do { printf 'HTTP/1.1 200 OK\r\nContent-Type: text/plain\r\n\r\n'; printf 'up 1\nscrape_target_requests_total 1\n'; } | nc -l -p 8080; done`},
					Ports:   []corev1.ContainerPort{{ContainerPort: 8080}},
					SecurityContext: &corev1.SecurityContext{
						AllowPrivilegeEscalation: &allowPrivilegeEscalation,
						Capabilities:             &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}},
					},
				},
			},
		},
	}
	if _, err := clientset.CoreV1().Pods(namespace).Create(ctx, pod, metav1.CreateOptions{}); err != nil && !apierrors.IsAlreadyExists(err) {
		return fmt.Errorf("creating scrape target pod: %w", err)
	}

	svc := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: scrapeTargetPodName, Namespace: namespace},
		Spec: corev1.ServiceSpec{
			Selector: map[string]string{"app": scrapeTargetPodName},
			Ports:    []corev1.ServicePort{{Port: 8080, TargetPort: intstr.FromInt32(8080)}},
		},
	}
	if _, err := clientset.CoreV1().Services(namespace).Create(ctx, svc, metav1.CreateOptions{}); err != nil && !apierrors.IsAlreadyExists(err) {
		return fmt.Errorf("creating scrape target service: %w", err)
	}
	return nil
}

func ensurePrometheusDeployment(ctx context.Context, clientset *kubernetes.Clientset, namespace string) error {
	runAsNonRoot := true
	runAsUser := int64(65534) // nobody -- prom/prometheus's own image runs as this UID by default
	allowPrivilegeEscalation := false
	replicas := int32(1)
	defaultMode := int32(0o444)

	deployment := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: prometheusDeploymentName, Namespace: namespace},
		Spec: appsv1.DeploymentSpec{
			Replicas: &replicas,
			Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": prometheusDeploymentName}},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"app": prometheusDeploymentName}},
				Spec: corev1.PodSpec{
					SecurityContext: &corev1.PodSecurityContext{
						RunAsNonRoot:   &runAsNonRoot,
						RunAsUser:      &runAsUser,
						SeccompProfile: &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeRuntimeDefault},
					},
					Volumes: []corev1.Volume{
						{
							Name: "config",
							VolumeSource: corev1.VolumeSource{
								ConfigMap: &corev1.ConfigMapVolumeSource{
									LocalObjectReference: corev1.LocalObjectReference{Name: prometheusConfigMapName},
									DefaultMode:          &defaultMode,
								},
							},
						},
					},
					Containers: []corev1.Container{
						{
							Name:  "prometheus",
							Image: "docker.io/prom/prometheus:latest",
							Args: []string{
								"--config.file=/etc/prometheus/prometheus.yml",
								"--web.enable-lifecycle", // lets fault handlers trigger a live reload via POST /-/reload, the real operator workflow
								"--storage.tsdb.retention.time=1h",
							},
							Ports: []corev1.ContainerPort{{ContainerPort: 9090}},
							// Mounted as a whole directory, NOT per-file with
							// SubPath -- a SubPath-mounted ConfigMap volume
							// does not receive kubelet's live ConfigMap-update
							// propagation (SubPath mounts bypass the
							// symlink-swap mechanism that makes a ConfigMap
							// update visible in-pod without a restart), which
							// would make fault handlers' /-/reload trigger a
							// no-op forever. Confirmed live: an earlier
							// SubPath-mounted version of this fixture left the
							// on-disk config byte-for-byte unchanged after a
							// real ConfigMap Update, caught by this fixture's
							// own integration test. Mounting the whole
							// ConfigMap as a directory does receive live
							// updates (typically within the kubelet sync
							// period), which /-/reload can then pick up. Rules
							// file path below matches this directory-mount
							// layout (/etc/prometheus/practice-rules.yml, not
							// a rules/ subdirectory -- there is no per-file
							// mount left to place it in a subdirectory).
							VolumeMounts: []corev1.VolumeMount{
								{Name: "config", MountPath: "/etc/prometheus"},
							},
							SecurityContext: &corev1.SecurityContext{
								AllowPrivilegeEscalation: &allowPrivilegeEscalation,
								Capabilities:             &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}},
							},
							ReadinessProbe: &corev1.Probe{
								ProbeHandler: corev1.ProbeHandler{
									HTTPGet: &corev1.HTTPGetAction{Path: "/-/ready", Port: intstr.FromInt32(9090)},
								},
								InitialDelaySeconds: 3,
								PeriodSeconds:       5,
							},
						},
					},
				},
			},
		},
	}
	if _, err := clientset.AppsV1().Deployments(namespace).Create(ctx, deployment, metav1.CreateOptions{}); err != nil && !apierrors.IsAlreadyExists(err) {
		return fmt.Errorf("creating prometheus deployment: %w", err)
	}
	return nil
}

func ensurePrometheusService(ctx context.Context, clientset *kubernetes.Clientset, namespace string) error {
	svc := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: prometheusServiceName, Namespace: namespace},
		Spec: corev1.ServiceSpec{
			Selector: map[string]string{"app": prometheusDeploymentName},
			Ports:    []corev1.ServicePort{{Port: 9090, TargetPort: intstr.FromInt32(9090)}},
		},
	}
	if _, err := clientset.CoreV1().Services(namespace).Create(ctx, svc, metav1.CreateOptions{}); err != nil && !apierrors.IsAlreadyExists(err) {
		return fmt.Errorf("creating prometheus service: %w", err)
	}
	return nil
}
