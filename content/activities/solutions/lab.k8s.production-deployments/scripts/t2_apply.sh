#!/bin/bash
# Reference solution for lab.k8s.production-deployments task t2. Trigger a
# rollout (env change) on web, then `rollout undo`. Bounded polling.
# Validators (SHELL_ASSERT): `kubectl rollout history deployment/web` has
#   REVISION lines; `kubectl rollout status deployment/web --timeout=30s` exit 0.
set -uo pipefail
kubectl get deployment web >/dev/null 2>&1 || kubectl apply --request-timeout=10s -f - <<'DEP'
apiVersion: apps/v1
kind: Deployment
metadata: {name: web, labels: {app: web}}
spec:
  replicas: 2
  selector: {matchLabels: {app: web}}
  template:
    metadata: {labels: {app: web}}
    spec:
      containers:
        - {name: web, image: registry:5000/practiceengine/linux-tools:v1, command: ["sh","-c","sleep 3600"]}
DEP
kubectl set env deployment/web ROLLOUT=v2 --overwrite --request-timeout=10s
kubectl rollout undo deployment/web --request-timeout=10s
for i in $(seq 1 25); do
  kubectl rollout status deployment/web --timeout=3s >/dev/null 2>&1 && { kubectl rollout history deployment/web | head -4; exit 0; }
  sleep 1
done
echo "web rollout did not stabilise in time" >&2; exit 1
