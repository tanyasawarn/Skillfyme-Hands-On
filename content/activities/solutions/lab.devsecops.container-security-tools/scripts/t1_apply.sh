#!/bin/bash
# lab.devsecops.container-security-tools t1: ~/app/check-root.sh exits
# non-zero if ~/app/Dockerfile has no non-root USER line. Bootstraps
# ~/app/Dockerfile in its insecure form (fx.dockerfile-insecure.v1 has
# no handler). Validator asserts check-root.sh exits non-zero here.
set -uo pipefail
mkdir -p ~/app
[ -f ~/app/Dockerfile ] || cat > ~/app/Dockerfile <<'DF'
FROM ubuntu:24.04
RUN apt-get update && apt-get install -y curl
COPY . /app
WORKDIR /app
CMD ["./run.sh"]
DF
cat > ~/app/check-root.sh <<'CR'
#!/bin/bash
# Fail if the Dockerfile sets no non-root USER.
if grep -qE '^USER +[^ ]+' Dockerfile && ! grep -qE '^USER +(root|0)\b' Dockerfile; then
  exit 0
fi
echo "INSECURE: Dockerfile runs as root (no non-root USER instruction)" >&2
exit 1
CR
chmod +x ~/app/check-root.sh
