#!/usr/bin/env bash
# Run inside the Lima VM as root: installs Firecracker, a Firecracker guest
# kernel, and sandboxd as a systemd service with its data on the XFS disk.
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
ARCH="$(uname -m)"                      # aarch64 | x86_64
GOARCH=$([ "$ARCH" = aarch64 ] && echo arm64 || echo amd64)
FC_VERSION="${FC_VERSION:-v1.12.1}"
FC_KERNEL_URL="${FC_KERNEL_URL:-https://s3.amazonaws.com/spec.ccfc.min/firecracker-ci/v1.12/${ARCH}/vmlinux-6.1.128}"
ART=/var/lib/sandboxd-artifacts

[ "$(id -u)" = 0 ] || { echo "run as root (sudo)"; exit 1; }
test -e /dev/kvm || { echo "/dev/kvm missing: nested virtualization is not enabled"; exit 1; }

apt-get update -q
apt-get install -y -q nftables iproute2 xfsprogs curl ca-certificates

# Firecracker (sha256 verified against the release checksum file).
if ! command -v firecracker >/dev/null; then
  t=$(mktemp -d)
  base="https://github.com/firecracker-microvm/firecracker/releases/download/${FC_VERSION}"
  tgz="firecracker-${FC_VERSION}-${ARCH}.tgz"
  curl -fsSL -o "$t/$tgz" "$base/$tgz"
  curl -fsSL -o "$t/$tgz.sha256.txt" "$base/$tgz.sha256.txt"
  (cd "$t" && sha256sum -c "$tgz.sha256.txt")
  tar -xzf "$t/$tgz" -C "$t"
  install -m 0755 "$t/release-${FC_VERSION}-${ARCH}/firecracker-${FC_VERSION}-${ARCH}" /usr/local/bin/firecracker
  install -m 0755 "$t/release-${FC_VERSION}-${ARCH}/jailer-${FC_VERSION}-${ARCH}" /usr/local/bin/jailer
  rm -rf "$t"
fi
firecracker --version

# Data root on the XFS additional disk (reflink-capable).
DATA_DISK=/mnt/lima-sandboxd-data
mkdir -p "$DATA_DISK/sandboxd"
if [ ! -L /var/lib/sandboxd ]; then
  [ -d /var/lib/sandboxd ] && [ -z "$(ls -A /var/lib/sandboxd)" ] && rmdir /var/lib/sandboxd
  ln -s "$DATA_DISK/sandboxd" /var/lib/sandboxd
fi

# Artifacts: Firecracker CI kernel + the rootfs and agent built on the Mac.
mkdir -p "$ART/images" "$ART/bin"
if [ ! -f "$ART/images/vmlinux-$GOARCH" ]; then
  curl -fsSL -o "$ART/images/vmlinux-$GOARCH" "$FC_KERNEL_URL"
  sha256sum "$ART/images/vmlinux-$GOARCH" | tee "$ART/images/vmlinux-$GOARCH.sha256"
fi
ln -sf "$ROOT/_out/images/base-rootfs-$GOARCH.ext4" "$ART/images/base-rootfs-$GOARCH.ext4"
ln -sf "$ROOT/_out/images/base-rootfs-$GOARCH.ext4.sha256" "$ART/images/base-rootfs-$GOARCH.ext4.sha256"
ln -sf "$ROOT/_out/bin/sandbox-agent-linux-$GOARCH" "$ART/bin/sandbox-agent-linux-$GOARCH"

install -m 0755 "$ROOT/_out/bin/linux-$GOARCH/sandboxd" /usr/local/bin/sandboxd
install -m 0755 "$ROOT/_out/bin/linux-$GOARCH/sandboxctl" /usr/local/bin/sandboxctl
sandboxd install --artifacts "$ART"
sandboxd doctor || true
echo
echo "Token for clients on the Mac (socket forwarded to ~/.lima/sandboxd-dev/sock/sandboxd.sock):"
sandboxd token
