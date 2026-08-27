package loop

import (
	"sync/atomic"
	"testing"
	"time"
)

// Short intervals + generous test timeouts, matching this codebase's own
// convention for exercising real ticker/timer behavior (see
// internal/faultinjection's live-cluster-adjacent tests) rather than
// mocking time.

func TestRunTicker_FiresOnEveryTick(t *testing.T) {
	var count int32
	stop := make(chan struct{})

	go RunTicker(stop, 10*time.Millisecond, func() {
		atomic.AddInt32(&count, 1)
	}, false)

	time.Sleep(55 * time.Millisecond)
	close(stop)
	time.Sleep(10 * time.Millisecond) // let RunTicker observe the close and return

	got := atomic.LoadInt32(&count)
	if got < 3 {
		t.Errorf("expected at least 3 ticks in ~55ms at a 10ms interval, got %d", got)
	}
}

func TestRunTicker_RunImmediatelyFiresBeforeFirstTick(t *testing.T) {
	var count int32
	fired := make(chan struct{}, 1)
	stop := make(chan struct{})
	defer close(stop)

	go RunTicker(stop, time.Hour, func() { // interval long enough that only the immediate fire could complete in this test's window
		if atomic.AddInt32(&count, 1) == 1 {
			fired <- struct{}{}
		}
	}, true)

	select {
	case <-fired:
		// good: fn ran before any tick could possibly have fired
	case <-time.After(200 * time.Millisecond):
		t.Fatal("expected fn to fire immediately when runImmediately=true, but it never fired")
	}
}

func TestRunTicker_NoRunImmediatelyWaitsForFirstTick(t *testing.T) {
	var count int32
	stop := make(chan struct{})
	defer close(stop)

	go RunTicker(stop, time.Hour, func() {
		atomic.AddInt32(&count, 1)
	}, false)

	// Give it a real window to (incorrectly) fire immediately if the
	// runImmediately=false path were broken -- with a 1-hour interval
	// and no immediate fire, count must still be 0 after this.
	time.Sleep(50 * time.Millisecond)
	if got := atomic.LoadInt32(&count); got != 0 {
		t.Errorf("expected 0 fires with runImmediately=false and a 1h interval, got %d -- this is exactly the warmpool-vs-reaper behavioral distinction this package exists to keep explicit", got)
	}
}

func TestRunTicker_StopsPromptlyWhenStopChannelCloses(t *testing.T) {
	stop := make(chan struct{})
	done := make(chan struct{})

	go func() {
		RunTicker(stop, 5*time.Millisecond, func() {}, false)
		close(done)
	}()

	time.Sleep(20 * time.Millisecond) // let it tick a few times first
	close(stop)

	select {
	case <-done:
		// good: RunTicker returned
	case <-time.After(500 * time.Millisecond):
		t.Fatal("RunTicker did not return within 500ms of the stop channel closing")
	}
}

func TestRunTicker_DoesNotFireAfterStop(t *testing.T) {
	var count int32
	stop := make(chan struct{})
	done := make(chan struct{})

	go func() {
		RunTicker(stop, 5*time.Millisecond, func() {
			atomic.AddInt32(&count, 1)
		}, false)
		close(done)
	}()

	time.Sleep(20 * time.Millisecond)
	close(stop)
	<-done

	countAtStop := atomic.LoadInt32(&count)
	time.Sleep(50 * time.Millisecond) // long enough for several more ticks if the loop were still running
	if got := atomic.LoadInt32(&count); got != countAtStop {
		t.Errorf("expected no further fires after stop, count changed from %d to %d", countAtStop, got)
	}
}
