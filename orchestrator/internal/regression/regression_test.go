package regression

import (
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// PLAN.md Phase 2: the NO_REGRESSION validator type (doc §6.2, §7.3
// step 3). diffMatrices is the pure "didn't fix by breaking something
// else" comparison -- one-directional: only worse-than-baseline counts.

func dep(name string, replicas, ready int32) DeploymentHealth {
	return DeploymentHealth{Name: name, Replicas: replicas, ReadyReplicas: ready, AvailableReplicas: ready}
}
func svc(name string, endpoints int) ServiceHealth {
	return ServiceHealth{Name: name, EndpointCount: endpoints}
}

func TestDiffMatrices_NoChangeYieldsNoRegressions(t *testing.T) {
	m := HealthMatrix{
		Deployments: []DeploymentHealth{dep("api", 3, 3)},
		Services:    []ServiceHealth{svc("api", 3)},
	}
	if got := diffMatrices(m, m); len(got) != 0 {
		t.Fatalf("expected no regressions, got %+v", got)
	}
}

func TestDiffMatrices_ReadyReplicasDropIsARegression(t *testing.T) {
	base := HealthMatrix{Deployments: []DeploymentHealth{dep("api", 3, 3)}}
	cur := HealthMatrix{Deployments: []DeploymentHealth{dep("api", 3, 1)}}
	got := diffMatrices(base, cur)
	if len(got) != 1 || got[0].Resource != "Deployment/api" {
		t.Fatalf("expected one Deployment/api regression, got %+v", got)
	}
}

func TestDiffMatrices_ReadyReplicasIncreaseIsNotARegression(t *testing.T) {
	base := HealthMatrix{Deployments: []DeploymentHealth{dep("api", 3, 1)}}
	cur := HealthMatrix{Deployments: []DeploymentHealth{dep("api", 3, 3)}}
	if got := diffMatrices(base, cur); len(got) != 0 {
		t.Fatalf("improvement must not be flagged, got %+v", got)
	}
}

func TestDiffMatrices_DeletedDeploymentIsARegression(t *testing.T) {
	base := HealthMatrix{Deployments: []DeploymentHealth{dep("api", 1, 1), dep("worker", 1, 1)}}
	cur := HealthMatrix{Deployments: []DeploymentHealth{dep("api", 1, 1)}}
	got := diffMatrices(base, cur)
	if len(got) != 1 || got[0].Resource != "Deployment/worker" {
		t.Fatalf("expected Deployment/worker gone regression, got %+v", got)
	}
}

func TestDiffMatrices_NewDeploymentIsNotARegression(t *testing.T) {
	base := HealthMatrix{Deployments: []DeploymentHealth{dep("api", 1, 1)}}
	cur := HealthMatrix{Deployments: []DeploymentHealth{dep("api", 1, 1), dep("cache", 1, 1)}}
	if got := diffMatrices(base, cur); len(got) != 0 {
		t.Fatalf("a new deployment is not a regression, got %+v", got)
	}
}

func TestDiffMatrices_ServiceEndpointsDroppingToZeroIsARegression(t *testing.T) {
	base := HealthMatrix{Services: []ServiceHealth{svc("payment", 2)}}
	cur := HealthMatrix{Services: []ServiceHealth{svc("payment", 0)}}
	got := diffMatrices(base, cur)
	if len(got) != 1 || got[0].Resource != "Service/payment" {
		t.Fatalf("expected Service/payment endpoints regression, got %+v", got)
	}
}

func TestDiffMatrices_ServiceEndpointsPartialDropIsNotFlagged(t *testing.T) {
	// Only 0 endpoints is a regression -- a partial drop (2 -> 1) is not,
	// per Diff's own one-directional framing (it tracks "went dark", not
	// "got smaller").
	base := HealthMatrix{Services: []ServiceHealth{svc("payment", 2)}}
	cur := HealthMatrix{Services: []ServiceHealth{svc("payment", 1)}}
	if got := diffMatrices(base, cur); len(got) != 0 {
		t.Fatalf("partial endpoint drop must not be flagged, got %+v", got)
	}
}

func TestDeploymentHealthOf_ReadsSpecAndStatus(t *testing.T) {
	r := int32(4)
	d := appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: "checkout"},
		Spec:       appsv1.DeploymentSpec{Replicas: &r},
		Status:     appsv1.DeploymentStatus{ReadyReplicas: 2, AvailableReplicas: 3},
	}
	h := deploymentHealthOf(d)
	if h.Name != "checkout" || h.Replicas != 4 || h.ReadyReplicas != 2 || h.AvailableReplicas != 3 {
		t.Fatalf("deploymentHealthOf = %+v", h)
	}
}

func TestDeploymentHealthOf_NilReplicasTreatedAsZero(t *testing.T) {
	d := appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: "x"}}
	if h := deploymentHealthOf(d); h.Replicas != 0 {
		t.Fatalf("nil Spec.Replicas should read as 0, got %d", h.Replicas)
	}
}
