#!/bin/bash
# Reference solution for lab.devops.fundamentals task t1.
# Idempotent: safe to re-run, always leaves ~/toolchain.txt containing
# the real `git --version` output.
set -euo pipefail
git --version > ~/toolchain.txt
