#!/bin/bash
# Reference solution for lab.k8s.statefulsets task t1. Headless Service
# db-svc + StatefulSet db (3 replicas, serviceName db-svc). Bounded wait
# for db-0 to exist and replicas==3, then for all 3 pods to be Running
# (v.pods-have-ordinal-names checks db-0..db-2).
#
# The pod template carries the full PodSecurity-"restricted"
# securityContext (pod + container): the env namespace enforces
# `restricted:latest`, so a template without it makes every StatefulSet
# pod fail admission and no ordinal pod is ever created.
set -uo pipefail
kubectl apply --request-timeout=10s -f - <<'STS'
apiVersion: v1
kind: Service
metadata: {name: db-svc}
spec: {clusterIP: None, selector: {app: db}, ports: [{port: 80, name: web}]}
---
apiVersion: apps/v1
kind: StatefulSet
metadata: {name: db}
spec:
  serviceName: db-svc
  replicas: 3
  selector: {matchLabels: {app: db}}
  template:
    metadata: {labels: {app: db}}
    spec:
      securityContext: {runAsNonRoot: true, runAsUser: 1000, seccompProfile: {type: RuntimeDefault}}
      containers:
        - name: main
          image: registry:5000/practiceengine/linux-tools:v1
          command: ["sh","-c","sleep 3600"]
          securityContext: {allowPrivilegeEscalation: false, capabilities: {drop: ["ALL"]}}
STS
for i in $(seq 1 60); do
  ready=$(kubectl get statefulset db -o jsonpath='{.status.readyReplicas}' 2>/dev/null)
  [ "$ready" = "3" ] && { echo sts-ok; exit 0; }
  sleep 1
done
echo "statefulset db not fully ready in time" >&2; kubectl get pods -l app=db -o wide >&2 || true; exit 1
