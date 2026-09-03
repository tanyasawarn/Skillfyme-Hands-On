#!/bin/bash
# Reference solution for lab.k8s.config-secrets task t1. ConfigMap
# app-config (LOG_LEVEL=info) + Secret app-secret (API_KEY=demo-key-123).
# Validators (K8S_ASSERT): cm .data.LOG_LEVEL==info; secret API_KEY b64-d==demo-key-123.
set -uo pipefail
kubectl delete configmap app-config --ignore-not-found --request-timeout=10s
kubectl delete secret app-secret --ignore-not-found --request-timeout=10s
kubectl create configmap app-config --from-literal=LOG_LEVEL=info --request-timeout=10s
kubectl create secret generic app-secret --from-literal=API_KEY=demo-key-123 --request-timeout=10s
echo "config+secret created"
