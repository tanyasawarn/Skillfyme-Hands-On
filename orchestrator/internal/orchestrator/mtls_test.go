package orchestrator

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/health"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
)

// This suite proves the mTLS handshake behavior end to end against a
// real net.Listen + grpc.NewServer(grpc.Creds(...)) -- not a mocked
// credentials.TransportCredentials -- using the real dev CA this repo's
// scripts/gen-certs.sh produces under certs/ at the repo root. It needs
// no Postgres/K8s/NATS (unlike ownership_rpc_test.go's pattern): only
// the cert files and a trivial gRPC service (the standard health
// service, already wired in main.go) to prove an RPC actually reaches a
// handler once the handshake succeeds. Skips (not fails) if certs/
// hasn't been generated yet, mirroring this repo's existing
// infra-not-available skip convention.

func certsDir(t *testing.T) string {
	t.Helper()
	// This package's test working directory is orchestrator/internal/orchestrator.
	dir := filepath.Join("..", "..", "..", "certs")
	if _, err := os.Stat(filepath.Join(dir, "ca.crt")); err != nil {
		t.Skipf("skipping: certs/ not generated (run scripts/gen-certs.sh first): %v", err)
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		t.Fatalf("resolving certs dir: %v", err)
	}
	return abs
}

// startTestServer spins up a real TLS-secured gRPC server on a random
// local port with the standard health service registered, and returns
// its address plus a cleanup func. serverCreds comes from
// ServerTLSCredentials against the real dev CA (mirrors main.go's own
// construction exactly).
func startTestServer(t *testing.T, certsDir string) string {
	t.Helper()
	creds, err := ServerTLSCredentials(
		filepath.Join(certsDir, "orchestrator.crt"),
		filepath.Join(certsDir, "orchestrator.key"),
		filepath.Join(certsDir, "ca.crt"),
	)
	if err != nil {
		t.Fatalf("building server TLS credentials: %v", err)
	}

	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}

	srv := grpc.NewServer(grpc.Creds(creds))
	healthSrv := health.NewServer()
	healthpb.RegisterHealthServer(srv, healthSrv)
	healthSrv.SetServingStatus("", healthpb.HealthCheckResponse_SERVING)

	go func() { _ = srv.Serve(lis) }()
	t.Cleanup(srv.Stop)

	return lis.Addr().String()
}

// dialAndCheck dials addr with the given client TLS config and attempts
// one real Health.Check RPC, returning whatever error surfaces (dial-time
// or call-time -- both are valid places for a TLS handshake failure to
// appear depending on grpc-go's lazy-connect behavior, so the test
// asserts on whichever one actually fails).
func dialAndCheck(addr string, tlsConfig *tls.Config) error {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	conn, err := grpc.NewClient(addr, grpc.WithTransportCredentials(credentials.NewTLS(tlsConfig)))
	if err != nil {
		return err
	}
	defer conn.Close()

	client := healthpb.NewHealthClient(conn)
	_, err = client.Check(ctx, &healthpb.HealthCheckRequest{})
	return err
}

func loadClientCert(t *testing.T, certFile, keyFile string) tls.Certificate {
	t.Helper()
	cert, err := tls.LoadX509KeyPair(certFile, keyFile)
	if err != nil {
		t.Fatalf("loading client cert/key %s/%s: %v", certFile, keyFile, err)
	}
	return cert
}

func loadCAPool(t *testing.T, caFile string) *x509.CertPool {
	t.Helper()
	pem, err := os.ReadFile(caFile)
	if err != nil {
		t.Fatalf("reading CA %s: %v", caFile, err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(pem) {
		t.Fatalf("CA file %s contains no valid PEM certs", caFile)
	}
	return pool
}

// mintExpiredClientCert signs a leaf certificate against the real dev CA
// (ca.crt/ca.key) with a validity window entirely in the past -- Go's
// crypto/x509 accepts an arbitrary NotBefore/NotAfter directly, unlike
// the LibreSSL build scripts/gen-certs.sh runs against (see that
// script's own comment on why this case isn't a pre-generated fixture).
func mintExpiredClientCert(t *testing.T, certsDir string) tls.Certificate {
	t.Helper()

	caCertPEM, err := os.ReadFile(filepath.Join(certsDir, "ca.crt"))
	if err != nil {
		t.Fatalf("reading ca.crt: %v", err)
	}
	caKeyPEM, err := os.ReadFile(filepath.Join(certsDir, "ca.key"))
	if err != nil {
		t.Fatalf("reading ca.key: %v", err)
	}
	caCertBlock, _ := pem.Decode(caCertPEM)
	if caCertBlock == nil {
		t.Fatalf("no PEM block found in ca.crt")
	}
	caCert, err := x509.ParseCertificate(caCertBlock.Bytes)
	if err != nil {
		t.Fatalf("parsing ca.crt: %v", err)
	}
	caKeyBlock, _ := pem.Decode(caKeyPEM)
	if caKeyBlock == nil {
		t.Fatalf("no PEM block found in ca.key")
	}
	caKey, err := x509.ParsePKCS1PrivateKey(caKeyBlock.Bytes)
	if err != nil {
		t.Fatalf("parsing ca.key: %v", err)
	}

	leafKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generating leaf key: %v", err)
	}

	template := &x509.Certificate{
		SerialNumber: big.NewInt(time.Now().UnixNano()),
		Subject:      pkix.Name{Organization: []string{"Practice Engine Dev"}, CommonName: "practice-core-expired"},
		DNSNames:     []string{"practice-core"},
		NotBefore:    time.Now().Add(-48 * time.Hour),
		NotAfter:     time.Now().Add(-24 * time.Hour), // expired yesterday
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}

	derBytes, err := x509.CreateCertificate(rand.Reader, template, caCert, &leafKey.PublicKey, caKey)
	if err != nil {
		t.Fatalf("signing expired leaf cert: %v", err)
	}

	return tls.Certificate{
		Certificate: [][]byte{derBytes},
		PrivateKey:  leafKey,
	}
}

func TestMTLS_ValidClientCertificate_Accepted(t *testing.T) {
	dir := certsDir(t)
	addr := startTestServer(t, dir)

	clientCert := loadClientCert(t, filepath.Join(dir, "practice-core-client.crt"), filepath.Join(dir, "practice-core-client.key"))
	caPool := loadCAPool(t, filepath.Join(dir, "ca.crt"))

	err := dialAndCheck(addr, &tls.Config{
		Certificates: []tls.Certificate{clientCert},
		RootCAs:      caPool,
		ServerName:   "localhost",
	})
	if err != nil {
		t.Fatalf("expected valid client cert to be accepted, got: %v", err)
	}
}

func TestMTLS_UntrustedCACertificate_Rejected(t *testing.T) {
	dir := certsDir(t)
	fixturesDir := filepath.Join(dir, "test-fixtures")
	if _, err := os.Stat(filepath.Join(fixturesDir, "untrusted-client.crt")); err != nil {
		t.Skipf("skipping: test fixture not generated: %v", err)
	}
	addr := startTestServer(t, dir)

	untrustedCert := loadClientCert(t, filepath.Join(fixturesDir, "untrusted-client.crt"), filepath.Join(fixturesDir, "untrusted-client.key"))
	caPool := loadCAPool(t, filepath.Join(dir, "ca.crt")) // client trusts the REAL server CA; only the client cert itself is signed by a different, untrusted CA

	err := dialAndCheck(addr, &tls.Config{
		Certificates: []tls.Certificate{untrustedCert},
		RootCAs:      caPool,
		ServerName:   "localhost",
	})
	if err == nil {
		t.Fatal("expected a client cert signed by an untrusted CA to be rejected, got no error")
	}
}

func TestMTLS_ExpiredClientCertificate_Rejected(t *testing.T) {
	dir := certsDir(t)
	addr := startTestServer(t, dir)

	expiredCert := mintExpiredClientCert(t, dir)
	caPool := loadCAPool(t, filepath.Join(dir, "ca.crt"))

	err := dialAndCheck(addr, &tls.Config{
		Certificates: []tls.Certificate{expiredCert},
		RootCAs:      caPool,
		ServerName:   "localhost",
	})
	if err == nil {
		t.Fatal("expected an expired client cert to be rejected, got no error")
	}
}

func TestMTLS_MissingClientCertificate_Rejected(t *testing.T) {
	dir := certsDir(t)
	addr := startTestServer(t, dir)

	caPool := loadCAPool(t, filepath.Join(dir, "ca.crt"))

	// No Certificates set at all -- one-way TLS only. Server's
	// RequireAndVerifyClientCert must reject this during the handshake.
	err := dialAndCheck(addr, &tls.Config{
		RootCAs:    caPool,
		ServerName: "localhost",
	})
	if err == nil {
		t.Fatal("expected a connection with no client certificate to be rejected, got no error")
	}
}

func TestMTLS_HostnameMismatch_Rejected(t *testing.T) {
	dir := certsDir(t)
	addr := startTestServer(t, dir)

	clientCert := loadClientCert(t, filepath.Join(dir, "practice-core-client.crt"), filepath.Join(dir, "practice-core-client.key"))
	caPool := loadCAPool(t, filepath.Join(dir, "ca.crt"))

	// orchestrator.crt's SANs are localhost/orchestrator/127.0.0.1 --
	// asking the client to verify against a hostname the server cert
	// does not cover must fail identity verification.
	err := dialAndCheck(addr, &tls.Config{
		Certificates: []tls.Certificate{clientCert},
		RootCAs:      caPool,
		ServerName:   "not-the-orchestrator.example.com",
	})
	if err == nil {
		t.Fatal("expected a server-identity (SAN) mismatch to be rejected, got no error")
	}
}

func TestMTLS_MalformedCertificateBytes_RejectedCleanly(t *testing.T) {
	dir := certsDir(t)
	fixturesDir := filepath.Join(dir, "test-fixtures")
	malformedPath := filepath.Join(fixturesDir, "malformed.crt")
	if _, err := os.Stat(malformedPath); err != nil {
		t.Skipf("skipping: test fixture not generated: %v", err)
	}

	// The malformed bytes aren't valid PEM/DER at all -- this must be
	// rejected at cert-load time with a clear error, not panic.
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("loading a malformed cert must return an error, not panic: %v", r)
		}
	}()
	_, err := tls.LoadX509KeyPair(malformedPath, filepath.Join(dir, "orchestrator.key"))
	if err == nil {
		t.Fatal("expected loading malformed certificate bytes to fail cleanly, got no error")
	}
}

func TestServerTLSCredentials_FailsCleanlyOnMissingFiles(t *testing.T) {
	_, err := ServerTLSCredentials("/nonexistent/cert.crt", "/nonexistent/key.key", "/nonexistent/ca.crt")
	if err == nil {
		t.Fatal("expected an error for nonexistent cert files, got none")
	}
}
