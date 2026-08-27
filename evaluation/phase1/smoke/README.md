# Compose `app` profile + E2E smoke test

Closes PHASE1_MVP_COMPLETION.md §1.1 items 1 and 2:

- `docker-compose.yml` now builds and runs `orchestrator`, `practice-core`
  and `web` as services (profile `app`), on top of the infra + `dev-a`
  services. Each has its own `Dockerfile` at its service root.
- `run-smoke.sh` is the scripted end-to-end proof: compose up → start an
  attempt from the catalog → real k3s-backed workspace pod → terminal WS
  URL → teardown.

## Bring the whole stack up

```
docker compose --profile app up -d --build
```

| Service | Host port | Notes |
|---|---|---|
| web (Next.js) | http://localhost:3000 | `NEXT_PUBLIC_API_BASE_URL` baked at build to `http://localhost:3001` |
| practice-core (NestJS) | http://localhost:3001 | real `GrpcOrchestratorClient` (`USE_FAKE_ORCHESTRATOR=false`) |
| orchestrator gRPC | localhost:50051 | shared-secret auth ON (`compose-dev-shared-secret`) |
| orchestrator WS gateway | ws://localhost:8081 | |
| orchestrator metrics/healthz | http://localhost:9090 | |
| k3s API | https://localhost:6443 | in-cluster the orchestrator dials `https://k3s:6443` (added `--tls-san=k3s`) |

Postgres migrations: Dev B's set is applied by the postgres image's
`docker-entrypoint-initdb.d` mount on first init; Dev A's `env`/`billing`
set is applied by the idempotent `db-migrate-orchestrator` one-shot on
every `up`.

## Run the smoke test

```
evaluation/phase1/smoke/run-smoke.sh
```

It layers `docker-compose.smoke.yml`, which shifts every host port
(`3100/3101/50151/8181/9190`) so the stack runs alongside a developer's
local non-container `orchestrator` / `nest start` / `next dev`. Requires
`curl`, `python3`, `grpcurl`, `kubectl`, and the repo's
`.local/k3s-output/kubeconfig.yaml`.

The most recent captured result is in
`evaluation/phase1/results/smoke-compose-app-<date>.md`.

## Known limitations

- The smoke test seeds `skill.skill_mastery` for the demo user so the
  eligibility prereq gate passes — a real learner reaches that state by
  completing prerequisites. This is a test fixture, not a bypass in
  product code.
- Single Docker host / single-node k3s: fine for a functional smoke test,
  not for the §7 load run (200 concurrent learners) which still needs a
  real multi-node cluster.
