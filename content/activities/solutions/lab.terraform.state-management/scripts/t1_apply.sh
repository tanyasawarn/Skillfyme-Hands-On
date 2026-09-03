#!/bin/bash
# Reference solution for lab.terraform.state-management (t1_apply.sh).
#
# BLOCKED-TOOL (local, Rs.0 environment): validator runs 'terraform state show'; terraform is not in linux-tools:v1 and the pod has no egress
#
# Expected to pass on a CI runner whose workspace image ships the tool.
# See evaluation/phase1/results/content-completion-matrix.md.
echo "BLOCKED-TOOL: validator runs 'terraform state show'; terraform is not in linux-tools:v1 and the pod has no egress" >&2
exit 1
