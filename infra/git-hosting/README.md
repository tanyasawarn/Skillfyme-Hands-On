# infra/git-hosting — per-learner platform Git hosting

Phase 3 (PLAN_PHASE3_PROJECTS.md **1.4 / B9**, decision **D-P3-1**). Self-hosted
**Forgejo** (the community fork of Gitea): one deployment, a per-learner org +
repo per project enrolment, **permanent retention** (portfolio value —
`memory.md` §12.3), and direct commit-history read access for the milestone-5
viva generator (Stage 3.8 / 1.7).

Why Forgejo and not a hosted provider: the sim fixture already runs Gitea
(`orchestrator/internal/fixture/handlers_gitea.go`) so the ops knowledge exists;
full control over retention; no per-seat cost at cohort scale; the Gitea/Forgejo
API is what `practice-core/src/modules/project/git.service.ts` (1.7) is written
against.

## Layout

| path | what | status |
|---|---|---|
| `compose/docker-compose.git-hosting.yml` | a **real, runnable** Forgejo on the local compose stack (profile `git-hosting`). Used to develop + test `git.service.ts` end-to-end today. | **works now** |
| `compose/forgejo-app.ini` | the config the compose Forgejo mounts (disabled signup, token auth, permanent repos). | works now |
| `helm/` | production Kubernetes deployment — a thin chart wrapping the upstream Forgejo image with a PVC for repo storage, an Ingress, and Postgres-backed metadata. | authored; **apply [B]** (needs a cluster) |
| `terraform/` | the AWS side: an EBS-backed `gp3` volume / EFS for repo storage, the RDS instance for Forgejo's metadata DB, DNS. Applies into the **Platform** account (not a learner sandbox). | authored; `tofu validate` clean; **apply [B]** (needs the Platform AWS account) |
| `scripts/verify-git-hosting.sh` | end-to-end check: create an org + repo via the admin token, push a commit over HTTP, read it back via the API, confirm the repo survives a simulated "post-project nuke" (i.e. it is not in any learner sandbox). | runs against the compose deploy |

## Run the local deploy

```
docker compose --profile git-hosting -f docker-compose.yml \
  -f infra/git-hosting/compose/docker-compose.git-hosting.yml up -d

# first-run admin user + token (idempotent)
infra/git-hosting/scripts/bootstrap-admin.sh

# end-to-end verify
infra/git-hosting/scripts/verify-git-hosting.sh
```

Forgejo comes up on <http://localhost:3300>. `practice-core` reads
`FORGEJO_BASE_URL` / `FORGEJO_ADMIN_TOKEN` from its env (see
`practice-core/.env.example` additions) — with those set, `git.service.ts`
provisions against this real instance.

## Production apply (blocked)

`helm/` and `terraform/` are review-ready. Applying them needs:

- the Platform AWS account (Stage 1.1) for `terraform/` — RDS + storage + DNS,
- a Kubernetes cluster with an ingress controller + a `StorageClass` for the
  repo PVC for `helm/`,
- secrets: `FORGEJO_ADMIN_PASSWORD`, the RDS password, an SMTP relay for
  notifications (optional).

Until then this item is **`[B]` on apply** — the deployment definitions and the
`git.service.ts` client that consumes them are complete and tested against the
local Forgejo.
