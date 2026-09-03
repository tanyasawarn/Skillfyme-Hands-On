#!/bin/bash
# Reference solution for lab.k8s.deploy-node-app (t2_apply.sh).
#
# BLOCKED (local, Rs.0 environment): requires docker in the workspace pod; linux-tools:v1 has no docker and the pod has no egress
#
# This labs golden path cannot be produced or verified in the local
# docker-compose stack. Expected to pass on a CI runner whose workspace
# image + cluster provide the missing dependency (see
# evaluation/phase1/results/content-completion-matrix.md). This stub fails
# loudly rather than fabricating a passing end state.
echo "BLOCKED: requires docker in the workspace pod; linux-tools:v1 has no docker and the pod has no egress" >&2
exit 1
