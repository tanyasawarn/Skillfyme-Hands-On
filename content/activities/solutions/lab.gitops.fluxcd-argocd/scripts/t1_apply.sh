#!/bin/bash
# Reference solution for lab.gitops.fluxcd-argocd (t1_apply.sh).
#
# BLOCKED (local, Rs.0 environment): fx.argocd-installed.v1 + fx.gitops-manifest-repo.v1 have no handlers; K8S_ASSERT targets argoproj.io Application CRD, unsupported
#
# This labs golden path cannot be produced or verified in the local
# docker-compose stack. Expected to pass on a CI runner whose workspace
# image + cluster provide the missing dependency (see
# evaluation/phase1/results/content-completion-matrix.md). This stub fails
# loudly rather than fabricating a passing end state.
echo "BLOCKED: fx.argocd-installed.v1 + fx.gitops-manifest-repo.v1 have no handlers; K8S_ASSERT targets argoproj.io Application CRD, unsupported" >&2
exit 1
