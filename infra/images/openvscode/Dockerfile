# Phase 3 3.1 — OpenVSCode Server image for the T3 workspace pod.
# OpenVSCode Server + the cloud CLIs (aws, terraform/tofu, kubectl, helm)
# + a few language servers, on a ~500 MB RAM budget (§5.4, line 1043).
# Runs as the editor container next to the STS broker sidecar in the T3
# pod (orchestrator/internal/k8s TierT3CloudAccount pod shape, 3.2).
#
# WSS is terminated at the platform and proxied inward over the
# control-plane channel — this image exposes the editor on 127.0.0.1
# only; there is no routable address into the pod (line 1040).
FROM gitpod/openvscode-server:latest

USER root

ARG TARGETARCH=amd64
ENV AWS_CLI_VERSION=2 \
    TF_VERSION=1.9.8 \
    KUBECTL_VERSION=v1.31.0 \
    HELM_VERSION=v3.16.2

RUN apt-get update && apt-get install -y --no-install-recommends \
      curl unzip ca-certificates jq git openssh-client python3-minimal \
    && rm -rf /var/lib/apt/lists/*

# aws v2
RUN curl -sfL "https://awscli.amazonaws.com/awscli-exe-linux-$(uname -m).zip" -o /tmp/awscliv2.zip \
    && unzip -q /tmp/awscliv2.zip -d /tmp && /tmp/aws/install && rm -rf /tmp/aws /tmp/awscliv2.zip

# opentofu (drop-in terraform)
RUN curl -sfL "https://github.com/opentofu/opentofu/releases/download/v${TF_VERSION}/tofu_${TF_VERSION}_linux_${TARGETARCH}.zip" -o /tmp/tofu.zip \
    && unzip -q /tmp/tofu.zip -d /usr/local/bin tofu && chmod +x /usr/local/bin/tofu \
    && ln -sf /usr/local/bin/tofu /usr/local/bin/terraform && rm /tmp/tofu.zip

# kubectl
RUN curl -sfLo /usr/local/bin/kubectl "https://dl.k8s.io/release/${KUBECTL_VERSION}/bin/linux/${TARGETARCH}/kubectl" \
    && chmod +x /usr/local/bin/kubectl

# helm
RUN curl -sfL "https://get.helm.sh/helm-${HELM_VERSION}-linux-${TARGETARCH}.tar.gz" -o /tmp/helm.tgz \
    && tar -xzf /tmp/helm.tgz -C /tmp && mv /tmp/linux-${TARGETARCH}/helm /usr/local/bin/helm \
    && chmod +x /usr/local/bin/helm && rm -rf /tmp/helm.tgz /tmp/linux-${TARGETARCH}

# language servers the project blueprints commonly need
RUN /home/.openvscode-server/bin/openvscode-server --install-extension hashicorp.terraform \
    && /home/.openvscode-server/bin/openvscode-server --install-extension ms-python.python \
    || true

# The broker sidecar writes AWS creds here; point the CLI at it.
ENV AWS_SHARED_CREDENTIALS_FILE=/var/run/secrets/aws/credentials \
    AWS_CONFIG_FILE=/var/run/secrets/aws/config

USER openvscode-server
# bind to localhost only — the platform proxy is the sole ingress path
EXPOSE 3000
ENTRYPOINT ["/bin/sh", "-c", \
  "exec /home/.openvscode-server/bin/openvscode-server --host 127.0.0.1 --port 3000 --without-connection-token --default-folder /workspace"]
