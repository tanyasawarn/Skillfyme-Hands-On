#!/bin/bash
# lab.devsecops.fundamentals t1: ~/repo/scan-secrets.sh exits non-zero if
# any staged file contains AWS_SECRET_ACCESS_KEY. Bootstraps ~/repo
# (fx.git-repo-empty.v1 has no handler). Validator stages a leaked.env
# and asserts the script exits non-zero.
set -uo pipefail
mkdir -p ~/repo && cd ~/repo
git rev-parse --is-inside-work-tree >/dev/null 2>&1 || git init -q
git config user.name  >/dev/null 2>&1 || git config user.name  "content-ci"
git config user.email >/dev/null 2>&1 || git config user.email "content-ci@example.dev"
cat > scan-secrets.sh <<'SS'
#!/bin/bash
# Fail if any staged change introduces the literal AWS_SECRET_ACCESS_KEY.
if git diff --cached | grep -q 'AWS_SECRET_ACCESS_KEY'; then
  echo "BLOCKED: staged changes contain AWS_SECRET_ACCESS_KEY" >&2
  exit 1
fi
exit 0
SS
chmod +x scan-secrets.sh
