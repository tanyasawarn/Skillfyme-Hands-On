package faultinjection

import (
	"context"
	"errors"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/kubernetes/fake"
)

// PLAN.md Phase 3's U7: getOrNotFound is the get-then-classify-error
// pattern every fault handler in this package hand-copied once per K8s
// resource kind before this extraction.

func TestGetOrNotFound_ReturnsObjectOnSuccess(t *testing.T) {
	clientset := fake.NewSimpleClientset(&appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: "checkout", Namespace: testNamespace},
	})

	obj, result, err := getOrNotFound(
		context.Background(),
		func(ctx context.Context) (*appsv1.Deployment, error) {
			return clientset.AppsV1().Deployments(testNamespace).Get(ctx, "checkout", metav1.GetOptions{})
		},
		"Deployment", "deployment", "checkout",
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if obj == nil || obj.Name != "checkout" {
		t.Fatalf("expected the real Deployment object back, got %+v", obj)
	}
	if result != (Result{}) {
		t.Errorf("expected a zero Result on success, got %+v", result)
	}
}

func TestGetOrNotFound_NotFoundReturnsNotFoundResult(t *testing.T) {
	clientset := fake.NewSimpleClientset() // empty -- nothing named "checkout" exists

	_, result, err := getOrNotFound(
		context.Background(),
		func(ctx context.Context) (*appsv1.Deployment, error) {
			return clientset.AppsV1().Deployments(testNamespace).Get(ctx, "checkout", metav1.GetOptions{})
		},
		"Deployment", "deployment", "checkout",
	)
	if err == nil {
		t.Fatal("expected a not-found error, got nil")
	}
	if result.Applied {
		t.Error("expected Applied=false on a not-found target")
	}
	wantMsg := `Deployment "checkout" not found in namespace -- fault targets a resource that doesn't exist yet (no fixture has seeded it, or the learner hasn't created it)`
	if err.Error() != wantMsg {
		t.Errorf("expected the exact notFoundResult() message, got: %v", err)
	}
}

func TestGetOrNotFound_NodeNotFoundUsesClusterScope(t *testing.T) {
	// Regression coverage for notFoundResult's own Node special-case
	// (cluster-scoped, "not found in cluster" not "in namespace") --
	// confirms the generic helper still routes through that logic
	// correctly rather than hardcoding the namespaced wording.
	_, _, err := getOrNotFound(
		context.Background(),
		func(ctx context.Context) (*struct{ Name string }, error) {
			return nil, apiNotFoundError()
		},
		"Node", "node", "worker-1",
	)
	if err == nil {
		t.Fatal("expected an error")
	}
	if got := err.Error(); got != `Node "worker-1" not found in cluster -- fault targets a resource that doesn't exist yet (no fixture has seeded it, or the learner hasn't created it)` {
		t.Errorf("expected cluster-scoped wording for Node, got: %v", got)
	}
}

func TestGetOrNotFound_OtherErrorsAreWrappedNotSwallowed(t *testing.T) {
	wantErr := errors.New("connection refused")

	_, result, err := getOrNotFound(
		context.Background(),
		func(ctx context.Context) (*appsv1.Deployment, error) {
			return nil, wantErr
		},
		"Deployment", "deployment", "checkout",
	)
	if err == nil {
		t.Fatal("expected a wrapped error, got nil")
	}
	if !errors.Is(err, wantErr) {
		t.Errorf("expected the original error to be wrapped (errors.Is true), got: %v", err)
	}
	if result.Applied {
		t.Error("expected Applied=false on a real get failure")
	}
}

func TestGetOrNotFound_UsesTheGivenErrLabelNotTheResourceKind(t *testing.T) {
	// Regression for the irregular-abbreviation cases the original 9
	// call sites had (e.g. "getting pvc %s", not "getting
	// PersistentVolumeClaim %s") -- confirms errLabel is used verbatim
	// in the wrapped-error message, not derived from resourceKind.
	_, _, err := getOrNotFound(
		context.Background(),
		func(ctx context.Context) (*appsv1.Deployment, error) {
			return nil, errors.New("boom")
		},
		"PersistentVolumeClaim", "pvc", "data-0",
	)
	if err == nil || err.Error() != "getting pvc data-0: boom" {
		t.Errorf(`expected "getting pvc data-0: boom", got: %v`, err)
	}
}

// apiNotFoundError builds a real apierrors-recognized NotFound error
// (isNotFound() narrows apierrors.IsNotFound, which checks the error's
// Status.Reason, not just that it's *some* error -- a plain errors.New
// would not be recognized as "not found" by the real code path).
func apiNotFoundError() error {
	return apierrors.NewNotFound(schema.GroupResource{Group: "", Resource: "nodes"}, "worker-1")
}
