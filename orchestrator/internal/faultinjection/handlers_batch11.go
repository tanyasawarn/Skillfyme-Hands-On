package faultinjection

import (
	"context"
	"fmt"
	"strings"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/tanyasawarn/skillfyme-hands-on/orchestrator/internal/k8s"
)

// Eleventh batch: f.ansible.inventory-host-unreachable, backed by
// fx.ansible-target.v1 (internal/fixture/handlers_ansible.go)
// provisioning a real Ansible runner + two real SSH-reachable target
// pods, both confirmed healthy at fixture-apply time.
//
// Mechanism note: the plan for this fault originally called for a
// NetworkPolicy blocking egress to the target, matching this codebase's
// existing applyNetworkPolicyOverblocksTraffic/
// applyEgressProxyAllowlistTooStrict handlers. Live-tested against this
// project's real dev cluster during this fault's own development: k3s's
// default CNI (Flannel, no netpol controller installed) does NOT enforce
// NetworkPolicy at all -- confirmed with a real deny-all-egress policy
// that had zero observable effect on real traffic. This is a real,
// pre-existing environmental limitation (flagged separately for the
// existing NetworkPolicy-based fault handlers, which may share this same
// live-verification gap), not something this fault's own handler can fix
// -- so this handler uses a mechanism this environment DOES genuinely
// enforce instead: corrupting the inventory's own ansible_host entry for
// the target so SSH itself fails at DNS resolution
// ("Could not resolve hostname"), confirmed live to produce exactly the
// fault's own canonical_diagnostic_path symptom (UNREACHABLE! for one
// specific host, not the whole play).
func init() {
	registerDynamic("f.ansible.inventory-host-unreachable", applyAnsibleInventoryHostUnreachable)
}

const ansibleTargetableHost = "target2" // must match ansibleInventoryHost in fx.ansible-target.v1

// applyAnsibleInventoryHostUnreachable: content/faults/f.ansible.inventory-host-unreachable.yaml
// params: inventory_host (must match the fixture's real blockable host,
// "target2" -- target1 is the fixture's always-reachable control host by
// design, validated below rather than trusted blindly).
func applyAnsibleInventoryHostUnreachable(ctx context.Context, provisioner *k8s.Provisioner, namespace string, params map[string]string) (Result, error) {
	inventoryHost := params["inventory_host"]
	if inventoryHost == "" {
		return Result{}, fmt.Errorf("f.ansible.inventory-host-unreachable requires param: inventory_host")
	}
	if inventoryHost != ansibleTargetableHost {
		return Result{}, fmt.Errorf("f.ansible.inventory-host-unreachable: inventory_host %q does not match the fixture's real blockable host %q", inventoryHost, ansibleTargetableHost)
	}

	cms := provisioner.Clientset().CoreV1().ConfigMaps(namespace)
	cm, notFoundOrErrResult, err := getOrNotFound(ctx, func(ctx context.Context) (*corev1.ConfigMap, error) {
		return cms.Get(ctx, ansibleInventoryConfigMapNameConst, metav1.GetOptions{})
	}, "ConfigMap", "configmap", ansibleInventoryConfigMapNameConst)
	if err != nil {
		return notFoundOrErrResult, err
	}

	current := cm.Data["inventory.ini"]
	const workingHostLine = "target2 ansible_host=practice-ansible-target2 ansible_port=2222 ansible_user=ansible"
	const brokenHostLine = "target2 ansible_host=practice-ansible-target2-unreachable ansible_port=2222 ansible_user=ansible"
	if strings.Contains(current, "practice-ansible-target2-unreachable") {
		return Result{Applied: true, SymptomVerified: true}, nil
	}
	if !strings.Contains(current, workingHostLine) {
		return Result{}, fmt.Errorf("expected inventory line for %s not found -- fixture ConfigMap may be out of sync", inventoryHost)
	}
	cm.Data["inventory.ini"] = strings.Replace(current, workingHostLine, brokenHostLine, 1)
	if _, err := cms.Update(ctx, cm, metav1.UpdateOptions{}); err != nil {
		return Result{}, fmt.Errorf("updating inventory ConfigMap: %w", err)
	}

	// The ConfigMap volume propagation delay this codebase's Prometheus/
	// Jaeger faults already document (kubelet's own sync interval, not
	// instantaneous) applies here too -- the runner pod picks up the
	// change on its own schedule, so SymptomVerified is conservatively
	// false rather than claiming an unobserved runtime effect (matching
	// f.jaeger.missing-trace-context-propagation's own stance). The
	// ConfigMap mutation itself (the real, durable fault state) has
	// already succeeded above regardless.
	return Result{Applied: true, SymptomVerified: false}, nil
}

const ansibleInventoryConfigMapNameConst = "practice-ansible-inventory"
