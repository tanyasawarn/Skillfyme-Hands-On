# Provision runbook — Sysbox-on-T1 practice cluster (single ARM VM)

The step-by-step to stand up the real practice cluster and close Phase 2
requirement **A** end-to-end. This is the ₹100/user host path
(`orchestrator/docs/t2-cost-optimization-100.md`).

Everything referenced here is authored and tested to the limit possible
without a server. Each `[VERIFY]` line is a hard gate — do not proceed
past a failing one.

---

## 0. Provision the server

**Primary: Oracle Cloud Always Free (Ampere ARM, Mumbai) — ₹0 forever.**

1. Oracle Cloud account with **home region `ap-mumbai-1`** (or
   `ap-hyderabad-1` if your account is already elsewhere — home region
   can't be changed later, and Always-Free ARM is locked to it).
2. Compute → Create Instance:
   - Image: **Canonical Ubuntu 24.04** (aarch64)
   - Shape: **VM.Standard.A1.Flex**, **4 OCPU / 24 GB** (the full free
     allotment; 2 OCPU / 12 GB also works if capacity is tight)
   - Boot volume: **100 GB**
   - New VCN + public subnet, **assign a public IPv4**
   - Paste your SSH **public** key
3. If **"Out of host capacity"**: retry over a few hours, try another
   Availability Domain, or drop to 2 OCPU first.
4. Firewall — **two places**:
   - **VCN Security List** (Networking → VCN → subnet → Security List):
     ingress `<your-ip>/32` → 22, 6443, 50051; `0.0.0.0/0` → 80, 443.
   - **Ubuntu iptables**: Oracle's image drops everything but SSH. The
     bootstrap script (Step 1) detects this and opens the k3s + platform
     ports automatically — no manual step needed.

**Fallback (paid, no capacity retries):** Hetzner **CAX41** (16 vCPU
Ampere ARM / 32 GB, €30/mo) or an Oracle paid Ampere shape. Same OS, same
bootstrap script.

You provide: `SERVER_IP` (public IPv4), SSH user (`ubuntu` on Oracle
Ubuntu; `root` on Hetzner), key path.

**Capacity note:** 4 OCPU / 24 GB comfortably hosts the platform +
~3–5 concurrent learner environments — enough to build, verify
requirement A, and run a ~20–30 learner pilot. Move to a bigger paid ARM
box when concurrency grows; the bootstrap and everything downstream is
identical.

---

## 1. Bootstrap k3s + Sysbox

```bash
# Oracle Ubuntu: user is 'ubuntu', needs sudo. Hetzner: user is 'root'.
SSH_USER=ubuntu   # or root
scp infra/practice-cluster/bootstrap/k3s-sysbox-node.sh $SSH_USER@$SERVER_IP:/tmp/
ssh $SSH_USER@$SERVER_IP 'sudo bash /tmp/k3s-sysbox-node.sh'
```

The script runs its own smoke test at the end.

**[VERIFY]** last lines of the script output show:
- `OK Docker-in-Docker works inside a Sysbox pod`
- `OK systemd runs as PID 1`
- `DONE` block with the "Next:" instructions

If it dies on the Sysbox `.deb` download, check `SYSBOX_VERSION` against
<https://github.com/nestybox/sysbox/releases> and re-run with
`SYSBOX_VERSION=<current> bash /root/k3s-sysbox-node.sh`.

---

## 2. Get a kubeconfig on your workstation

```bash
ssh root@$SERVER_IP 'cat /etc/rancher/k3s/k3s.yaml' \
  | sed "s/127.0.0.1/$SERVER_IP/" > ~/.kube/practice-cluster.yaml
export KUBECONFIG=~/.kube/practice-cluster.yaml
kubectl get nodes
kubectl get runtimeclass sysbox-runc
```

**[VERIFY]** one `Ready` node labelled `practiceengine.dev/tier=t1`;
`sysbox-runc` RuntimeClass exists.

---

## 3. Deploy the platform — in-cluster, with mTLS + NetworkPolicy (Steps A + D)

The full manifest set is `orchestrator/manifests/platform/` (kustomize
base). This deploys the orchestrator and practice-core as **in-cluster
Pods** — the topology requirement D needs — with mTLS on and the
NetworkPolicies applied. See that directory's `README.md` for the detail;
the short version:

```bash
export KUBECONFIG=~/.kube/practice-cluster.yaml

# a) datastores — Postgres, Redis, NATS as Deployments in the platform ns,
#    labelled app=postgres / app=redis / app=nats. (Helm, or simple
#    manifests — your choice; they're not in the kustomize base.)
kubectl create ns practiceengine-platform --dry-run=client -o yaml | kubectl apply -f -
#    ...deploy postgres/redis/nats here...

# b) mTLS certs (dev CA; prod = cert-manager, see 20-mtls-certs.yaml)
scripts/gen-certs.sh
kubectl -n practiceengine-platform create secret generic orchestrator-mtls \
  --from-file=tls.crt=certs/orchestrator.crt --from-file=tls.key=certs/orchestrator.key \
  --from-file=ca.crt=certs/ca.crt --dry-run=client -o yaml | kubectl apply -f -
kubectl -n practiceengine-platform create secret generic practice-core-mtls \
  --from-file=tls.crt=certs/practice-core-client.crt --from-file=tls.key=certs/practice-core-client.key \
  --from-file=ca.crt=certs/ca.crt --dry-run=client -o yaml | kubectl apply -f -

# c) app config secrets
cp orchestrator/manifests/platform/secrets.orchestrator.env{.example,}   && $EDITOR ...
cp orchestrator/manifests/platform/secrets.practice-core.env{.example,}  && $EDITOR ...
kubectl -n practiceengine-platform create secret generic orchestrator-config \
  --from-env-file=orchestrator/manifests/platform/secrets.orchestrator.env --dry-run=client -o yaml | kubectl apply -f -
kubectl -n practiceengine-platform create secret generic practice-core-config \
  --from-env-file=orchestrator/manifests/platform/secrets.practice-core.env --dry-run=client -o yaml | kubectl apply -f -

# d) build + push images, set tags in kustomization.yaml, then:
kubectl apply -k orchestrator/manifests/platform/
kubectl -n practiceengine-platform rollout status deploy/orchestrator deploy/practice-core
```

Run DB migrations: `orchestrator/db/migrations/` (env/billing) and
`practice-core/db/migrations/`.

**[VERIFY]**
- `kubectl -n practiceengine-platform get pods` — all Running
- orchestrator logs show `mTLS enabled: server requires and verifies client certificates`
- orchestrator logs show it used **in-cluster config** (no "kubeconfig not found")
- `kubectl -n practiceengine-platform get netpol` — 4 policies present

---

## 4. Run the end-to-end T2 lifecycle harness  ← the requirement-A gate

```bash
export KUBECONFIG=~/.kube/practice-cluster.yaml
export ORCH_GRPC=$SERVER_IP:50051
export ORCH_METRICS=http://$SERVER_IP:9090
export ORCHESTRATOR_SHARED_SECRET=<the secret from Step 3>
export DATABASE_URL=postgres://practice:practice@$SERVER_IP:5432/practice_engine   # or a port-forward
export T2_BLUEPRINT_ID=<a real T2-tier blueprint id from content/>
export SEED_ATTEMPT=1     # inserts + cleans a throwaway attempt row

scripts/t2-lifecycle-check.sh
```

**[VERIFY]** exit 0 and the final line:
```
PASS T2 full lifecycle + DinD + multi-node k3s + systemd + eBPF + zero leftover — ALL VERIFIED
```

The harness fails loudly if the pod isn't really `sysbox-runc`, if any of
DinD / nested k3s / systemd / eBPF don't work, or if the namespace / PVs /
orphan counter show any leftover after Destroy.

---

## 5. Run the Go live test against the real cluster

```bash
# point the test's kubeconfig path at the real cluster
cp ~/.kube/practice-cluster.yaml .local/k3s-output/kubeconfig.yaml
cd orchestrator
go test ./internal/k8s/ -run TestProvisionT2_Lifecycle -v -count=1
```

**[VERIFY]** the test takes the **positive** branch and logs:
```
T2 lifecycle verified: Provision -> Sysbox pod (root-in-userns, unprivileged)
  -> PSS privileged ns -> quota matches DefaultT2Resources -> Destroy
  -> namespace gone, no PV leaked
```
(On any cluster without `sysbox-runc` it takes the negative branch and
asserts the honest `RuntimeClass "sysbox-runc" not found` failure — that
is a pass too, but NOT the requirement-A gate.)

---

## 6. Run the D security harness  ← the requirement-D gate

The orchestrator is already deployed as an in-cluster pod with mTLS on
and the NetworkPolicies applied (Step 3). Now verify enforcement:

```bash
export KUBECONFIG=~/.kube/practice-cluster.yaml
scripts/d-security-check.sh
```

**[VERIFY]** exit 0 and the final line:
```
PASS Phase 2 D — mTLS (valid accepted; no-cert / untrusted-CA / expired all rejected)
  + NetworkPolicy (practice-core allowed; all other traffic blocked; enforcement
  confirmed active) — ALL VERIFIED
```

The harness fails loudly if a no-cert / untrusted-CA / expired cert is
accepted, if a non-practice-core pod can reach `orchestrator:50051`, or
if the CNI isn't actually enforcing NetworkPolicy (positive self-test).

Also run the mTLS unit suite (the CI gate for the same properties):
```bash
cd orchestrator && go test ./internal/orchestrator/ -run MTLS -v -count=1
```

## 6b. Run the B fault-injection harness  ← the requirement-B gate

```bash
export KUBECONFIG=~/.kube/practice-cluster.yaml
export ORCH_GRPC=localhost:50051   # via: kubectl -n practiceengine-platform port-forward svc/orchestrator 50051:50051
export ORCHESTRATOR_SHARED_SECRET=<the secret from Step 3>
export DATABASE_URL=postgres://practice:...@localhost:5432/practice_engine   # or a port-forward
scripts/b-fault-injection-check.sh
```

**[VERIFY]** exit 0 and:
```
PASS Phase 2 B — health-gate-before-inject + real InjectFault (Dev B triggers, Dev A
  executes) + symptom visible + ownership enforced + T2 precondition + blast_radius
  (forbidden cmd -> event bus -> scoring) + zero leftover — ALL VERIFIED
```

Notes:
- Exit 3 means steps 1–6 + 8 passed but the `blast_radius` step needs the
  real Session Broker WS attach for a full live pass (the harness
  approximates the tap outside the WS client). The pipeline itself is
  covered by `command-executed-consumer.integration.spec.ts` +
  `telemetry_tap_test.go`; for the live pass, attach to the `Connect`
  RPC's `terminalWsUrl` and type a `blast_radius.forbidden` command.
- Run once with `T2_RUNTIME=none` too — that exercises the T1-rejection
  precondition for a T2-only fault live (skipped when the env is T2).

## 6c. Run the E scoring harness  ← the requirement-E gate

```bash
export PC_URL=http://<practice-core svc / ingress>
export PC_JWT=<a learner JWT>            # or PC_AUTH_DISABLED=1
export DATABASE_URL=postgres://practice:...@localhost:5432/practice_engine
export E_ACTIVITY_VERSION_ID=<published PRODUCTION_SIM activity_version_id>
export E_TENANT_ID=<real tenant>  E_USER_ID=<real user>
export E_PRIMARY_SKILL_SLUG=k8s.services
scripts/e-scoring-check.sh
```

**[VERIFY]** exit 0:
```
PASS Phase 2 E — sp.production-sim.default active + diagnostic_efficiency/hypothesis_ordering
  computed + NO_REGRESSION/HTTP_SLO run + full sim scoring end-to-end + Elo updated
  + retry/cooldown applied — VERIFIED
```

## 6d. Run the C AI-grader calibration  ← the requirement-C gate

```bash
cd practice-core
export ANTHROPIC_API_KEY=sk-ant-...
export ANTHROPIC_GRADER_MODEL=claude-sonnet-4-5
npx ts-node -r tsconfig-paths/register scripts/rubric-calibrate.ts rub.incident-note.v2
```

**[VERIFY]** exit 0 with:
```
PASS — record this run in rub-calibration.md and sign it
  overall weighted kappa >= 0.60 : PASS
  injection-defence : PASS
```

Then, per `evaluation/phase1/rubric-calibration/rub.incident-note.v2/rub-calibration.md`:
- record the run + reviewer sign-off in that file's Runs table
- flip `content/rubrics/rub.incident-note.v2.yaml` `human_review.policy`
  to `RANDOM_AUDIT_10_PCT`
- give the artifact criterion a real weight in `sp.production-sim.default`

**Note:** the 6 seed cases are the minimum. Full C completion needs the
SME team to expand to ~20 cases (~3–4 wk) before this run is
authoritative — until then the AI grade stays weight-zero/advisory
(which is safe: it never affects `finalScore`).

Cost after calibration: set `ANTHROPIC_GRADER_SAMPLE_COUNT=1` →
~₹1.7/user/mo (see `practice-core/docs/ai-grader-cost.md`).

## 7. Record and close A + B + C + D + E

Paste the Step 4, 5, 6, 6b, 6c, and 6d outputs into `PHASE2_CLOSEOUT.md`
under requirements A, B, C, D, and E. Only then are they "done — tested
end-to-end in a real environment".

Then continue to **Step 6 (F — content) and Step 7 (G — UI)**.

---

## Cost watch (ongoing)

- The VM is a **flat monthly cost** — no per-attempt compute billing, no
  autoscaler. You only pay more when you add a second VM (~4–5 concurrent
  envs saturates a CAX41).
- Object storage for snapshots/recordings (~$3/mo) is the only variable
  add — set `RECORDING_S3_BUCKET` + `S3_ENDPOINT_URL` to a Hetzner/B2
  bucket when you want recordings; until then they're dropped (logged).
- Per `t2-cost-optimization-100.md` §3: ~₹93/user/mo at 50 users,
  ~₹52 at 100, dropping from there.
