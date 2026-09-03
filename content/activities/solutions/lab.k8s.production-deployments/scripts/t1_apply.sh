#!/bin/bash
# Reference solution for lab.k8s.production-deployments task t1. Create
# Deployment web (fx.deployment-web.v1 has no handler), patch strategy to
# rollingUpdate maxUnavailable=0 maxSurge=1.
# Validator (K8S_ASSERT): deployment/web .spec.strategy.rollingUpdate.maxUnavailable == "0".
set -uo pipefail
kubectl apply --request-timeout=10s -f - <<'DEP'
apiVersion: apps/v1
kind: Deployment
metadata: {name: web, labels: {app: web}}
spec:
  replicas: 2
  selector: {matchLabels: {app: web}}
  strategy: {type: RollingUpdate, rollingUpdate: {maxUnavailable: 0, maxSurge: 1}}
  template:
    metadata: {labels: {app: web}}
    spec:
      containers:
        - {name: web, image: registry:5000/practiceengine/linux-tools:v1, command: ["sh","-c","sleep 3600"]}
DEP
v=$(kubectl get deployment web -o jsonpath='{.spec.strategy.rollingUpdate.maxUnavailable}')
[ "$v" = "0" ] && echo "strategy set" || { echo "maxUnavailable=$v" >&2; exit 1; }
