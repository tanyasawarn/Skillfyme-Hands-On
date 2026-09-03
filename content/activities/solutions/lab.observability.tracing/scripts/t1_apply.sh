#!/bin/bash
# Reference solution for lab.observability.tracing (t1_apply.sh).
#
# BLOCKED (local, Rs.0 environment): fx.jaeger-installed.v1 + fx.trace-emitter-script.v1 have no handlers; needs a running Jaeger
#
# This labs golden path cannot be produced or verified in the local
# docker-compose stack. Expected to pass on a CI runner whose workspace
# image + cluster provide the missing dependency (see
# evaluation/phase1/results/content-completion-matrix.md). This stub fails
# loudly rather than fabricating a passing end state.
echo "BLOCKED: fx.jaeger-installed.v1 + fx.trace-emitter-script.v1 have no handlers; needs a running Jaeger" >&2
exit 1
