#!/bin/bash
# lab.cicd.troubleshooting t1: the validator runs `~/repo/pipeline.sh` and
# expects exit 0. fx.broken-pipeline-script.v1 (which seeds the broken
# script) has no handler, so this writes the FIXED end-state pipeline.sh
# -- a build -> test -> deploy script that runs clean end-to-end. On a
# runner where the fixture seeds the broken version, the learner edits it;
# this reference is the corrected result.
set -uo pipefail
mkdir -p ~/repo && cd ~/repo
git rev-parse --is-inside-work-tree >/dev/null 2>&1 || git init -q
cat > pipeline.sh <<'PS'
#!/bin/bash
set -euo pipefail
echo "== build =="
mkdir -p build
echo "artifact" > build/app.bin
echo "== test =="
test -f build/app.bin || { echo "build artifact missing"; exit 1; }
echo "all tests passed"
echo "== deploy =="
mkdir -p deploy
cp build/app.bin deploy/
echo "deployed"
PS
chmod +x pipeline.sh
./pipeline.sh
