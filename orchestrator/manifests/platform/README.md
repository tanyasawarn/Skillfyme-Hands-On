# orchestrator/manifests/platform/ — Phase 2 D

Deploys the platform (orchestrator + practice-core) as **in-cluster
Pods** with **mTLS** and **NetworkPolicy** — the topology change
`PHASE2_CLOSEOUT.md` named as the blocker for live security verification.

| File | What |
|---|---|
| `00-namespace.yaml` | `practiceengine-platform` ns, PSS `restricted` |
| `10-orchestrator-rbac.yaml` | `orchestrator` ServiceAccount + a **least-privilege ClusterRole** (every verb derived from an actual `clientset.*` call in `internal/`, not `cluster-admin`) + binding. Replaces the external kubeconfig. |
| `20-mtls-certs.yaml` | placeholder `orchestrator-mtls` / `practice-core-mtls` Secrets; cert-manager path commented for production |
| `30-orchestrator-deployment.yaml` | orchestrator Deployment (`ORCHESTRATOR_TLS_ENABLED=true`, in-cluster SA, no `KUBECONFIG`) + ClusterIP Service |
| `40-practice-core-deployment.yaml` | practice-core Deployment (mTLS client cert, `USE_FAKE_ORCHESTRATOR=false`, gRPC target = orchestrator Service DNS) + Service |
| `60-networkpolicies.yaml` | `default-deny-ingress` + `orchestrator-grpc-ingress` (only `app: practice-core` → :50051/:8081) + `practice-core-http-ingress` + `orchestrator-egress` |
| `kustomization.yaml` | `kubectl apply -k .` |
| `secrets.*.env.example` | templates for the two config Secrets (gitignored real files) |

## Apply

```bash
# 1. mTLS material  (dev: gen-certs.sh; prod: cert-manager — see 20-mtls-certs.yaml)
scripts/gen-certs.sh
kubectl create ns practiceengine-platform --dry-run=client -o yaml | kubectl apply -f -
kubectl -n practiceengine-platform create secret generic orchestrator-mtls \
  --from-file=tls.crt=certs/orchestrator.crt --from-file=tls.key=certs/orchestrator.key \
  --from-file=ca.crt=certs/ca.crt --dry-run=client -o yaml | kubectl apply -f -
kubectl -n practiceengine-platform create secret generic practice-core-mtls \
  --from-file=tls.crt=certs/practice-core-client.crt --from-file=tls.key=certs/practice-core-client.key \
  --from-file=ca.crt=certs/ca.crt --dry-run=client -o yaml | kubectl apply -f -

# 2. app config
cp orchestrator/manifests/platform/secrets.orchestrator.env{.example,}   # then edit
cp orchestrator/manifests/platform/secrets.practice-core.env{.example,}  # then edit
kubectl -n practiceengine-platform create secret generic orchestrator-config \
  --from-env-file=orchestrator/manifests/platform/secrets.orchestrator.env --dry-run=client -o yaml | kubectl apply -f -
kubectl -n practiceengine-platform create secret generic practice-core-config \
  --from-env-file=orchestrator/manifests/platform/secrets.practice-core.env --dry-run=client -o yaml | kubectl apply -f -

# 3. datastores — Postgres/Redis/NATS, however you run them, labelled
#    app=postgres / app=redis / app=nats, reachable at the URLs above.

# 4. build + push images, set the tags in kustomization.yaml, then:
kubectl apply -k orchestrator/manifests/platform/
kubectl -n practiceengine-platform rollout status deploy/orchestrator deploy/practice-core

# 5. VERIFY — the requirement-D gate:
scripts/d-security-check.sh
```

## What `d-security-check.sh` proves

- **mTLS**: valid client cert → gRPC call succeeds; **no cert / untrusted-CA
  cert / expired cert → all rejected at the TLS handshake**, before any RPC
  handler. (The Go unit suite `internal/orchestrator/mtls_test.go` — 7
  scenarios — is the CI gate for the same properties.)
- **NetworkPolicy**: `practice-core → orchestrator:50051` allowed; **any
  other pod → orchestrator:50051 blocked**; metrics :9090 blocked from
  non-monitoring pods; and a **positive enforcement self-test** so a
  non-enforcing CNI produces a hard FAIL, not a false pass.

## CNI requirement

NetworkPolicy is only enforced by a supporting CNI. k3s bundles
**kube-router**'s netpol controller (on by default in current k3s) — the
`k3s-sysbox-node.sh` bootstrap keeps it. `d-security-check.sh` step 2
fails loudly if no enforcing CNI is found.
