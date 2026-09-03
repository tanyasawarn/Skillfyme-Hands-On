#!/bin/bash
# Reference solution for lab.sre.size-replicas-for-load (t1_apply.sh).
#
# BLOCKED (local, Rs.0 environment): K8S_ASSERT on horizontalpodautoscalers; fx.k3s-ready.v1 learner Role does not grant the autoscaling API group
#
# This labs golden path cannot be produced or verified in the local
# docker-compose stack. Expected to pass on a CI runner whose workspace
# image + cluster provide the missing dependency (see
# evaluation/phase1/results/content-completion-matrix.md). This stub fails
# loudly rather than fabricating a passing end state.
echo "BLOCKED: K8S_ASSERT on horizontalpodautoscalers; fx.k3s-ready.v1 learner Role does not grant the autoscaling API group" >&2
exit 1
