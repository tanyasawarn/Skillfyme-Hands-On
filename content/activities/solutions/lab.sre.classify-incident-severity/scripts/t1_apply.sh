#!/bin/bash
# Reference solution for lab.sre.classify-incident-severity (t1_apply.sh).
#
# BLOCKED (local, Rs.0 environment): SHELL_ASSERT runs kubectl in-pod vs broken-app; learner RBAC + ExecShell exit-marker fragility block a reliable local golden-path
#
# This labs golden path cannot be produced or verified in the local
# docker-compose stack. Expected to pass on a CI runner whose workspace
# image + cluster provide the missing dependency (see
# evaluation/phase1/results/content-completion-matrix.md). This stub fails
# loudly rather than fabricating a passing end state.
echo "BLOCKED: SHELL_ASSERT runs kubectl in-pod vs broken-app; learner RBAC + ExecShell exit-marker fragility block a reliable local golden-path" >&2
exit 1
