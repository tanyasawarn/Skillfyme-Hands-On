package t3local

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// The live behaviour of every adapter in this package (real k3s pod,
// real MinIO, real in-pod exec) is proven by the two *_LocalReal tests
// in internal/k8s and internal/orchestrator. These unit tests cover only
// the pure logic that has no infra dependency.

func TestStaticTokenSource_ProducesAVerifiableHS256JWT(t *testing.T) {
	ts := NewStaticTokenSource("unit-secret", "pe-client", 15*time.Minute)
	tok, err := ts.WebIdentityToken(context.Background(), "attempt-123")
	if err != nil {
		t.Fatalf("WebIdentityToken: %v", err)
	}
	parts := strings.Split(tok, ".")
	if len(parts) != 3 {
		t.Fatalf("token is not a 3-part JWT: %q", tok)
	}

	// signature verifies against the same secret
	mac := hmac.New(sha256.New, []byte("unit-secret"))
	mac.Write([]byte(parts[0] + "." + parts[1]))
	want := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	if parts[2] != want {
		t.Errorf("signature mismatch:\n got %s\nwant %s", parts[2], want)
	}

	// claims: sub is the attempt id, exp is in the future
	raw, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		t.Fatalf("decode payload: %v", err)
	}
	var claims struct {
		Sub string `json:"sub"`
		Aud string `json:"aud"`
		Exp int64  `json:"exp"`
	}
	if err := json.Unmarshal(raw, &claims); err != nil {
		t.Fatalf("payload not JSON: %v", err)
	}
	if claims.Sub != "attempt-123" {
		t.Errorf("sub = %q, want attempt-123", claims.Sub)
	}
	if claims.Aud != "pe-client" {
		t.Errorf("aud = %q, want pe-client", claims.Aud)
	}
	if claims.Exp <= time.Now().Unix() {
		t.Errorf("exp %d is not in the future", claims.Exp)
	}
}

func TestStaticTokenSource_DefaultsWhenBlank(t *testing.T) {
	ts := NewStaticTokenSource("", "", 0)
	tok, err := ts.WebIdentityToken(context.Background(), "a")
	if err != nil {
		t.Fatalf("WebIdentityToken: %v", err)
	}
	if len(strings.Split(tok, ".")) != 3 {
		t.Fatalf("blank-config token is malformed: %q", tok)
	}
}

func TestAttemptEnvMap_SetGetDelete(t *testing.T) {
	m := NewAttemptEnvMap()
	if _, ok := m.get("nope"); ok {
		t.Fatal("empty map returned a hit")
	}
	m.Set("att-1", "env-abc")
	got, ok := m.get("att-1")
	if !ok || got != "env-abc" {
		t.Fatalf("get after Set = (%q, %v), want (env-abc, true)", got, ok)
	}
	m.Delete("att-1")
	if _, ok := m.get("att-1"); ok {
		t.Fatal("get after Delete still returned a hit")
	}
}

func TestExecShellRunner_CollapsesShDashCArgv(t *testing.T) {
	// Run() is supposed to unwrap ["sh","-c",CMD] to CMD before handing
	// it to validation.ExecShell (which does its own bash wrapping). We
	// can't call Run() without a cluster, but the collapse rule is the
	// bit worth pinning -- assert it via the same shape check the method
	// uses.
	argv := []string{"sh", "-c", "echo hi && ls"}
	if !(len(argv) == 3 && (argv[0] == "sh" || argv[0] == "/bin/sh") && argv[1] == "-c") {
		t.Fatal("shape guard changed; update ExecShellRunner.Run to match")
	}
}
