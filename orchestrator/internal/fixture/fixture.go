// Package fixture implements PLAN.md's M1.3 milestone: "Idempotent,
// ordered, checksummed fixture application step in the provisioning
// pipeline" (doc §5.5 step 3). Every activity's environment.seed
// declares a list of fixture ids (contracts/activity_spec.schema.json:
// seed: [{fixture: string}]) applied in order, after the namespace
// template and workspace pod exist but before the health gate runs
// (doc §5.5's step ordering: "create ns -> quota/netpol -> create pods
// -> fixture apply -> health gate").
//
// Same registry pattern as internal/faultinjection (register/Apply,
// typed not-implemented error) -- deliberately the same shape as a
// sibling package solving a structurally similar problem (content
// declares an id, a Go handler makes it real), not a coincidence.
package fixture

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/tanyasawarn/skillfyme-hands-on/orchestrator/internal/k8s"
)

// Handler applies one fixture against an already-provisioned
// environment's namespace (the workspace pod exists and is Ready by the
// time fixtures run -- doc §5.5's step ordering places fixture-apply
// after pod creation).
type Handler func(ctx context.Context, provisioner *k8s.Provisioner, envID, namespace string) error

var registry = map[string]Handler{}

func register(fixtureID string, h Handler) {
	if _, exists := registry[fixtureID]; exists {
		panic(fmt.Sprintf("fixture: duplicate handler registered for %s", fixtureID))
	}
	registry[fixtureID] = h
}

// ErrNoHandler distinguishes "this fixture has no real implementation
// yet" from a handler-internal failure -- same rationale as
// faultinjection.ErrNoHandler: most of content's authored fixture ids
// (see content/activities/*.yaml's seed: blocks) don't have a handler
// here yet, and a caller needs to tell the two cases apart.
type ErrNoHandler struct{ FixtureID string }

func (e ErrNoHandler) Error() string {
	return fmt.Sprintf("no fixture handler implemented for %s", e.FixtureID)
}

// Checksum is a simple content-addressed idempotency guard: Apply skips
// re-running a fixture whose checksum was already recorded as applied
// for this environment (see AppliedTracker). Doc §5.5 step 3's own
// wording -- "idempotent, ordered, checksummed" -- names checksumming as
// the mechanism, not just a nice-to-have: a retried Provision() call (or
// a future re-seed-in-place operation) must not re-run a fixture whose
// content hasn't changed, but MUST re-run one whose content has (e.g. a
// fixture handler's implementation was updated to fix a bug).
type Checksum = string

// checksums maps fixture id -> a stable, deterministic version tag for
// that fixture's CURRENT implementation. This is the "content" a
// checksum is taken over: since fixtures are Go handlers (not files on
// disk the way solution_apply scripts are), the checksum is a
// hand-maintained version string per handler -- bump it whenever a
// handler's behavior changes, so AppliedTracker correctly treats it as
// "different content, re-apply" rather than silently skipping a fixture
// whose implementation changed underneath an already-provisioned
// environment. This mirrors doc §3.6's ActivityVersion immutability
// story at a smaller scale: content identity is (id, version), not just
// id.
var checksums = map[string]Checksum{}

func registerChecksum(fixtureID string, checksum Checksum) {
	checksums[fixtureID] = checksum
}

// AppliedTracker records which (fixture id, checksum) pairs have already
// been applied to a given environment, so Apply can skip a fixture
// that's already in its target state -- the "idempotent" half of doc
// §5.5 step 3. A minimal interface (not a concrete store) so the real
// implementation (Postgres-backed, see internal/orchestrator's caller)
// stays outside this package, matching internal/faultinjection's own
// "this package doesn't own persistence" stance.
type AppliedTracker interface {
	// IsApplied reports whether fixtureID at this exact checksum has
	// already been applied to envID.
	IsApplied(ctx context.Context, envID, fixtureID string, checksum Checksum) (bool, error)
	// MarkApplied records that fixtureID at this checksum has now been
	// applied to envID.
	MarkApplied(ctx context.Context, envID, fixtureID string, checksum Checksum) error
}

// Apply runs fixtureID's handler against envID, in the caller-supplied
// order (Apply itself doesn't sequence a list -- see ApplyAll for the
// ordered-list half of doc §5.5 step 3's "idempotent, ordered,
// checksummed"). Skips (returns nil without calling the handler) if
// tracker reports this exact checksum already applied.
func Apply(ctx context.Context, provisioner *k8s.Provisioner, tracker AppliedTracker, envID, namespace, fixtureID string) error {
	handler, ok := registry[fixtureID]
	if !ok {
		return ErrNoHandler{FixtureID: fixtureID}
	}
	checksum := checksums[fixtureID] // zero value "" is a valid checksum for a handler that never registered one explicitly

	if tracker != nil {
		already, err := tracker.IsApplied(ctx, envID, fixtureID, checksum)
		if err != nil {
			return fmt.Errorf("checking applied state for fixture %s: %w", fixtureID, err)
		}
		if already {
			return nil
		}
	}

	if err := handler(ctx, provisioner, envID, namespace); err != nil {
		return fmt.Errorf("applying fixture %s: %w", fixtureID, err)
	}

	if tracker != nil {
		if err := tracker.MarkApplied(ctx, envID, fixtureID, checksum); err != nil {
			return fmt.Errorf("recording applied state for fixture %s: %w", fixtureID, err)
		}
	}
	return nil
}

// ApplyAll runs every fixtureID in fixtureIDs, IN ORDER -- doc §5.5 step
// 3's "ordered" requirement: a later fixture may depend on an earlier
// one's state (e.g. a repo-clone fixture before a k8s-manifest-apply
// fixture that deploys code from that repo), so fixtures are never
// parallelized or reordered.
//
// ErrNoHandler for one fixture does NOT stop the batch -- most of
// content's authored fixture ids don't have a handler yet (same
// "content authored ahead of platform work" gap internal/faultinjection
// documents for faults), and one unimplemented fixture in an activity's
// seed: list must not silently prevent every LATER fixture in that same
// list from applying. All ErrNoHandler fixture ids encountered are
// collected and returned together (wrapped in a single ErrNoHandler
// listing every missing id) after every fixture that DOES have a
// handler has run -- the caller can distinguish "some fixtures are
// unimplemented" (a content/platform gap, not fatal) from any other
// error (which stops the batch immediately, since a real failure mid-list
// means a later fixture's assumed starting state may not hold).
func ApplyAll(ctx context.Context, provisioner *k8s.Provisioner, tracker AppliedTracker, envID, namespace string, fixtureIDs []string) error {
	// Every multi-pod fixture needs its own pods to reach each other
	// within the namespace -- the real T1/T2 NetworkPolicy baseline
	// (default-deny + egress-proxy-allowlist) doesn't grant that by
	// itself. Applied once here, idempotently, rather than per-fixture,
	// since it's not any one fixture's concern. See
	// ensureIntraNamespacePodTrafficAllowed's doc comment.
	if len(fixtureIDs) > 0 && provisioner.Clientset() != nil {
		if err := ensureIntraNamespacePodTrafficAllowed(ctx, provisioner.Clientset(), namespace); err != nil {
			return fmt.Errorf("allowing intra-namespace pod traffic: %w", err)
		}
	}

	var missing []string
	for _, id := range fixtureIDs {
		err := Apply(ctx, provisioner, tracker, envID, namespace, id)
		if err == nil {
			continue
		}
		var noHandler ErrNoHandler
		if errors.As(err, &noHandler) {
			missing = append(missing, id)
			continue
		}
		return err
	}
	if len(missing) > 0 {
		return ErrNoHandler{FixtureID: strings.Join(missing, ", ")}
	}
	return nil
}
