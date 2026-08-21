# practiceengine/linux-tools -- real bp.linux.v1/bp.docker.v1/
# bp.k8s-single-node.v1 base image (doc §5.2 image strategy, M1.11),
# replacing the bare ubuntu:24.04 placeholder every workspace pod used
# until now. Confirmed live multiple times this session that bare
# ubuntu:24.04 has no git, no kubectl -- this fixes exactly that gap for
# the tools authored lab content actually depends on
# (content/activities/*.yaml's `run:` fields).
FROM ubuntu:24.04

ARG KUBECTL_VERSION=v1.31.0
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
    && rm -rf /var/lib/apt/lists/*

# kubectl: no apt package for it in Ubuntu's default repos, install the
# real upstream binary directly (matches how content/activities' K8s
# labs expect `kubectl` to just work, and how execFileContent's
# validator content already assumes the toolchain includes it).
RUN curl -fsSL "https://dl.k8s.io/release/${KUBECTL_VERSION}/bin/linux/${TARGETARCH}/kubectl" -o /usr/local/bin/kubectl \
    && chmod +x /usr/local/bin/kubectl

# UID 1000 matches k8s/provision.go's hardcoded RunAsUser (PodSecurity
# `restricted` enforcement, confirmed live this session) -- ubuntu:24.04
# already ships a uid-1000 `ubuntu` user with /home/ubuntu, so this just
# makes it explicit rather than relying on the base image default.
RUN mkdir -p /home/ubuntu && chown -R 1000:1000 /home/ubuntu

USER 1000
WORKDIR /home/ubuntu
