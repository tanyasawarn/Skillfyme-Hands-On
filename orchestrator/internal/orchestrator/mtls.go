package orchestrator

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"os"

	"google.golang.org/grpc/credentials"
)

// ServerTLSCredentials builds the gRPC transport credentials for the
// orchestrator's server side: it presents certFile/keyFile as its own
// identity and requires every connecting client to present a certificate
// signed by caFile, verified during the TLS handshake itself
// (tls.RequireAndVerifyClientCert) -- a connection with no client
// certificate, an untrusted-CA certificate, or an expired certificate
// never reaches any RPC handler; it's rejected at the transport layer,
// before the shared-secret interceptor (auth.go) or any application code
// runs. That interceptor is layered on top of this, not replaced by it --
// mTLS proves transport identity, the interceptor stays as
// defense-in-depth in case a certificate is ever mis-issued to the wrong
// party.
func ServerTLSCredentials(certFile, keyFile, caFile string) (credentials.TransportCredentials, error) {
	cert, err := tls.LoadX509KeyPair(certFile, keyFile)
	if err != nil {
		return nil, fmt.Errorf("loading server cert/key: %w", err)
	}

	caCert, err := os.ReadFile(caFile)
	if err != nil {
		return nil, fmt.Errorf("reading CA cert: %w", err)
	}
	caPool := x509.NewCertPool()
	if !caPool.AppendCertsFromPEM(caCert) {
		return nil, fmt.Errorf("CA cert file %s contains no valid PEM certificates", caFile)
	}

	tlsConfig := &tls.Config{
		Certificates: []tls.Certificate{cert},
		ClientCAs:    caPool,
		ClientAuth:   tls.RequireAndVerifyClientCert,
		MinVersion:   tls.VersionTLS12,
	}
	return credentials.NewTLS(tlsConfig), nil
}
