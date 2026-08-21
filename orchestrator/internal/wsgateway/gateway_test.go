package wsgateway

import (
	"net/http/httptest"
	"testing"
	"time"
)

func TestTokenValidator_ExpiresAfterTTL(t *testing.T) {
	v := NewTokenValidator("test-secret")
	now := time.Now()
	v.now = func() time.Time { return now }

	tok, err := v.Register("attempt-1", "env-1")
	if err != nil {
		t.Fatalf("Register: %v", err)
	}

	if attemptID, envID, ok := v.Validate(tok); !ok || attemptID != "attempt-1" || envID != "env-1" {
		t.Fatalf("expected freshly-registered token to be valid with correct claims, got attemptID=%q envID=%q ok=%v", attemptID, envID, ok)
	}

	now = now.Add(sessionTokenTTL + time.Second)
	if _, _, ok := v.Validate(tok); ok {
		t.Fatal("expected token to be expired after sessionTokenTTL")
	}
}

func TestTokenValidator_RevokeInvalidatesImmediately(t *testing.T) {
	v := NewTokenValidator("test-secret")
	tok, err := v.Register("attempt-1", "env-1")
	if err != nil {
		t.Fatalf("Register: %v", err)
	}
	v.Revoke(tok)

	if _, _, ok := v.Validate(tok); ok {
		t.Fatal("expected revoked token to be invalid")
	}
}

func TestTokenValidator_WrongSecretRejected(t *testing.T) {
	v := NewTokenValidator("test-secret")
	tok, err := v.Register("attempt-1", "env-1")
	if err != nil {
		t.Fatalf("Register: %v", err)
	}

	other := NewTokenValidator("different-secret")
	if _, _, ok := other.Validate(tok); ok {
		t.Fatal("expected a token signed with a different secret to be rejected")
	}
}

func TestTokenValidator_UnknownTokenRejected(t *testing.T) {
	v := NewTokenValidator("test-secret")
	if _, _, ok := v.Validate("never-registered"); ok {
		t.Fatal("expected unknown token to be invalid")
	}
}

func TestGateway_CheckOrigin_NoAllowlistAllowsAny(t *testing.T) {
	g := New(NewTokenValidator("test-secret"), nil, nil)
	req := httptest.NewRequest("GET", "/v1/env/e1/terminal", nil)
	req.Header.Set("Origin", "https://evil.example.com")
	if !g.upgrader.CheckOrigin(req) {
		t.Fatal("expected no-allowlist fallback to accept any origin (dev mode)")
	}
}

func TestGateway_CheckOrigin_AllowlistRejectsUnknownOrigin(t *testing.T) {
	g := New(NewTokenValidator("test-secret"), nil, []string{"https://app.example.com"})
	req := httptest.NewRequest("GET", "/v1/env/e1/terminal", nil)
	req.Header.Set("Origin", "https://evil.example.com")
	if g.upgrader.CheckOrigin(req) {
		t.Fatal("expected allowlisted gateway to reject an unlisted origin")
	}
}

func TestGateway_CheckOrigin_AllowlistAcceptsKnownOrigin(t *testing.T) {
	g := New(NewTokenValidator("test-secret"), nil, []string{"https://app.example.com"})
	req := httptest.NewRequest("GET", "/v1/env/e1/terminal", nil)
	req.Header.Set("Origin", "https://app.example.com")
	if !g.upgrader.CheckOrigin(req) {
		t.Fatal("expected allowlisted gateway to accept a listed origin")
	}
}
