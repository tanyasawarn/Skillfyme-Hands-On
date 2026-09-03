#!/bin/bash
# lab.jenkins.basics t1: ~/job/build.sh runs ordered steps against ~/app:
# build (create ~/app/build.out), then test (fail if build.out missing).
# Validators: build.sh is -x; running it exits 0; ~/app/build.out exists.
set -uo pipefail
mkdir -p ~/job ~/app
cat > ~/job/build.sh <<'BS'
#!/bin/bash
set -euo pipefail
APP="$HOME/app"
# Step 1: build
echo "building"
touch "$APP/build.out"
# Step 2: test (depends on step 1's artifact)
echo "testing"
test -f "$APP/build.out" || { echo "build artifact missing"; exit 1; }
echo "ok"
BS
chmod +x ~/job/build.sh
~/job/build.sh
