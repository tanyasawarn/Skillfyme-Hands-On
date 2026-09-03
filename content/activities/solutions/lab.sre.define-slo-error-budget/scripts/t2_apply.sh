#!/bin/bash
# Reference solution for lab.sre.define-slo-error-budget (t2_apply.sh).
#
# BLOCKED (local, Rs.0 environment): fx.prometheus-alertmanager-installed.v1 has no handler; both tasks curl localhost:9090 recording-rule APIs
#
# This labs golden path cannot be produced or verified in the local
# docker-compose stack. Expected to pass on a CI runner whose workspace
# image + cluster provide the missing dependency (see
# evaluation/phase1/results/content-completion-matrix.md). This stub fails
# loudly rather than fabricating a passing end state.
echo "BLOCKED: fx.prometheus-alertmanager-installed.v1 has no handler; both tasks curl localhost:9090 recording-rule APIs" >&2
exit 1
