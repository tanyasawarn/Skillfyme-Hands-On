package k8s

import (
	"errors"
	"testing"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

// PLAN.md Phase 3's U10: ignoreAlreadyExists is the pure decision every
// applyX() Create-then-check-AlreadyExists call site in this file
// duplicated 8 times before this extraction.

func TestIgnoreAlreadyExists_AlreadyExistsErrorBecomesNil(t *testing.T) {
	gr := schema.GroupResource{Group: "", Resource: "namespaces"}
	err := apierrors.NewAlreadyExists(gr, "env-abc123")

	got := ignoreAlreadyExists(err)
	if got != nil {
		t.Errorf("expected AlreadyExists to be swallowed (idempotent retry), got: %v", got)
	}
}

func TestIgnoreAlreadyExists_NilStaysNil(t *testing.T) {
	if got := ignoreAlreadyExists(nil); got != nil {
		t.Errorf("expected nil in, nil out, got: %v", got)
	}
}

func TestIgnoreAlreadyExists_OtherErrorsPassThroughUnchanged(t *testing.T) {
	// A real, unrelated failure (e.g. a connection error, a validation
	// error, a NotFound on some OTHER call) must not be silently
	// swallowed -- only the specific AlreadyExists case is idempotent-
	// safe to ignore. Confirms this doesn't degrade into "ignore every
	// Create error," which would hide genuine provisioning failures.
	original := errors.New("connection refused")
	got := ignoreAlreadyExists(original)
	if got != original {
		t.Errorf("expected the original non-AlreadyExists error to pass through unchanged, got: %v", got)
	}
}

func TestIgnoreAlreadyExists_NotFoundIsNotConfusedWithAlreadyExists(t *testing.T) {
	// A real bug this test would catch: a naive implementation that
	// checked "is this ANY apierrors.APIStatus error" rather than
	// specifically IsAlreadyExists would also swallow a NotFound,
	// hiding a genuinely different failure mode (e.g. the target
	// namespace itself doesn't exist yet, a real provisioning ordering
	// bug) behind the same "safe to ignore" treatment.
	gr := schema.GroupResource{Group: "", Resource: "namespaces"}
	notFoundErr := apierrors.NewNotFound(gr, "env-abc123")

	got := ignoreAlreadyExists(notFoundErr)
	if got == nil {
		t.Error("SECURITY/CORRECTNESS REGRESSION: a NotFound error must never be silently swallowed as if it were AlreadyExists")
	}
	if got != notFoundErr {
		t.Errorf("expected the NotFound error to pass through unchanged, got: %v", got)
	}
}
