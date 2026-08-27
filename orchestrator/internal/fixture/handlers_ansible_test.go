package fixture

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/tanyasawarn/skillfyme-hands-on/orchestrator/internal/k8s"
)

func TestAnsibleFixtureAndFault_LiveIntegration(t *testing.T) {
	provisioner := setupLiveProvisioner(t)
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Second)
	defer cancel()

	envID := uuid.New().String()
	ns := "fx-ansible-test-" + envID[:8]

	clientset := provisioner.Clientset()
	// No PodSecurity "restricted" enforcement on this namespace --
	// linuxserver/openssh-server's own s6-overlay init requires starting
	// as root to manage sshd host keys/permissions (confirmed live
	// during this fixture's own build), which "restricted" cannot
	// accommodate. Documented as a deliberate, scoped exception in
	// handlers_ansible.go's own doc comment.
	if _, err := clientset.CoreV1().Namespaces().Create(ctx, &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{Name: ns},
	}, metav1.CreateOptions{}); err != nil {
		t.Fatalf("creating test namespace: %v", err)
	}
	t.Cleanup(func() {
		_ = clientset.CoreV1().Namespaces().Delete(context.Background(), ns, metav1.DeleteOptions{})
	})
	applyRealT1NetworkBaseline(t, ctx, provisioner, ns)

	if err := applyAnsibleTarget(ctx, provisioner, envID, ns); err != nil {
		t.Fatalf("applyAnsibleTarget failed: %v", err)
	}

	runnerPod, err := k8s.FindPodByLabel(ctx, provisioner, ns, "app="+ansibleRunnerDeployment)
	if err != nil {
		t.Fatalf("finding ansible runner pod: %v", err)
	}

	t.Run("healthy baseline: both inventory hosts respond to a real ansible ping", func(t *testing.T) {
		result, err := k8s.ExecInPod(ctx, provisioner, ns, runnerPod, "ansible",
			"ansible targets -i /ansible-config/inventory.ini -m ping", 30*time.Second)
		if err != nil || result.ExitCode != 0 {
			t.Fatalf("ansible ping failed: err=%v result=%+v", err, result)
		}
		if !strings.Contains(result.Stdout, `"ping": "pong"`) {
			t.Fatalf("expected at least one successful pong, got:\n%s", result.Stdout)
		}
		if strings.Contains(result.Stdout, "UNREACHABLE") {
			t.Fatalf("expected no UNREACHABLE hosts in the healthy baseline, got:\n%s", result.Stdout)
		}
	})

	t.Run("f.ansible.inventory-host-unreachable: target2 genuinely becomes unreachable while target1 stays healthy", func(t *testing.T) {
		cm, err := clientset.CoreV1().ConfigMaps(ns).Get(ctx, ansibleInventoryConfigMapName, metav1.GetOptions{})
		if err != nil {
			t.Fatalf("reading inventory ConfigMap: %v", err)
		}
		const workingHostLine = "target2 ansible_host=practice-ansible-target2 ansible_port=2222 ansible_user=ansible"
		const brokenHostLine = "target2 ansible_host=practice-ansible-target2-unreachable ansible_port=2222 ansible_user=ansible"
		if !strings.Contains(cm.Data["inventory.ini"], workingHostLine) {
			t.Fatalf("fixture's inventory does not contain the expected target2 line -- fixture/fault contract drift:\n%s", cm.Data["inventory.ini"])
		}
		cm.Data["inventory.ini"] = strings.Replace(cm.Data["inventory.ini"], workingHostLine, brokenHostLine, 1)
		if _, err := clientset.CoreV1().ConfigMaps(ns).Update(ctx, cm, metav1.UpdateOptions{}); err != nil {
			t.Fatalf("updating inventory ConfigMap: %v", err)
		}

		// Poll for the ConfigMap change to actually propagate to the
		// runner pod's mounted volume (same kubelet sync-interval
		// caveat this codebase's Prometheus/Jaeger faults document) --
		// re-running the ping until target2 genuinely fails or the
		// deadline is reached.
		deadline := time.Now().Add(90 * time.Second)
		var lastOutput string
		for time.Now().Before(deadline) {
			result, err := k8s.ExecInPod(ctx, provisioner, ns, runnerPod, "ansible",
				"ansible targets -i /ansible-config/inventory.ini -m ping", 30*time.Second)
			lastOutput = result.Stdout + result.Stderr
			if err == nil && strings.Contains(lastOutput, "target2") && strings.Contains(lastOutput, "UNREACHABLE") {
				if strings.Contains(lastOutput, `target1 | SUCCESS`) {
					return
				}
			}
			time.Sleep(5 * time.Second)
		}
		t.Fatalf("expected target2 to become UNREACHABLE while target1 stays SUCCESS within 90s, last output:\n%s", lastOutput)
	})
}
