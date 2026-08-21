// Package faultinjection implements the real (non-stub) side of Phase 2's
// InjectFault RPC (contracts/orchestrator.proto, PLAN.md Phase 2). Doc
// §3.3's contract: "Environment provisioned to a broken state via a
// fault injection manifest applied after a healthy baseline is
// confirmed." This package is the "apply" half -- it takes a fault_id +
// params and performs the real K8s API mutation that manifests the
// fault, using the same clientset internal/k8s.Provisioner already uses
// (not internal execution inside the workspace pod: the workspace image
// is bare ubuntu with no kubectl/cluster credentials, confirmed against
// the real M1.11 image gap -- so fault application has to happen from
// the orchestrator process, which does have cluster access, not from
// inside the learner's pod).
//
// Design constraint this package is honest about: a fault breaks a
// resource that must already exist in the environment (doc's own
// contract implies a pre-seeded baseline). No Production Sim fixtures
// exist yet to guarantee that (content/faults/ is Guided-Lab-adjacent
// content only, authored ahead of the fixture/blueprint work Phase 2
// still needs) -- every handler here returns a clear not-found error
// rather than silently no-op'ing when its target resource doesn't exist,
// so a caller can distinguish "fault genuinely applied" from "nothing to
// break yet."
package faultinjection

import (
	"context"
	"fmt"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"

	"github.com/tanyasawarn/skillfyme-hands-on/orchestrator/internal/k8s"
)

// Result mirrors contracts/orchestrator.proto's InjectFaultResponse
// fields exactly, so internal/orchestrator/server.go's InjectFault RPC
// handler is a thin pass-through.
type Result struct {
	Applied         bool
	SymptomVerified bool
}

// Handler applies one fault against an already-provisioned environment's
// namespace. params come from InjectFaultRequest.params (proto: map<string,
// string>) -- content authors document each fault's expected keys in its
// YAML's params_schema (contracts/fault.schema.json), but the wire type
// is a plain string map, so handlers parse/validate their own params.
type Handler func(ctx context.Context, clientset *kubernetes.Clientset, namespace string, params map[string]string) (Result, error)

// registry maps fault_id -> Handler. Populated by registerHandlers() in
// handlers.go, kept as a package-level map (not a struct) since faults
// are content-defined, global, and stateless -- there is exactly one
// correct way to apply f.k8s.memory-limit-too-low regardless of which
// environment it targets.
var registry = map[string]Handler{}

// register is called from each handler file's init() (see handlers.go).
// Panics on a duplicate registration -- that's a content/code bug caught
// at process startup, not something to handle gracefully at runtime.
func register(faultID string, h Handler) {
	if _, exists := registry[faultID]; exists {
		panic(fmt.Sprintf("faultinjection: duplicate handler registered for %s", faultID))
	}
	registry[faultID] = h
}

// ErrNoHandler distinguishes "this fault has no real implementation yet"
// (most of the 35-fault library -- only a first batch is wired) from a
// handler-internal failure, so InjectFault's gRPC status code can differ
// (Unimplemented vs Internal/NotFound).
type ErrNoHandler struct{ FaultID string }

func (e ErrNoHandler) Error() string {
	return fmt.Sprintf("no fault-injection handler implemented for %s", e.FaultID)
}

// Apply looks up and runs the handler for faultID. envID is the
// orchestrator's environment id (not the raw K8s namespace name) --
// namespace resolution happens here via k8s.NamespaceForEnv so callers
// don't duplicate that convention.
func Apply(ctx context.Context, provisioner *k8s.Provisioner, envID, faultID string, params map[string]string) (Result, error) {
	handler, ok := registry[faultID]
	if !ok {
		return Result{}, ErrNoHandler{FaultID: faultID}
	}
	ns := k8s.NamespaceForEnv(envID)
	return handler(ctx, provisioner.Clientset(), ns, params)
}

// notFoundResult is the shared shape handlers return when their target
// resource doesn't exist yet -- see package doc: this is a real, expected
// outcome given no fixtures pre-seed fault targets, not a bug to paper
// over. resourceKind determines the wording since not every fault target
// is namespaced (e.g. Node is cluster-scoped -- "not found in namespace"
// would be a false claim there, caught by a live test against a real
// nonexistent-node param).
func notFoundResult(resourceKind, name string) (Result, error) {
	scope := "in namespace"
	if resourceKind == "Node" {
		scope = "in cluster"
	}
	return Result{Applied: false, SymptomVerified: false},
		fmt.Errorf("%s %q not found %s -- fault targets a resource that doesn't exist yet (no fixture has seeded it, or the learner hasn't created it)", resourceKind, name, scope)
}

// isNotFound narrows apierrors.IsNotFound to this package's call sites
// without re-importing it everywhere.
func isNotFound(err error) bool {
	return apierrors.IsNotFound(err)
}

// strategicMergePatch is a small helper: JSON merge-patch bytes for the
// common "patch one field on an existing object" case every T1 fault
// handler in this first batch needs.
func strategicMergePatch(patchJSON string) ([]byte, types.PatchType) {
	return []byte(patchJSON), types.StrategicMergePatchType
}

// mustQuantity parses a resource.Quantity from a fault param, panicking
// only on genuinely-impossible input (content-authored params_schema
// values, not learner input) -- callers validate presence before calling
// this so a panic here means a fault YAML shipped a malformed default.
func parseQuantity(s string) (resource.Quantity, error) {
	return resource.ParseQuantity(s)
}
