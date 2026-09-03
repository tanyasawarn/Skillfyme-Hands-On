#!/bin/bash
# Reference solution for lab.cloud.api-container-security (t1_apply.sh).
#
# BLOCKED (local, Rs.0 environment): fx.pod-privileged-insecure.v1 has no handler; K8S_ASSERT needs the seeded insecure pod
#
# This labs golden path cannot be produced or verified in the local
# docker-compose stack. Expected to pass on a CI runner whose workspace
# image + cluster provide the missing dependency (see
# evaluation/phase1/results/content-completion-matrix.md). This stub fails
# loudly rather than fabricating a passing end state.
echo "BLOCKED: fx.pod-privileged-insecure.v1 has no handler; K8S_ASSERT needs the seeded insecure pod" >&2
exit 1
