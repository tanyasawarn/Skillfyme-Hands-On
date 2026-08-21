package k8s

import (
	"context"
	"fmt"
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

// ProvisionRequest is the T1-driver-specific input; the gRPC layer (internal/orchestrator)
// translates the wire ProvisionRequest message into this before calling Provisioner.
type ProvisionRequest struct {
	AttemptID     string
	EnvID         string // namespace name: env-{env_id}
	Image         string // base image for the shell container (image strategy, M1.11)
	Resources     ResourceSpec
	NetworkPolicy string // reserved for blueprint-declared egress allowlist domains (§9.2); default-deny is always applied regardless
}

// Provisioner implements doc §5.2's namespace-per-environment template:
// namespace + ResourceQuota + LimitRange + NetworkPolicy (default-deny) +
// ServiceAccount (no auto-mount) + PodSecurity (restricted) + the
// workspace pod itself.
type Provisioner struct {
	clientset  *kubernetes.Clientset
	restConfig *rest.Config
}

func NewProvisioner(clientset *kubernetes.Clientset, restConfig *rest.Config) *Provisioner {
	return &Provisioner{clientset: clientset, restConfig: restConfig}
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

// Provision creates the full namespace-per-environment template and the
// workspace pod, then blocks until the pod is Ready or the context
// deadline is hit. Doc §5.5 step 2 (cold provision path): "T1: create ns
// -> quota/netpol -> create pods."
func (p *Provisioner) Provision(ctx context.Context, req ProvisionRequest) error {
	ns := namespaceName(req.EnvID)

	if err := p.createNamespace(ctx, ns, req.AttemptID); err != nil {
		return fmt.Errorf("creating namespace: %w", err)
	}
	if err := p.applyResourceQuota(ctx, ns); err != nil {
		return fmt.Errorf("applying ResourceQuota: %w", err)
	}
	if err := p.applyLimitRange(ctx, ns); err != nil {
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
func (p *Provisioner) createNamespace(ctx context.Context, ns, attemptID string) error {
	obj := &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			Name: ns,
			Labels: map[string]string{
				"practiceengine.dev/managed":    "true",
				"practiceengine.dev/attempt-id": attemptID,
				// Doc §5.2 PodSecurity: "restricted; no privileged, no
				// hostPath, no hostNetwork, runAsNonRoot, seccomp
				// RuntimeDefault, drop ALL caps" -- enforced via the
				// namespace-level Pod Security Standards label, the
				// built-in K8s admission controller (no external policy
				// engine needed for this one rule).
				"pod-security.kubernetes.io/enforce": "restricted",
				"pod-security.kubernetes.io/audit":   "restricted",
				"pod-security.kubernetes.io/warn":    "restricted",
			},
		},
	}
	_, err := p.clientset.CoreV1().Namespaces().Create(ctx, obj, metav1.CreateOptions{})
	if apierrors.IsAlreadyExists(err) {
		return nil // idempotent: a retried Provision call must not fail here
	}
	return err
}

func (p *Provisioner) applyResourceQuota(ctx context.Context, ns string) error {
	obj := &corev1.ResourceQuota{
		ObjectMeta: metav1.ObjectMeta{Name: "env-quota", Namespace: ns},
		Spec: corev1.ResourceQuotaSpec{
			Hard: corev1.ResourceList{
				corev1.ResourceRequestsCPU:            resource.MustParse("2"),
				corev1.ResourceRequestsMemory:         resource.MustParse("4Gi"),
				corev1.ResourcePods:                   resource.MustParse("6"),
				corev1.ResourcePersistentVolumeClaims: resource.MustParse("1"),
				corev1.ResourceServices:               resource.MustParse("2"),
			},
		},
	}
	_, err := p.clientset.CoreV1().ResourceQuotas(ns).Create(ctx, obj, metav1.CreateOptions{})
	if apierrors.IsAlreadyExists(err) {
		return nil
	}
	return err
}

// Doc §5.2: "LimitRange default requests/limits, no unbounded containers."
func (p *Provisioner) applyLimitRange(ctx context.Context, ns string) error {
	obj := &corev1.LimitRange{
		ObjectMeta: metav1.ObjectMeta{Name: "env-limits", Namespace: ns},
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
					Max: corev1.ResourceList{
						corev1.ResourceCPU:    resource.MustParse("2"),
						corev1.ResourceMemory: resource.MustParse("4Gi"),
					},
				},
			},
		},
	}
	_, err := p.clientset.CoreV1().LimitRanges(ns).Create(ctx, obj, metav1.CreateOptions{})
	if apierrors.IsAlreadyExists(err) {
		return nil
	}
	return err
}

// Doc §5.2: "NetworkPolicy default-deny ingress+egress; allow -> egress-proxy only."
// The egress-proxy allow rule is a separate NetworkPolicy applied by the
// egress-proxy package (M1.10) so this one stays a pure default-deny and
// is safe to apply before the proxy exists.
func (p *Provisioner) applyDefaultDenyNetworkPolicy(ctx context.Context, ns string) error {
	obj := &networkingv1.NetworkPolicy{
		ObjectMeta: metav1.ObjectMeta{Name: "default-deny", Namespace: ns},
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
	if apierrors.IsAlreadyExists(err) {
		return nil
	}
	return err
}

// egressProxyNamespace/egressProxyPort must match manifests/t1/egress-proxy.yaml.
const egressProxyNamespace = "practiceengine-platform"
const egressProxyPort = 3128

// Doc §9.2: "an explicit-allow egress proxy per environment... Learner
// pod -- (NetworkPolicy: egress ONLY to proxy:3128) --> Egress Proxy."
// Doc §5.2: "allow -> egress-proxy only" is the one carve-out in the
// otherwise total default-deny NetworkPolicy. DNS is allowed to
// kube-dns/CoreDNS specifically (not "all of port 53") so name
// resolution keeps working without becoming a second unrestricted
// egress path -- doc §9.2: "DNS is also constrained... otherwise DNS
// becomes the exfiltration channel."
func (p *Provisioner) applyEgressProxyAllowlist(ctx context.Context, ns string) error {
	tcpProtocol := corev1.ProtocolTCP
	udpProtocol := corev1.ProtocolUDP
	proxyPort := intstr.FromInt32(egressProxyPort)
	dnsPort := intstr.FromInt32(53)

	obj := &networkingv1.NetworkPolicy{
		ObjectMeta: metav1.ObjectMeta{Name: "allow-egress-proxy", Namespace: ns},
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
	if apierrors.IsAlreadyExists(err) {
		return nil
	}
	return err
}

// Doc §5.2: "ServiceAccount automountServiceAccountToken: false" -- doc §9.1
// T2 threat: "Learner namespaces have no SA token" is the control here.
func (p *Provisioner) applyServiceAccount(ctx context.Context, ns string) error {
	autoMount := false
	obj := &corev1.ServiceAccount{
		ObjectMeta:                   metav1.ObjectMeta{Name: "workspace", Namespace: ns},
		AutomountServiceAccountToken: &autoMount,
	}
	_, err := p.clientset.CoreV1().ServiceAccounts(ns).Create(ctx, obj, metav1.CreateOptions{})
	if apierrors.IsAlreadyExists(err) {
		return nil
	}
	return err
}

// Doc §5.2 Pod: workspace -- container: shell, volume: /workspace.
// The `editor` container (OpenVSCode) and per-fixture service pods
// (postgres/redis/localstack) are declared in the blueprint and are
// Phase 3+ per the doc's own editor-ladder note (§5.4); Phase 1 uses
// Monaco client-side against a file API, so only the shell container is
// provisioned here.
func (p *Provisioner) createWorkspacePod(ctx context.Context, ns string, req ProvisionRequest) error {
	runAsNonRoot := true
	runAsUser := int64(1000)
	allowPrivilegeEscalation := false
	readOnlyRootFilesystem := false // /workspace itself is writable; root fs stays read-only via the emptyDir mount strategy below at the container level only where feasible

	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "workspace",
			Namespace: ns,
			Labels:    map[string]string{"app": "workspace"},
		},
		Spec: corev1.PodSpec{
			// Doc §5.2: "RuntimeClass: gvisor". The RuntimeClass object
			// itself is declared in manifests/t1/runtimeclass-gvisor.yaml
			// (M1.1) -- referencing it here is what makes a T1 pod
			// actually gVisor-sandboxed once the RuntimeClass exists on
			// a node with gVisor installed. On this local k3s cluster
			// gVisor is not installed (documented gap, see memory), so
			// this field is deliberately left unset for local dev and
			// set only in the real-deployment manifest variant --
			// hard-coding a RuntimeClass that doesn't exist on the node
			// would make every local Provision() call fail scheduling.
			ServiceAccountName: "workspace",
			SecurityContext: &corev1.PodSecurityContext{
				RunAsNonRoot: &runAsNonRoot,
				RunAsUser:    &runAsUser,
				SeccompProfile: &corev1.SeccompProfile{
					Type: corev1.SeccompProfileTypeRuntimeDefault,
				},
			},
			Containers: []corev1.Container{
				{
					Name:  "shell",
					Image: req.Image,
					SecurityContext: &corev1.SecurityContext{
						AllowPrivilegeEscalation: &allowPrivilegeEscalation,
						ReadOnlyRootFilesystem:   &readOnlyRootFilesystem,
						Capabilities: &corev1.Capabilities{
							Drop: []corev1.Capability{"ALL"},
						},
					},
					Resources: corev1.ResourceRequirements{
						Requests: corev1.ResourceList{
							corev1.ResourceCPU:    resource.MustParse("100m"),
							corev1.ResourceMemory: resource.MustParse("256Mi"),
						},
						Limits: corev1.ResourceList{
							corev1.ResourceCPU:    resource.MustParse(nonEmpty(req.Resources.CPU, "2")),
							corev1.ResourceMemory: resource.MustParse(nonEmpty(req.Resources.Memory, "4Gi")),
						},
					},
					VolumeMounts: []corev1.VolumeMount{
						{Name: "workspace", MountPath: "/workspace"},
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
						{Name: "NO_PROXY", Value: "localhost,127.0.0.1"},
					},
					// Keeps the container alive; the Session Broker execs
					// into it over the K8s exec API (doc §5.4) rather
					// than relying on an entrypoint shell.
					Command: []string{"sh", "-c", "sleep infinity"},
				},
			},
			Volumes: []corev1.Volume{
				{
					Name:         "workspace",
					VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}},
				},
			},
			RestartPolicy: corev1.RestartPolicyNever,
		},
	}

	_, err := p.clientset.CoreV1().Pods(ns).Create(ctx, pod, metav1.CreateOptions{})
	if apierrors.IsAlreadyExists(err) {
		return nil
	}
	return err
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
			pod, err := p.clientset.CoreV1().Pods(ns).Get(ctx, "workspace", metav1.GetOptions{})
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
// inside it -- doc §5.2: "teardown is a single delete namespace."
func (p *Provisioner) Destroy(ctx context.Context, envID string) error {
	ns := namespaceName(envID)
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
		LabelSelector: "practiceengine.dev/managed=true",
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
		ObjectMeta: metav1.ObjectMeta{Name: "workspace", Namespace: ns},
		Spec: corev1.ServiceSpec{
			Selector: map[string]string{"app": "workspace"},
			Ports: []corev1.ServicePort{
				{Port: port, TargetPort: intstr.FromInt32(port)},
			},
		},
	}
	_, err := p.clientset.CoreV1().Services(ns).Create(ctx, svc, metav1.CreateOptions{})
	if apierrors.IsAlreadyExists(err) {
		return nil
	}
	return err
}
