#!/bin/bash
# Reference solution for lab.k8s.storage task t1. PVC data-pvc (1Gi RWO) +
# a consumer pod so k3s local-path (WaitForFirstConsumer) binds it.
# Validator (K8S_ASSERT): pvc/data-pvc .status.phase == Bound.
#
# The consumer pod carries the full PodSecurity-"restricted" securityContext
# (pod + container): the env namespace enforces `restricted:latest`, so a
# pod without runAsNonRoot / seccompProfile / drop-ALL / no-privilege-esc
# is rejected outright and the PVC never gets its first consumer.
set -uo pipefail
kubectl apply --request-timeout=10s -f - <<'PVC'
apiVersion: v1
kind: PersistentVolumeClaim
metadata: {name: data-pvc}
spec: {accessModes: [ReadWriteOnce], resources: {requests: {storage: 1Gi}}}
PVC
kubectl apply --request-timeout=10s -f - <<'POD'
apiVersion: v1
kind: Pod
metadata: {name: pvc-binder}
spec:
  restartPolicy: Never
  securityContext: {runAsNonRoot: true, runAsUser: 1000, seccompProfile: {type: RuntimeDefault}}
  containers:
    - name: main
      image: registry:5000/practiceengine/linux-tools:v1
      command: ["sh","-c","sleep 3600"]
      securityContext: {allowPrivilegeEscalation: false, capabilities: {drop: ["ALL"]}}
      volumeMounts: [{name: d, mountPath: /data}]
  volumes: [{name: d, persistentVolumeClaim: {claimName: data-pvc}}]
POD
for i in $(seq 1 40); do
  [ "$(kubectl get pvc data-pvc -o jsonpath='{.status.phase}' 2>/dev/null)" = "Bound" ] && { echo bound; exit 0; }
  sleep 1
done
echo "data-pvc not Bound in time" >&2; kubectl describe pvc data-pvc | tail -15 >&2; exit 1
