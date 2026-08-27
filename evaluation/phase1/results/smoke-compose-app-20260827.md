# Compose `app` profile — E2E smoke test result

**Run:** 2026-08-27T10:48:19Z · **Host:** Darwin arm64 · Docker Desktop k3s (single Docker host)

Executed `evaluation/phase1/smoke/run-smoke.sh` (PHASE1_MVP_COMPLETION.md §1.1). Images built from the three new service Dockerfiles, carrying the structured-logging (§4.2) and identity-from-JWT (§1.1/§5) changes.

## Result: PASSED (exit 0)

| Step | Result |
|---|---|
| `docker compose --profile app up --build` (orchestrator + practice-core + web + infra) | 9 containers running / healthy |
| practice-core real `GrpcOrchestratorClient` -> orchestrator gRPC, shared-secret auth ON, `USE_FAKE_ORCHESTRATOR=false` | connected |
| orchestrator `/healthz` + `/metrics` serving `orchestrator_*` series | ok |
| `POST /v1/practice/attempts` (`lab.linux.navigate-filesystem`, L1) | attempt CREATED |
| `POST /v1/practice/attempts/:id/provision` -> orchestrator cold-provision -> **real k3s** | attempt READY, `environment_id` assigned |
| `kubectl get pod workspace -n env-<id>` | Running |
| `POST /v1/practice/attempts/:id/connect` | real `terminalWsUrl` (`ws://.../terminal?session=<signed JWT>`) |
| orchestrator `Destroy` RPC | `env-<id>` namespace Terminating -> gone |
| orchestrator logs during the run | structured JSON: `{"level":"INFO","msg":"cold-provisioning environment","component":"orchestrator","env_id":...,"attempt_id":...,"source":"cold"}` |

## Structured-log sample (from this run)

```json
{"time":"2026-08-27T10:45:45.293388009Z","level":"INFO","msg":"started","component":"idledetect"}
{"time":"2026-08-27T10:45:45.2942233Z","level":"INFO","msg":"started","component":"reaper","interval":"1m0s"}
{"time":"2026-08-27T10:47:11.626551673Z","level":"INFO","msg":"cold-provisioning environment","component":"orchestrator","env_id":"7cc8e4a0-59c7-4ee6-b90c-e995c90e4228","attempt_id":"1cd47e82-832c-4f3f-a8b6-1bf6f9ad0cd5","blueprint":"bp.linux.v1","tier":"TIER_T1_SHARED_CONTAINER","source":"cold"}
{"time":"2026-08-27T10:47:15.161358842Z","level":"INFO","msg":"destroying environment","component":"orchestrator","env_id":"7cc8e4a0-59c7-4ee6-b90c-e995c90e4228","reason":"admin"}
```

## Full run log

```

[1;34m== building + starting the app profile[0m
 Image practice-engine-practice-core Building 
 Image practice-engine-orchestrator Building 
 Image practice-engine-web Building 
#1 [internal] load local bake definitions
#1 reading from stdin 1.61kB done
#1 DONE 0.0s

#2 [practice-core internal] load build definition from Dockerfile
#2 transferring dockerfile: 1.90kB 0.0s done
#2 DONE 0.1s

#3 [web internal] load build definition from Dockerfile
#3 transferring dockerfile: 1.39kB 0.0s done
#3 DONE 0.1s

#4 [orchestrator internal] load build definition from Dockerfile
#4 transferring dockerfile: 1.55kB 0.0s done
#4 DONE 0.1s

#5 [practice-core internal] load metadata for docker.io/library/node:22-slim
#5 ...

#6 [orchestrator internal] load metadata for gcr.io/distroless/static-debian12:nonroot
#6 DONE 0.7s

#7 [orchestrator internal] load metadata for docker.io/library/golang:1.26
#7 DONE 1.6s

#5 [practice-core internal] load metadata for docker.io/library/node:22-slim
#5 DONE 1.6s

#8 [orchestrator internal] load .dockerignore
#8 transferring context: 260B done
#8 DONE 0.0s

#9 [web internal] load .dockerignore
#9 transferring context: 99B done
#9 DONE 0.0s

#10 [practice-core internal] load .dockerignore
#10 transferring context: 116B done
#10 DONE 0.0s

#11 [orchestrator stage-1 1/3] FROM gcr.io/distroless/static-debian12:nonroot@sha256:afa5c872c891853ca7fcf1f12c3edb23f7eeef36189728842dd51042ff57f7ab
#11 resolve gcr.io/distroless/static-debian12:nonroot@sha256:afa5c872c891853ca7fcf1f12c3edb23f7eeef36189728842dd51042ff57f7ab 0.0s done
#11 DONE 0.0s

#12 [orchestrator build 1/6] FROM docker.io/library/golang:1.26@sha256:dc2521c2a906db43073b8b4d99f491b6341cf15610b6ebbab187c45153f9959e
#12 resolve docker.io/library/golang:1.26@sha256:dc2521c2a906db43073b8b4d99f491b6341cf15610b6ebbab187c45153f9959e 0.0s done
#12 DONE 0.0s

#13 [web deps 1/4] FROM docker.io/library/node:22-slim@sha256:83f487e0a63425e5b4d146fb5e5be574bcbe1b7b843d3ebafdd95eaf7767a7e5
#13 resolve docker.io/library/node:22-slim@sha256:83f487e0a63425e5b4d146fb5e5be574bcbe1b7b843d3ebafdd95eaf7767a7e5 0.0s done
#13 DONE 0.0s

#14 [web internal] load build context
#14 transferring context: 408.56kB 0.0s done
#14 DONE 0.0s

#15 [web deps 2/4] WORKDIR /app
#15 CACHED

#16 [web deps 3/4] COPY package.json package-lock.json ./
#16 CACHED

#17 [web deps 4/4] RUN npm ci
#17 CACHED

#18 [web build 3/5] COPY --from=deps /app/node_modules ./node_modules
#18 CACHED

#19 [practice-core internal] load build context
#19 transferring context: 670.85kB 0.1s done
#19 DONE 0.1s

#20 [practice-core build 3/5] COPY --from=deps /app/practice-core/node_modules ./node_modules
#20 CACHED

#21 [practice-core runtime 3/7] RUN apt-get update && apt-get install -y --no-install-recommends tini     && rm -rf /var/lib/apt/lists/*
#21 CACHED

#22 [practice-core deps 3/4] COPY package.json package-lock.json ./
#22 CACHED

#23 [practice-core build 5/5] RUN npm run build
#23 CACHED

#24 [practice-core runtime 6/7] COPY package.json ./
#24 CACHED

#25 [practice-core proddeps 4/4] RUN npm ci --omit=dev
#25 CACHED

#26 [practice-core runtime 5/7] COPY --from=build /app/practice-core/dist ./dist
#26 CACHED

#27 [practice-core deps 2/4] WORKDIR /app/practice-core
#27 CACHED

#28 [practice-core deps 4/4] RUN npm ci
#28 CACHED

#29 [practice-core build 4/5] COPY . .
#29 CACHED

#30 [practice-core runtime 4/7] COPY --from=proddeps /app/practice-core/node_modules ./node_modules
#30 CACHED

#31 [practice-core runtime 7/7] COPY db ./db
#31 CACHED

#32 [orchestrator internal] load build context
#32 transferring context: 390.66kB 0.1s done
#32 DONE 0.1s

#33 [orchestrator build 3/6] COPY go.mod go.sum ./
#33 CACHED

#34 [orchestrator stage-1 2/3] WORKDIR /app
#34 CACHED

#35 [orchestrator build 5/6] COPY . .
#35 CACHED

#36 [orchestrator build 6/6] RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags='-s -w'     -o /out/orchestrator ./cmd/orchestrator
#36 CACHED

#37 [orchestrator build 2/6] WORKDIR /src
#37 CACHED

#38 [orchestrator build 4/6] RUN go mod download
#38 CACHED

#39 [orchestrator stage-1 3/3] COPY --from=build /out/orchestrator /app/orchestrator
#39 CACHED

#40 [orchestrator] exporting to image
#40 exporting layers done
#40 exporting manifest sha256:da0df13d3b7d4c89d1db1f290919a3a153c37f91bd0b234cad7db6e13c9f0adb done
#40 exporting config sha256:3996eb1e7c4fe9a9d483eb57cb7b3071b29a3e2c5a8445fded80ba56e5fe1920 done
#40 exporting attestation manifest sha256:7b1b6d957f3d23afae5c94b26094f06a05ca4fed772dfe0a150a2802a79e78c8
#40 exporting attestation manifest sha256:7b1b6d957f3d23afae5c94b26094f06a05ca4fed772dfe0a150a2802a79e78c8 0.0s done
#40 exporting manifest list sha256:8165ea9c520e4d3e4e87fd2c65e01ebace6fcf0648197bdd3cda43e5662f79ff done
#40 naming to docker.io/library/practice-engine-orchestrator:latest done
#40 unpacking to docker.io/library/practice-engine-orchestrator:latest 0.0s done
#40 DONE 0.2s

#41 [web build 4/5] COPY . .
#41 DONE 0.1s

#42 [practice-core] exporting to image
#42 exporting layers done
#42 exporting manifest sha256:b4b7fb43e9a878356ac1c6b1d4cfbc7dcec9932b3fd76277db557801cfb23811 0.0s done
#42 exporting config sha256:f57680c698b0c52ffb90cb2789c68431a03861bfb33f5f53c079c18ff3731fa6 done
#42 exporting attestation manifest sha256:cf08ef1886c90dca9a40d0fcdaa126d3fd8f438898a7d0d714d42f998f2ff5c7 0.0s done
#42 exporting manifest list sha256:46db37439d9a124c6e60702a2a58f8aeeba904b2c6b99cc0e7a3115c0d7f8843 done
#42 naming to docker.io/library/practice-engine-practice-core:latest 0.0s done
#42 unpacking to docker.io/library/practice-engine-practice-core:latest done
#42 DONE 0.1s

#43 [web build 5/5] RUN npm run build
#43 ...

#44 [practice-core] resolving provenance for metadata file
#44 DONE 0.0s

#45 [orchestrator] resolving provenance for metadata file
#45 DONE 0.0s

#43 [web build 5/5] RUN npm run build
#43 0.525 
#43 0.525 > web@0.1.0 build
#43 0.525 > next build
#43 0.525 
#43 0.838 ▲ Next.js 16.3.1 (Turbopack)
#43 0.943 ✓ Running next.config.ts took 105ms
#43 0.946 Attention: Next.js now collects completely anonymous telemetry regarding usage.
#43 0.946 This information is used to shape Next.js' roadmap and prioritize features.
#43 0.946 You can learn more, including how to opt-out if you'd not like to participate in this anonymous program, by visiting the following URL:
#43 0.946 https://nextjs.org/telemetry
#43 0.946 
#43 0.956 
#43 0.991   Creating an optimized production build ...
#43 9.914 ✓ Compiled successfully in 8.3s
#43 9.920   Running TypeScript ...
#43 12.39   Finished TypeScript in 2.5s ...
#43 12.40   Collecting page data using 9 workers ...
#43 16.80   Generating static pages using 9 workers (0/7) ...
#43 19.57   Generating static pages using 9 workers (1/7) 
#43 19.63   Generating static pages using 9 workers (3/7) 
#43 19.64   Generating static pages using 9 workers (5/7) 
#43 19.64 ✓ Generating static pages using 9 workers (7/7) in 2.8s
#43 19.66   Finalizing page optimization ...
#43 20.45 
#43 20.46 Route (app)
#43 20.46 ┌ ○ /
#43 20.46 ├ ○ /_not-found
#43 20.46 ├ ƒ /attempts/[id]
#43 20.46 ├ ○ /catalog
#43 20.46 ├ ƒ /catalog/[activityVersionId]
#43 20.46 ├ ○ /history
#43 20.46 └ ○ /skills
#43 20.46 
#43 20.46 
#43 20.46 ○  (Static)   prerendered as static content
#43 20.46 ƒ  (Dynamic)  server-rendered on demand
#43 20.46 
#43 20.51 npm notice
#43 20.51 npm notice New major version of npm available! 10.9.8 -> 12.0.2
#43 20.51 npm notice Changelog: https://github.com/npm/cli/releases/tag/v12.0.2
#43 20.51 npm notice To update run: npm install -g npm@12.0.2
#43 20.51 npm notice
#43 DONE 21.2s

#46 [web runtime 3/6] RUN apt-get update && apt-get install -y --no-install-recommends tini     && rm -rf /var/lib/apt/lists/*
#46 CACHED

#47 [web runtime 4/6] COPY --from=build /app/.next/standalone ./
#47 DONE 0.9s

#48 [web runtime 5/6] COPY --from=build /app/.next/static ./.next/static
#48 DONE 0.1s

#49 [web runtime 6/6] COPY --from=build /app/public ./public
#49 DONE 0.0s

#50 [web] exporting to image
#50 exporting layers
#50 exporting layers 1.4s done
#50 exporting manifest sha256:1d54c26ebf928e1f4f4fa2aa3aa33e6169d590881b26b7167b0cdaf8d9d4a9da done
#50 exporting config sha256:ed84664039fc4d00766dc34cec78b96cf61892941f0cb89d57b62032239829bd 0.0s done
#50 exporting attestation manifest sha256:48194884a4d902317e9a7707bb837de3527978612410b2dcaf4100974fe3b5ce 0.0s done
#50 exporting manifest list sha256:6535e772a6797b4c180ceeda0270e1f8f15d93515d99712ca0d8036df82dcbdb done
#50 naming to docker.io/library/practice-engine-web:latest done
#50 unpacking to docker.io/library/practice-engine-web:latest
#50 unpacking to docker.io/library/practice-engine-web:latest 0.5s done
#50 DONE 2.0s

#51 [web] resolving provenance for metadata file
#51 DONE 0.0s
 Image practice-engine-orchestrator Built 
 Image practice-engine-practice-core Built 
 Image practice-engine-web Built 
 Container practice-engine-practice-core-1 Running 
 Container practice-engine-registry-1 Running 
 Container practice-engine-k3s-1 Running 
 Container practice-engine-nats-1 Running 
 Container practice-engine-postgres-1 Running 
 Container practice-engine-orchestrator-1 Running 
 Container practice-engine-redis-1 Running 
 Container practice-engine-minio-1 Running 
 Container practice-engine-web-1 Recreate 
 Container practice-engine-web-1 Recreated 
 Container practice-engine-minio-1 Waiting 
 Container practice-engine-postgres-1 Waiting 
 Container practice-engine-kubeconfig-internal-1 Starting 
 Container practice-engine-kubeconfig-internal-1 Started 
 Container practice-engine-postgres-1 Healthy 
 Container practice-engine-minio-1 Healthy 
 Container practice-engine-db-migrate-orchestrator-1 Starting 
 Container practice-engine-minio-init-1 Starting 
 Container practice-engine-db-migrate-orchestrator-1 Started 
 Container practice-engine-redis-1 Waiting 
 Container practice-engine-kubeconfig-internal-1 Waiting 
 Container practice-engine-postgres-1 Waiting 
 Container practice-engine-db-migrate-orchestrator-1 Waiting 
 Container practice-engine-minio-init-1 Started 
 Container practice-engine-kubeconfig-internal-1 Exited 
 Container practice-engine-db-migrate-orchestrator-1 Exited 
 Container practice-engine-postgres-1 Healthy 
 Container practice-engine-redis-1 Healthy 
 Container practice-engine-postgres-1 Waiting 
 Container practice-engine-redis-1 Waiting 
 Container practice-engine-postgres-1 Healthy 
 Container practice-engine-redis-1 Healthy 
 Container practice-engine-web-1 Starting 
 Container practice-engine-web-1 Started 

[1;34m== waiting for practice-core to answer[0m
dev token acquired (281 chars)

[1;34m== orchestrator /healthz + /metrics[0m

[1;34m== seeding prereq mastery for the smoke user (a real learner would have this after prerequisites)[0m
INSERT 0 2

[1;34m== picking a published L1 lab from the catalog[0m

[1;34m== starting an attempt (4c3faedf-00f0-44d8-8a43-999bdb94f1fd)[0m
attempt id: 1cd47e82-832c-4f3f-a8b6-1bf6f9ad0cd5

[1;34m== provisioning (practice-core -> orchestrator gRPC -> real k3s)[0m
environment id: 7cc8e4a0-59c7-4ee6-b90c-e995c90e4228

[1;34m== asserting a real workspace Pod exists in k3s[0m

[1;34m== /connect returns a terminal WebSocket URL[0m
terminalWsUrl OK: ws://localhost:8081/v1/env/7cc8e4a0-59c7-4ee6-b90c-e995c90e4228/terminal

[1;34m== tearing the environment down via the orchestrator Destroy RPC[0m

[1;32mSMOKE TEST PASSED[0m
leave the stack up for inspection, or: docker compose --profile app -f docker-compose.yml -f evaluation/phase1/smoke/docker-compose.smoke.yml down
```
