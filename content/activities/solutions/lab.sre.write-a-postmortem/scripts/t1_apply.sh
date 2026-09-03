#!/bin/bash
# lab.sre.write-a-postmortem t1: the only validator is FILE_EXISTS on
# /workspace/incident-facts.md, which fx.postmortem-incident-facts.v1 is
# meant to seed. That fixture has no handler, so this script writes the
# facts file the lab expects. Idempotent.
set -uo pipefail
mkdir -p /workspace 2>/dev/null || true
DEST=/workspace/incident-facts.md
if ! [ -w "$(dirname "$DEST")" ] 2>/dev/null; then DEST="$HOME/incident-facts.md"; fi
cat > "$DEST" <<'MD'
# Incident facts — checkout latency spike

- **Detected:** 14:02 UTC, PagerDuty alert `checkout_p99_latency > 2s`.
- **Impact:** ~11 min of elevated checkout latency (p99 4.8s), ~3% of
  checkout requests timed out.
- **Diagnosis commands run:**
  - `kubectl get pods -n checkout` — one pod in CrashLoopBackOff
  - `kubectl logs checkout-7b9c -n checkout --previous` — OOMKilled
  - `kubectl top pod -n checkout` — memory at limit
- **Root cause:** a config change lowered the checkout container memory
  limit from 512Mi to 256Mi; steady-state usage is ~300Mi.
- **Fix:** reverted the limit to 512Mi via `kubectl set resources`.
- **Resolved:** 14:13 UTC.
MD
test -f /workspace/incident-facts.md || test -f "$HOME/incident-facts.md"
echo "incident-facts.md present at $DEST"
