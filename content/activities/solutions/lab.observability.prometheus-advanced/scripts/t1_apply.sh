#!/bin/bash
# Reference solution for lab.observability.prometheus-advanced (t1_apply.sh).
#
# BLOCKED (local, Rs.0 environment): fx.prometheus-installed.v1 has no handler; validator curls a running Prometheus
#
# This labs golden path cannot be produced or verified in the local
# docker-compose stack. Expected to pass on a CI runner whose workspace
# image + cluster provide the missing dependency (see
# evaluation/phase1/results/content-completion-matrix.md). This stub fails
# loudly rather than fabricating a passing end state.
echo "BLOCKED: fx.prometheus-installed.v1 has no handler; validator curls a running Prometheus" >&2
exit 1
