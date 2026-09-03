#!/bin/bash
# Reference solution for lab.terraform.state-management (t2_apply.sh).
#
# BLOCKED-TOOL (local, Rs.0 environment): validator runs 'terraform state list' + 'terraform plan'; terraform is not in linux-tools:v1
#
# Expected to pass on a CI runner whose workspace image ships the tool.
# See evaluation/phase1/results/content-completion-matrix.md.
echo "BLOCKED-TOOL: validator runs 'terraform state list' + 'terraform plan'; terraform is not in linux-tools:v1" >&2
exit 1
