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
	register("fx.jaeger-minimal.v1", applyJaegerMinimal)
	registerChecksum("fx.jaeger-minimal.v1", "v1")
}

const (
	jaegerDeploymentName = "practice-jaeger"
	jaegerServiceName    = "practice-jaeger"
	// jaegerFrontendServiceName is the fixed name
	// f.jaeger.missing-trace-context-propagation's params_schema targets
	// (service) -- content authors reference this exact name. It's the
	// service whose outbound call the fault handler breaks.
	jaegerFrontendServiceName = "practice-frontend"
	jaegerBackendServiceName  = "practice-backend"
)

// jaegerFrontendConfigMapName/jaegerFrontendScriptKey: the frontend
// service's request-handling script lives in a ConfigMap (not baked into
// the pod spec's Command directly) specifically so
// f.jaeger.missing-trace-context-propagation's handler can patch it in
// place -- same "ConfigMap as the real, live-patchable configuration
// surface" pattern fx.prometheus-minimal.v1 established, with the same
// whole-directory (not SubPath) mount so a patch actually propagates
// in-pod (see that fixture's own doc comment on why SubPath breaks
// live updates).
const (
	jaegerFrontendConfigMapName = "practice-jaeger-frontend-script"
	jaegerFrontendScriptKey     = "serve.sh"
)

// jaegerFrontendScript: a tiny HTTP server (busybox nc -l -p PORT >
// requestfile, response piped in via stdin -- confirmed live as the
// correct request/response pattern for busybox's nc, which has no -q
// flag; see fx.prometheus-minimal.v1's own doc comment on that
// constraint) that, on each request, generates one trace_id, reports a
// span to Jaeger under jaegerFrontendServiceName for that trace_id, and
// calls the backend PROPAGATING that same trace_id via a query param
// (?trace_id=...) -- the backend then reports its own span under the
// SAME trace_id, so Jaeger shows one connected two-span trace. This is
// the healthy baseline the fault handler breaks by making the frontend
// generate and send a DIFFERENT trace_id to the backend than the one it
// reported its own span under.
//
// date has no %N (nanosecond) support in this busybox image (confirmed
// live: `date +%s%N` prints the literal string "<epoch>%N", not a real
// number) -- OTLP timestamps are synthesized from whole seconds with
// zero-padded fractional digits instead, real enough for Jaeger's
// storage/query layer (which only needs a valid, monotonic-enough
// nanosecond integer) without needing sub-second precision this
// fixture's spans don't actually need.
const jaegerFrontendScript = `#!/bin/sh
JAEGER_OTLP="http://practice-jaeger:4318/v1/traces"
while true; do
  TRACE_ID=$(head -c 16 /dev/urandom | od -An -tx1 | tr -d ' \n')
  SPAN_ID=$(head -c 8 /dev/urandom | od -An -tx1 | tr -d ' \n')
  NOW_S=$(date +%s)
  NOW="${NOW_S}000000000"
  END="${NOW_S}050000000"
  cat > /tmp/span.json <<SPANEOF
{"resourceSpans":[{"resource":{"attributes":[{"key":"service.name","value":{"stringValue":"practice-frontend"}}]},"scopeSpans":[{"spans":[{"traceId":"$TRACE_ID","spanId":"$SPAN_ID","name":"frontend-handle","startTimeUnixNano":"$NOW","endTimeUnixNano":"$END","kind":2}]}]}]}
SPANEOF
  wget -q -O- --post-file=/tmp/span.json --header="Content-Type: application/json" "$JAEGER_OTLP" >/dev/null 2>&1
  wget -q -O- "http://practice-backend:8080/?trace_id=$TRACE_ID" >/dev/null 2>&1
  { printf 'HTTP/1.1 200 OK\r\nContent-Type: text/plain\r\n\r\n'; printf 'ok trace_id=%s\n' "$TRACE_ID"; } | nc -l -p 8080 > /tmp/req.txt
done
`

// jaegerBackendScript: reports its own span under whatever trace_id was
// passed in the request's query string (via a real HTTP GET, so this is
// genuine request-driven propagation, not a canned value) -- if the
// frontend forwards its own trace_id, Jaeger sees one trace; if not
// (the fault), the backend's span lands under a DIFFERENT trace_id than
// the frontend's, i.e. two fragmented traces.
const jaegerBackendScript = `#!/bin/sh
JAEGER_OTLP="http://practice-jaeger:4318/v1/traces"
while true; do
  { printf 'HTTP/1.1 200 OK\r\nContent-Type: text/plain\r\n\r\n'; printf 'ok\n'; } | nc -l -p 8080 > /tmp/req.txt
  TRACE_ID=$(grep -o 'trace_id=[0-9a-f]*' /tmp/req.txt | head -1 | cut -d= -f2)
  if [ -z "$TRACE_ID" ]; then TRACE_ID=$(head -c 16 /dev/urandom | od -An -tx1 | tr -d ' \n'); fi
  SPAN_ID=$(head -c 8 /dev/urandom | od -An -tx1 | tr -d ' \n')
  NOW_S=$(date +%s)
  NOW="${NOW_S}000000000"
  END="${NOW_S}030000000"
  cat > /tmp/span.json <<SPANEOF
{"resourceSpans":[{"resource":{"attributes":[{"key":"service.name","value":{"stringValue":"practice-backend"}}]},"scopeSpans":[{"spans":[{"traceId":"$TRACE_ID","spanId":"$SPAN_ID","name":"backend-handle","startTimeUnixNano":"$NOW","endTimeUnixNano":"$END","kind":2}]}]}]}
SPANEOF
  wget -q -O- --post-file=/tmp/span.json --header="Content-Type: application/json" "$JAEGER_OTLP" >/dev/null 2>&1
done
`

// applyJaegerMinimal is fx.jaeger-minimal.v1: a real
// jaegertracing/all-in-one Deployment (OTLP HTTP receiver on :4318,
// query UI/API on :16686 -- both confirmed live) plus a real 2-service
// sample app (frontend, backend) that legitimately propagates trace
// context end to end, matching this fault's own
// canonical_diagnostic_path's healthy-contrast step ("compare with a
// service that correctly forwards headers").
func applyJaegerMinimal(ctx context.Context, provisioner *k8s.Provisioner, envID, namespace string) error {
	clientset := provisioner.Clientset()

	if err := ensureJaegerDeployment(ctx, clientset, namespace); err != nil {
		return fmt.Errorf("ensuring jaeger deployment: %w", err)
	}
	if err := ensureJaegerService(ctx, clientset, namespace); err != nil {
		return fmt.Errorf("ensuring jaeger service: %w", err)
	}
	if err := ensureJaegerFrontendConfigMap(ctx, clientset, namespace, jaegerFrontendScript); err != nil {
		return fmt.Errorf("ensuring frontend script ConfigMap: %w", err)
	}
	if err := ensureJaegerSampleService(ctx, clientset, namespace, jaegerFrontendServiceName, true); err != nil {
		return fmt.Errorf("ensuring frontend pod: %w", err)
	}
	if err := ensureJaegerSampleService(ctx, clientset, namespace, jaegerBackendServiceName, false); err != nil {
		return fmt.Errorf("ensuring backend pod: %w", err)
	}
	return nil
}

func ensureJaegerFrontendConfigMap(ctx context.Context, clientset *kubernetes.Clientset, namespace, script string) error {
	cms := clientset.CoreV1().ConfigMaps(namespace)
	data := map[string]string{jaegerFrontendScriptKey: script}
	existing, err := cms.Get(ctx, jaegerFrontendConfigMapName, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		_, err := cms.Create(ctx, &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{Name: jaegerFrontendConfigMapName, Namespace: namespace},
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

func ensureJaegerDeployment(ctx context.Context, clientset *kubernetes.Clientset, namespace string) error {
	runAsNonRoot := true
	runAsUser := int64(10001) // jaegertracing/all-in-one's own default non-root UID
	allowPrivilegeEscalation := false
	replicas := int32(1)

	deployment := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: jaegerDeploymentName, Namespace: namespace},
		Spec: appsv1.DeploymentSpec{
			Replicas: &replicas,
			Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": jaegerDeploymentName}},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"app": jaegerDeploymentName}},
				Spec: corev1.PodSpec{
					SecurityContext: &corev1.PodSecurityContext{
						RunAsNonRoot:   &runAsNonRoot,
						RunAsUser:      &runAsUser,
						SeccompProfile: &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeRuntimeDefault},
					},
					Containers: []corev1.Container{
						{
							Name:  "jaeger",
							Image: "docker.io/jaegertracing/all-in-one:latest",
							Ports: []corev1.ContainerPort{
								{ContainerPort: 4318},  // OTLP HTTP
								{ContainerPort: 16686}, // query UI/API
							},
							SecurityContext: &corev1.SecurityContext{
								AllowPrivilegeEscalation: &allowPrivilegeEscalation,
								Capabilities:             &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}},
							},
							ReadinessProbe: &corev1.Probe{
								ProbeHandler: corev1.ProbeHandler{
									HTTPGet: &corev1.HTTPGetAction{Path: "/", Port: intstr.FromInt32(16686)},
								},
								InitialDelaySeconds: 5,
								PeriodSeconds:       5,
							},
						},
					},
				},
			},
		},
	}
	if _, err := clientset.AppsV1().Deployments(namespace).Create(ctx, deployment, metav1.CreateOptions{}); err != nil && !apierrors.IsAlreadyExists(err) {
		return fmt.Errorf("creating jaeger deployment: %w", err)
	}
	return nil
}

func ensureJaegerService(ctx context.Context, clientset *kubernetes.Clientset, namespace string) error {
	svc := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: jaegerServiceName, Namespace: namespace},
		Spec: corev1.ServiceSpec{
			Selector: map[string]string{"app": jaegerDeploymentName},
			Ports: []corev1.ServicePort{
				{Name: "otlp-http", Port: 4318, TargetPort: intstr.FromInt32(4318)},
				{Name: "query", Port: 16686, TargetPort: intstr.FromInt32(16686)},
			},
		},
	}
	if _, err := clientset.CoreV1().Services(namespace).Create(ctx, svc, metav1.CreateOptions{}); err != nil && !apierrors.IsAlreadyExists(err) {
		return fmt.Errorf("creating jaeger service: %w", err)
	}
	return nil
}

// ensureJaegerSampleService creates one of the two sample services as a
// single-replica Deployment (NOT a bare Pod) specifically so
// f.jaeger.missing-trace-context-propagation's handler can force a real
// config-reload by deleting the running pod: the frontend's shell script
// is read once at process startup (`sh /scripts/serve.sh` interprets the
// whole file up front, not per-request), so a ConfigMap patch alone
// never takes effect on an already-running process -- confirmed during
// this fixture's own build that the only honest way to make a patched
// script actually govern the frontend's behavior is a real pod restart,
// and a Deployment's controller gives the fault handler a real,
// verifiable mechanism for that (delete the pod, the controller
// recreates it with the now-updated ConfigMap mount already
// current -- versus a bare Pod, where a delete would just be a
// permanent teardown with nothing to recreate it). Only the frontend's
// script comes from the (fault-patchable) ConfigMap -- the backend's
// script is fixed/inline (nothing about the backend itself is ever the
// fault target; only the frontend's outbound propagation is), but both
// are Deployments for consistency and so either could be extended later
// without another fixture-shape change.
func ensureJaegerSampleService(ctx context.Context, clientset *kubernetes.Clientset, namespace, name string, useConfigMapScript bool) error {
	runAsNonRoot := true
	runAsUser := int64(1000)
	allowPrivilegeEscalation := false
	defaultMode := int32(0o555)
	replicas := int32(1)

	var command []string
	var volumes []corev1.Volume
	var mounts []corev1.VolumeMount

	if useConfigMapScript {
		command = []string{"sh", "/scripts/" + jaegerFrontendScriptKey}
		volumes = []corev1.Volume{
			{
				Name: "script",
				VolumeSource: corev1.VolumeSource{
					ConfigMap: &corev1.ConfigMapVolumeSource{
						LocalObjectReference: corev1.LocalObjectReference{Name: jaegerFrontendConfigMapName},
						DefaultMode:          &defaultMode,
					},
				},
			},
		}
		mounts = []corev1.VolumeMount{{Name: "script", MountPath: "/scripts"}}
	} else {
		command = []string{"sh", "-c", jaegerBackendScript}
	}

	deployment := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
		Spec: appsv1.DeploymentSpec{
			Replicas: &replicas,
			Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": name}},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"app": name}},
				Spec: corev1.PodSpec{
					SecurityContext: &corev1.PodSecurityContext{
						RunAsNonRoot:   &runAsNonRoot,
						RunAsUser:      &runAsUser,
						SeccompProfile: &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeRuntimeDefault},
					},
					Volumes: volumes,
					Containers: []corev1.Container{
						{
							Name:         "app",
							Image:        "docker.io/library/busybox:latest",
							Command:      command,
							Ports:        []corev1.ContainerPort{{ContainerPort: 8080}},
							VolumeMounts: mounts,
							SecurityContext: &corev1.SecurityContext{
								AllowPrivilegeEscalation: &allowPrivilegeEscalation,
								Capabilities:             &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}},
							},
						},
					},
				},
			},
		},
	}
	if _, err := clientset.AppsV1().Deployments(namespace).Create(ctx, deployment, metav1.CreateOptions{}); err != nil && !apierrors.IsAlreadyExists(err) {
		return fmt.Errorf("creating deployment %s: %w", name, err)
	}

	svc := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
		Spec: corev1.ServiceSpec{
			Selector: map[string]string{"app": name},
			Ports:    []corev1.ServicePort{{Port: 8080, TargetPort: intstr.FromInt32(8080)}},
		},
	}
	if _, err := clientset.CoreV1().Services(namespace).Create(ctx, svc, metav1.CreateOptions{}); err != nil && !apierrors.IsAlreadyExists(err) {
		return fmt.Errorf("creating service %s: %w", name, err)
	}
	return nil
}
