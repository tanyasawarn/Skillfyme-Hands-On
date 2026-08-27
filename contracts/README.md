# contracts/

The frozen interface between the two service tracks. Everything here is consumed by **both**
`practice-core` (Dev B) and `orchestrator` (Dev A), so a change that is not carefully reviewed
breaks the other side silently.

| File | What it is | Consumed by |
|---|---|---|
| `orchestrator.proto` | the gRPC contract — `Provision / Connect / Snapshot / Restore / MintValidatorCredentials / Destroy / InjectFault / CaptureBaseline / CheckRegression / ExecValidator / ExecShell` | orchestrator (generates `pkg/pb/*.pb.go`); practice-core (loads at runtime via `@grpc/proto-loader`) |
| `events.md` + `events/*.schema.json` | the NATS event taxonomy and payload schemas (`attempt_events`, `env.*`, `env.telemetry.*`) | both sides — publisher and consumer |
| `activity_spec.schema.json` | JSON Schema for the activity YAML (`content/activities/*.yaml`) | practice-core content lint + CMS |
| `fault.schema.json` | JSON Schema for fault definitions (`content/faults/*.yaml`) | practice-core fault lint |
| `CHANGELOG.md` | one entry per change, classified PATCH / MINOR / MAJOR | humans |
| `buf.yaml` / `buf.gen.yaml` | `buf lint` + `buf breaking` config, and deterministic Go codegen | CI + `buf generate` |

## The rule

From `PLAN.md`: contracts are frozen after Phase 0; changes go through a PR that **both**
`contracts/` code owners approve (`../.github/CODEOWNERS`), saved for the weekly integration
checkpoint rather than made ad hoc. In practice that lapsed once (see the CHANGELOG's
2026-08-27 reconcile entry). It is now enforced mechanically:

1. **`../.github/CODEOWNERS`** makes any PR touching `contracts/` require review from both
   owners. (GitHub branch protection with "Require review from Code Owners" is the one-time
   repo-settings step that makes this blocking — see below.)

2. **The `contracts` CI job** (`../.github/workflows/ci.yml`) runs on every PR that touches
   `contracts/**` or `orchestrator/pkg/pb/**`:
   - `buf lint` — style consistency (STANDARD rules; the pre-existing already-shipped naming
     choices are frozen as documented exceptions in `buf.yaml`, new messages/RPCs get the full
     rule set).
   - `buf breaking` against `origin/main` — **rejects any wire-incompatible change**: field
     renumber, type change, field or RPC removal. This is what structurally guarantees
     post-freeze changes stay additive.
   - `buf generate` + `git diff --exit-code orchestrator/pkg/pb/` — the generated Go stubs
     must already be regenerated and committed. A proto edit that forgets `buf generate`
     fails here.

3. **`CHANGELOG.md`** gets an entry in the same PR. MAJOR changes additionally need the §3.6
   canary rollout and a coordinated deploy of both services.

## Making a change

```sh
# 1. edit orchestrator.proto (additive only, unless you mean to do a MAJOR)
# 2. regenerate the Go stubs
cd contracts && buf generate

# 3. check it yourself before pushing
buf lint
buf breaking . --against '.git#branch=main,subdir=contracts'   # run from repo root: buf breaking contracts --against ...

# 4. add a CHANGELOG.md entry (PATCH / MINOR / MAJOR)
# 5. open the PR — both code owners review, the `contracts` CI job must be green
```

The **TypeScript side generates nothing**: practice-core loads `orchestrator.proto` at runtime
(`practice-core/src/common/base-grpc-client.ts`). The `.proto` file itself is the artifact it
consumes — so keeping the proto correct is the whole job on that side.

## One-time repo settings (operator)

- Install `buf` on CI runners (the `contracts` job does `go install
  github.com/bufbuild/buf/cmd/buf@v1.72.0` — pin to match local).
- In GitHub → Settings → Branches → `main` protection: enable **Require review from Code
  Owners** and add the `contracts` job to **Require status checks to pass**.
- Fill real handles into `../.github/CODEOWNERS` (currently placeholders).
