#!/bin/bash
# Reference solution for lab.observability.nagios-alerting task t1.
# A Nagios-convention disk-usage check plugin for /: exit 0/1/2 for
# OK/WARNING/CRITICAL, one-line stdout message. Idempotent: rewrites the
# script and makes it executable every run.
# Validators: ~/check_disk.sh is -x; runs with exit <= 3; prints output.
set -euo pipefail
cat > ~/check_disk.sh <<'PLUGIN'
#!/bin/bash
# Nagios-style check: disk usage of / -> OK(0) / WARNING(1) / CRITICAL(2).
USAGE=$(df -P / | awk 'NR==2 {gsub("%","",$5); print $5}')
if [ -z "$USAGE" ]; then
  echo "UNKNOWN - could not determine / usage"
  exit 3
fi
if [ "$USAGE" -ge 90 ]; then
  echo "CRITICAL - / usage ${USAGE}% (>= 90%)"
  exit 2
elif [ "$USAGE" -ge 70 ]; then
  echo "WARNING - / usage ${USAGE}% (70-89%)"
  exit 1
else
  echo "OK - / usage ${USAGE}% (< 70%)"
  exit 0
fi
PLUGIN
chmod +x ~/check_disk.sh
