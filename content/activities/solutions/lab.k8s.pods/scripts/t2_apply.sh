#!/bin/bash
# Reference solution for lab.k8s.pods task t2. Apply ~/pod.yaml (PSS-
# restricted compliant), bounded wait (<25s) for Running.
# Validator (K8S_ASSERT): pod/writer-reader .status.phase == Running.
set -uo pipefail
[ -f ~/pod.yaml ] || cat > ~/pod.yaml <<'POD'
apiVersion: v1
kind: Pod
metadata: {name: writer-reader}
spec:
  restartPolicy: Never
  securityContext: {runAsNonRoot: true, runAsUser: 1000, seccompProfile: {type: RuntimeDefault}}
  volumes: [{name: shared, emptyDir: {}}]
  containers:
    - {name: writer, image: registry:5000/practiceengine/linux-tools:v1, command: ["sh","-c","echo hi > /data/msg.txt && sleep 3600"], securityContext: {allowPrivilegeEscalation: false, capabilities: {drop: ["ALL"]}}, volumeMounts: [{name: shared, mountPath: /data}]}
    - {name: reader, image: registry:5000/practiceengine/linux-tools:v1, command: ["sh","-c","sleep 3600"], securityContext: {allowPrivilegeEscalation: false, capabilities: {drop: ["ALL"]}}, volumeMounts: [{name: shared, mountPath: /data}]}
POD
kubectl apply -f ~/pod.yaml --request-timeout=10s || { echo "apply failed" >&2; exit 1; }
# 60s, not 24s: the first schedule of this pod on a fresh node pulls the
# workspace image, which can exceed a 24s window on a cold containerd
# cache (seen live in content-CI). The lab's own estimated_minutes gives
# the learner far more than this; the bound is only here so a genuinely
# stuck pod fails the script instead of hanging it.
for i in $(seq 1 60); do
  [ "$(kubectl get pod writer-reader -o jsonpath='{.status.phase}' 2>/dev/null)" = "Running" ] && { echo running; exit 0; }
  sleep 1
done
echo "writer-reader not Running in time" >&2; kubectl get pod writer-reader -o wide >&2; kubectl describe pod writer-reader | tail -20 >&2 || true; exit 1
