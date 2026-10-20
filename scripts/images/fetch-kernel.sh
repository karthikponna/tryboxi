#!/usr/bin/env bash
# Downloads the Kata Containers arm64 guest kernel (the same build Apple's
# `container` tool uses) and verifies both the tarball and kernel by sha256.
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
DL="$ROOT/_out/dl"
OUT="$ROOT/_out/images"
KATA_VERSION="3.17.0"
TARBALL="kata-static-${KATA_VERSION}-arm64.tar.xz"
TARBALL_SHA256="647c7612e6edf789d5e14698c48c99d8bac15ad139ffaa1c8bb7d229f748d181"
KERNEL_MEMBER="./opt/kata/share/kata-containers/vmlinux-6.12.28-153"
KERNEL_SHA256="67bac9f416af4cdc9b151e4ba4962d6515e0ad7acc53816761cf964aa6af6ea0"

mkdir -p "$DL" "$OUT"
if [[ -f "$OUT/vmlinux-arm64" ]] && echo "$KERNEL_SHA256  $OUT/vmlinux-arm64" | shasum -a 256 -c - >/dev/null 2>&1; then
  echo "kernel already present: $OUT/vmlinux-arm64"
  exit 0
fi
if [[ ! -f "$DL/$TARBALL" ]]; then
  curl -fL --retry 3 -o "$DL/$TARBALL.part" \
    "https://github.com/kata-containers/kata-containers/releases/download/${KATA_VERSION}/${TARBALL}"
  mv "$DL/$TARBALL.part" "$DL/$TARBALL"
fi
echo "$TARBALL_SHA256  $DL/$TARBALL" | shasum -a 256 -c -
tar -xJf "$DL/$TARBALL" -C "$DL" "$KERNEL_MEMBER"
cp "$DL/${KERNEL_MEMBER#./}" "$OUT/vmlinux-arm64.tmp"
echo "$KERNEL_SHA256  $OUT/vmlinux-arm64.tmp" | shasum -a 256 -c -
mv "$OUT/vmlinux-arm64.tmp" "$OUT/vmlinux-arm64"
echo "kernel: $OUT/vmlinux-arm64"
