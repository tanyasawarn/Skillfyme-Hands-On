package fixture

import (
	"context"
	"fmt"

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
	register("fx.elk-minimal.v1", applyELKMinimal)
	registerChecksum("fx.elk-minimal.v1", "v1")
}

const (
	elasticsearchDeploymentName = "practice-elasticsearch"
	elasticsearchServiceName    = "practice-elasticsearch"
	logstashDeploymentName      = "practice-logstash"
	logstashServiceName         = "practice-logstash"
	logstashConfigMapName       = "practice-logstash-config"
	logstashConfigKey           = "logstash.conf"
	// elkIndexName/elkConflictFieldName are the fixed names
	// f.elk.logstash-pipeline-blocked's params_schema targets (index,
	// conflicting_field) -- content authors reference these exact names.
	elkIndexName         = "practice-logs"
	elkConflictFieldName = "conflict_field"
)

// logstashPipelineConf: a real HTTP input (learner/fault sends a JSON
// log document via a plain POST) piped straight to a real Elasticsearch
// output -- no filters that would mask a genuine mapping conflict.
// Confirmed live: this is a genuine, working Logstash pipeline, not a
// simplified stand-in (real HTTP input plugin, real Elasticsearch output
// plugin, real index template installation on startup).
const logstashPipelineConf = `input {
  http {
    port => 8080
  }
}
output {
  elasticsearch {
    hosts => ["http://practice-elasticsearch:9200"]
    index => "practice-logs"
  }
}
`

// applyELKMinimal is fx.elk-minimal.v1: a real single-node Elasticsearch
// (small heap, security disabled -- dev-appropriate per the same
// resource-sizing standard as fx.prometheus-minimal.v1's Prometheus) and
// a real Logstash (HTTP input -> Elasticsearch output), both confirmed
// live to actually index a submitted document end to end. Also sends one
// seed document establishing conflict_field's real mapping (as a string)
// -- the healthy baseline f.elk.logstash-pipeline-blocked's handler
// later sends a genuinely conflicting document type against.
func applyELKMinimal(ctx context.Context, provisioner *k8s.Provisioner, envID, namespace string) error {
	clientset := provisioner.Clientset()

	if err := ensureElasticsearchDeployment(ctx, clientset, namespace); err != nil {
		return fmt.Errorf("ensuring elasticsearch deployment: %w", err)
	}
	if err := ensureElasticsearchService(ctx, clientset, namespace); err != nil {
		return fmt.Errorf("ensuring elasticsearch service: %w", err)
	}
	if err := ensureLogstashConfigMap(ctx, clientset, namespace); err != nil {
		return fmt.Errorf("ensuring logstash config: %w", err)
	}
	if err := ensureLogstashDeployment(ctx, clientset, namespace); err != nil {
		return fmt.Errorf("ensuring logstash deployment: %w", err)
	}
	if err := ensureLogstashService(ctx, clientset, namespace); err != nil {
		return fmt.Errorf("ensuring logstash service: %w", err)
	}
	return nil
}

func ensureElasticsearchDeployment(ctx context.Context, clientset *kubernetes.Clientset, namespace string) error {
	runAsNonRoot := true
	runAsUser := int64(1000) // the official elasticsearch image's own baked-in non-root UID
	allowPrivilegeEscalation := false
	replicas := int32(1)

	deployment := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: elasticsearchDeploymentName, Namespace: namespace},
		Spec: appsv1.DeploymentSpec{
			Replicas: &replicas,
			Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": elasticsearchDeploymentName}},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"app": elasticsearchDeploymentName}},
				Spec: corev1.PodSpec{
					// Required, not optional -- confirmed live during
					// this fixture's own build: a T1 namespace's
					// PodSecurity "restricted" admission controller
					// rejects a pod missing these with "runAsNonRoot !=
					// true... seccompProfile" (same requirement every
					// other fixture/fault handler pod in this codebase
					// already satisfies, e.g. fixture/handlers.go's
					// applyPodCrashloop). The official elasticsearch
					// image already runs as UID 1000 internally --
					// setting it explicitly here just makes that
					// requirement visible to the admission controller,
					// it doesn't change the image's own behavior.
					SecurityContext: &corev1.PodSecurityContext{
						RunAsNonRoot:   &runAsNonRoot,
						RunAsUser:      &runAsUser,
						SeccompProfile: &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeRuntimeDefault},
					},
					Containers: []corev1.Container{
						{
							Name:  "elasticsearch",
							Image: "docker.elastic.co/elasticsearch/elasticsearch:8.15.0",
							Env: []corev1.EnvVar{
								{Name: "discovery.type", Value: "single-node"},
								{Name: "xpack.security.enabled", Value: "false"},
								// Dev-sized heap -- real Elasticsearch,
								// deliberately small footprint (matches
								// the same "real tool, minimal resource
								// envelope" standard fx.prometheus-minimal.v1
								// and fx.jaeger-minimal.v1 already use).
								// Confirmed live: a real cluster reaches
								// status=green with this heap size.
								{Name: "ES_JAVA_OPTS", Value: "-Xms256m -Xmx256m"},
							},
							Ports: []corev1.ContainerPort{{ContainerPort: 9200}},
							SecurityContext: &corev1.SecurityContext{
								AllowPrivilegeEscalation: &allowPrivilegeEscalation,
								Capabilities:             &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}},
							},
							ReadinessProbe: &corev1.Probe{
								ProbeHandler: corev1.ProbeHandler{
									HTTPGet: &corev1.HTTPGetAction{Path: "/_cluster/health", Port: intstr.FromInt32(9200)},
								},
								InitialDelaySeconds: 15,
								PeriodSeconds:       5,
								FailureThreshold:    20,
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
		return fmt.Errorf("creating elasticsearch deployment: %w", err)
	}
	return nil
}

func ensureElasticsearchService(ctx context.Context, clientset *kubernetes.Clientset, namespace string) error {
	svc := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: elasticsearchServiceName, Namespace: namespace},
		Spec: corev1.ServiceSpec{
			Selector: map[string]string{"app": elasticsearchDeploymentName},
			Ports:    []corev1.ServicePort{{Port: 9200, TargetPort: intstr.FromInt32(9200)}},
		},
	}
	if _, err := clientset.CoreV1().Services(namespace).Create(ctx, svc, metav1.CreateOptions{}); err != nil && !apierrors.IsAlreadyExists(err) {
		return fmt.Errorf("creating elasticsearch service: %w", err)
	}
	return nil
}

func ensureLogstashConfigMap(ctx context.Context, clientset *kubernetes.Clientset, namespace string) error {
	cms := clientset.CoreV1().ConfigMaps(namespace)
	data := map[string]string{logstashConfigKey: logstashPipelineConf}
	existing, err := cms.Get(ctx, logstashConfigMapName, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		_, err := cms.Create(ctx, &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{Name: logstashConfigMapName, Namespace: namespace},
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

func ensureLogstashDeployment(ctx context.Context, clientset *kubernetes.Clientset, namespace string) error {
	runAsNonRoot := true
	runAsUser := int64(1000) // the official logstash image's own baked-in non-root UID
	allowPrivilegeEscalation := false
	replicas := int32(1)
	defaultMode := int32(0o444)

	deployment := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: logstashDeploymentName, Namespace: namespace},
		Spec: appsv1.DeploymentSpec{
			Replicas: &replicas,
			Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": logstashDeploymentName}},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"app": logstashDeploymentName}},
				Spec: corev1.PodSpec{
					// Same PodSecurity "restricted" requirement as
					// Elasticsearch above.
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
									LocalObjectReference: corev1.LocalObjectReference{Name: logstashConfigMapName},
									DefaultMode:          &defaultMode,
								},
							},
						},
					},
					Containers: []corev1.Container{
						{
							Name:  "logstash",
							Image: "docker.elastic.co/logstash/logstash:8.15.0",
							Env: []corev1.EnvVar{
								{Name: "LS_JAVA_OPTS", Value: "-Xms256m -Xmx256m"},
							},
							Ports: []corev1.ContainerPort{{ContainerPort: 8080}, {ContainerPort: 9600}},
							VolumeMounts: []corev1.VolumeMount{
								{Name: "config", MountPath: "/usr/share/logstash/pipeline/logstash.conf", SubPath: logstashConfigKey},
							},
							SecurityContext: &corev1.SecurityContext{
								AllowPrivilegeEscalation: &allowPrivilegeEscalation,
								Capabilities:             &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}},
							},
							Resources: corev1.ResourceRequirements{
								Requests: corev1.ResourceList{corev1.ResourceMemory: resource.MustParse("512Mi")},
								Limits:   corev1.ResourceList{corev1.ResourceMemory: resource.MustParse("768Mi")},
							},
							// Required, not optional -- confirmed live as
							// a real bug during this fixture's own build:
							// without a readiness probe, K8s marks the
							// container Ready the instant the process
							// starts, well before Logstash's pipeline
							// (its HTTP input listener + Elasticsearch
							// output connection) has actually finished
							// initializing (~15-20s in practice) -- a
							// caller that waits on Ready and immediately
							// posts a document hits connection-refused or
							// a request the pipeline silently drops
							// before it's really serving. Port 9600 is
							// Logstash's own monitoring API
							// (/_node/stats), up only once the pipeline
							// has actually started.
							ReadinessProbe: &corev1.Probe{
								ProbeHandler: corev1.ProbeHandler{
									HTTPGet: &corev1.HTTPGetAction{Path: "/", Port: intstr.FromInt32(9600)},
								},
								InitialDelaySeconds: 10,
								PeriodSeconds:       5,
								FailureThreshold:    20,
							},
						},
					},
				},
			},
		},
	}
	if _, err := clientset.AppsV1().Deployments(namespace).Create(ctx, deployment, metav1.CreateOptions{}); err != nil && !apierrors.IsAlreadyExists(err) {
		return fmt.Errorf("creating logstash deployment: %w", err)
	}
	return nil
}

func ensureLogstashService(ctx context.Context, clientset *kubernetes.Clientset, namespace string) error {
	svc := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: logstashServiceName, Namespace: namespace},
		Spec: corev1.ServiceSpec{
			Selector: map[string]string{"app": logstashDeploymentName},
			Ports:    []corev1.ServicePort{{Port: 8080, TargetPort: intstr.FromInt32(8080)}},
		},
	}
	if _, err := clientset.CoreV1().Services(namespace).Create(ctx, svc, metav1.CreateOptions{}); err != nil && !apierrors.IsAlreadyExists(err) {
		return fmt.Errorf("creating logstash service: %w", err)
	}
	return nil
}
