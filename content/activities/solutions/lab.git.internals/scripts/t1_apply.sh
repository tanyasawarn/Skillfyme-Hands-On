#!/bin/bash
# Reference solution for lab.git.internals task t1 (inspect a commit's
# tree object). Idempotent: always rewrites ~/tree-hash.txt with the
# current HEAD tree hash. Validators: file exists; its content equals
# `git rev-parse HEAD^{tree}`.
set -euo pipefail
cd ~/repo
git rev-parse "HEAD^{tree}" > ~/tree-hash.txt
