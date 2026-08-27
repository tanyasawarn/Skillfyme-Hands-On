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
	"encoding/json"
	"fmt"

	corev1 "k8s.io/api/core/v1"
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
//
// Takes kubernetes.Interface, not the concrete *kubernetes.Clientset --
// *kubernetes.Clientset already satisfies this interface, so Apply's call
// to provisioner.Clientset() below is unchanged, but handlers become
// testable against k8s.io/client-go/kubernetes/fake without a live
// cluster (see handlers_batch2_test.go).
type Handler func(ctx context.Context, clientset kubernetes.Interface, namespace string, params map[string]string) (Result, error)

// DynamicHandler is Handler's counterpart for faults that target a CRD
// this package has no generated typed client for (Tekton, Istio, Argo
// CD -- see handlers_batch5.go onward) and so need
// client-go/dynamic.Interface + unstructured objects instead of a
// kubernetes.Interface call. A second registry rather than changing
// Handler's own signature: 13 existing handlers only ever need the
// typed clientset, and threading an unused *rest.Config through every
// one of them (and every existing test's handler-call signature) just
// for the few CRD-targeting faults would be real, unjustified churn.
type DynamicHandler func(ctx context.Context, provisioner *k8s.Provisioner, namespace string, params map[string]string) (Result, error)

// registry maps fault_id -> Handler. Populated by registerHandlers() in
// handlers.go, kept as a package-level map (not a struct) since faults
// are content-defined, global, and stateless -- there is exactly one
// correct way to apply f.k8s.memory-limit-too-low regardless of which
// environment it targets.
var registry = map[string]Handler{}

// dynamicRegistry mirrors registry for DynamicHandler-registered faults.
// A fault_id is registered in exactly one of the two registries, never
// both -- registerDynamic panics on a collision with either map, same as
// register's own duplicate-registration panic.
var dynamicRegistry = map[string]DynamicHandler{}

func registerDynamic(faultID string, h DynamicHandler) {
	if _, exists := registry[faultID]; exists {
		panic(fmt.Sprintf("faultinjection: %s already registered as a Handler, cannot also register as DynamicHandler", faultID))
	}
	if _, exists := dynamicRegistry[faultID]; exists {
		panic(fmt.Sprintf("faultinjection: duplicate dynamic handler registered for %s", faultID))
	}
	dynamicRegistry[faultID] = h
}

// register is called from each handler file's init() (see handlers.go).
// Panics on a duplicate registration -- that's a content/code bug caught
// at process startup, not something to handle gracefully at runtime.
func register(faultID string, h Handler) {
	if _, exists := registry[faultID]; exists {
		panic(fmt.Sprintf("faultinjection: duplicate handler registered for %s", faultID))
	}
	if _, exists := dynamicRegistry[faultID]; exists {
		panic(fmt.Sprintf("faultinjection: %s already registered as a DynamicHandler, cannot also register as Handler", faultID))
	}
	registry[faultID] = h
}

// registerUnsupported registers faultID against a handler that always
// returns ErrUnsupportedMechanism{FaultID: faultID, Reason: reason} --
// used for faults that have been triaged (someone looked at what it
// would take to apply them) and found to need infrastructure this
// package doesn't have yet. This deliberately still occupies a registry
// slot (rather than being left absent, which would fall through to
// ErrNoHandler) so InjectFault can tell a caller *why* in a stable,
// machine-checkable way instead of "nobody's looked at this."
func registerUnsupported(faultID, reason string) {
	register(faultID, func(_ context.Context, _ kubernetes.Interface, _ string, _ map[string]string) (Result, error) {
		return Result{}, ErrUnsupportedMechanism{FaultID: faultID, Reason: reason}
	})
}

// ErrNoHandler distinguishes "this fault has no real implementation yet"
// from a handler-internal failure, so InjectFault's gRPC status code can
// differ (Unimplemented vs Internal/NotFound). It means the fault_id is
// simply not in registry at all -- genuinely un-triaged, not yet looked
// at.
type ErrNoHandler struct{ FaultID string }

func (e ErrNoHandler) Error() string {
	return fmt.Sprintf("no fault-injection handler implemented for %s", e.FaultID)
}

// ErrUnsupportedMechanism is returned by a fault that HAS been triaged
// and IS registered, but whose execution mechanism is architecturally
// out of reach right now -- distinct from ErrNoHandler ("nobody has
// looked at this yet"). Two concrete reasons this package uses it for:
//
//  1. The fault targets an object (a Jenkins agent, a Terraform state
//     lock, a Helm release, a Prometheus scrape config...) that no
//     blueprint or fixture provisions before InjectFault runs at T0 --
//     the tool itself is something the *learner* installs/configures as
//     part of the lab's own tasks, so there is nothing pre-existing for
//     the orchestrator to mutate via its K8s API access (the same
//     "orchestrator has cluster access, workspace pod does not" gap
//     documented in this package's doc comment, just for a target this
//     package can't apply to unless it *is* a K8s API object). Real
//     support needs a Production Sim fixture that provisions the target
//     tool first -- fixture/blueprint work, not a handler.
//  2. The fault requires an execution tier that doesn't exist yet
//     (T2_ISOLATED_MICROVM) -- see min_tier in content/faults/*.yaml.
//
// Reason is a short, stable machine-checkable tag (see the Reason*
// constants below), not a free-form string, so callers/tests can assert
// on *why* without string-matching prose.
type ErrUnsupportedMechanism struct {
	FaultID string
	Reason  string
}

func (e ErrUnsupportedMechanism) Error() string {
	return fmt.Sprintf("fault-injection mechanism unsupported for %s: %s", e.FaultID, e.Reason)
}

// Stable reason tags for ErrUnsupportedMechanism -- part of the public
// contract other packages (server.go's gRPC mapping, tests) match against.
const (
	// ReasonNoBaselineFixture: the fault's target tool/object has no
	// blueprint or fixture that provisions it before InjectFault runs,
	// so there's nothing to break yet (see case 1 in the doc above).
	ReasonNoBaselineFixture = "no_baseline_fixture"
	// ReasonTierUnavailable: the fault's min_tier (T2/T3) has no driver
	// implemented in this orchestrator yet (see case 2 above).
	ReasonTierUnavailable = "tier_unavailable"
	// ReasonMetricsContractPending: specific to
	// f.k8s.hpa-metrics-unavailable -- unlike every other fault, its
	// target is cluster infrastructure (the metrics-server
	// Deployment/APIService) rather than a single object a content
	// author names via params, so it needs a separate,
	// not-yet-designed contract (which metrics-server
	// deployment/namespace to degrade, and how). Kept distinct from
	// ReasonNoBaselineFixture because the blocker here is "the
	// contract shape isn't decided," not "no fixture provisions the
	// target" -- deferred pending that design, not pending content
	// work.
	ReasonMetricsContractPending = "metrics_degradation_contract_pending"
)

// Apply looks up and runs the handler for faultID. envID is the
// orchestrator's environment id (not the raw K8s namespace name) --
// namespace resolution happens here via k8s.NamespaceForEnv so callers
// don't duplicate that convention. Checks registry first, then
// dynamicRegistry -- a faultID is registered in at most one (register/
// registerDynamic both panic on a cross-registry collision at init time,
// so this ordering never masks a real registration in the other map).
func Apply(ctx context.Context, provisioner *k8s.Provisioner, envID, faultID string, params map[string]string) (Result, error) {
	ns := k8s.NamespaceForEnv(envID)

	if handler, ok := registry[faultID]; ok {
		return handler(ctx, provisioner.Clientset(), ns, params)
	}
	if handler, ok := dynamicRegistry[faultID]; ok {
		return handler(ctx, provisioner, ns, params)
	}
	return Result{}, ErrNoHandler{FaultID: faultID}
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

// getOrNotFound is PLAN.md Phase 3's U7: the
// "Get -> isNotFound? notFoundResult() : Get -> other err? wrap : ok"
// pattern was hand-copied 9 times across handlers.go/handlers_batch2.go/
// handlers_batch4.go, once per K8s resource kind this package's fault
// handlers target (Deployment, Service, ConfigMap, Node, ResourceQuota,
// PersistentVolumeClaim, StatefulSet, NetworkPolicy).
//
// Generic over T (the concrete K8s object type each subresource client's
// own Get() returns -- *appsv1.Deployment, *corev1.Service, etc.) since
// client-go has no single shared "Get(ctx, name) (T, error)" interface
// across resource kinds; get is a thin closure each call site provides
// wrapping its own typed .Get() call, letting this function stay generic
// over the RESULT rather than needing a generic K8s client abstraction
// (a much bigger, out-of-scope undertaking).
//
// Takes BOTH resourceKind (display-cased, for notFoundResult's
// "Deployment %q not found") and errLabel (the lowercase noun for the
// generic-error wrap, "getting %s %s") as separate parameters rather
// than deriving one from the other -- the original 9 call sites'
// wording doesn't actually follow a mechanical lowercase-first-letter
// rule ("PersistentVolumeClaim" -> "pvc", "ResourceQuota" ->
// "resourcequota", both irregular abbreviations/compressions a
// derivation would have gotten wrong), so preserving the exact original
// wording per call site needs an explicit second string, not a
// computed one.
//
// Returns (T, Result, error) rather than just (T, error): on the
// NotFound path there is no T to return (the zero value would be
// misleading -- callers must check the returned bool-like "did this
// succeed" via whether err is nil, matching every existing call site's
// own `if isNotFound(err) { return notFoundResult(...) }` early-return
// shape) and the caller needs the exact Result{} + wrapped-error pair
// notFoundResult already produces, not a generic error it would have to
// re-wrap into a Result itself.
func getOrNotFound[T any](ctx context.Context, get func(context.Context) (T, error), resourceKind, errLabel, name string) (T, Result, error) {
	obj, err := get(ctx)
	if isNotFound(err) {
		result, wrapErr := notFoundResult(resourceKind, name)
		return obj, result, wrapErr
	}
	if err != nil {
		return obj, Result{}, fmt.Errorf("getting %s %s: %w", errLabel, name, err)
	}
	return obj, Result{}, nil
}

// containerPatch is PLAN.md Phase 3's U11's real fix target: a
// hand-declared (NOT corev1.Container reused directly) struct
// representing exactly the fields this package's 4 patch call sites
// actually set. Reusing corev1.Container directly was the first attempt
// -- reverted after this file's own tests caught it producing wrong
// output: corev1.Probe's TimeoutSeconds/InitialDelaySeconds/
// PeriodSeconds are `int32` with `omitempty`, so a real corev1.Probe
// value with InitialDelaySeconds explicitly set to 0 marshals
// IDENTICALLY to one where it was never touched -- Go's encoding/json
// omitempty cannot distinguish "explicitly zero" from "zero value,
// unset" for non-pointer int types. That distinction is NOT cosmetic
// under strategic-merge-patch semantics: the original hand-written
// patches always emitted "initialDelaySeconds":0 explicitly, which
// resets that field to 0 on the live object regardless of what it was
// before; omitting the field instead leaves the live object's existing
// value untouched. A container whose probe already had a nonzero
// initialDelaySeconds would have kept that stale value silently under
// the corev1.Container-reuse version -- a real correctness regression
// this extraction's own tests caught before it shipped, not a
// theoretical concern.
//
// ReadinessProbe's three timing fields are *int32 here specifically so
// a caller can represent "explicitly set to 0" (a non-nil pointer to 0)
// distinctly from "don't touch this field" (a nil pointer, omitted via
// omitempty on the pointer itself, which DOES correctly distinguish nil
// from a pointed-to zero).
type containerPatch struct {
	Name      string                   `json:"name"`
	Image     string                   `json:"image,omitempty"`
	Resources *containerPatchResources `json:"resources,omitempty"`
	Readiness *containerPatchProbe     `json:"readinessProbe,omitempty"`
}

type containerPatchResources struct {
	Limits map[corev1.ResourceName]resource.Quantity `json:"limits,omitempty"`
}

type containerPatchProbe struct {
	Exec                *corev1.ExecAction `json:"exec,omitempty"`
	InitialDelaySeconds *int32             `json:"initialDelaySeconds,omitempty"`
	TimeoutSeconds      *int32             `json:"timeoutSeconds,omitempty"`
	PeriodSeconds       *int32             `json:"periodSeconds,omitempty"`
}

// int32Ptr lets call sites write patchFirstContainer(...,
// func(c *containerPatch) { c.Readiness.InitialDelaySeconds =
// int32Ptr(0) }) instead of needing a local variable just to take its
// address -- the exact "explicitly zero" case this whole redesign
// exists to make representable.
func int32Ptr(v int32) *int32 { return &v }

// containerPatchEnvelope mirrors the exact
// {"spec":{"template":{"spec":{"containers":[{...}]}}}} shape every
// patchFirstContainer call site needs.
type containerPatchEnvelope struct {
	Spec struct {
		Template struct {
			Spec struct {
				Containers []containerPatch `json:"containers"`
			} `json:"spec"`
		} `json:"template"`
	} `json:"spec"`
}

// patchFirstContainer is PLAN.md Phase 3's U11: builds a strategic
// merge patch targeting the named container within a Deployment or
// StatefulSet's pod template, via mutate -- a caller-supplied function
// that fills in only the containerPatch fields it actually wants
// patched. Replaces 4 hand-built fmt.Sprintf JSON strings (a mix of %q,
// safe, and %d, safe given each int WAS validated before interpolation
// -- see applyReadinessProbeTooAggressive's strconv.Atoi guard -- so not
// unsafe in practice, but fragile by construction: nothing checked the
// result was even valid JSON) with a real struct marshal, which cannot
// produce malformed JSON by definition and is unit-tested byte-for-byte
// against every original call site's exact output (see
// patch_first_container_test.go).
func patchFirstContainer(containerName string, mutate func(*containerPatch)) ([]byte, types.PatchType, error) {
	container := containerPatch{Name: containerName}
	mutate(&container)

	var envelope containerPatchEnvelope
	envelope.Spec.Template.Spec.Containers = []containerPatch{container}

	patchBytes, err := json.Marshal(envelope)
	if err != nil {
		return nil, "", fmt.Errorf("marshaling container patch for %s: %w", containerName, err)
	}
	return patchBytes, types.StrategicMergePatchType, nil
}

// mustQuantity parses a resource.Quantity from a fault param, panicking
// only on genuinely-impossible input (content-authored params_schema
// values, not learner input) -- callers validate presence before calling
// this so a panic here means a fault YAML shipped a malformed default.
func parseQuantity(s string) (resource.Quantity, error) {
	return resource.ParseQuantity(s)
}
