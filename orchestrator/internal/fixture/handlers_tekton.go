package fixture

import (
	"context"
	"fmt"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"

	"github.com/tanyasawarn/skillfyme-hands-on/orchestrator/internal/clusterbootstrap"
	"github.com/tanyasawarn/skillfyme-hands-on/orchestrator/internal/k8s"
)

func init() {
	register("fx.tekton-pipeline.v1", applyTektonPipeline)
	registerChecksum("fx.tekton-pipeline.v1", "v1")
}

// tektonTaskGVR/tektonTaskRunGVR/tektonPipelineRunGVR: Tekton's CRDs
// aren't part of k8s.io/api (a separate, third-party API group this
// repo doesn't vendor a typed client for), so this fixture -- and
// f.tekton.task-missing-workspace-binding's handler -- use the generic
// dynamic.Interface + unstructured.Unstructured, the standard client-go
// pattern for a CRD this codebase has no generated types for. Avoids
// pulling in Tekton's own (large) Go SDK for a handful of Create/Patch
// calls against 3 known, stable CRDs.
var (
	tektonTaskGVR    = schema.GroupVersionResource{Group: "tekton.dev", Version: "v1", Resource: "tasks"}
	tektonTaskRunGVR = schema.GroupVersionResource{Group: "tekton.dev", Version: "v1", Resource: "taskruns"}
)

const tektonTaskCRDName = "tasks.tekton.dev"

// applyTektonPipeline is fx.tekton-pipeline.v1: installs the real Tekton
// Pipelines controller cluster-wide (once per cluster -- idempotent, see
// clusterbootstrap.CRDInstalled's guard) and then creates a real Task
// (declaring a required workspace) plus a real, CORRECTLY-bound TaskRun
// in this environment's own namespace -- the healthy baseline
// f.tekton.task-missing-workspace-binding's handler later breaks by
// removing the workspace binding from a fresh TaskRun.
//
// Cluster-scoped install runs from a fixture handler (not a one-time
// deploy-time step) because this codebase has no separate cluster-
// bootstrap phase in its provisioning pipeline -- the first environment
// that seeds this fixture pays the one-time install cost (tens of
// seconds), every later one is a fast no-op via CRDInstalled's check.
func applyTektonPipeline(ctx context.Context, provisioner *k8s.Provisioner, envID, namespace string) error {
	restConfig := provisioner.RestConfig()

	installed, err := clusterbootstrap.CRDInstalled(ctx, restConfig, tektonTaskCRDName)
	if err != nil {
		return fmt.Errorf("checking Tekton CRD installed: %w", err)
	}
	if !installed {
		installCtx, cancel := context.WithTimeout(ctx, 120*time.Second)
		defer cancel()
		if err := clusterbootstrap.ApplyManifestURL(installCtx, restConfig, "https://storage.googleapis.com/tekton-releases/pipeline/latest/release.yaml"); err != nil {
			return fmt.Errorf("installing Tekton Pipelines: %w", err)
		}
		waitCtx, waitCancel := context.WithTimeout(ctx, 60*time.Second)
		defer waitCancel()
		if err := clusterbootstrap.WaitForCRDEstablished(waitCtx, restConfig, tektonTaskCRDName); err != nil {
			return fmt.Errorf("waiting for Tekton CRDs to establish: %w", err)
		}
	}

	dyn, err := dynamic.NewForConfig(restConfig)
	if err != nil {
		return fmt.Errorf("building dynamic client: %w", err)
	}

	const taskName = "practice-task"
	task := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "tekton.dev/v1",
		"kind":       "Task",
		"metadata": map[string]any{
			"name":      taskName,
			"namespace": namespace,
		},
		"spec": map[string]any{
			"workspaces": []any{
				map[string]any{"name": "source", "description": "workspace this Task requires to run"},
			},
			"steps": []any{
				map[string]any{
					"name":   "write",
					"image":  "docker.io/library/busybox:latest",
					"script": "#!/bin/sh\necho ok > $(workspaces.source.path)/marker.txt\n",
				},
			},
		},
	}}
	if _, err := dyn.Resource(tektonTaskGVR).Namespace(namespace).Create(ctx, task, metav1.CreateOptions{}); err != nil && !apierrors.IsAlreadyExists(err) {
		return fmt.Errorf("creating Tekton Task %s: %w", taskName, err)
	}

	// A real PVC-backed workspace for the healthy-baseline TaskRun (and
	// for the fault handler's broken TaskRun to correctly omit later).
	pvcs := provisioner.Clientset().CoreV1().PersistentVolumeClaims(namespace)
	const pvcName = "practice-task-workspace"
	storageClass := "" // cluster default
	pvc := &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{Name: pvcName, Namespace: namespace},
		Spec: corev1.PersistentVolumeClaimSpec{
			AccessModes:      []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
			StorageClassName: &storageClass,
			Resources: corev1.VolumeResourceRequirements{
				Requests: corev1.ResourceList{corev1.ResourceStorage: resource.MustParse("64Mi")},
			},
		},
	}
	if _, err := pvcs.Create(ctx, pvc, metav1.CreateOptions{}); err != nil && !apierrors.IsAlreadyExists(err) {
		return fmt.Errorf("creating Tekton workspace PVC %s: %w", pvcName, err)
	}

	const healthyTaskRunName = "practice-taskrun-healthy"
	taskRun := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "tekton.dev/v1",
		"kind":       "TaskRun",
		"metadata": map[string]any{
			"name":      healthyTaskRunName,
			"namespace": namespace,
		},
		"spec": map[string]any{
			"taskRef": map[string]any{"name": taskName},
			"workspaces": []any{
				map[string]any{
					"name":                  "source",
					"persistentVolumeClaim": map[string]any{"claimName": pvcName},
				},
			},
		},
	}}
	if _, err := dyn.Resource(tektonTaskRunGVR).Namespace(namespace).Create(ctx, taskRun, metav1.CreateOptions{}); err != nil && !apierrors.IsAlreadyExists(err) {
		return fmt.Errorf("creating healthy Tekton TaskRun %s: %w", healthyTaskRunName, err)
	}

	return nil
}
