package orchestrator

import (
	"context"
	"crypto/subtle"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

// AuthInterceptor enforces a shared-secret bearer token on every RPC
// except the standard gRPC health check. Closes the access-control gap
// flagged in PHASE2_CLOSEOUT.md and confirmed by the Phase 0-2 audit:
// Provision/Destroy/Connect/InjectFault/MintValidatorCredentials/
// ExecShell/ExecValidator previously had no caller-identity check at
// all -- grpc.NewServer() had zero interceptors, so anyone who could
// reach the port could call any RPC.
//
// Design: a single pre-shared bearer token (ORCHESTRATOR_SHARED_SECRET),
// checked via constant-time comparison, carried in gRPC metadata as
// "authorization: Bearer <token>" -- the same header shape a real bearer
// scheme uses, so this can be swapped for a richer scheme (mTLS, OIDC)
// later without changing the metadata contract practice-core's client
// already speaks. This is NOT per-attempt/per-environment authorization
// (verifying a caller "owns" a specific attempt_id) -- it's service-level
// authentication (verifying the caller IS practice-core's backend, not
// an arbitrary network peer). The doc's own trust model ("Practice Core
// is the only caller, learners never reach this service directly")
// assumed a network boundary that was never actually enforced in code;
// this interceptor is that enforcement. Per-RPC resource-level
// authorization (e.g. "can THIS caller destroy THIS environment") stays
// out of scope -- practice-core is still the single trusted caller for
// every attempt, not a multi-tenant caller population this token scheme
// would need to distinguish between.
type AuthInterceptor struct {
	// sharedSecret is compared against the "authorization" metadata
	// header's Bearer token on every RPC. Empty string means auth is
	// disabled entirely (local dev without ORCHESTRATOR_SHARED_SECRET
	// set) -- see NewAuthInterceptor's doc comment for why this is a
	// deliberate opt-in rather than a hard requirement.
	sharedSecret string
}

// NewAuthInterceptor builds an AuthInterceptor. An empty sharedSecret
// disables auth entirely (every RPC passes through unchecked) --
// deliberate, not an oversight: this matches every other opt-in security
// knob in this codebase (ORCHESTRATOR_T2_ENABLED, WARM_POOL_TARGETS) and
// keeps `go test ./...`/local `docker-compose up` working without
// requiring every developer to mint and configure a shared secret before
// the orchestrator will even start. Production deployments MUST set
// ORCHESTRATOR_SHARED_SECRET -- see cmd/orchestrator/main.go's own log
// warning when it's unset.
func NewAuthInterceptor(sharedSecret string) *AuthInterceptor {
	return &AuthInterceptor{sharedSecret: sharedSecret}
}

// Enabled reports whether this interceptor will actually check anything.
// Exposed so main.go can log a clear warning when auth is off, rather
// than silently starting an unauthenticated server.
func (a *AuthInterceptor) Enabled() bool {
	return a.sharedSecret != ""
}

// healthCheckMethod is exempted from auth: grpc_health_v1.Health/Check
// is the standard k8s liveness/readiness-probe endpoint, conventionally
// unauthenticated (kubelet itself has no bearer token to present), and
// leaks no sensitive information -- it only reports SERVING/NOT_SERVING.
var healthCheckMethod = "/" + grpc_health_v1.Health_ServiceDesc.ServiceName + "/Check"

// Unary returns a grpc.UnaryServerInterceptor enforcing the shared
// secret. Wired into grpc.NewServer via grpc.UnaryInterceptor in
// cmd/orchestrator/main.go.
func (a *AuthInterceptor) Unary() grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		if !a.Enabled() {
			return handler(ctx, req)
		}
		if info.FullMethod == healthCheckMethod {
			return handler(ctx, req)
		}
		if err := a.checkToken(ctx); err != nil {
			return nil, err
		}
		return handler(ctx, req)
	}
}

func (a *AuthInterceptor) checkToken(ctx context.Context) error {
	md, ok := metadata.FromIncomingContext(ctx)
	if !ok {
		return status.Error(codes.Unauthenticated, "missing metadata")
	}
	values := md.Get("authorization")
	if len(values) == 0 {
		return status.Error(codes.Unauthenticated, "missing authorization header")
	}
	// Only the first value is honored -- a caller sending multiple
	// authorization headers is malformed/suspicious, not something to
	// silently pick-a-winner from.
	const prefix = "Bearer "
	presented := values[0]
	if len(presented) <= len(prefix) || presented[:len(prefix)] != prefix {
		return status.Error(codes.Unauthenticated, "authorization header must be \"Bearer <token>\"")
	}
	token := presented[len(prefix):]
	// crypto/subtle.ConstantTimeCompare: a naive == comparison
	// short-circuits on the first mismatched byte, a real (if narrow)
	// timing side-channel for a secret this token is meant to protect.
	// ConstantTimeCompare itself returns 0 immediately on a length
	// mismatch (documented behavior, not a timing leak in practice since
	// token length isn't the secret -- only its content is), then
	// compares every byte of equal-length inputs in constant time.
	if subtle.ConstantTimeCompare([]byte(token), []byte(a.sharedSecret)) != 1 {
		return status.Error(codes.Unauthenticated, "invalid token")
	}
	return nil
}
