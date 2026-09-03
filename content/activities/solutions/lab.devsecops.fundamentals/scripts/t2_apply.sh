#!/bin/bash
# lab.devsecops.fundamentals t2: install scan-secrets.sh as the repo's
# executable pre-commit hook. Validator: ~/repo/.git/hooks/pre-commit is -x.
set -uo pipefail
mkdir -p ~/repo && cd ~/repo
git rev-parse --is-inside-work-tree >/dev/null 2>&1 || git init -q
if [ ! -f scan-secrets.sh ]; then
  cat > scan-secrets.sh <<'SS'
#!/bin/bash
git diff --cached | grep -q 'AWS_SECRET_ACCESS_KEY' && { echo BLOCKED >&2; exit 1; }
exit 0
SS
  chmod +x scan-secrets.sh
fi
cp scan-secrets.sh .git/hooks/pre-commit
chmod +x .git/hooks/pre-commit
test -x .git/hooks/pre-commit && echo "pre-commit hook installed"
