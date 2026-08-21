// Package regression implements the real (non-stub) side of the
// NO_REGRESSION validator type (doc §6.2, §7.3 step 3: "Capture full
// resource inventory + health matrix. This is the NO_REGRESSION
// reference and the 'what did they break' diff source."). Same
// architectural pattern as internal/faultinjection: the orchestrator
// process has real cluster API access (via internal/k8s.Provisioner's
// clientset) that practice-core's evaluation engine doesn't, so baseline
// capture/diff happens here rather than in Dev B's ValidatorExecutor.
//
// Health matrix scope (deliberately narrow, matching the doc's own
// worked example): Deployment readyReplicas/replicas and Service
// endpoint presence, across the whole namespace. Not StatefulSets, not
// custom resources, not cross-namespace -- a real gap, not silently
// ignored (see HealthMatrix's own doc comment).
package regression

import (
	"context"
	"fmt"

	appsv1 "k8s.io/api/apps/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"

	"github.com/tanyasawarn/skillfyme-hands-on/orchestrator/internal/k8s"
)

// DeploymentHealth captures the subset of a Deployment's status the
// doc's own NO_REGRESSION example checks (K8S_ASSERT worked example:
// "readyReplicas >= 3").
type DeploymentHealth struct {
	Name              string `json:"name"`
	Replicas          int32  `json:"replicas"`
	ReadyReplicas     int32  `json:"ready_replicas"`
	AvailableReplicas int32  `json:"available_replicas"`
}

// ServiceHealth captures whether a Service currently routes to anything
// -- doc §3.4's own fault (f.k8s.wrong-service-selector) targets exactly
// this signal, so it's the natural regression check to pair with it.
type ServiceHealth struct {
	Name          string `json:"name"`
	EndpointCount int    `json:"endpoint_count"`
}

// HealthMatrix is the JSON shape persisted in
// env.regression_baseline.health_matrix and compared by Diff.
type HealthMatrix struct {
	Deployments []DeploymentHealth `json:"deployments"`
	Services    []ServiceHealth    `json:"services"`
}

// Capture reads the current health matrix for every Deployment and
// Service in the environment's namespace. Doc §7.3 step 3: called once,
// after the baseline is confirmed healthy, before any fault is injected.
func Capture(ctx context.Context, provisioner *k8s.Provisioner, envID string) (HealthMatrix, error) {
	clientset := provisioner.Clientset()
	ns := k8s.NamespaceForEnv(envID)

	matrix := HealthMatrix{}

	deployments, err := clientset.AppsV1().Deployments(ns).List(ctx, metav1.ListOptions{})
	if err != nil {
		return HealthMatrix{}, fmt.Errorf("listing deployments in %s: %w", ns, err)
	}
	for _, d := range deployments.Items {
		matrix.Deployments = append(matrix.Deployments, deploymentHealthOf(d))
	}

	services, err := clientset.CoreV1().Services(ns).List(ctx, metav1.ListOptions{})
	if err != nil {
		return HealthMatrix{}, fmt.Errorf("listing services in %s: %w", ns, err)
	}
	for _, svc := range services.Items {
		count, err := endpointCount(ctx, clientset, ns, svc.Name)
		if err != nil {
			// A Service with no matching Endpoints object yet (freshly
			// created, controller hasn't reconciled) is a real 0, not an
			// error worth failing the whole capture over.
			count = 0
		}
		matrix.Services = append(matrix.Services, ServiceHealth{Name: svc.Name, EndpointCount: count})
	}

	return matrix, nil
}

func deploymentHealthOf(d appsv1.Deployment) DeploymentHealth {
	replicas := int32(0)
	if d.Spec.Replicas != nil {
		replicas = *d.Spec.Replicas
	}
	return DeploymentHealth{
		Name:              d.Name,
		Replicas:          replicas,
		ReadyReplicas:     d.Status.ReadyReplicas,
		AvailableReplicas: d.Status.AvailableReplicas,
	}
}

func endpointCount(ctx context.Context, clientset *kubernetes.Clientset, namespace, service string) (int, error) {
	ep, err := clientset.CoreV1().Endpoints(namespace).Get(ctx, service, metav1.GetOptions{})
	if err != nil {
		return 0, err
	}
	count := 0
	for _, subset := range ep.Subsets {
		count += len(subset.Addresses)
	}
	return count, nil
}

// Regression describes one resource that got worse between baseline and
// current -- human-readable per resource, matching InjectFaultResponse's
// style of returning something a learner-facing UI or grader could show
// directly rather than a raw diff structure.
type Regression struct {
	Resource string
	Detail   string
}

// Diff compares a captured baseline against the environment's current
// live state and reports any resource that regressed. "Regressed" is
// deliberately one-directional (doc's own framing: "didn't fix by
// breaking something else") -- a resource that IMPROVED since baseline
// (e.g. more ready replicas) is never flagged.
func Diff(ctx context.Context, provisioner *k8s.Provisioner, envID string, baseline HealthMatrix) ([]Regression, error) {
	current, err := Capture(ctx, provisioner, envID)
	if err != nil {
		return nil, fmt.Errorf("capturing current state for diff: %w", err)
	}

	var regressions []Regression

	baselineDeployments := make(map[string]DeploymentHealth, len(baseline.Deployments))
	for _, d := range baseline.Deployments {
		baselineDeployments[d.Name] = d
	}
	currentDeployments := make(map[string]DeploymentHealth, len(current.Deployments))
	for _, d := range current.Deployments {
		currentDeployments[d.Name] = d
	}

	for name, before := range baselineDeployments {
		after, stillExists := currentDeployments[name]
		if !stillExists {
			regressions = append(regressions, Regression{
				Resource: fmt.Sprintf("Deployment/%s", name),
				Detail:   "existed at baseline, no longer exists",
			})
			continue
		}
		if after.ReadyReplicas < before.ReadyReplicas {
			regressions = append(regressions, Regression{
				Resource: fmt.Sprintf("Deployment/%s", name),
				Detail:   fmt.Sprintf("readyReplicas dropped %d -> %d", before.ReadyReplicas, after.ReadyReplicas),
			})
		}
	}

	baselineServices := make(map[string]ServiceHealth, len(baseline.Services))
	for _, s := range baseline.Services {
		baselineServices[s.Name] = s
	}
	currentServices := make(map[string]ServiceHealth, len(current.Services))
	for _, s := range current.Services {
		currentServices[s.Name] = s
	}

	for name, before := range baselineServices {
		after, stillExists := currentServices[name]
		if !stillExists {
			regressions = append(regressions, Regression{
				Resource: fmt.Sprintf("Service/%s", name),
				Detail:   "existed at baseline, no longer exists",
			})
			continue
		}
		if before.EndpointCount > 0 && after.EndpointCount == 0 {
			regressions = append(regressions, Regression{
				Resource: fmt.Sprintf("Service/%s", name),
				Detail:   fmt.Sprintf("endpoints dropped from %d to 0", before.EndpointCount),
			})
		}
	}

	return regressions, nil
}
