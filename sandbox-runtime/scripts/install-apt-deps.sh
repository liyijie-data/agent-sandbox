#!/bin/sh
set -eu

APT_MIRROR="${APT_MIRROR:-mirrors.aliyun.com}"

if [ -f /etc/apt/sources.list.d/debian.sources ]; then
  sed -i "s|deb.debian.org|${APT_MIRROR}|g" /etc/apt/sources.list.d/debian.sources
  sed -i "s|security.debian.org|${APT_MIRROR}|g" /etc/apt/sources.list.d/debian.sources
fi
if [ -f /etc/apt/sources.list ]; then
  sed -i "s|deb.debian.org|${APT_MIRROR}|g" /etc/apt/sources.list
  sed -i "s|security.debian.org|${APT_MIRROR}|g" /etc/apt/sources.list
fi

attempt=1
while [ "$attempt" -le 5 ]; do
  if apt-get update \
    && apt-get install -y --no-install-recommends --fix-missing \
      bash curl ca-certificates git xz-utils; then
    rm -rf /var/lib/apt/lists/*
    exit 0
  fi
  echo "apt install failed (attempt ${attempt}), retrying..."
  sleep 3
  attempt=$((attempt + 1))
done

echo "apt install failed after 5 attempts" >&2
exit 1
