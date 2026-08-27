// Package logging gives the orchestrator one structured (JSON) logger so
// every lifecycle line carries machine-parseable fields instead of a
// free-text string.
//
// PHASE1_MVP_COMPLETION.md §4.2 / doc §13.5 #1: "attempt_id as a log
// field on every cross-service log." The reaper, idle detector, cost
// meter and destroyer paths -- the ones an operator greps during an
// incident -- previously emitted `log.Printf("[reaper] force-destroying
// overdue environment %s ...", envID)`. That is unfilterable: you cannot
// `{env_id="..."}` it in Loki, and there is no stable `reason` field to
// alert on. This package replaces those with slog JSON records keyed on
// the canonical field names (env_id, attempt_id, reason, component).
//
// Scope is deliberately the lifecycle/teardown paths, not a
// whole-codebase sweep -- the same boundary PHASE1_MVP_COMPLETION.md
// §4.2 draws. `log.Printf` elsewhere (boot banners, warnings) is left
// alone; it still goes to the same stderr.
package logging

import (
	"context"
	"log"
	"log/slog"
	"os"
)

// Canonical structured-field keys. Use these constants, never string
// literals, so a rename is a compile error and Loki/Grafana queries stay
// stable.
const (
	KeyComponent = "component"
	KeyEnvID     = "env_id"
	KeyAttemptID = "attempt_id"
	KeyReason    = "reason"
	KeyNamespace = "namespace"
	KeyError     = "error"
	KeyCount     = "count"
)

var base *slog.Logger

func init() {
	// Default before Init(): JSON to stderr at Info. Init() from main()
	// can swap the level, but nothing should log a nil logger just
	// because Init() ran late.
	base = slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))
}

// Init installs the process-wide JSON logger and points the standard
// library's log package at it too, so a stray log.Printf in a path this
// package hasn't converted still lands as a structured record (message
// under the "msg" key, level INFO) on the same stream rather than as a
// second, differently-formatted line.
func Init(level slog.Level) {
	h := slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: level})
	base = slog.New(h)
	slog.SetDefault(base)
	// Bridge log.Printf/Println -> slog at Info. slog.NewLogLogger wires
	// the std logger's output through this handler.
	std := slog.NewLogLogger(h, slog.LevelInfo)
	log.SetFlags(0)
	log.SetOutput(std.Writer())
}

// Component returns a logger tagged with component=<name>, e.g.
// logging.Component("reaper").
func Component(name string) *slog.Logger {
	return base.With(KeyComponent, name)
}

// L returns the untagged base logger (rare -- prefer Component).
func L() *slog.Logger { return base }

// Env is shorthand for the very common "log about one environment"
// case: logging.Env("reaper", envID).Info("force-destroying", ...).
func Env(component, envID string) *slog.Logger {
	return base.With(KeyComponent, component, KeyEnvID, envID)
}

// WithContext is a placeholder seam for later request-scoped fields
// (e.g. a trace id pulled off ctx). Today it just returns Component(name)
// -- kept so call sites that already thread ctx don't need rewriting when
// that lands.
func WithContext(_ context.Context, name string) *slog.Logger {
	return Component(name)
}
