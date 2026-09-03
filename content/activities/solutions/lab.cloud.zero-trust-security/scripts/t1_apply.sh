#!/bin/bash
# Reference solution for lab.cloud.zero-trust-security (t1_apply.sh).
#
# BLOCKED (local, Rs.0 environment): fx.frontend-backend-pods.v1 has no handler; NetworkPolicy K8S_ASSERT needs the seeded pods
#
# This labs golden path cannot be produced or verified in the local
# docker-compose stack. Expected to pass on a CI runner whose workspace
# image + cluster provide the missing dependency (see
# evaluation/phase1/results/content-completion-matrix.md). This stub fails
# loudly rather than fabricating a passing end state.
echo "BLOCKED: fx.frontend-backend-pods.v1 has no handler; NetworkPolicy K8S_ASSERT needs the seeded pods" >&2
exit 1
