package k8s

import (
	"bytes"
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/tools/remotecommand"
)

// ExecResult is the raw outcome of running a command inside an arbitrary
// pod's container -- same shape internal/validation's own execResult
// uses for the workspace pod specifically.
type ExecResult struct {
	Stdout   string
	Stderr   string
	ExitCode int
}

// ExecInPod runs cmd non-interactively inside podName's container via the
// same SPDY-exec mechanism internal/validation's execInPod uses for the
// workspace pod -- generalized to target ANY pod in the namespace, not
// just the workspace pod, since Phase 2's fixture/fault handlers for
// tools that need a real CLI binary (terraform, helm, ansible-playbook)
// or a REST call against a fixture's own controller (Jenkins) run inside
// a dedicated fixture pod that has that tool installed, not inside the
// learner's bare workspace image. internal/validation intentionally
// keeps its own workspace-pod-only execInPod rather than depending on
// this more general helper, to avoid the validation package depending on
// fixture/fault-handler concerns -- this lives in internal/k8s instead,
// which both internal/fixture and internal/faultinjection already import.
func ExecInPod(ctx context.Context, provisioner *Provisioner, namespace, podName, containerName, cmd string, timeout time.Duration) (ExecResult, error) {
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	execCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	clientset := provisioner.Clientset()
	restConfig := provisioner.RestConfig()

	// Same exit-code-marker trick as internal/validation's execInPod:
	// remotecommand.StreamWithContext doesn't otherwise surface the
	// remote process's real exit code.
	wrapped := fmt.Sprintf("%s\necho \"__EXIT_CODE__:$?\"", cmd)

	req := clientset.CoreV1().RESTClient().Post().
		Resource("pods").
		Namespace(namespace).
		Name(podName).
		SubResource("exec").
		VersionedParams(&corev1.PodExecOptions{
			Container: containerName,
			Command:   []string{"/bin/sh", "-c", wrapped},
			Stdin:     false,
			Stdout:    true,
			Stderr:    true,
			TTY:       false,
		}, scheme.ParameterCodec)

	executor, err := remotecommand.NewSPDYExecutor(restConfig, "POST", req.URL())
	if err != nil {
		return ExecResult{}, fmt.Errorf("building exec executor for pod %s/%s: %w", namespace, podName, err)
	}

	var stdout, stderr bytes.Buffer
	streamErr := executor.StreamWithContext(execCtx, remotecommand.StreamOptions{
		Stdout: &stdout,
		Stderr: &stderr,
	})
	out := stdout.String()
	code, foundMarker := parseExecExitCodeMarker(out)
	if !foundMarker {
		if streamErr != nil {
			return ExecResult{}, fmt.Errorf("exec stream error for pod %s/%s (no exit marker found): %w", namespace, podName, streamErr)
		}
		return ExecResult{}, fmt.Errorf("exit code marker not found in output from pod %s/%s", namespace, podName)
	}

	return ExecResult{Stdout: stripExecExitCodeMarker(out), Stderr: stderr.String(), ExitCode: code}, nil
}

func parseExecExitCodeMarker(out string) (int, bool) {
	idx := strings.LastIndex(out, "__EXIT_CODE__:")
	if idx == -1 {
		return 0, false
	}
	rest := strings.TrimSpace(out[idx+len("__EXIT_CODE__:"):])
	code, err := strconv.Atoi(rest)
	if err != nil {
		return 0, false
	}
	return code, true
}

func stripExecExitCodeMarker(out string) string {
	idx := strings.LastIndex(out, "__EXIT_CODE__:")
	if idx == -1 {
		return out
	}
	return strings.TrimRight(out[:idx], "\n")
}

// WaitForNamedPodReady polls until podName in namespace reports
// PodReady=true, or ctx is done -- same polling shape as Provisioner's
// own WaitForPodReady, generalized to an arbitrary pod name for fixture
// pods (a Jenkins controller, a Prometheus deployment, a DinD pod) that
// aren't the learner's workspace pod.
func WaitForNamedPodReady(ctx context.Context, provisioner *Provisioner, namespace, podName string) error {
	clientset := provisioner.Clientset()
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return fmt.Errorf("timed out waiting for pod %s/%s ready: %w", namespace, podName, ctx.Err())
		case <-ticker.C:
			pod, err := clientset.CoreV1().Pods(namespace).Get(ctx, podName, metav1.GetOptions{})
			if err != nil {
				if apierrors.IsNotFound(err) {
					continue
				}
				return err
			}
			for _, cond := range pod.Status.Conditions {
				if cond.Type == corev1.PodReady && cond.Status == corev1.ConditionTrue {
					return nil
				}
			}
		}
	}
}

// FindPodByLabel returns the first pod matching labelSelector in
// namespace whose PodReady condition is true (i.e. its container has
// actually started and passed any readiness probe -- NOT just
// Status.Phase == Running, which a pod reaches while still
// ContainerCreating/pulling its image and would make an exec attempt
// fail with "container not found"; confirmed live during
// fx.helm-release.v1's own build against a cold, not-yet-pulled
// alpine/helm image), or the first pod found if none is ready yet (so a
// caller with its own more specific readiness check, e.g.
// waitForJenkinsHTTPReady, still gets a name to act on). Used to resolve
// a Deployment-managed pod's name, which isn't known in advance (a
// ReplicaSet-generated suffix) -- generalizes the same
// wait-then-find-by-label pattern that had been hand-duplicated per
// fixture (Jenkins, Prometheus, Jaeger, ELK all wrote their own copy
// before this).
func FindPodByLabel(ctx context.Context, provisioner *Provisioner, namespace, labelSelector string) (string, error) {
	pods, err := provisioner.Clientset().CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{LabelSelector: labelSelector})
	if err != nil {
		return "", err
	}
	for _, p := range pods.Items {
		for _, cond := range p.Status.Conditions {
			if cond.Type == corev1.PodReady && cond.Status == corev1.ConditionTrue {
				return p.Name, nil
			}
		}
	}
	if len(pods.Items) > 0 {
		return pods.Items[0].Name, nil
	}
	return "", fmt.Errorf("no pod found matching label %q in namespace %s", labelSelector, namespace)
}

// WaitForPodByLabel polls until a pod matching labelSelector is actually
// Ready (see FindPodByLabel's own doc comment for why phase==Running
// alone isn't sufficient), or timeout, then returns its name.
func WaitForPodByLabel(ctx context.Context, provisioner *Provisioner, namespace, labelSelector string, timeout time.Duration) (string, error) {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		pods, err := provisioner.Clientset().CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{LabelSelector: labelSelector})
		if err == nil {
			for _, p := range pods.Items {
				for _, cond := range p.Status.Conditions {
					if cond.Type == corev1.PodReady && cond.Status == corev1.ConditionTrue {
						return p.Name, nil
					}
				}
			}
		}
		time.Sleep(2 * time.Second)
	}
	return "", fmt.Errorf("no pod matching label %q became Ready in namespace %s within %s", labelSelector, namespace, timeout)
}
