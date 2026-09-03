#!/bin/bash
# Reference solution for lab.k8s.storage task t2. Pod storage-demo mounts
# data-pvc at /data; write marker; delete+recreate pod; confirm survival.
# Validator (SHELL_ASSERT): `kubectl exec storage-demo -- test -f /data/marker.txt`.
#
# storage-demo carries the full PodSecurity-"restricted" securityContext
# (see t1_apply.sh's note) -- without it the namespace admission webhook
# rejects the pod and wait_ready never succeeds.
set -uo pipefail
kubectl get pvc data-pvc >/dev/null 2>&1 || kubectl apply --request-timeout=10s -f - <<'PVC'
apiVersion: v1
kind: PersistentVolumeClaim
metadata: {name: data-pvc}
spec: {accessModes: [ReadWriteOnce], resources: {requests: {storage: 1Gi}}}
PVC
mkpod() { kubectl apply --request-timeout=10s -f - <<'POD'
apiVersion: v1
kind: Pod
metadata: {name: storage-demo}
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
}
wait_ready() { for i in $(seq 1 40); do [ "$(kubectl get pod storage-demo -o jsonpath='{.status.phase}' 2>/dev/null)" = "Running" ] && return 0; sleep 1; done; return 1; }
kubectl delete pod storage-demo --ignore-not-found --wait=true --request-timeout=20s
mkpod; wait_ready || { echo "pod1 not ready" >&2; kubectl describe pod storage-demo | tail -20 >&2; exit 1; }
kubectl exec storage-demo -- sh -c 'echo survived > /data/marker.txt' || { echo "write failed" >&2; exit 1; }
kubectl delete pod storage-demo --wait=true --request-timeout=20s
mkpod; wait_ready || { echo "pod2 not ready" >&2; exit 1; }
kubectl exec storage-demo -- test -f /data/marker.txt && echo "marker survived"
