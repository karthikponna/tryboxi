# Base guest rootfs for sandboxd: Ubuntu 24.04 + common dev tooling + Docker.
# The sandbox-agent binary is injected at boot from an initramfs (PID 1).
FROM ubuntu:24.04

ARG DEBIAN_FRONTEND=noninteractive
ARG NODE_MAJOR=22

RUN apt-get update && apt-get install -y --no-install-recommends \
        ca-certificates curl wget git sudo build-essential pkg-config \
        python3 python3-pip python3-venv python-is-python3 \
        iproute2 iputils-ping dnsutils procps psmisc less vim-tiny \
        e2fsprogs unzip xz-utils zstd jq file openssh-client tzdata locales \
        docker.io docker-compose-v2 iptables \
    && curl -fsSL https://deb.nodesource.com/setup_${NODE_MAJOR}.x | bash - \
    && apt-get install -y --no-install-recommends nodejs \
    && npm install -g corepack && corepack enable \
    && apt-get clean && rm -rf /var/lib/apt/lists/* /root/.npm /tmp/*

# The guest kernel has legacy iptables (not nf_tables), which Docker needs.
RUN update-alternatives --set iptables /usr/sbin/iptables-legacy \
    && update-alternatives --set ip6tables /usr/sbin/ip6tables-legacy

# There is no systemd (sandbox-agent is PID 1), so dockerd starts on first
# use of `docker` instead of at boot: idle sandboxes don't pay for it.
COPY start-docker /usr/local/sbin/start-docker
COPY docker-wrapper /usr/local/bin/docker
RUN chmod 0755 /usr/local/sbin/start-docker /usr/local/bin/docker

# The ubuntu image ships a uid 1000 "ubuntu" user; replace it with "agent".
RUN (userdel -r ubuntu 2>/dev/null || true) \
    && useradd -m -u 1000 -U -s /bin/bash -G docker agent \
    && echo 'agent ALL=(ALL) NOPASSWD:ALL' > /etc/sudoers.d/agent \
    && chmod 0440 /etc/sudoers.d/agent \
    && mkdir -p /workspace /var/lib/sandbox-agent/spool \
    && chown agent:agent /workspace \
    && echo 'sandbox' > /etc/hostname \
    && locale-gen en_US.UTF-8 \
    && printf 'export LANG=en_US.UTF-8\nexport PATH=$HOME/.local/bin:$PATH\n' > /etc/profile.d/sandbox.sh
