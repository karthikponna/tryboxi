# Base guest rootfs for sandboxd: Ubuntu 24.04 + common dev tooling.
# The sandbox-agent binary is injected later as /sbin/sandbox-agent (PID 1).
FROM ubuntu:24.04

ARG DEBIAN_FRONTEND=noninteractive
ARG NODE_MAJOR=22

RUN apt-get update && apt-get install -y --no-install-recommends \
        ca-certificates curl wget git sudo build-essential pkg-config \
        python3 python3-pip python3-venv python-is-python3 \
        iproute2 iputils-ping dnsutils procps psmisc less vim-tiny \
        e2fsprogs unzip xz-utils zstd jq file openssh-client tzdata locales \
    && curl -fsSL https://deb.nodesource.com/setup_${NODE_MAJOR}.x | bash - \
    && apt-get install -y --no-install-recommends nodejs \
    && npm install -g corepack && corepack enable \
    && apt-get clean && rm -rf /var/lib/apt/lists/* /root/.npm /tmp/*

# The ubuntu image ships a uid 1000 "ubuntu" user; replace it with "agent".
RUN (userdel -r ubuntu 2>/dev/null || true) \
    && useradd -m -u 1000 -U -s /bin/bash agent \
    && echo 'agent ALL=(ALL) NOPASSWD:ALL' > /etc/sudoers.d/agent \
    && chmod 0440 /etc/sudoers.d/agent \
    && mkdir -p /workspace /var/lib/sandbox-agent/spool \
    && chown agent:agent /workspace \
    && echo 'sandbox' > /etc/hostname \
    && locale-gen en_US.UTF-8 \
    && printf 'export LANG=en_US.UTF-8\nexport PATH=$HOME/.local/bin:$PATH\n' > /etc/profile.d/sandbox.sh
