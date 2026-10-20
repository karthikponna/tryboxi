#!/usr/bin/env bash
# Builds the base guest rootfs as a sparse ext4 file using Docker Desktop.
# Nothing touches real disks: mke2fs runs inside a container and writes a
# plain file into _out/images/.
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
OUT="$ROOT/_out/images"
SIZE="${ROOTFS_SIZE:-4G}"
ARCH="${ARCH:-arm64}"
TAG="sandboxd-base:dev-$ARCH"

mkdir -p "$OUT"
docker build --platform "linux/$ARCH" -t "$TAG" "$ROOT/images/base"

cid="$(docker create --platform "linux/$ARCH" "$TAG" /bin/true)"
trap 'docker rm -f "$cid" >/dev/null 2>&1 || true' EXIT
docker export "$cid" -o "$OUT/base-rootfs-$ARCH.tar"

rm -f "$OUT/base-rootfs-$ARCH.ext4.tmp"
docker run --rm --platform "linux/$ARCH" -v "$OUT:/out" debian:trixie-slim sh -euc "
  apt-get update -qq && apt-get install -y -qq e2fsprogs >/dev/null
  mkdir /r && tar -xpf /out/base-rootfs-$ARCH.tar -C /r --numeric-owner
  rm -f /r/.dockerenv
  printf 'nameserver 192.168.127.1\n' > /r/etc/resolv.conf
  printf '127.0.0.1 localhost\n127.0.1.1 sandbox\n' > /r/etc/hosts
  mke2fs -q -t ext4 -L sbxroot -E root_owner=0:0 -d /r /out/base-rootfs-$ARCH.ext4.tmp $SIZE
"
mv "$OUT/base-rootfs-$ARCH.ext4.tmp" "$OUT/base-rootfs-$ARCH.ext4"
rm -f "$OUT/base-rootfs-$ARCH.tar"
shasum -a 256 "$OUT/base-rootfs-$ARCH.ext4" | tee "$OUT/base-rootfs-$ARCH.ext4.sha256"
