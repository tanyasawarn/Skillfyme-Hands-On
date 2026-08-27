package faultinjection

import "fmt"

// t2OnlyFaultIDs mirrors content/faults/*.yaml's min_tier:
// T2_ISOLATED_MICROVM declaration for exactly these 5 faults
// (contracts/fault.schema.json's own source of truth) -- duplicated
// here as a small Go-side set since this package has no content-YAML
// parsing of its own (server.go's practice-core caller is the one that
// actually reads content/faults/), and a fault whose K8s-level mutation
// assumes T2-only objects (Istio CRDs, Argo CD Applications reachable
// only from a T2-isolated workload) must never be attempted against a
// T1 environment's namespace even if a caller's own tier bookkeeping is
// wrong or stale.
var t2OnlyFaultIDs = map[string]bool{
	"f.gitops.argocd-out-of-sync-manual-drift":  true,
	"f.istio.mtls-mode-mismatch":                true,
	"f.istio.virtualservice-weight-sum-invalid": true,
}

// ErrTierPrecondition is returned by Apply when a T2-only fault is
// requested against a non-T2 environment. Distinct from
// ErrUnsupportedMechanism (which means "no handler exists for this
// fault at all") -- this means a real handler exists and IS being
// invoked, but the caller's own environment doesn't meet the fault's
// documented precondition. server.go maps this to gRPC
// codes.FailedPrecondition, not Unimplemented.
type ErrTierPrecondition struct {
	FaultID      string
	RequiredTier string
	ActualTier   string
}

func (e ErrTierPrecondition) Error() string {
	return fmt.Sprintf("fault %s requires tier %s, but this environment is tier %s", e.FaultID, e.RequiredTier, e.ActualTier)
}

// RequiresT2 reports whether faultID's content declares min_tier:
// T2_ISOLATED_MICROVM -- exported so server.go's InjectFault can check
// this BEFORE calling Apply, using the environment's own persisted tier
// (env.environment.tier), rather than only checking inside the handler
// after cluster mutations may have already started.
func RequiresT2(faultID string) bool {
	return t2OnlyFaultIDs[faultID]
}
