#!/bin/bash
# Reference solution for lab.k8s.services task t1. fx.deployment-web.v1
# has no handler, so create Deployment web (app=web, port 8080) first,
# then ClusterIP Service web-svc (selector app=web, 80->8080). Bounded
# waits within the ExecShell budget.
# Validators (K8S_ASSERT): svc .spec.type==ClusterIP; .spec.selector.app==web;
#   endpoints .subsets[0].addresses exists.
#
# The Deployment's pod template carries the full PodSecurity-"restricted"
# securityContext (pod + container): the env namespace enforces
# `restricted:latest`, so a template without it makes every ReplicaSet
# pod creation fail admission and the Service never gets endpoints.
set -uo pipefail
kubectl apply --request-timeout=10s -f - <<'DEP'
apiVersion: apps/v1
kind: Deployment
metadata: {name: web, labels: {app: web}}
spec:
  replicas: 1
  selector: {matchLabels: {app: web}}
  template:
    metadata: {labels: {app: web}}
    spec:
      securityContext: {runAsNonRoot: true, runAsUser: 1000, seccompProfile: {type: RuntimeDefault}}
      containers:
        - name: web
          image: registry:5000/practiceengine/linux-tools:v1
          command: ["sh","-c","sleep 3600"]
          ports: [{containerPort: 8080}]
          securityContext: {allowPrivilegeEscalation: false, capabilities: {drop: ["ALL"]}}
DEP
kubectl apply --request-timeout=10s -f - <<'SVC'
apiVersion: v1
kind: Service
metadata: {name: web-svc}
spec:
  type: ClusterIP
  selector: {app: web}
  ports: [{port: 80, targetPort: 8080}]
SVC
for i in $(seq 1 40); do
  [ -n "$(kubectl get endpoints web-svc -o jsonpath='{.subsets[0].addresses}' 2>/dev/null)" ] && { echo endpoints-ready; exit 0; }
  sleep 1
done
echo "web-svc endpoints not ready" >&2; kubectl get pods -l app=web -o wide >&2 || true; exit 1
