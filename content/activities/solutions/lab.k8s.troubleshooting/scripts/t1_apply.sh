#!/bin/bash
# Reference solution for lab.k8s.troubleshooting task t1. fx.pod-crashloop.v1
# (handler exists) seeds a CrashLoopBackOff pod `broken-app`. Fix = replace
# with a corrected spec that runs stably.
# Validators (K8S_ASSERT): pod/broken-app .status.phase==Running;
#   .status.containerStatuses[0].restartCount < 5.
set -uo pipefail
IMG=$(kubectl get pod broken-app -o jsonpath='{.spec.containers[0].image}' 2>/dev/null)
[ -n "$IMG" ] || IMG="registry:5000/practiceengine/linux-tools:v1"
kubectl delete pod broken-app --ignore-not-found --wait=true --request-timeout=15s
kubectl apply --request-timeout=10s -f - <<POD
apiVersion: v1
kind: Pod
metadata: {name: broken-app}
spec:
  restartPolicy: Always
  securityContext: {runAsNonRoot: true, runAsUser: 1000, seccompProfile: {type: RuntimeDefault}}
  containers:
    - name: main
      image: ${IMG}
      command: ["sh","-c","echo started ok; while true; do sleep 30; done"]
      securityContext: {allowPrivilegeEscalation: false, capabilities: {drop: ["ALL"]}}
POD
for i in $(seq 1 40); do
  [ "$(kubectl get pod broken-app -o jsonpath='{.status.phase}' 2>/dev/null)" = "Running" ] && { echo running; exit 0; }
  sleep 1
done
echo "broken-app not Running in time" >&2; exit 1
