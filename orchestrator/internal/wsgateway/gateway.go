// Package wsgateway implements doc §5.4's WS Gateway: "stateless, JWT/
// session auth per socket, attempt-scoped authorization, rate +
// backpressure limits." M1.6. Doc §8.1: separates the stateless
// authenticating front door from the Session Broker's stateful PTY
// multiplexing -- this package owns only the HTTP upgrade + auth +
// routing to Broker.Attach, no session state of its own (that's the
// literal meaning of "stateless" here: any gateway instance can serve
// any request, since it holds nothing that must survive a restart).
package wsgateway

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/gorilla/websocket"
	"golang.org/x/time/rate"

	"github.com/tanyasawarn/skillfyme-hands-on/orchestrator/internal/metrics"
	"github.com/tanyasawarn/skillfyme-hands-on/orchestrator/internal/ttl"
)

// SessionValidator checks that a session_token (minted by
// Connect/Provision, doc §5.4) is valid before the gateway upgrades the
// connection -- a real signed JWT (HS256), not an opaque UUID: claims
// (attempt_id, env_id, exp) are verifiable independently of the
// in-memory registry below, which exists only to support Revoke()
// (JWTs are normally stateless; immediate revocation needs a
// server-side check a bare signature can't provide). Signed with
// WS_GATEWAY_JWT_SECRET, the same shared secret Practice Core's
// JWT_SECRET uses (see cmd/orchestrator/main.go) -- doc §5.4's "JWT/
// session auth per socket" cross-track contract.
type SessionValidator interface {
	// Validate returns the (attemptID, envID) pair a token is valid for,
	// or ok=false. Both come from the token record, not the URL -- the
	// URL's {envID} path segment is used only to route the HTTP request
	// to this handler and is cross-checked against the token's own envID
	// below, so a stale/forged path segment can't be paired with a valid
	// token for a *different* environment.
	Validate(token string) (attemptID, envID string, ok bool)
}

// Attacher is the narrow interface wsgateway needs from
// internal/sessionbroker.Broker -- kept separate so this package doesn't
// import sessionbroker directly, matching the doc's "stateless front
// door, stateful broker behind it" split at the code layer too.
type Attacher interface {
	Attach(ctx context.Context, attemptID, envID string, ws *websocket.Conn) error
}

type Gateway struct {
	validator SessionValidator
	broker    Attacher
	upgrader  websocket.Upgrader
	limiter   *rate.Limiter
}

// New constructs a Gateway. allowedOrigins is the WS_GATEWAY_ALLOWED_ORIGINS
// allowlist (see cmd/orchestrator/main.go) -- an empty slice means no
// allowlist is configured, which this treats as "local dev only" and
// accepts any origin (loudly logged, never the right posture for a real
// deployment: see the CSWSH note below). A non-empty slice enforces an
// exact match against the WebSocket upgrade request's Origin header.
func New(validator SessionValidator, broker Attacher, allowedOrigins []string) *Gateway {
	if len(allowedOrigins) == 0 {
		log.Printf("[wsgateway] WARNING: WS_GATEWAY_ALLOWED_ORIGINS is unset -- accepting WebSocket upgrades from any Origin. This is only safe for local dev; a real deployment MUST set this to the platform's own frontend origin(s) or the terminal socket is open to cross-site WebSocket hijacking (CSWSH).")
	}
	originSet := make(map[string]struct{}, len(allowedOrigins))
	for _, o := range allowedOrigins {
		originSet[o] = struct{}{}
	}

	return &Gateway{
		validator: validator,
		broker:    broker,
		upgrader: websocket.Upgrader{
			// Doc §5.4: learners never get a routable address into their
			// environment; this gateway is the only origin allowed to
			// open the tunnel -- a page from any other origin must not be
			// able to open this WebSocket even carrying a valid session
			// token in the URL (CSWSH), which is why this checks Origin
			// rather than relying on the token alone.
			CheckOrigin: func(r *http.Request) bool {
				if len(originSet) == 0 {
					return true // local dev fallback; see the startup warning above
				}
				_, ok := originSet[r.Header.Get("Origin")]
				return ok
			},
		},
		// Doc §5.4 "rate + backpressure limits". Doc §9.4's WS message
		// rate default: "100, with backpressure" per session/second.
		limiter: rate.NewLimiter(rate.Limit(100), 100),
	}
}

// HandleTerminal serves the terminal WebSocket at
// /v1/env/{envID}/terminal?session=<token> -- the URL this package's own
// server.go connectionEndpoints() generates. (The doc's learner-facing
// route in §8.3, WSS /v1/practice/attempts/{id}/terminal, is Practice
// Core's API gateway path; this is the Orchestrator-side URL that
// terminalWsUrl points the browser at directly, per doc §5.4's
// architecture diagram showing the WS Gateway as a distinct hop.)
func (g *Gateway) HandleTerminal(w http.ResponseWriter, r *http.Request) {
	pathEnvID := r.PathValue("envID")
	token := r.URL.Query().Get("session")
	if token == "" {
		http.Error(w, "session token required", http.StatusUnauthorized)
		return
	}

	attemptID, tokenEnvID, ok := g.validator.Validate(token)
	if !ok {
		http.Error(w, "invalid or expired session token", http.StatusUnauthorized)
		return
	}
	if tokenEnvID != pathEnvID {
		// Doc §9.1 T3 threat ("cross-learner data access"): a token
		// minted for one environment must never be usable against a
		// different environment's URL, even if both tokens are
		// individually valid.
		http.Error(w, "session token does not match environment", http.StatusForbidden)
		return
	}
	envID := tokenEnvID

	conn, err := g.upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("[wsgateway] upgrade failed: %v", err)
		return
	}
	defer conn.Close()

	// doc §5.4: the client reconnect loop
	// (web/src/components/WorkspaceTerminal.tsx) opens a fresh socket
	// with a new session token on every drop, so a rising connection
	// count relative to distinct active attempts is the signal for
	// gateway or network instability affecting learners mid-lab.
	metrics.WSConnectionsTotal.Inc()
	metrics.WSSessionsActive.Inc()
	defer metrics.WSSessionsActive.Dec()

	ctx, cancel := context.WithTimeout(r.Context(), 6*time.Hour) // doc-consistent upper bound; real teardown is TTL/idle-driven, not this timeout
	defer cancel()

	if err := g.rateLimitedAttach(ctx, attemptID, envID, conn); err != nil {
		log.Printf("[wsgateway] session ended for attempt=%s env=%s: %v", attemptID, envID, err)
	}
}

func (g *Gateway) rateLimitedAttach(ctx context.Context, attemptID, envID string, conn *websocket.Conn) error {
	if err := g.limiter.WaitN(ctx, 1); err != nil {
		return err
	}
	return g.broker.Attach(ctx, attemptID, envID, conn)
}

// sessionTokenTTL bounds how long a minted terminal session token stays
// valid. A leaked token (browser history, proxy log, referrer) must not
// stay usable indefinitely -- reconnect goes through Connect() again,
// which mints a fresh token, so this doesn't need to outlive a single
// working session. Kept well under ttl.EnvironmentDefault (90min, doc
// §5.5, now a compile-time-linked reference via internal/ttl) so a
// token can't outlive a reasonable session even on a long-running
// environment.
const sessionTokenTTL = ttl.SessionToken

// sessionClaims is the JWT payload minted for a terminal session --
// attempt_id/env_id are the authorization-relevant claims HandleTerminal
// checks (envID cross-checked against the URL's {envID} path segment,
// same anti-cross-environment-reuse property the old opaque-token
// registry provided, now verifiable from the signature alone).
type sessionClaims struct {
	AttemptID string `json:"attempt_id"`
	EnvID     string `json:"env_id"`
	jwt.RegisteredClaims
}

// TokenValidator issues and verifies signed JWTs (HS256) for terminal
// sessions. A small in-memory revocation registry sits alongside the
// signature check -- Register/Revoke give Destroy() a way to invalidate
// a still-unexpired token immediately (doc §5.4's teardown path), which
// a bare JWT signature check alone can't do. Sufficient for Phase 1's
// single-orchestrator-instance deployment; a multi-instance deployment
// would need the revocation registry backed by Redis (the same store
// warmpool already depends on) so any gateway instance sees a revocation
// issued via any orchestrator instance -- signature verification itself
// needs no such sharing, since every instance holds the same secret.
type TokenValidator struct {
	mu      sync.Mutex
	revoked map[string]struct{}
	secret  []byte
	now     func() time.Time
}

// NewTokenValidator builds a validator signing/verifying with secret.
// secret must be non-empty and shared with Practice Core's JWT_SECRET
// (or any other verifier) -- see cmd/orchestrator/main.go's
// WS_GATEWAY_JWT_SECRET wiring.
func NewTokenValidator(secret string) *TokenValidator {
	if secret == "" {
		panic("wsgateway: NewTokenValidator requires a non-empty secret")
	}
	return &TokenValidator{revoked: make(map[string]struct{}), secret: []byte(secret), now: time.Now}
}

// Register mints a signed JWT for the given (attemptID, envID) pair,
// valid for sessionTokenTTL. Named Register (not Mint) to keep the
// SessionValidator-adjacent call sites in server.go unchanged -- the
// caller still treats this as "register a session," it just now returns
// the token to hand back to the client rather than taking one in.
func (v *TokenValidator) Register(attemptID, envID string) (string, error) {
	now := v.now()
	claims := sessionClaims{
		AttemptID: attemptID,
		EnvID:     envID,
		RegisteredClaims: jwt.RegisteredClaims{
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(now.Add(sessionTokenTTL)),
		},
	}
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	signed, err := token.SignedString(v.secret)
	if err != nil {
		return "", fmt.Errorf("signing session token: %w", err)
	}
	return signed, nil
}

// Revoke invalidates a token immediately, even though it hasn't expired
// yet -- e.g. on Destroy(), so a terminal WS reconnect attempt against a
// torn-down environment fails closed rather than succeeding on a still
// cryptographically-valid signature.
func (v *TokenValidator) Revoke(token string) {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.revoked[token] = struct{}{}
}

func (v *TokenValidator) Validate(token string) (attemptID, envID string, ok bool) {
	v.mu.Lock()
	_, isRevoked := v.revoked[token]
	v.mu.Unlock()
	if isRevoked {
		return "", "", false
	}

	claims := &sessionClaims{}
	parsed, err := jwt.ParseWithClaims(token, claims, func(t *jwt.Token) (interface{}, error) {
		if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, errors.New("unexpected signing method")
		}
		return v.secret, nil
	}, jwt.WithTimeFunc(v.now))
	if err != nil || !parsed.Valid {
		return "", "", false
	}
	return claims.AttemptID, claims.EnvID, true
}

// SplitBearer is a small helper for extracting a bearer token from an
// Authorization header, kept here since the WS upgrade path in Go's
// stdlib http doesn't parse it for you and doc §5.4 mentions
// "JWT/session auth per socket" without specifying the exact channel
// (query param vs header) -- Phase 1 uses the query param (simpler for
// a browser WebSocket client, which cannot set arbitrary headers on the
// upgrade request), this helper exists for the header-based path some
// non-browser clients (grpcurl-style test tools) may prefer.
func SplitBearer(header string) (string, bool) {
	const prefix = "Bearer "
	if !strings.HasPrefix(header, prefix) {
		return "", false
	}
	return strings.TrimPrefix(header, prefix), true
}
