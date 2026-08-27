#!/usr/bin/env bash
# bootstrap-content-ci-runner.sh
#
# One-time (idempotent) provisioning of a Linux VM as the self-hosted
# GitHub Actions runner that .github/workflows/content-ci.yml targets.
# After this script, the box has:
#
#   - k3s (single node) with the T1 manifests applied
#   - Postgres 16, Redis 7, NATS (JetStream) as containers
#   - the orchestrator built and running as a systemd service, talking
#     to that k3s + Postgres, with a shared secret
#   - the GitHub Actions runner agent installed (you run ./config.sh to
#     register it -- URL + token are printed at the end)
#
# Re-running is safe: every step checks-then-acts. Nothing here is
# specific to a cloud provider; it assumes Ubuntu 22.04/24.04 with sudo,
# ~4 vCPU / 8 GiB RAM / 40 GiB disk. See docs/content-ci-runner.md for
# the full procedure and the rationale for each choice.
set -euo pipefail

# --- config (override via env) ----------------------------------------
: "${GH_REPO_URL:?set GH_REPO_URL=https://github.com/<owner>/<repo>}"
RUNNER_LABELS="${RUNNER_LABELS:-self-hosted,content-ci}"
RUNNER_VERSION="${RUNNER_VERSION:-2.320.0}"
ORCH_SHARED_SECRET="${ORCH_SHARED_SECRET:-$(head -c 32 /dev/urandom | base64 | tr -d '/+=' | head -c 40)}"
WORKDIR="${WORKDIR:-$HOME/content-ci}"
PG_PASSWORD="${PG_PASSWORD:-practice}"
DB_NAME="${DB_NAME:-practice_engine}"

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"

echo "==> workdir: $WORKDIR"
mkdir -p "$WORKDIR"

# --- 1. base packages -----------------------------------------------------
echo "==> [1/7] base packages (docker, go, node, jq, psql client)"
if ! command -v docker >/dev/null; then
  curl -fsSL https://get.docker.com | sudo sh
  sudo usermod -aG docker "$USER" || true
fi
if ! command -v go >/dev/null; then
  GO_VER=1.26.6
  curl -fsSL "https://go.dev/dl/go${GO_VER}.linux-amd64.tar.gz" | sudo tar -C /usr/local -xz
  echo 'export PATH=$PATH:/usr/local/go/bin:$HOME/go/bin' | sudo tee /etc/profile.d/go.sh >/dev/null
  export PATH="$PATH:/usr/local/go/bin:$HOME/go/bin"
fi
if ! command -v node >/dev/null; then
  curl -fsSL https://deb.nodesource.com/setup_22.x | sudo -E bash -
  sudo apt-get install -y nodejs
fi
sudo apt-get install -y jq postgresql-client >/dev/null

# --- 2. k3s -------------------------------------------------------------
echo "==> [2/7] k3s"
if ! command -v k3s >/dev/null; then
  curl -fsSL https://get.k3s.io | sudo sh -s - --disable=traefik --write-kubeconfig-mode=644
fi
export KUBECONFIG=/etc/rancher/k3s/k3s.yaml
sudo k3s kubectl get nodes

echo "==> [2b/7] apply T1 manifests"
for m in "$REPO_ROOT"/orchestrator/manifests/t1/*.yaml; do
  echo "    kubectl apply -f $(basename "$m")"
  sudo k3s kubectl apply -f "$m"
done

# --- 3. datastores ----------------------------------------------------
echo "==> [3/7] Postgres / Redis / NATS containers"
docker rm -f cci-postgres cci-redis cci-nats >/dev/null 2>&1 || true
docker run -d --name cci-postgres --restart unless-stopped \
  -e POSTGRES_USER=practice -e POSTGRES_PASSWORD="$PG_PASSWORD" -e POSTGRES_DB="$DB_NAME" \
  -p 5432:5432 postgres:16
docker run -d --name cci-redis --restart unless-stopped -p 6379:6379 redis:7
docker run -d --name cci-nats  --restart unless-stopped -p 4222:4222 nats:2 -js

echo "==> [3b/7] wait for Postgres, apply migrations"
for i in $(seq 1 30); do
  PGPASSWORD="$PG_PASSWORD" psql -h localhost -U practice -d "$DB_NAME" -c 'select 1' >/dev/null 2>&1 && break
  sleep 2
done
# orchestrator's own schemas (env, billing, ...) + practice-core's, since
# content-ci.ts seeds skills into the same DB the orchestrator uses.
for f in "$REPO_ROOT"/orchestrator/db/migrations/*.sql \
         "$REPO_ROOT"/practice-core/db/migrations/*.sql; do
  echo "    psql -f $(basename "$f")"
  PGPASSWORD="$PG_PASSWORD" psql -h localhost -U practice -d "$DB_NAME" -v ON_ERROR_STOP=1 -f "$f"
done

# --- 4. build + install the orchestrator -----------------------------
echo "==> [4/7] build orchestrator"
( cd "$REPO_ROOT/orchestrator" && /usr/local/go/bin/go build -o "$WORKDIR/orchestrator" ./cmd/orchestrator )

echo "==> [4b/7] orchestrator systemd unit"
sudo tee /etc/systemd/system/content-ci-orchestrator.service >/dev/null <<UNIT
[Unit]
Description=Practice Engine orchestrator (content-ci runner)
After=docker.service k3s.service
Wants=docker.service k3s.service

[Service]
Environment=KUBECONFIG=/etc/rancher/k3s/k3s.yaml
Environment=DATABASE_URL=postgres://practice:${PG_PASSWORD}@localhost:5432/${DB_NAME}
Environment=REDIS_URL=redis://localhost:6379
Environment=NATS_URL=nats://localhost:4222
Environment=ORCHESTRATOR_GRPC_PORT=50051
Environment=ORCHESTRATOR_WS_PORT=8081
Environment=WS_GATEWAY_BASE_URL=ws://localhost:8081
Environment=WS_GATEWAY_JWT_SECRET=content-ci-ws-secret
Environment=ORCHESTRATOR_SHARED_SECRET=${ORCH_SHARED_SECRET}
Environment=ORCHESTRATOR_GVISOR_ENABLED=false
Environment=DEFAULT_BUDGET_USD=0.08
ExecStart=${WORKDIR}/orchestrator
Restart=on-failure
User=${USER}

[Install]
WantedBy=multi-user.target
UNIT
sudo systemctl daemon-reload
sudo systemctl enable --now content-ci-orchestrator.service
sleep 3
sudo systemctl --no-pager -l status content-ci-orchestrator.service | head -20

# --- 5. persist the shared secret for the workflow ------------------
echo "==> [5/7] shared secret"
echo "$ORCH_SHARED_SECRET" > "$WORKDIR/orchestrator-shared-secret.txt"
chmod 600 "$WORKDIR/orchestrator-shared-secret.txt"
cat <<NOTE

    The orchestrator shared secret is:
        $ORCH_SHARED_SECRET
    Saved to $WORKDIR/orchestrator-shared-secret.txt

    Add it as a GitHub Actions secret named ORCHESTRATOR_SHARED_SECRET
    (repo Settings -> Secrets and variables -> Actions), OR set it in the
    runner's environment file (.env in the runner dir). content-ci.yml
    reads it from \${{ secrets.ORCHESTRATOR_SHARED_SECRET }}.

NOTE

# --- 6. GitHub Actions runner agent --------------------------------
echo "==> [6/7] GitHub Actions runner agent"
RUNNER_DIR="$WORKDIR/actions-runner"
if [ ! -d "$RUNNER_DIR" ]; then
  mkdir -p "$RUNNER_DIR" && cd "$RUNNER_DIR"
  curl -fsSL -o runner.tar.gz \
    "https://github.com/actions/runner/releases/download/v${RUNNER_VERSION}/actions-runner-linux-x64-${RUNNER_VERSION}.tar.gz"
  tar xzf runner.tar.gz && rm runner.tar.gz
fi

# --- 7. next steps ------------------------------------------------------
echo "==> [7/7] DONE. Register the runner:"
cat <<NEXT

    cd $RUNNER_DIR
    # Get a registration token:
    #   GitHub -> $GH_REPO_URL/settings/actions/runners/new
    #   (or: gh api -X POST repos/<owner>/<repo>/actions/runners/registration-token -q .token)
    ./config.sh --url $GH_REPO_URL --token <REG_TOKEN> --labels $RUNNER_LABELS --unattended
    sudo ./svc.sh install
    sudo ./svc.sh start

    Then in .github/workflows/content-ci.yml, trigger a manual run
    (workflow_dispatch) and confirm it goes green. Once green, the
    nightly schedule takes over.

NEXT
