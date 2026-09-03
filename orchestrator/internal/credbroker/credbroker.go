// Package credbroker implements Stage 2.1's STS credential broker: the
// sidecar-shaped component that keeps short-lived AWS credentials fresh
// in a T3 workspace pod and stops the instant the attempt leaves
// IN_PROGRESS (memory.md §5.3, §9.3, D9; PLAN_PHASE3_PROJECTS.md A3).
//
// Per attempt:
//   - the platform IdP mints a short-lived JWT keyed on the attempt id
//     (sub = attempt id, aud = the platform client id) — that half is
//     the IdP's job; this package takes the token and does the exchange;
//   - AssumeRoleWithWebIdentity against LearnerSandboxRole in the
//     claimed account (cloudaws.Client);
//   - writes the creds to a shared path (an emptyDir in the real pod;
//     an injected Writer here so it is testable);
//   - refreshes at 50% of the credential TTL;
//   - stops the moment Stop() is called — the caller wires Stop() to the
//     attempt-state stream (the same env.lifecycle.* / ENV_DESTROYED
//     signal the reaper consumes), so a suspended attempt's next refresh
//     never happens and the outstanding creds expire within the hour.
//
// No AWS dependency in tests: cloudaws.FakeClient + an in-memory Writer.
package credbroker

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/tanyasawarn/skillfyme-hands-on/orchestrator/internal/cloudaws"
	"github.com/tanyasawarn/skillfyme-hands-on/orchestrator/internal/logging"
	"github.com/tanyasawarn/skillfyme-hands-on/orchestrator/internal/metrics"
)

var log = logging.Component("credbroker")

// CredsWriter persists a credential set where the workspace pod's tools
// read it. The real impl writes an AWS-CLI credentials file to the
// shared emptyDir; tests use an in-memory implementation.
type CredsWriter interface {
	Write(ctx context.Context, attemptID string, creds cloudaws.AssumeRoleResult) error
}

// TokenSource returns the current platform-IdP JWT for an attempt. The
// real impl calls the IdP's token endpoint; it must return a fresh,
// unexpired token each call (the IdP tokens are themselves short-lived).
type TokenSource interface {
	WebIdentityToken(ctx context.Context, attemptID string) (string, error)
}

// Config for one broker instance (one attempt).
type Config struct {
	AttemptID string
	AccountID string
	RoleName  string
	// CredTTL is the requested STS session duration. Refresh happens at
	// RefreshFraction of this.
	CredTTL         time.Duration
	RefreshFraction float64
}

// Broker runs the refresh loop for one attempt's sandbox credentials.
type Broker struct {
	cfg    Config
	aws    cloudaws.Client
	writer CredsWriter
	tokens TokenSource

	mu      sync.Mutex
	stopped bool
	stopCh  chan struct{}
	doneCh  chan struct{}
}

func New(cfg Config, aws cloudaws.Client, writer CredsWriter, tokens TokenSource) *Broker {
	if cfg.CredTTL <= 0 {
		cfg.CredTTL = time.Hour // §5.3: max-1h session
	}
	if cfg.RefreshFraction <= 0 || cfg.RefreshFraction >= 1 {
		cfg.RefreshFraction = 0.5 // "refreshes at 50% TTL"
	}
	return &Broker{
		cfg:    cfg,
		aws:    aws,
		writer: writer,
		tokens: tokens,
		stopCh: make(chan struct{}),
		doneCh: make(chan struct{}),
	}
}

// Start does an initial credential mint (blocking, so the caller knows
// the pod has creds before it reports READY) and launches the refresh
// loop. Returns the first credential expiry.
func (b *Broker) Start(ctx context.Context) (time.Time, error) {
	first, err := b.refreshOnce(ctx)
	if err != nil {
		return time.Time{}, fmt.Errorf("credbroker: initial mint: %w", err)
	}
	go b.loop()
	return first.Expiration, nil
}

// Stop halts the refresh loop. The next scheduled refresh never runs, so
// the outstanding creds simply expire (within CredTTL). Idempotent.
func (b *Broker) Stop() {
	b.mu.Lock()
	if b.stopped {
		b.mu.Unlock()
		return
	}
	b.stopped = true
	close(b.stopCh)
	b.mu.Unlock()
	<-b.doneCh
	metrics.CredBrokerRefreshTotal.WithLabelValues("stopped").Inc()
	log.Info("credential broker stopped — outstanding creds will expire",
		"attempt_id", b.cfg.AttemptID, "within", b.cfg.CredTTL)
}

// Stopped reports whether Stop has been called.
func (b *Broker) Stopped() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.stopped
}

func (b *Broker) loop() {
	defer close(b.doneCh)
	interval := time.Duration(float64(b.cfg.CredTTL) * b.cfg.RefreshFraction)
	t := time.NewTimer(interval)
	defer t.Stop()
	for {
		select {
		case <-b.stopCh:
			return
		case <-t.C:
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			res, err := b.refreshOnce(ctx)
			cancel()
			if err != nil {
				// A failed refresh is not fatal — the current creds are
				// still valid until their expiry; try again on the next
				// tick. If the attempt was suspended, Stop() will have
				// closed stopCh and we won't get here.
				log.Warn("credential refresh failed (will retry)",
					"attempt_id", b.cfg.AttemptID, "err", err)
				t.Reset(interval / 4)
				continue
			}
			// schedule the next refresh at 50% of the *actual* remaining
			// lifetime, in case STS granted less than requested.
			remaining := time.Until(res.Expiration)
			next := time.Duration(float64(remaining) * b.cfg.RefreshFraction)
			if next < time.Minute {
				next = time.Minute
			}
			t.Reset(next)
		}
	}
}

func (b *Broker) refreshOnce(ctx context.Context) (cloudaws.AssumeRoleResult, error) {
	token, err := b.tokens.WebIdentityToken(ctx, b.cfg.AttemptID)
	if err != nil {
		metrics.CredBrokerRefreshTotal.WithLabelValues("error").Inc()
		return cloudaws.AssumeRoleResult{}, fmt.Errorf("token source: %w", err)
	}
	creds, err := b.aws.AssumeRoleWithWebIdentity(ctx, b.cfg.AccountID, b.cfg.RoleName, token, b.cfg.CredTTL)
	if err != nil {
		metrics.CredBrokerRefreshTotal.WithLabelValues("error").Inc()
		return cloudaws.AssumeRoleResult{}, fmt.Errorf("assume-role: %w", err)
	}
	if err := b.writer.Write(ctx, b.cfg.AttemptID, creds); err != nil {
		metrics.CredBrokerRefreshTotal.WithLabelValues("error").Inc()
		return cloudaws.AssumeRoleResult{}, fmt.Errorf("write creds: %w", err)
	}
	metrics.CredBrokerRefreshTotal.WithLabelValues("ok").Inc()
	log.Debug("brokered credentials refreshed",
		"attempt_id", b.cfg.AttemptID, "expires_at", creds.Expiration.Format(time.RFC3339))
	return creds, nil
}

// --- Registry: one broker per active T3 attempt -----------------------

// Registry owns the set of running brokers and the wiring to stop one
// when its attempt leaves IN_PROGRESS. cmd/orchestrator subscribes to
// the attempt-state stream and calls StopFor on ENV_DESTROYED / suspend.
type Registry struct {
	mu      sync.Mutex
	brokers map[string]*Broker // attemptID -> broker
}

func NewRegistry() *Registry {
	return &Registry{brokers: map[string]*Broker{}}
}

// Add registers and starts a broker for an attempt. Returns the first
// credential expiry so the driver can surface it.
func (r *Registry) Add(ctx context.Context, cfg Config, aws cloudaws.Client, w CredsWriter, ts TokenSource) (time.Time, error) {
	b := New(cfg, aws, w, ts)
	exp, err := b.Start(ctx)
	if err != nil {
		return time.Time{}, err
	}
	r.mu.Lock()
	// Replace any prior broker for this attempt (resume-from-suspend).
	if old := r.brokers[cfg.AttemptID]; old != nil {
		go old.Stop()
	}
	r.brokers[cfg.AttemptID] = b
	r.mu.Unlock()
	return exp, nil
}

// StopFor stops and removes the broker for an attempt (no-op if none).
// This is what the caller wires to the attempt-left-IN_PROGRESS signal.
func (r *Registry) StopFor(attemptID string) {
	r.mu.Lock()
	b := r.brokers[attemptID]
	delete(r.brokers, attemptID)
	r.mu.Unlock()
	if b != nil {
		b.Stop()
	}
}

// Active returns the attempt ids with a running broker.
func (r *Registry) Active() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	ids := make([]string, 0, len(r.brokers))
	for id := range r.brokers {
		ids = append(ids, id)
	}
	return ids
}
