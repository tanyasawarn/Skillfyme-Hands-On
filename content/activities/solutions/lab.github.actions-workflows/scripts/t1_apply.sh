#!/bin/bash
# lab.github.actions-workflows t1: ~/repo/.github/workflows/ci.yml, on:
# push, job with strategy.matrix.node-version [16,18,20]. Bootstraps
# ~/repo (fx.git-repo-empty.v1 has no handler).
set -uo pipefail
mkdir -p ~/repo/.github/workflows && cd ~/repo
git rev-parse --is-inside-work-tree >/dev/null 2>&1 || git init -q
cat > .github/workflows/ci.yml <<'YML'
name: ci
on: push
jobs:
  test:
    runs-on: ubuntu-latest
    strategy:
      matrix:
        node-version: [16, 18, 20]
    steps:
      - uses: actions/checkout@v4
      - uses: actions/setup-node@v4
        with:
          node-version: ${{ matrix.node-version }}
      - run: npm test
YML
echo "ci.yml written"
