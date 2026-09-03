#!/bin/bash
# Reference solution for lab.cloud.twelve-factor (t1_apply.sh).
#
# BLOCKED-TOOL (local, Rs.0 environment): validator runs 'python3 -c'; linux-tools:v1 has no python3 and the pod has no egress to install it
#
# Expected to pass on a CI runner whose workspace image ships the tool.
# See evaluation/phase1/results/content-completion-matrix.md.
echo "BLOCKED-TOOL: validator runs 'python3 -c'; linux-tools:v1 has no python3 and the pod has no egress to install it" >&2
exit 1
