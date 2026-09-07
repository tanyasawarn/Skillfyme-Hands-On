# practiceengine/t3-tools -- the T3 (TIER_T3_CLOUD_ACCOUNT) workspace-pod
# image (PLAN.md Phase 3 / PLAN_PHASE3_PROJECTS.md 3.1).
#
# In production this slot is the OpenVSCode Server image
# (infra/images/openvscode). For the LOCAL-REAL T3 mode
# (CLOUD_ACCOUNTS_MODE=fake) the workspace pod actually runs
# `terraform` + `aws` + `kubectl` against a MinIO-backed state backend
# and a fake AWS control plane, so it needs those CLIs on PATH -- an
# OpenVSCode image without a toolchain would make `terraform apply` in
# the pod fail. This image = linux-tools + terraform + aws-cli-v2.
#
# Build + push to the compose registry:
#   docker build -t localhost:5001/practiceengine/t3-tools:v1 \
#     -f orchestrator/images/t3-tools/Dockerfile orchestrator/images/t3-tools
#   docker push localhost:5001/practiceengine/t3-tools:v1
# (k3s pulls it as registry:5000/practiceengine/t3-tools:v1 -- the
#  T3_EDITOR_IMAGE default in internal/config.)

FROM ubuntu:24.04

ARG KUBECTL_VERSION=v1.31.0
ARG TERRAFORM_VERSION=1.9.8
ARG TARGETARCH=arm64

RUN apt-get update && apt-get install -y --no-install-recommends \
    git \
    curl \
    ca-certificates \
    jq \
    vim \
    less \
    procps \
    net-tools \
    iputils-ping \
    unzip \
    python3 \
    && rm -rf /var/lib/apt/lists/*

# kubectl (same as linux-tools).
RUN curl -fsSL "https://dl.k8s.io/release/${KUBECTL_VERSION}/bin/linux/${TARGETARCH}/kubectl" -o /usr/local/bin/kubectl \
    && chmod +x /usr/local/bin/kubectl

# terraform (upstream zip; no apt package). The local-real T3 flow runs
# `terraform init -reconfigure` + `apply` against a MinIO S3 backend.
RUN case "${TARGETARCH}" in \
      arm64) TF_ARCH=arm64 ;; \
      amd64) TF_ARCH=amd64 ;; \
      *) TF_ARCH="${TARGETARCH}" ;; \
    esac && \
    curl -fsSL "https://releases.hashicorp.com/terraform/${TERRAFORM_VERSION}/terraform_${TERRAFORM_VERSION}_linux_${TF_ARCH}.zip" -o /tmp/tf.zip && \
    unzip /tmp/tf.zip -d /usr/local/bin && \
    rm /tmp/tf.zip && \
    chmod +x /usr/local/bin/terraform

# aws-cli v2 (needed for `aws resource-explorer-2 search` in the snapshot
# cloud-inventory step; in fake mode that command is allowed to fail and
# fall back to an empty inventory, but the binary must exist so the
# fallback path -- `|| echo '{"Resources":[]}'` -- is what runs, not a
# "command not found").
RUN case "${TARGETARCH}" in \
      arm64) AWS_ARCH=aarch64 ;; \
      amd64) AWS_ARCH=x86_64 ;; \
      *) AWS_ARCH="${TARGETARCH}" ;; \
    esac && \
    curl -fsSL "https://awscli.amazonaws.com/awscli-exe-linux-${AWS_ARCH}.zip" -o /tmp/awscliv2.zip && \
    unzip -q /tmp/awscliv2.zip -d /tmp && \
    /tmp/aws/install && \
    rm -rf /tmp/aws /tmp/awscliv2.zip

# UID 1000 matches k8s/provision.go's RunAsUser under PodSecurity
# `restricted` (same as linux-tools).
RUN mkdir -p /home/ubuntu /workspace && chown -R 1000:1000 /home/ubuntu /workspace

USER 1000
WORKDIR /workspace
