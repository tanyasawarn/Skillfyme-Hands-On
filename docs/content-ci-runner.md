# Content-CI self-hosted runner — operator procedure

`content-ci.yml` needs a real k3s cluster and a running orchestrator, which GitHub-hosted
runners don't provide. This is the one-time setup for a self-hosted runner that does. After
this, the nightly and per-PR content-CI jobs run themselves.

Everything runnable was delivered in the repo (`scripts/ci/`, `.github/workflows/content-ci.yml`,
`evaluation/content-ci/README.md`). The steps below are the parts that need a machine and a
GitHub token — they can't be done from a coding session.

## 1. Provision a VM

| Resource | Minimum | Comfortable |
|---|---|---|
| vCPU | 4 | 8 |
| RAM | 8 GiB | 16 GiB |
| Disk | 40 GiB | 80 GiB |
| OS | Ubuntu 22.04 / 24.04 LTS | same |

Needs outbound internet (pull images, GitHub runner agent, Go/Node). No inbound ports required
— the runner agent dials out to GitHub.

## 2. Run the bootstrap script

```sh
git clone <this-repo> ~/practice-engine
cd ~/practice-engine
export GH_REPO_URL=https://github.com/<owner>/<repo>
bash scripts/ci/bootstrap-content-ci-runner.sh
```

It is idempotent (safe to re-run). It installs Docker / Go / Node / k3s, applies
`orchestrator/manifests/t1/*`, starts Postgres + Redis + NATS containers, applies the DB
migrations, builds the orchestrator and installs it as `content-ci-orchestrator.service`, and
downloads the GitHub Actions runner agent.

It prints, at the end:
- the generated `ORCHESTRATOR_SHARED_SECRET` (also saved to
  `~/content-ci/orchestrator-shared-secret.txt`),
- the exact `./config.sh` command to register the runner.

## 3. Register the runner

```sh
cd ~/content-ci/actions-runner
# registration token: GitHub -> repo Settings -> Actions -> Runners -> New self-hosted runner
#   or: gh api -X POST repos/<owner>/<repo>/actions/runners/registration-token -q .token
./config.sh --url $GH_REPO_URL --token <REG_TOKEN> --labels self-hosted,content-ci --unattended
sudo ./svc.sh install
sudo ./svc.sh start
```

Confirm it shows **Idle** under repo Settings → Actions → Runners.

## 4. Give the workflow the shared secret

Repo Settings → Secrets and variables → Actions → New repository secret:

- Name: `ORCHESTRATOR_SHARED_SECRET`
- Value: the secret the bootstrap script printed

(`content-ci.yml` passes it to `run-content-ci.sh`, which passes it to `content-ci.ts`, which
sends it as the `authorization: Bearer` metadata the orchestrator's `auth.go` interceptor
requires.)

## 5. First green run

GitHub → Actions → `content-ci` → **Run workflow** (workflow_dispatch). Leave `selectors`
empty for a full-library run, or put e.g. `lab.devops.fundamentals` to test one fast.

When it's green, save the evidence:

```sh
# in the repo, on any machine
mkdir -p evaluation/content-ci/results
cat > evaluation/content-ci/results/first-green-$(date +%F).md <<EOF
# content-ci — first green run

- Run URL: <paste the Actions run URL>
- Trigger: workflow_dispatch, selectors=<...>
- Runner: <hostname>
- Result: PASS
- Activities exercised: <from the run log's "content-ci: N activities selected" line>
EOF
git add evaluation/content-ci/results/ && git commit -m "content-ci: first green run"
```

This closes `PHASE0_1_2_PENDING_CLOSEOUT.md` item **2B.3**.

## 6. Let the schedule run

The `schedule:` cron (`0 3 * * *`, 03:00 UTC) now runs the full library nightly. The
`pull_request:` trigger runs changed-activity-only checks on any PR touching `content/`.

After ≥1 nightly full-library green run **and** ≥1 green per-PR run on a real content PR,
record both (append to a `results/` file) — that closes items **2B.4** and **2C.5**.

## Maintenance

- **Rotate the shared secret**: regenerate, update the systemd unit's
  `ORCHESTRATOR_SHARED_SECRET=`, `systemctl restart content-ci-orchestrator`, update the GitHub
  secret.
- **Update the orchestrator binary** after orchestrator changes land:
  `cd ~/practice-engine && git pull && (cd orchestrator && go build -o ~/content-ci/orchestrator ./cmd/orchestrator) && sudo systemctl restart content-ci-orchestrator`.
  (A follow-up improvement: have the workflow rebuild + restart it as a first step.)
- **Disk**: k3s + image pulls grow. `k3s crictl rmi --prune` periodically; `docker system prune`.
- **Teardown**: `sudo ~/content-ci/actions-runner/svc.sh stop && sudo ./svc.sh uninstall`,
  `./config.sh remove --token <TOKEN>`, `sudo systemctl disable --now content-ci-orchestrator`,
  `/usr/local/bin/k3s-uninstall.sh`, `docker rm -f cci-postgres cci-redis cci-nats`.
