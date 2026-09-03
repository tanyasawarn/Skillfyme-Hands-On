#!/bin/bash
# Reference solution for lab.tekton.pipelines (t1_apply.sh).
#
# BLOCKED (local, Rs.0 environment): fx.tekton-installed.v1 has no handler; K8S_ASSERT targets tekton.dev Task/TaskRun CRDs, unsupported
#
# This labs golden path cannot be produced or verified in the local
# docker-compose stack. Expected to pass on a CI runner whose workspace
# image + cluster provide the missing dependency (see
# evaluation/phase1/results/content-completion-matrix.md). This stub fails
# loudly rather than fabricating a passing end state.
echo "BLOCKED: fx.tekton-installed.v1 has no handler; K8S_ASSERT targets tekton.dev Task/TaskRun CRDs, unsupported" >&2
exit 1
