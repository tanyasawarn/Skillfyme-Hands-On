package faultinjection

import (
	"context"
	"fmt"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"

	"github.com/tanyasawarn/skillfyme-hands-on/orchestrator/internal/k8s"
)

// Fifth batch: the first of the 17 ReasonNoBaselineFixture faults now
// wired for real, backed by fx.tekton-pipeline.v1
// (internal/fixture/handlers_tekton.go) provisioning a real Task +
// PVC-backed workspace baseline first. See that fixture's doc comment
// for why Tekton uses dynamic.Interface/unstructured rather than a
// generated typed client -- this handler follows the same pattern, and
// is registered as a DynamicHandler (not Handler) for exactly that
// reason.
func init() {
	registerDynamic("f.tekton.task-missing-workspace-binding", applyTektonTaskMissingWorkspaceBinding)
}

var (
	tektonTaskGVR    = schema.GroupVersionResource{Group: "tekton.dev", Version: "v1", Resource: "tasks"}
	tektonTaskRunGVR = schema.GroupVersionResource{Group: "tekton.dev", Version: "v1", Resource: "taskruns"}
)

// applyTektonTaskMissingWorkspaceBinding: content/faults/f.tekton.task-missing-workspace-binding.yaml
// params: taskrun (name for the new, broken TaskRun -- distinct from the
// fixture's own healthy "practice-taskrun-healthy" TaskRun, since a
// TaskRun's spec is immutable once created, so the fault can't just
// strip the workspace binding off the existing one), missing_workspace
// (the workspace name to omit -- must match the Task's own declared
// workspace name, "source", for the omission to actually manifest the
// fault; validated against the real Task object below rather than
// trusted blindly).
func applyTektonTaskMissingWorkspaceBinding(ctx context.Context, provisioner *k8s.Provisioner, namespace string, params map[string]string) (Result, error) {
	taskRunName := params["taskrun"]
	missingWorkspace := params["missing_workspace"]
	if taskRunName == "" || missingWorkspace == "" {
		return Result{}, fmt.Errorf("f.tekton.task-missing-workspace-binding requires params: taskrun, missing_workspace")
	}

	dyn, err := dynamic.NewForConfig(provisioner.RestConfig())
	if err != nil {
		return Result{}, fmt.Errorf("building dynamic client: %w", err)
	}

	const taskName = "practice-task"
	task, err := dyn.Resource(tektonTaskGVR).Namespace(namespace).Get(ctx, taskName, metav1.GetOptions{})
	if err != nil {
		return notFoundResult("Task", taskName)
	}

	declaredWorkspaces, _, _ := unstructured.NestedSlice(task.Object, "spec", "workspaces")
	found := false
	for _, w := range declaredWorkspaces {
		wm, ok := w.(map[string]any)
		if ok && wm["name"] == missingWorkspace {
			found = true
			break
		}
	}
	if !found {
		return Result{}, fmt.Errorf("Task %s declares no workspace named %q -- fault params don't match the fixture's real Task", taskName, missingWorkspace)
	}

	// A TaskRun is immutable once created (Tekton rejects a spec Update
	// after creation) -- the fault manifests by creating a NEW TaskRun
	// that invokes the same Task but deliberately omits the workspace
	// binding, rather than mutating the fixture's existing healthy
	// TaskRun. Idempotent: re-running against an already-created broken
	// TaskRun of the same name is a no-op success (same "already applied"
	// stance every other handler in this package takes).
	broken := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "tekton.dev/v1",
		"kind":       "TaskRun",
		"metadata": map[string]any{
			"name":      taskRunName,
			"namespace": namespace,
		},
		"spec": map[string]any{
			"taskRef": map[string]any{"name": taskName},
			// workspaces: intentionally omitted -- this is the fault.
		},
	}}

	_, err = dyn.Resource(tektonTaskRunGVR).Namespace(namespace).Get(ctx, taskRunName, metav1.GetOptions{})
	if err == nil {
		return Result{Applied: true, SymptomVerified: true}, nil
	}
	if !isNotFound(err) {
		return Result{}, fmt.Errorf("checking existing TaskRun %s: %w", taskRunName, err)
	}

	created, err := dyn.Resource(tektonTaskRunGVR).Namespace(namespace).Create(ctx, broken, metav1.CreateOptions{})
	if err != nil {
		return Result{}, fmt.Errorf("creating broken TaskRun %s: %w", taskRunName, err)
	}

	workspaces, _, _ := unstructured.NestedSlice(created.Object, "spec", "workspaces")
	verified := len(workspaces) == 0
	return Result{Applied: true, SymptomVerified: verified}, nil
}
