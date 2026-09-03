#!/bin/bash
# Reference solution for lab.k8s.config-secrets task t2. Pod config-demo
# injects LOG_LEVEL + API_KEY via envFrom.
# NOTE: this task's validators use `kubectl exec ... -- printenv` typed as
# K8S_ASSERT, which the orchestrator K8S_ASSERT executor does not support
# (parses only `kubectl get <kind> <name>`). End state here is correct;
# the executor gap is a content/executor bug recorded in the matrix.
set -uo pipefail
kubectl get configmap app-config >/dev/null 2>&1 || kubectl create configmap app-config --from-literal=LOG_LEVEL=info
kubectl get secret app-secret >/dev/null 2>&1 || kubectl create secret generic app-secret --from-literal=API_KEY=demo-key-123
kubectl delete pod config-demo --ignore-not-found --wait=true --request-timeout=15s
kubectl apply --request-timeout=10s -f - <<'POD'
apiVersion: v1
kind: Pod
metadata: {name: config-demo}
spec:
  restartPolicy: Never
  securityContext: {runAsNonRoot: true, runAsUser: 1000, seccompProfile: {type: RuntimeDefault}}
  containers:
    - name: main
      image: registry:5000/practiceengine/linux-tools:v1
      command: ["sh","-c","sleep 3600"]
      securityContext: {allowPrivilegeEscalation: false, capabilities: {drop: ["ALL"]}}
      envFrom: [{configMapRef: {name: app-config}}, {secretRef: {name: app-secret}}]
POD
for i in $(seq 1 40); do
  [ "$(kubectl get pod config-demo -o jsonpath='{.status.phase}' 2>/dev/null)" = "Running" ] && { echo running; exit 0; }
  sleep 1
done
echo "config-demo not Running in time" >&2; exit 1
