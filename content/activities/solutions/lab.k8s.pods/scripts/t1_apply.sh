#!/bin/bash
# Reference solution for lab.k8s.pods task t1. Writes ~/pod.yaml: Pod
# writer-reader, two containers sharing an emptyDir at /data. Manifest is
# PodSecurity-"restricted" compliant (the env namespace enforces it).
# Validators: ~/pod.yaml exists; valid YAML; contains "emptyDir" + "/data".
set -uo pipefail
cat > ~/pod.yaml <<'POD'
apiVersion: v1
kind: Pod
metadata:
  name: writer-reader
spec:
  restartPolicy: Never
  securityContext: {runAsNonRoot: true, runAsUser: 1000, seccompProfile: {type: RuntimeDefault}}
  volumes:
    - name: shared
      emptyDir: {}
  containers:
    - name: writer
      image: registry:5000/practiceengine/linux-tools:v1
      command: ["sh","-c","echo hi > /data/msg.txt && sleep 3600"]
      securityContext: {allowPrivilegeEscalation: false, capabilities: {drop: ["ALL"]}}
      volumeMounts: [{name: shared, mountPath: /data}]
    - name: reader
      image: registry:5000/practiceengine/linux-tools:v1
      command: ["sh","-c","until [ -f /data/msg.txt ]; do sleep 1; done; cat /data/msg.txt; sleep 3600"]
      securityContext: {allowPrivilegeEscalation: false, capabilities: {drop: ["ALL"]}}
      volumeMounts: [{name: shared, mountPath: /data}]
POD
echo "pod.yaml written"
