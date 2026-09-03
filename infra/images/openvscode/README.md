# infra/images/openvscode — Phase 3 3.1

The OpenVSCode Server container for the T3 workspace pod. Editor +
`aws` / `tofu` (aliased `terraform`) / `kubectl` / `helm` + Terraform &
Python language servers, ~500 MB RAM budget.

## Build

```
docker build -t <registry>/practice/openvscode:v1 infra/images/openvscode
docker push <registry>/practice/openvscode:v1
```

Then set `T3_EDITOR_IMAGE=<registry>/practice/openvscode:v1` on the
orchestrator (consumed by `internal/k8s` when it builds the
`TierT3CloudAccount` pod shape — 3.2).

## Isolation

- Binds to `127.0.0.1:3000` only. There is **no routable address** into
  the pod (memory.md line 1040); the platform WS proxy is the sole
  ingress, terminated platform-side and proxied inward over the
  control-plane channel.
- Reads AWS creds from `/var/run/secrets/aws/{credentials,config}` — the
  `emptyDir` the STS broker sidecar (Stage 2.1) refreshes. The editor
  container never holds a long-lived key.

## Status

Dockerfile authored; **not built/pushed** (needs a registry + a
multi-arch build). The 3.2 T3 driver references it by
`T3_EDITOR_IMAGE`; until the image exists + the driver lands, the
milestone state machine runs against `FakeProjectOrchestrator`.
