package orchestrator

import (
	"context"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

func passThroughHandler(ctx context.Context, req any) (any, error) {
	return "ok", nil
}

func infoFor(method string) *grpc.UnaryServerInfo {
	return &grpc.UnaryServerInfo{FullMethod: method}
}

func TestAuthInterceptor_DisabledWhenSecretEmpty(t *testing.T) {
	interceptor := NewAuthInterceptor("")
	if interceptor.Enabled() {
		t.Fatal("expected Enabled()=false for an empty secret")
	}

	// No metadata at all -- must still pass through when disabled.
	resp, err := interceptor.Unary()(context.Background(), nil, infoFor("/x/Y"), passThroughHandler)
	if err != nil {
		t.Fatalf("expected no error when auth is disabled, got: %v", err)
	}
	if resp != "ok" {
		t.Fatalf("expected handler's response to pass through, got: %v", resp)
	}
}

func TestAuthInterceptor_RejectsMissingMetadata(t *testing.T) {
	interceptor := NewAuthInterceptor("real-secret")
	_, err := interceptor.Unary()(context.Background(), nil, infoFor("/x/Provision"), passThroughHandler)
	if err == nil {
		t.Fatal("expected error for a request with no metadata at all")
	}
	if status.Code(err) != codes.Unauthenticated {
		t.Errorf("expected codes.Unauthenticated, got %v", status.Code(err))
	}
}

func TestAuthInterceptor_RejectsMissingAuthorizationHeader(t *testing.T) {
	interceptor := NewAuthInterceptor("real-secret")
	md := metadata.New(map[string]string{"some-other-header": "x"})
	ctx := metadata.NewIncomingContext(context.Background(), md)

	_, err := interceptor.Unary()(ctx, nil, infoFor("/x/Provision"), passThroughHandler)
	if err == nil {
		t.Fatal("expected error for metadata with no authorization header")
	}
	if status.Code(err) != codes.Unauthenticated {
		t.Errorf("expected codes.Unauthenticated, got %v", status.Code(err))
	}
}

func TestAuthInterceptor_RejectsMalformedAuthorizationHeader(t *testing.T) {
	interceptor := NewAuthInterceptor("real-secret")
	cases := []string{
		"real-secret",        // missing "Bearer " prefix
		"Basic real-secret",  // wrong scheme
		"Bearer",             // no token at all
		"bearer real-secret", // wrong case (RFC 7235 scheme names are case-insensitive in spec, but this impl is deliberately strict since gRPC clients are code we control, not arbitrary browsers)
	}
	for _, header := range cases {
		md := metadata.New(map[string]string{"authorization": header})
		ctx := metadata.NewIncomingContext(context.Background(), md)
		_, err := interceptor.Unary()(ctx, nil, infoFor("/x/Provision"), passThroughHandler)
		if err == nil {
			t.Errorf("expected error for malformed header %q", header)
			continue
		}
		if status.Code(err) != codes.Unauthenticated {
			t.Errorf("header %q: expected codes.Unauthenticated, got %v", header, status.Code(err))
		}
	}
}

func TestAuthInterceptor_RejectsWrongToken(t *testing.T) {
	interceptor := NewAuthInterceptor("real-secret")
	md := metadata.New(map[string]string{"authorization": "Bearer wrong-token"})
	ctx := metadata.NewIncomingContext(context.Background(), md)

	_, err := interceptor.Unary()(ctx, nil, infoFor("/x/Provision"), passThroughHandler)
	if err == nil {
		t.Fatal("expected error for an incorrect token")
	}
	if status.Code(err) != codes.Unauthenticated {
		t.Errorf("expected codes.Unauthenticated, got %v", status.Code(err))
	}
}

// TestAuthInterceptor_RejectsTokenThatIsAPrefixOrSuffixOfTheRealOne guards
// against a naive substring-match bug (e.g. strings.HasPrefix instead of
// exact comparison) that would accept "real-secret-extra" or "real-secre"
// as valid.
func TestAuthInterceptor_RejectsTokenThatIsAPrefixOrSuffixOfTheRealOne(t *testing.T) {
	interceptor := NewAuthInterceptor("real-secret")
	cases := []string{"real-secret-extra", "real-secre", "xreal-secret", ""}
	for _, token := range cases {
		md := metadata.New(map[string]string{"authorization": "Bearer " + token})
		ctx := metadata.NewIncomingContext(context.Background(), md)
		_, err := interceptor.Unary()(ctx, nil, infoFor("/x/Provision"), passThroughHandler)
		if err == nil {
			t.Errorf("expected rejection for near-miss token %q", token)
		}
	}
}

func TestAuthInterceptor_AcceptsCorrectToken(t *testing.T) {
	interceptor := NewAuthInterceptor("real-secret")
	md := metadata.New(map[string]string{"authorization": "Bearer real-secret"})
	ctx := metadata.NewIncomingContext(context.Background(), md)

	resp, err := interceptor.Unary()(ctx, nil, infoFor("/x/Provision"), passThroughHandler)
	if err != nil {
		t.Fatalf("unexpected error for the correct token: %v", err)
	}
	if resp != "ok" {
		t.Fatalf("expected handler's response to pass through, got: %v", resp)
	}
}

// TestAuthInterceptor_HealthCheckExemptEvenWhenEnabled confirms the one
// deliberate exception: the standard gRPC health-check RPC must remain
// reachable without a token even when auth is enabled, since kubelet's
// liveness/readiness probes have no bearer token to present and the
// health check leaks nothing sensitive (only SERVING/NOT_SERVING).
func TestAuthInterceptor_HealthCheckExemptEvenWhenEnabled(t *testing.T) {
	interceptor := NewAuthInterceptor("real-secret")
	// No metadata at all -- would be rejected for any other method.
	resp, err := interceptor.Unary()(context.Background(), nil, infoFor(healthCheckMethod), passThroughHandler)
	if err != nil {
		t.Fatalf("expected health check to bypass auth, got error: %v", err)
	}
	if resp != "ok" {
		t.Fatalf("expected handler's response to pass through, got: %v", resp)
	}
}

// TestAuthInterceptor_NonHealthMethodsStillRequireAuthWhenEnabled is the
// converse regression guard: confirms the health-check exemption is
// scoped to exactly that one method, not accidentally broadened (e.g. a
// substring match on "Health" that would also exempt something else).
func TestAuthInterceptor_NonHealthMethodsStillRequireAuthWhenEnabled(t *testing.T) {
	interceptor := NewAuthInterceptor("real-secret")
	for _, method := range []string{
		"/practiceengine.orchestrator.v1.EnvironmentOrchestrator/Provision",
		"/practiceengine.orchestrator.v1.EnvironmentOrchestrator/Destroy",
		"/practiceengine.orchestrator.v1.EnvironmentOrchestrator/InjectFault",
	} {
		_, err := interceptor.Unary()(context.Background(), nil, infoFor(method), passThroughHandler)
		if err == nil {
			t.Errorf("method %s: expected auth to be required, but request with no metadata succeeded", method)
		}
	}
}
