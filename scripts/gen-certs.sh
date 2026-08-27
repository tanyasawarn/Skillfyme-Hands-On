#!/usr/bin/env bash
# Generates a self-signed dev CA and the mTLS cert/key pairs the
# orchestrator's gRPC server and practice-core's gRPC clients need.
#
# This is a dev-appropriate setup (openssl, no cert-manager/Vault) --
# PLAN.md Phase 2's mTLS requirement doesn't call for enterprise PKI, and
# cert-manager pulls in a k8s controller dependency for something two
# local processes need. Rotation in this setup means re-running this
# script and restarting both processes; a real deployment would swap
# this for a managed CA (cert-manager, Vault PKI, ACM Private CA) behind
# the same file paths orchestrator/config.go and base-grpc-client.ts
# already read, so nothing above this script needs to change to adopt one.
#
# Output goes to certs/ (gitignored) at the repo root:
#   certs/ca.crt                    -- CA cert both sides trust
#   certs/ca.key                    -- CA private key (dev only, never ship this)
#   certs/orchestrator.crt/.key     -- server cert (SANs: localhost, orchestrator, 127.0.0.1)
#   certs/practice-core-client.crt/.key -- client cert practice-core presents
#   certs/test-fixtures/untrusted-ca.crt/.key -- leaf cert signed by a DIFFERENT CA (test-only, proves untrusted-CA rejection)
#   certs/test-fixtures/malformed.crt -- garbage bytes, not valid PEM/DER (test-only)
#
# The expired-cert test case is minted at test time by mtls_test.go
# directly against certs/ca.crt+ca.key (Go's crypto/x509 supports
# arbitrary NotBefore/NotAfter; this script's openssl build does not).
#
# Safe to re-run: regenerates everything from scratch each time.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"
CERTS_DIR="$REPO_ROOT/certs"
FIXTURES_DIR="$CERTS_DIR/test-fixtures"

rm -rf "$CERTS_DIR"
mkdir -p "$CERTS_DIR" "$FIXTURES_DIR"
cd "$CERTS_DIR"

DAYS_CA=3650
DAYS_LEAF=825

echo "==> Generating dev CA"
openssl genrsa -out ca.key 4096 2>/dev/null
openssl req -x509 -new -nodes -key ca.key -sha256 -days "$DAYS_CA" \
  -subj "/O=Practice Engine Dev/CN=Practice Engine Dev CA" \
  -out ca.crt 2>/dev/null

gen_leaf() {
  local name="$1" cn="$2" san="$3" signing_ca_crt="$4" signing_ca_key="$5" days="$6"
  openssl genrsa -out "${name}.key" 2048 2>/dev/null
  openssl req -new -key "${name}.key" -subj "/O=Practice Engine Dev/CN=${cn}" \
    -out "${name}.csr" 2>/dev/null
  openssl x509 -req -in "${name}.csr" \
    -CA "$signing_ca_crt" -CAkey "$signing_ca_key" -CAcreateserial \
    -days "$days" -sha256 \
    -extfile <(printf "subjectAltName=%s\nextendedKeyUsage=%s" "$san" "$7") \
    -out "${name}.crt" 2>/dev/null
  rm -f "${name}.csr"
}

echo "==> Generating orchestrator server cert (SANs: localhost, orchestrator, 127.0.0.1)"
gen_leaf orchestrator "orchestrator" \
  "DNS:localhost,DNS:orchestrator,IP:127.0.0.1" \
  ca.crt ca.key "$DAYS_LEAF" "serverAuth"

echo "==> Generating practice-core client cert"
gen_leaf practice-core-client "practice-core" \
  "DNS:practice-core" \
  ca.crt ca.key "$DAYS_LEAF" "clientAuth"

echo "==> Generating test fixture: expired client cert (backdated, already expired)"
# LibreSSL's `openssl x509 -req` supports neither negative -days nor
# -not_before/-not_after, so an already-expired cert can't be minted
# with this openssl build via the CLI. The Go mTLS test suite
# (orchestrator/internal/orchestrator/mtls_test.go) mints its own
# expired leaf certificate at test time instead, using Go's crypto/x509
# directly against this same CA (certs/ca.crt + certs/ca.key) -- more
# portable across openssl/LibreSSL versions than shelling out here, and
# keeps the "expired" case exercised by the same Go test suite as every
# other mTLS scenario rather than a separate pre-built fixture file.
echo "    (skipped -- minted at test time by mtls_test.go using this CA)"

echo "==> Generating test fixture: untrusted-CA client cert (different, throwaway CA)"
openssl genrsa -out "$FIXTURES_DIR/untrusted-ca.key" 4096 2>/dev/null
openssl req -x509 -new -nodes -key "$FIXTURES_DIR/untrusted-ca.key" -sha256 -days "$DAYS_CA" \
  -subj "/O=Not Practice Engine/CN=Untrusted Test CA" \
  -out "$FIXTURES_DIR/untrusted-ca.crt" 2>/dev/null
gen_leaf "$FIXTURES_DIR/untrusted-client" "practice-core" \
  "DNS:practice-core" \
  "$FIXTURES_DIR/untrusted-ca.crt" "$FIXTURES_DIR/untrusted-ca.key" "$DAYS_LEAF" "clientAuth"

echo "==> Generating test fixture: malformed cert (garbage bytes, not valid PEM/DER)"
head -c 256 /dev/urandom > "$FIXTURES_DIR/malformed.crt"

rm -f "$CERTS_DIR"/*.srl "$FIXTURES_DIR"/*.srl

chmod 600 ca.key orchestrator.key practice-core-client.key "$FIXTURES_DIR"/*.key 2>/dev/null || true

echo ""
echo "Done. Certs written to $CERTS_DIR (gitignored)."
echo "Set these in orchestrator/.env and practice-core/.env to enable mTLS:"
echo "  ORCHESTRATOR_TLS_ENABLED=true"
echo "  ORCHESTRATOR_TLS_CERT=$CERTS_DIR/orchestrator.crt"
echo "  ORCHESTRATOR_TLS_KEY=$CERTS_DIR/orchestrator.key"
echo "  ORCHESTRATOR_TLS_CA=$CERTS_DIR/ca.crt"
echo "  (practice-core) ORCHESTRATOR_TLS_CA=$CERTS_DIR/ca.crt"
echo "  (practice-core) ORCHESTRATOR_CLIENT_TLS_CERT=$CERTS_DIR/practice-core-client.crt"
echo "  (practice-core) ORCHESTRATOR_CLIENT_TLS_KEY=$CERTS_DIR/practice-core-client.key"
