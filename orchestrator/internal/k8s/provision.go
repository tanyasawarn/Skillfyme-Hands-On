package k8s

import (
	"context"
	"fmt"
	"log"
	"time"

	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
)

// ResourceSpec mirrors contracts/orchestrator.proto's ResourceSpec message.
type ResourceSpec struct {
	CPU    string // e.g. "2"
	Memory string // e.g. "4Gi"
	Disk   string // e.g. "10Gi"
}

// PLAN.md Phase 4's K15: T1's "2 CPU / 4Gi" and T2's "8 CPU / 16Gi" were
// each independently hardcoded as bare string literals at 3 real sites
// -- limitRangeMaxFor's per-tier LimitRange ceiling, and
// createWorkspacePod/applyT2PodShape's own workspace container Limits
// fallback (used when a ProvisionRequest doesn't specify its own
// resources) -- a real gap this codebase's own comments already flagged
// (applyResourceQuota's doc comment derives the T2 numbers from doc
// §5.1's cost band, but that reasoning lived nowhere the workspace pod's
// own hardcoded "8"/"16Gi" fallback could reference it). DefaultT1Resources/
// DefaultT2Resources are now the single source of truth both the
// LimitRange ceiling and the workspace pod's own Limits fallback read
// from, so the two can no longer silently drift apart.
var (
	DefaultT1Resources = ResourceSpec{CPU: "2", Memory: "4Gi"}
	DefaultT2Resources = ResourceSpec{CPU: "8", Memory: "16Gi"}
)

// PLAN.md K13: "workspace"/"shell" were repeated as bare string literals
// 12+ times across 6 files (provision.go, sessionbroker/broker.go,
// sessionbroker/session_registry.go, orchestrator/credentials.go,
// idledetect/detector.go, validation/validation.go) with zero
// compile-time link between the Pod's own Name/ServiceAccountName/
// container Name fields and every other package's exec/metrics/attach
// target string -- a typo in any one of them wouldn't fail to compile,
// it would just silently stop matching the real object at runtime.
const (
	WorkspacePodName            = "workspace"
	WorkspaceContainerName      = "shell"
	WorkspaceServiceAccountName = "workspace"
)

// PLAN.md U13: the `metav1.ObjectMeta{Name: X, Namespace: ns}` struct
// literal was repeated verbatim at 6 real call sites (applyResourceQuota,
// applyLimitRange, applyDefaultDenyNetworkPolicy, applyEgressProxyAllowlist,
// applyServiceAccount, createWorkspacePod's Service) -- confirmed via
// grep before extracting: 2 other ObjectMeta literals in this file
// (createNamespace's cluster-scoped Namespace object, createWorkspacePod's
// own Pod) also set Labels, a genuinely different, 3-field shape, and
// stay as their own literals rather than being forced through this
// 2-field helper.
func ObjectMeta(name, ns string) metav1.ObjectMeta {
	return metav1.ObjectMeta{Name: name, Namespace: ns}
}

// PLAN.md K16: the managed-namespace label was created via a map
// literal (createNamespace) and matched via a differently-formatted
// selector string built by hand (ListManagedNamespaces's
// "practiceengine.dev/managed=true") -- zero compile-time link between
// the two, so a typo in either would silently break the reaper's orphan
// sweep (doc §5.6) without any error, just namespaces that stop being
// found. ManagedNamespaceLabelSelector is now built from the same two
// consts the label map itself uses.
const (
	ManagedNamespaceLabelKey   = "practiceengine.dev/managed"
	ManagedNamespaceLabelValue = "true"
)

var ManagedNamespaceLabelSelector = fmt.Sprintf("%s=%s", ManagedNamespaceLabelKey, ManagedNamespaceLabelValue)

// Tier selects which driver-specific pod shape Provision builds. Mirrors
// contracts/orchestrator.proto's Tier enum's two implemented values --
// TIER_T0_BROWSER and TIER_T3_CLOUD_ACCOUNT have no K8s-side driver at
// all (T0 never reaches this package; T3 is Phase 3 scope), so this type
// deliberately only names the two tiers this package can actually
// provision, rather than mirroring the full proto enum and having to
// explain why half its values panic.
type Tier int

const (
	// TierT1SharedContainer is the zero value so every existing caller
	// that doesn't set Tier (there were none before this field existed,
	// but any future caller that forgets to) gets the tier this package
	// has always provisioned, not an unrecognised/zero-Tier error.
	TierT1SharedContainer Tier = iota
	TierT2IsolatedMicroVM
)

// ProvisionRequest is the K8s-driver input; the gRPC layer (internal/orchestrator)
// translates the wire ProvisionRequest message into this before calling Provisioner.
type ProvisionRequest struct {
	AttemptID     string
	EnvID         string // namespace name: env-{env_id}
	Tier          Tier   // selects T1 vs T2 pod shape; see Provision's doc comment
	Image         string // base image for the shell container (image strategy, M1.11)
	Resources     ResourceSpec
	NetworkPolicy string // reserved for blueprint-declared egress allowlist domains (§9.2); default-deny is always applied regardless

	// PrivilegedWorkload, when true on a T2 request, additionally sets
	// securityContext.privileged on the shell container. Sysbox already
	// delivers real DinD / systemd / nested multi-node k3s WITHOUT
	// privileged (its whole point -- user-namespace isolation, not a
	// capability grant), so the default T2 pod is unprivileged. The only
	// T2 workloads that still need privileged are the handful of eBPF
	// program types Sysbox can't load unprivileged (LSM-BPF, some XDP);
	// a blueprint that declares an eBPF capability sets this so those
	// sims work, and pays the isolation cost (a privileged container on a
	// shared kernel) only for that content. Ignored for T1 (PSS
	// `restricted` forbids privileged there and nothing needs it).
	PrivilegedWorkload bool
}

// Provisioner implements doc §5.2's namespace-per-environment template:
// namespace + ResourceQuota + LimitRange + NetworkPolicy (default-deny) +
// ServiceAccount (no auto-mount) + PodSecurity (restricted) + the
// workspace pod itself.
type Provisioner struct {
	clientset  *kubernetes.Clientset
	restConfig *rest.Config
	cfg        ProvisionerConfig
}

// ProvisionerConfig carries the cluster-wide capability facts the pod
// shape depends on -- "does this cluster's node pool actually have X
// installed?" -- as opposed to per-request choices (which live on
// ProvisionRequest). Grouped into one struct so adding a new capability
// knob (gVisor, then the T2 runtime, and whatever comes next) doesn't
// churn NewProvisioner's signature and every test call site each time.
type ProvisionerConfig struct {
	// GVisorEnabled gates whether T1 pods get RuntimeClassName: gvisor.
	// Defaults false: hardcoding a RuntimeClass that doesn't exist on the
	// node makes every Provision() call fail scheduling, so this must be
	// an explicit opt-in from an operator who has confirmed gVisor is
	// actually installed on the T1 node pool -- same pattern as
	// Server.t2Enabled (internal/orchestrator/server.go) and
	// ORCHESTRATOR_T2_ENABLED.
	GVisorEnabled bool

	// T2RuntimeClass is the RuntimeClass name a T2 (isolated-workload)
	// pod is scheduled under. As of the ₹100/user cost decision this is
	// "sysbox-runc" (Sysbox: real DinD/systemd/nested-k3s on the SAME
	// shared node pool as T1, no dedicated metal, no microVM) rather than
	// "kata" (hardware-virtualised microVM on a dedicated bare-metal
	// pool -- kept as the documented scale-up path in
	// infra/practice-cluster/t2-nodepool-kata/, viable only once
	// concurrent T2 volume makes metal packing efficient). Configurable
	// via ORCHESTRATOR_T2_RUNTIME_CLASS so the switch back to "kata" (or
	// forward to something else) is an operator decision, not a rebuild.
	// Empty string means the same "no runtimeClassName set" behavior the
	// T1 path has when gVisor is disabled: the pod runs under the node's
	// default runtime -- honest for a local dev cluster with no Sysbox,
	// where it degrades to a plain (non-isolated) container rather than
	// failing to schedule.
	T2RuntimeClass string
}

// T2RuntimeClassDefault is the RuntimeClass a T2 pod uses unless an
// operator overrides ORCHESTRATOR_T2_RUNTIME_CLASS. See
// ProvisionerConfig.T2RuntimeClass for why Sysbox, not Kata.
const T2RuntimeClassDefault = "sysbox-runc"

func NewProvisioner(clientset *kubernetes.Clientset, restConfig *rest.Config, cfg ProvisionerConfig) *Provisioner {
	return &Provisioner{clientset: clientset, restConfig: restConfig, cfg: cfg}
}

// Clientset exposes the underlying K8s client for packages that need to
// operate on a *managed environment's own resources* rather than the
// namespace/pod-lifecycle primitives Provisioner itself owns -- e.g.
// internal/faultinjection patching a Deployment or ConfigMap inside an
// already-provisioned namespace (M1.2/Phase 2's InjectFault). Kept as a
// deliberate escape hatch rather than growing Provisioner into a
// catch-all K8s API surface for every resource kind a fault might touch.
func (p *Provisioner) Clientset() *kubernetes.Clientset {
	return p.clientset
}

// RestConfig exposes the raw *rest.Config for packages that need
// remotecommand.NewSPDYExecutor (pod exec), not just the typed clientset
// -- e.g. internal/validation's ExecValidator, same non-interactive exec
// mechanism sessionbroker already uses for the telemetry hook install.
func (p *Provisioner) RestConfig() *rest.Config {
	return p.restConfig
}

// NamespaceForEnv exposes the env-id -> namespace-name convention so
// other packages don't duplicate the naming rule (see also
// sessionbroker.namespaceForEnv, which duplicates this deliberately to
// avoid a broker->k8s package dependency; faultinjection has no such
// constraint, so it imports this instead of redefining it).
func NamespaceForEnv(envID string) string {
	return namespaceName(envID)
}

// namespaceName follows doc §5.2: "Namespace: env-{env_id} (one namespace per environment)".
func namespaceName(envID string) string {
	return fmt.Sprintf("env-%s", envID)
}

// ignoreAlreadyExists is PLAN.md Phase 3's U10: the
// "if apierrors.IsAlreadyExists(err) { return nil }; return err" idempotent-
// create pattern was hand-copied 8 times across this file's various
// applyX() functions (createNamespace, applyResourceQuota,
// applyLimitRange, applyDefaultDenyNetworkPolicy, and others) --
// every one of them creates a namespace-scoped K8s object as part of
// Provision()'s pipeline, and every one needs a retried/re-run
// Provision call to be a no-op on an object that already exists (doc
// §5.5's own retry framing), not a hard failure.
//
// A pure function of error -> error, independent of any K8s client, so
// unlike U8 (which needed a live *Provisioner) this one has a real
// fake-clientset-free unit test seam.
func ignoreAlreadyExists(err error) error {
	if apierrors.IsAlreadyExists(err) {
		return nil
	}
	return err
}

// RestrictedPodSecurityContext is PLAN.md Phase 3's U12: this exact
// PodSecurityContext shape (RunAsNonRoot, RunAsUser=1000,
// SeccompProfile RuntimeDefault) was independently, identically
// duplicated in this file's createWorkspacePod AND in
// internal/faultinjection/handlers_batch3.go's traffic-spike fault Job
// (both need PSS "restricted" admission to accept the pod they create --
// confirmed live earlier this session: a pod with no SecurityContext at
// all is rejected outright by this cluster's real PSS enforcement, the
// original bug U12 traces back to). Exported (capital R) so
// faultinjection can call it -- faultinjection already imports internal/
// k8s (k8s.NamespaceForEnv, k8s.Provisioner), so this direction has no
// import-cycle risk; the reverse (k8s importing faultinjection) never
// happens.
func RestrictedPodSecurityContext() *corev1.PodSecurityContext {
	runAsNonRoot := true
	runAsUser := int64(1000)
	return &corev1.PodSecurityContext{
		RunAsNonRoot: &runAsNonRoot,
		RunAsUser:    &runAsUser,
		SeccompProfile: &corev1.SeccompProfile{
			Type: corev1.SeccompProfileTypeRuntimeDefault,
		},
	}
}

// RestrictedContainerSecurityContext is U12's container-level half.
// readOnlyRootFilesystem is a parameter, not hardcoded, because the two
// real call sites genuinely disagree on it: createWorkspacePod sets
// false (the learner's own /workspace and package-manager writes need a
// writable root fs -- see that function's own comment), while the
// traffic-spike fault's Job containers have no such requirement and
// could reasonably want true. Every OTHER field
// (AllowPrivilegeEscalation=false, Capabilities.Drop=[ALL]) is identical
// between both real call sites, so those stay fixed rather than becoming
// parameters nobody actually needs to vary.
func RestrictedContainerSecurityContext(readOnlyRootFilesystem bool) *corev1.SecurityContext {
	allowPrivilegeEscalation := false
	return &corev1.SecurityContext{
		AllowPrivilegeEscalation: &allowPrivilegeEscalation,
		ReadOnlyRootFilesystem:   &readOnlyRootFilesystem,
		Capabilities: &corev1.Capabilities{
			Drop: []corev1.Capability{"ALL"},
		},
	}
}

// Provision creates the full namespace-per-environment template and the
// workspace pod, then blocks until the pod is Ready or the context
// deadline is hit. Doc §5.5 step 2 (cold provision path): "T1: create ns
// -> quota/netpol -> create pods." T2 follows the identical pipeline --
// namespace, quota, network policy, egress allowlist, ServiceAccount are
// all tier-independent (doc §5.2's template applies to any shared-cluster
// tier; only the pod's own RuntimeClass/SecurityContext/resource shape
// differs, which is where createWorkspacePod branches on req.Tier). This
// is the "single Environment Orchestrator interface... with N driver
// implementations" shape doc §5.1's trade-offs section calls for: T1 and
// T2 are not two parallel code paths, they're one pipeline with one
// tier-aware step.
func (p *Provisioner) Provision(ctx context.Context, req ProvisionRequest) error {
	ns := namespaceName(req.EnvID)

	if err := p.createNamespace(ctx, ns, req.AttemptID, req.Tier); err != nil {
		return fmt.Errorf("creating namespace: %w", err)
	}
	if err := p.applyResourceQuota(ctx, ns, req.Tier); err != nil {
		return fmt.Errorf("applying ResourceQuota: %w", err)
	}
	if err := p.applyLimitRange(ctx, ns, req.Tier); err != nil {
		return fmt.Errorf("applying LimitRange: %w", err)
	}
	if err := p.applyDefaultDenyNetworkPolicy(ctx, ns); err != nil {
		return fmt.Errorf("applying NetworkPolicy: %w", err)
	}
	if err := p.applyEgressProxyAllowlist(ctx, ns); err != nil {
		return fmt.Errorf("applying egress proxy allowlist: %w", err)
	}
	if err := p.applyServiceAccount(ctx, ns); err != nil {
		return fmt.Errorf("applying ServiceAccount: %w", err)
	}
	if err := p.createWorkspacePod(ctx, ns, req); err != nil {
		return fmt.Errorf("creating workspace pod: %w", err)
	}
	return nil
}

// Doc §5.2: "ResourceQuota cpu 2, mem 4Gi, pods 6, pvc 1, services 2".
//
// PodSecurity level is tier-dependent, and getting this wrong for T2
// isn't cosmetic -- it's a scheduling correctness bug. PSS `restricted`
// (T1's level) unconditionally forbids `privileged: true` containers at
// the ADMISSION-CONTROLLER layer, before the scheduler even considers
// RuntimeClass or node placement. applyT2PodShape sets
// shell.SecurityContext.Privileged = true (required for DinD/systemd/
// eBPF per doc §5.1), so a T2 namespace stuck at `restricted` would
// reject every T2 pod outright, regardless of whether a Kata-capable
// node exists. T2 namespaces use PSS `privileged` (the least restrictive
// built-in level) instead -- doc §5.1's own isolation column is the
// justification: T2's boundary is Kata's hardware virtualisation (its
// own kernel), not the container's capability set, so PSS's
// container-level restrictions are the wrong control for this tier; T1
// keeps `restricted` because gVisor's isolation IS the syscall
// boundary PSS's capability/privilege rules are meant to protect.
func (p *Provisioner) createNamespace(ctx context.Context, ns, attemptID string, tier Tier) error {
	pssLevel := pssLevelFor(tier)
	obj := &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name: ns,
			Labels: map[string]string{
				ManagedNamespaceLabelKey:        ManagedNamespaceLabelValue,
				"practiceengine.dev/attempt-id": attemptID,
				// Doc §5.2 PodSecurity: "restricted; no privileged, no
				// hostPath, no hostNetwork, runAsNonRoot, seccomp
				// RuntimeDefault, drop ALL caps" -- enforced via the
				// namespace-level Pod Security Standards label, the
				// built-in K8s admission controller (no external policy
				// engine needed for this one rule). T2 overrides to
				// `privileged`; see this function's doc comment for why.
				"pod-security.kubernetes.io/enforce": pssLevel,
				"pod-security.kubernetes.io/audit":   pssLevel,
				"pod-security.kubernetes.io/warn":    pssLevel,
			},
		},
	}
	_, err := p.clientset.CoreV1().Namespaces().Create(ctx, obj, metav1.CreateOptions{})
	return ignoreAlreadyExists(err) // idempotent: a retried Provision call must not fail here
}

// pssLevelFor is the pure tier -> Pod Security Standards level mapping
// createNamespace applies -- extracted so the (real, not cosmetic) T1/T2
// distinction is directly unit-testable without a K8s client. See
// createNamespace's doc comment for why T2 needs `privileged`, not just
// a looser default.
func pssLevelFor(tier Tier) string {
	if tier == TierT2IsolatedMicroVM {
		return "privileged"
	}
	return "restricted"
}

// resourceQuotaFor is the pure tier -> ResourceQuota.Hard mapping
// applyResourceQuota applies -- see that method's doc comment for the
// T1/T2 numbers' basis.
func resourceQuotaFor(tier Tier) corev1.ResourceList {
	if tier == TierT2IsolatedMicroVM {
		return corev1.ResourceList{
			corev1.ResourceRequestsCPU:            resource.MustParse(DefaultT2Resources.CPU),
			corev1.ResourceRequestsMemory:         resource.MustParse(DefaultT2Resources.Memory),
			corev1.ResourcePods:                   resource.MustParse("20"),
			corev1.ResourcePersistentVolumeClaims: resource.MustParse("4"),
			corev1.ResourceServices:               resource.MustParse("4"),
		}
	}
	return corev1.ResourceList{
		corev1.ResourceRequestsCPU:            resource.MustParse(DefaultT1Resources.CPU),
		corev1.ResourceRequestsMemory:         resource.MustParse(DefaultT1Resources.Memory),
		corev1.ResourcePods:                   resource.MustParse("6"),
		corev1.ResourcePersistentVolumeClaims: resource.MustParse("1"),
		corev1.ResourceServices:               resource.MustParse("2"),
	}
}

// limitRangeMaxFor is the pure tier -> LimitRange per-container Max
// mapping applyLimitRange applies -- see that method's doc comment.
func limitRangeMaxFor(tier Tier) corev1.ResourceList {
	spec := DefaultT1Resources
	if tier == TierT2IsolatedMicroVM {
		spec = DefaultT2Resources
	}
	return corev1.ResourceList{
		corev1.ResourceCPU:    resource.MustParse(spec.CPU),
		corev1.ResourceMemory: resource.MustParse(spec.Memory),
	}
}

// Doc §5.2's T1 quota table: "cpu 2, mem 4Gi, pods 6, pvc 1, services 2".
// T2 has no equivalent named table in the doc (§5.1's tier table only
// gives cost/latency/isolation, not a quota spec), but §5.1 names what
// T2 must support that T1 explicitly cannot: "Docker-in-Docker with real
// kernel features... k3s/kind full Kubernetes... multi-node K8s
// (nested)". A nested k3s control plane plus a handful of worker pods
// inside one environment cannot fit T1's 6-pod/2-CPU/4Gi budget --
// scaled up to the next reasonable tier consistent with T2's $0.10-0.35/
// hr cost band (roughly 3-6x T1's $0.02-0.06/hr), not an arbitrary
// number.
func (p *Provisioner) applyResourceQuota(ctx context.Context, ns string, tier Tier) error {
	obj := &corev1.ResourceQuota{
		ObjectMeta: ObjectMeta("env-quota", ns),
		Spec:       corev1.ResourceQuotaSpec{Hard: resourceQuotaFor(tier)},
	}
	_, err := p.clientset.CoreV1().ResourceQuotas(ns).Create(ctx, obj, metav1.CreateOptions{})
	return ignoreAlreadyExists(err)
}

// Doc §5.2: "LimitRange default requests/limits, no unbounded containers."
// T2's higher per-container ceiling matches applyResourceQuota's reasoning
// above -- a DinD/nested-k3s workload's individual containers (e.g. the
// nested control plane) legitimately need more than T1's 2-CPU/4Gi cap.
func (p *Provisioner) applyLimitRange(ctx context.Context, ns string, tier Tier) error {
	obj := &corev1.LimitRange{
		ObjectMeta: ObjectMeta("env-limits", ns),
		Spec: corev1.LimitRangeSpec{
			Limits: []corev1.LimitRangeItem{
				{
					Type: corev1.LimitTypeContainer,
					Default: corev1.ResourceList{
						corev1.ResourceCPU:    resource.MustParse("1"),
						corev1.ResourceMemory: resource.MustParse("1Gi"),
					},
					DefaultRequest: corev1.ResourceList{
						corev1.ResourceCPU:    resource.MustParse("100m"),
						corev1.ResourceMemory: resource.MustParse("128Mi"),
					},
					Max: limitRangeMaxFor(tier),
				},
			},
		},
	}
	_, err := p.clientset.CoreV1().LimitRanges(ns).Create(ctx, obj, metav1.CreateOptions{})
	return ignoreAlreadyExists(err)
}

// Doc §5.2: "NetworkPolicy default-deny ingress+egress; allow -> egress-proxy only."
// The egress-proxy allow rule is a separate NetworkPolicy applied by the
// egress-proxy package (M1.10) so this one stays a pure default-deny and
// is safe to apply before the proxy exists.
// ApplyDefaultDenyNetworkPolicy exports applyDefaultDenyNetworkPolicy for
// callers (e.g. fixture integration tests) that need to replicate the
// real T1/T2 network baseline on a namespace outside the full Provision
// pipeline.
func (p *Provisioner) ApplyDefaultDenyNetworkPolicy(ctx context.Context, ns string) error {
	return p.applyDefaultDenyNetworkPolicy(ctx, ns)
}

func (p *Provisioner) applyDefaultDenyNetworkPolicy(ctx context.Context, ns string) error {
	obj := &networkingv1.NetworkPolicy{
		ObjectMeta: ObjectMeta("default-deny", ns),
		Spec: networkingv1.NetworkPolicySpec{
			PodSelector: metav1.LabelSelector{}, // empty selector = all pods in namespace
			PolicyTypes: []networkingv1.PolicyType{
				networkingv1.PolicyTypeIngress,
				networkingv1.PolicyTypeEgress,
			},
			// No Ingress/Egress rules specified => deny all, per the K8s
			// NetworkPolicy semantics doc §5.2 relies on.
		},
	}
	_, err := p.clientset.NetworkingV1().NetworkPolicies(ns).Create(ctx, obj, metav1.CreateOptions{})
	return ignoreAlreadyExists(err)
}

// egressProxyNamespace/egressProxyPort must match manifests/t1/egress-proxy.yaml.
const egressProxyNamespace = "practiceengine-platform"
const egressProxyPort = 3128

// EgressProxyURL is the HTTP(S)_PROXY value any pod needing internet
// access under the real default-deny NetworkPolicy must set -- exported
// for callers outside this package (e.g. fixture handlers) that build
// their own pods needing the same carve-out ApplyEgressProxyAllowlist
// grants.
const EgressProxyURL = "http://egress-proxy." + egressProxyNamespace + ".svc.cluster.local:" + "3128"

// Doc §9.2: "an explicit-allow egress proxy per environment... Learner
// pod -- (NetworkPolicy: egress ONLY to proxy:3128) --> Egress Proxy."
// Doc §5.2: "allow -> egress-proxy only" is the one carve-out in the
// otherwise total default-deny NetworkPolicy. DNS is allowed to
// kube-dns/CoreDNS specifically (not "all of port 53") so name
// resolution keeps working without becoming a second unrestricted
// egress path -- doc §9.2: "DNS is also constrained... otherwise DNS
// becomes the exfiltration channel."
// ApplyEgressProxyAllowlist exports applyEgressProxyAllowlist for callers
// (e.g. fixture integration tests) that need to replicate the real
// T1/T2 network baseline on a namespace outside the full Provision
// pipeline.
func (p *Provisioner) ApplyEgressProxyAllowlist(ctx context.Context, ns string) error {
	return p.applyEgressProxyAllowlist(ctx, ns)
}

func (p *Provisioner) applyEgressProxyAllowlist(ctx context.Context, ns string) error {
	tcpProtocol := corev1.ProtocolTCP
	udpProtocol := corev1.ProtocolUDP
	proxyPort := intstr.FromInt32(egressProxyPort)
	dnsPort := intstr.FromInt32(53)

	obj := &networkingv1.NetworkPolicy{
		ObjectMeta: ObjectMeta("allow-egress-proxy", ns),
		Spec: networkingv1.NetworkPolicySpec{
			PodSelector: metav1.LabelSelector{},
			PolicyTypes: []networkingv1.PolicyType{networkingv1.PolicyTypeEgress},
			Egress: []networkingv1.NetworkPolicyEgressRule{
				{
					To: []networkingv1.NetworkPolicyPeer{
						{
							NamespaceSelector: &metav1.LabelSelector{
								MatchLabels: map[string]string{"kubernetes.io/metadata.name": egressProxyNamespace},
							},
						},
					},
					Ports: []networkingv1.NetworkPolicyPort{
						{Protocol: &tcpProtocol, Port: &proxyPort},
					},
				},
				{
					// DNS resolution -- doc §9.2: constrained to the
					// cluster resolver, not general port-53 egress.
					To: []networkingv1.NetworkPolicyPeer{
						{
							NamespaceSelector: &metav1.LabelSelector{
								MatchLabels: map[string]string{"kubernetes.io/metadata.name": "kube-system"},
							},
						},
					},
					Ports: []networkingv1.NetworkPolicyPort{
						{Protocol: &udpProtocol, Port: &dnsPort},
						{Protocol: &tcpProtocol, Port: &dnsPort},
					},
				},
			},
		},
	}
	_, err := p.clientset.NetworkingV1().NetworkPolicies(ns).Create(ctx, obj, metav1.CreateOptions{})
	return ignoreAlreadyExists(err)
}

// Doc §5.2: "ServiceAccount automountServiceAccountToken: false" -- doc §9.1
// T2 threat: "Learner namespaces have no SA token" is the control here.
func (p *Provisioner) applyServiceAccount(ctx context.Context, ns string) error {
	autoMount := false
	obj := &corev1.ServiceAccount{
		ObjectMeta:                   ObjectMeta(WorkspaceServiceAccountName, ns),
		AutomountServiceAccountToken: &autoMount,
	}
	_, err := p.clientset.CoreV1().ServiceAccounts(ns).Create(ctx, obj, metav1.CreateOptions{})
	return ignoreAlreadyExists(err)
}

// Doc §5.2 Pod: workspace -- container: shell, volume: /workspace.
// The `editor` container (OpenVSCode) and per-fixture service pods
// (postgres/redis/localstack) are declared in the blueprint and are
// Phase 3+ per the doc's own editor-ladder note (§5.4); Phase 1 uses
// Monaco client-side against a file API, so only the shell container is
// provisioned here.
//
// T1 vs T2 differ in exactly the ways doc §5.1's tier table implies and
// nothing else -- same namespace template, same egress proxy, same
// pod-Ready health gate, same Session Broker exec mechanism:
//   - RuntimeClass: gvisor (T1, syscall-interception sandbox) vs kata
//     (T2, hardware-virtualised microVM) -- manifests/t1/
//     runtimeclass-gvisor.yaml and manifests/t2/runtimeclass-kata.yaml.
//   - SecurityContext: T1 drops ALL capabilities and forbids privilege
//     escalation (gVisor's isolation is the syscall boundary itself, so
//     the container can stay maximally restricted). T2 needs
//     privileged: true -- doc §5.1 T2 explicitly supports "Docker-in-
//     Docker with real kernel features" and "systemd, eBPF, kernel
//     tuning," none of which run un-privileged in any container runtime;
//     Kata's hardware virtualisation (its own kernel per doc's isolation
//     column) is what makes that safe to grant here where it would be a
//     real container-escape risk on T1's shared-kernel gVisor sandbox.
//   - Toleration: T2 pods tolerate the tainted T2 node pool (see
//     manifests/t2/node-pool-taint.md, same real-deployment-only caveat
//     as T1's) so they land only on Kata-capable nodes.
//   - Resources: T2's default Limits come from req.Resources (typically
//     the LimitRange's higher T2 max) rather than T1's hardcoded 2/4Gi
//     fallback, matching applyLimitRange's T2 ceiling above.
func (p *Provisioner) createWorkspacePod(ctx context.Context, ns string, req ProvisionRequest) error {
	podSpec := corev1.PodSpec{
		// Doc §5.2: "RuntimeClass: gvisor". The RuntimeClass object
		// itself is declared in manifests/t1/runtimeclass-gvisor.yaml
		// (M1.1) -- referencing it here is what makes a T1 pod
		// actually gVisor-sandboxed once the RuntimeClass exists on a
		// node with gVisor installed. Gated on p.gVisorEnabled (see its
		// doc comment) rather than always set: on this local k3s
		// cluster gVisor is not installed (documented gap), so hardcoding
		// it here unconditionally would make every local Provision() call
		// fail scheduling. Set ORCHESTRATOR_GVISOR_ENABLED=true only in a
		// real deployment that has confirmed gVisor is actually installed
		// on the T1 node pool (manifests/t1/node-pool-taint.md).
		ServiceAccountName: WorkspaceServiceAccountName,
		SecurityContext:    RestrictedPodSecurityContext(),
		Containers: []corev1.Container{
			{
				Name:  WorkspaceContainerName,
				Image: req.Image,
				// readOnlyRootFilesystem=false: /workspace itself is
				// writable; root fs stays read-only via the emptyDir
				// mount strategy below at the container level only
				// where feasible.
				SecurityContext: RestrictedContainerSecurityContext(false),
				Resources: corev1.ResourceRequirements{
					Requests: corev1.ResourceList{
						corev1.ResourceCPU:    resource.MustParse("100m"),
						corev1.ResourceMemory: resource.MustParse("256Mi"),
					},
					Limits: corev1.ResourceList{
						corev1.ResourceCPU:    resource.MustParse(nonEmpty(req.Resources.CPU, DefaultT1Resources.CPU)),
						corev1.ResourceMemory: resource.MustParse(nonEmpty(req.Resources.Memory, DefaultT1Resources.Memory)),
					},
				},
				VolumeMounts: []corev1.VolumeMount{
					{Name: WorkspacePodName, MountPath: "/workspace"},
				},
				// Doc §9.2: package managers (apt/pip/npm) need to be
				// pointed at the egress proxy explicitly -- the
				// NetworkPolicy allowlist (applyEgressProxyAllowlist)
				// only permits the *connection*; tools still default
				// to a direct connection attempt unless told to use a
				// proxy, which the default-deny policy would then
				// silently hang/timeout on rather than clearly fail.
				Env: []corev1.EnvVar{
					{Name: "HTTP_PROXY", Value: fmt.Sprintf("http://egress-proxy.%s.svc.cluster.local:%d", egressProxyNamespace, egressProxyPort)},
					{Name: "HTTPS_PROXY", Value: fmt.Sprintf("http://egress-proxy.%s.svc.cluster.local:%d", egressProxyNamespace, egressProxyPort)},
					{Name: "http_proxy", Value: fmt.Sprintf("http://egress-proxy.%s.svc.cluster.local:%d", egressProxyNamespace, egressProxyPort)},
					{Name: "https_proxy", Value: fmt.Sprintf("http://egress-proxy.%s.svc.cluster.local:%d", egressProxyNamespace, egressProxyPort)},
					// kubernetes.default.svc: a real bug caught during
					// this session's live-cluster testing of
					// fx.k3s-ready.v1 -- without this, kubectl's calls to
					// the API server also went through HTTPS_PROXY (curl/
					// Go's net/http both honor these env vars for ALL
					// HTTPS traffic, not just external egress), and the
					// egress proxy's Squid ACL (manifests/t1/egress-proxy.yaml,
					// allow-lists package registries only) correctly
					// denied that CONNECT with a 403 -- which surfaced to
					// kubectl as a generic, misleading "Forbidden" that
					// looked like an RBAC problem but was actually a
					// proxy-routing problem. In-cluster API traffic must
					// never route through the external egress proxy
					// regardless of which fixture/tool initiates it, so
					// this belongs in the base template, not scoped to
					// one fixture.
					{Name: "NO_PROXY", Value: "localhost,127.0.0.1,kubernetes.default.svc,.svc,.svc.cluster.local"},
				},
				// Keeps the container alive; the Session Broker execs
				// into it over the K8s exec API (doc §5.4) rather
				// than relying on an entrypoint shell.
				Command: []string{"sh", "-c", "sleep infinity"},
			},
		},
		Volumes: []corev1.Volume{
			{
				Name:         WorkspacePodName,
				VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}},
			},
		},
		RestartPolicy: corev1.RestartPolicyNever,
	}

	if req.Tier == TierT2IsolatedMicroVM {
		p.applyT2PodShape(&podSpec, req)
	} else if rc := runtimeClassForT1(p.cfg.GVisorEnabled); rc != nil {
		podSpec.RuntimeClassName = rc
	}

	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      WorkspacePodName,
			Namespace: ns,
			Labels:    map[string]string{"app": WorkspacePodName},
		},
		Spec: podSpec,
	}

	_, err := p.clientset.CoreV1().Pods(ns).Create(ctx, pod, metav1.CreateOptions{})
	return ignoreAlreadyExists(err)
}

// applyT2PodShape mutates a T1-shaped PodSpec into T2's shape in place --
// kept as a small, separate, tier-specific step (rather than threading
// if req.Tier == TierT2 branches through every field above) so the T1
// path stays exactly what it was before T2 existed, and a reviewer can
// see the complete T2 delta in one place.
//
// Cost decision (₹100/user ceiling): T2 runs under the **Sysbox**
// runtime (p.cfg.T2RuntimeClass, default "sysbox-runc") on the SAME
// shared node pool as T1 -- NOT on a dedicated Kata bare-metal pool.
// Consequences for this function versus the old Kata shape:
//
//   - RuntimeClassName is p.cfg.T2RuntimeClass, not a hardcoded "kata".
//     Empty config value -> no runtimeClassName set (local dev with no
//     Sysbox degrades to a plain container, same honest-degradation
//     stance runtimeClassForT1 takes when gVisor is off -- it does NOT
//     fail to schedule, unlike the old Kata assignment).
//   - NO nodeSelector / toleration for a "practiceengine.dev/tier2"
//     metal pool -- there is no such pool. A T2 pod schedules onto the
//     ordinary learner nodes alongside T1 pods. (The learner-node
//     toleration itself is added by createWorkspacePod for every
//     workspace pod regardless of tier, so nothing to add here.)
//   - privileged is NOT set by default. Sysbox delivers real DinD /
//     systemd / nested multi-node k3s via user-namespace isolation
//     WITHOUT any capability grant -- that is the entire reason it fits
//     this budget and this threat model. Only a blueprint that declares
//     an eBPF capability (req.PrivilegedWorkload) gets privileged, and
//     only its shell container, so the shared-kernel exposure is paid
//     for exactly the content that needs it.
//   - The T2 resource ceiling (DefaultT2Resources, 8/16) still applies
//     as the LimitRange max; req.Resources still wins when set (a
//     cost-optimised blueprint requests 4/8, docs/t2-cost-optimization.md
//     §2.4).
//
// The Kata shape is preserved in git history and documented as the
// scale-up path in infra/practice-cluster/t2-nodepool-kata/README.md;
// switching back is ORCHESTRATOR_T2_RUNTIME_CLASS=kata plus that pool.
func (p *Provisioner) applyT2PodShape(podSpec *corev1.PodSpec, req ProvisionRequest) {
	if rc := p.cfg.T2RuntimeClass; rc != "" {
		podSpec.RuntimeClassName = stringPtr(rc)
	}

	// Sysbox needs the workspace container to run as root INSIDE its own
	// user namespace -- systemd-as-PID-1 and dockerd both expect uid 0.
	// This is NOT a host-privilege grant: Sysbox remaps that in-container
	// uid 0 to an unprivileged host uid range. Both the pod-level and the
	// container-level SecurityContext createWorkspacePod set
	// (RestrictedPodSecurityContext / RestrictedContainerSecurityContext:
	// RunAsNonRoot=true, RunAsUser=1000, drop ALL caps) have to be
	// relaxed here or the container can't come up. The T2 namespace is
	// already PSS `privileged` (pssLevelFor), so admission permits this.
	runAsNonRoot := false
	var runAsUser int64 = 0
	podSpec.SecurityContext = &corev1.PodSecurityContext{
		RunAsNonRoot: &runAsNonRoot,
		RunAsUser:    &runAsUser,
		SeccompProfile: &corev1.SeccompProfile{
			// Sysbox installs its own seccomp handling; RuntimeDefault is
			// still the right baseline and Sysbox loosens it internally.
			Type: corev1.SeccompProfileTypeRuntimeDefault,
		},
	}

	shell := &podSpec.Containers[0]
	allowPrivilegeEscalation := true // systemd/dockerd inside need setuid helpers
	shell.SecurityContext = &corev1.SecurityContext{
		RunAsNonRoot:             &runAsNonRoot,
		RunAsUser:                &runAsUser,
		AllowPrivilegeEscalation: &allowPrivilegeEscalation,
	}

	// eBPF-capability blueprints only: additionally privileged, on the
	// shell container alone. See ProvisionRequest.PrivilegedWorkload.
	if req.PrivilegedWorkload {
		privileged := true
		shell.SecurityContext.Privileged = &privileged
	}

	// T2's LimitRange ceiling (applyLimitRange) is DefaultT2Resources --
	// use req.Resources directly rather than T1's DefaultT1Resources
	// fallback, so a T2 ProvisionRequest that sets real resource values
	// isn't silently clamped back down to T1's defaults.
	shell.Resources.Limits = corev1.ResourceList{
		corev1.ResourceCPU:    resource.MustParse(nonEmpty(req.Resources.CPU, DefaultT2Resources.CPU)),
		corev1.ResourceMemory: resource.MustParse(nonEmpty(req.Resources.Memory, DefaultT2Resources.Memory)),
	}
}

func stringPtr(s string) *string { return &s }

// runtimeClassForT1 is the pure gVisorEnabled -> RuntimeClassName mapping
// createWorkspacePod applies for T1 pods (T2 has its own unconditional
// "kata" assignment in applyT2PodShape) -- extracted so this decision is
// directly unit-testable without a K8s client, same rationale as
// pssLevelFor/resourceQuotaFor/limitRangeMaxFor above.
func runtimeClassForT1(gVisorEnabled bool) *string {
	if !gVisorEnabled {
		return nil
	}
	return stringPtr("gvisor")
}

func nonEmpty(v, fallback string) string {
	if v == "" {
		return fallback
	}
	return v
}

// WaitForPodReady polls until the workspace pod's containers are Ready
// (doc §5.5 step 4 health gate precondition) or the context deadline
// elapses.
func (p *Provisioner) WaitForPodReady(ctx context.Context, envID string) error {
	ns := namespaceName(envID)
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return fmt.Errorf("timed out waiting for pod ready in namespace %s: %w", ns, ctx.Err())
		case <-ticker.C:
			pod, err := p.clientset.CoreV1().Pods(ns).Get(ctx, WorkspacePodName, metav1.GetOptions{})
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

// Destroy deletes the entire namespace, which cascades to every object
// inside it -- doc §5.2: "teardown is a single delete namespace." One
// real exception: internal/fixture's fx.k3s-ready.v1 creates a
// ClusterRoleBinding (learner-discovery-<namespace>) so the learner's
// kubectl can reach the API server's discovery endpoints -- a
// cluster-scoped object, so namespace deletion does NOT cascade-delete
// it (confirmed live: a real Destroy() call left the binding behind,
// caught during this session's live-cluster verification, not a
// hypothetical). Deleted explicitly here by its deterministic name
// rather than tracked separately -- best-effort (IsNotFound is a
// success, same idempotency contract as the namespace delete itself;
// any other error is logged, not returned, since the namespace delete
// below is this method's primary contract and a stray leftover
// ClusterRoleBinding, while real waste, doesn't block environment
// teardown from otherwise succeeding).
func (p *Provisioner) Destroy(ctx context.Context, envID string) error {
	ns := namespaceName(envID)

	discoveryBindingName := "learner-discovery-" + ns
	if err := p.clientset.RbacV1().ClusterRoleBindings().Delete(ctx, discoveryBindingName, metav1.DeleteOptions{}); err != nil && !apierrors.IsNotFound(err) {
		log.Printf("[k8s] WARNING: failed to delete cluster-scoped discovery binding %s for env=%s: %v", discoveryBindingName, envID, err)
	}

	err := p.clientset.CoreV1().Namespaces().Delete(ctx, ns, metav1.DeleteOptions{})
	if apierrors.IsNotFound(err) {
		return nil // idempotent: doc §4.1 DestroyResponse.already_destroyed contract
	}
	return err
}

// NamespaceExists is used by the reaper's orphan sweep (M1.7) and by
// Connect() to check liveness before handing out endpoints.
func (p *Provisioner) NamespaceExists(ctx context.Context, envID string) (bool, error) {
	ns := namespaceName(envID)
	_, err := p.clientset.CoreV1().Namespaces().Get(ctx, ns, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}

// ListManagedNamespaces returns every namespace this Orchestrator
// created (labelled practiceengine.dev/managed=true), for the reaper's
// orphan sweep (doc §5.6).
func (p *Provisioner) ListManagedNamespaces(ctx context.Context) ([]string, error) {
	list, err := p.clientset.CoreV1().Namespaces().List(ctx, metav1.ListOptions{
		LabelSelector: ManagedNamespaceLabelSelector,
	})
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(list.Items))
	for _, ns := range list.Items {
		names = append(names, ns.Name)
	}
	return names, nil
}

// ExposePreviewPort is referenced by doc §5.4's wildcard-subdomain preview
// routing; full ingress wiring is out of scope for this Phase 1 slice
// (no learner-run web apps in the guided-lab content set yet), but the
// Service object itself is cheap to create alongside the pod so a later
// ingress controller can pick it up without a provisioning-path change.
func (p *Provisioner) ExposeWorkspaceService(ctx context.Context, envID string, port int32) error {
	ns := namespaceName(envID)
	svc := &corev1.Service{
		ObjectMeta: ObjectMeta(WorkspacePodName, ns),
		Spec: corev1.ServiceSpec{
			Selector: map[string]string{"app": WorkspacePodName},
			Ports: []corev1.ServicePort{
				{Port: port, TargetPort: intstr.FromInt32(port)},
			},
		},
	}
	_, err := p.clientset.CoreV1().Services(ns).Create(ctx, svc, metav1.CreateOptions{})
	return ignoreAlreadyExists(err)
}
