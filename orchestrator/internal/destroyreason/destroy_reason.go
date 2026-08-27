// Package destroyreason centralizes the destroy-reason strings that feed
// Destroyer.Destroy's consumer-facing NATS event field
// (env.environment.destroy_reason, ENV_DESTROYED's payload) -- PLAN.md
// K19. Each producer (idledetect, costmeter-triggered budget hard-stop
// in main.go, the reaper) previously wrote its own bare string literal
// with no shared source. A leaf package (imports nothing internal) so
// idledetect (which deliberately avoids importing internal/k8s or
// internal/orchestrator -- see idledetect/detector.go's own doc
// comment) and internal/orchestrator (which constructs idledetect.
// Detector, so the reverse import would be a real cycle) can both
// import it without creating one.
package destroyreason

const (
	Idle   = "idle"
	Budget = "budget"
	Reaper = "reaper"
	// Submit is never constructed by this codebase -- practice-core
	// supplies it over the wire via DestroyRequest.Reason (the "clean
	// submit" teardown path). Named here anyway so every value this
	// event field can carry has exactly one documented source of truth,
	// not three Go-side consts plus a fourth value nobody names at all.
	Submit = "submit"
)
