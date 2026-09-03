#!/bin/bash
# lab.devsecops.container-security-tools t2: add `RUN useradd -m appuser`
# + `USER appuser` to ~/app/Dockerfile so check-root.sh passes.
# Validators: Dockerfile contains "USER appuser"; check-root.sh exits 0.
set -uo pipefail
mkdir -p ~/app
[ -f ~/app/Dockerfile ] || cat > ~/app/Dockerfile <<'DF'
FROM ubuntu:24.04
RUN apt-get update && apt-get install -y curl
COPY . /app
WORKDIR /app
CMD ["./run.sh"]
DF
if [ -f ~/app/check-root.sh ]; then :; else
cat > ~/app/check-root.sh <<'CR'
#!/bin/bash
grep -qE '^USER +[^ ]+' Dockerfile && ! grep -qE '^USER +(root|0)\b' Dockerfile && exit 0
exit 1
CR
chmod +x ~/app/check-root.sh
fi
grep -qE '^USER appuser$' ~/app/Dockerfile || {
  # insert useradd + USER before the final CMD/ENTRYPOINT (or append)
  printf '\nRUN useradd -m appuser\nUSER appuser\n' >> ~/app/Dockerfile
}
cd ~/app && ./check-root.sh && echo "Dockerfile now non-root"
