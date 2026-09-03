#!/bin/bash
# Reference solution for lab.cloud.mtls-spiffe (t1_apply.sh).
#
# BLOCKED (local, Rs.0 environment): fx.istio-installed.v1 + fx.backend-pod.v1 have no handlers; needs a real Istio control plane
#
# This labs golden path cannot be produced or verified in the local
# docker-compose stack. Expected to pass on a CI runner whose workspace
# image + cluster provide the missing dependency (see
# evaluation/phase1/results/content-completion-matrix.md). This stub fails
# loudly rather than fabricating a passing end state.
echo "BLOCKED: fx.istio-installed.v1 + fx.backend-pod.v1 have no handlers; needs a real Istio control plane" >&2
exit 1
