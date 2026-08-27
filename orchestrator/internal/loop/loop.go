// Package loop implements PLAN.md Phase 3's U9: the
// "ticker + select{stop, tick}" background-loop shape was reimplemented
// 4 times (internal/reaper, internal/idledetect, internal/costmeter,
// internal/warmpool) before this extraction, with a real behavioral
// divergence between copies -- warmpool.Filler.Run calls fn once
// immediately before entering the ticker loop ("don't wait a full
// interval before the first fill on startup"), the other three don't.
//
// That divergence is deliberate, not a bug to unify away: warmpool
// exists specifically so a Claim() call finds a pre-provisioned
// environment ready; if the pool started empty and stayed empty for a
// full interval (its own default: 20s) before the first fill, every
// early claim would miss the exact thing the pool exists to prevent.
// reaper/idledetect/costmeter are monitoring/sweeping loops with
// nothing meaningful to check at cold start (nothing has had time to go
// idle, go overdue, or accrue cost in the first tick of a fresh
// process), so waiting one interval before the first run is harmless
// for them. RunTicker keeps this as an explicit, named parameter
// (runImmediately) rather than picking one behavior for every caller --
// exactly PLAN.md's own signature.
package loop

import "time"

// RunTicker runs fn every interval until stop is closed (or receives a
// value). Takes a plain <-chan struct{} rather than a context.Context
// directly: context.Context.Done() already returns exactly this type,
// so every ctx.Done()-based caller (reaper, idledetect, warmpool) passes
// that unchanged, while costmeter -- whose lifecycle is
// NewMeter()...Close(), not request-scoped, and has no context.Context
// in its constructor at all -- can pass its own stopCh without being
// forced into an unrelated API change just to adopt this helper.
func RunTicker(stop <-chan struct{}, interval time.Duration, fn func(), runImmediately bool) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	if runImmediately {
		fn()
	}

	for {
		select {
		case <-stop:
			return
		case <-ticker.C:
			fn()
		}
	}
}
