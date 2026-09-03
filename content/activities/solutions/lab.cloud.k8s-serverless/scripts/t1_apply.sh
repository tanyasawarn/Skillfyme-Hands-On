#!/bin/bash
# Reference solution for lab.cloud.k8s-serverless (t1_apply.sh).
#
# BLOCKED (local, Rs.0 environment): K8S_ASSERT targets a Knative Service CRD; executor supports only core/apps kinds and Knative is not installed
#
# This labs golden path cannot be produced or verified in the local
# docker-compose stack. Expected to pass on a CI runner whose workspace
# image + cluster provide the missing dependency (see
# evaluation/phase1/results/content-completion-matrix.md). This stub fails
# loudly rather than fabricating a passing end state.
echo "BLOCKED: K8S_ASSERT targets a Knative Service CRD; executor supports only core/apps kinds and Knative is not installed" >&2
exit 1
