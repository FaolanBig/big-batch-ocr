#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/../.." && pwd)"
default_certificate=/usr/local/share/ca-certificates/root_cert.crt
if (( $# > 1 )); then
  printf 'Usage: %s [certificate-path]\n' "$0" >&2
  exit 2
fi
certificate="${1:-$default_certificate}"
cd -- "$repo_root"

if ! sudo test -r "$certificate"; then
  printf 'Cannot read company CA certificate: %s\n' "$certificate" >&2
  if (( $# == 0 )); then
    printf 'Provide a certificate path as an argument, for example: %s /path/to/root_cert.crt\n' "$0" >&2
  fi
  exit 1
fi

sudo docker buildx build --load \
  --secret id=root_cert,src="$certificate" \
  -f api_host/docker/Dockerfile_proxied_cert \
  -t big-batch-ocr-api-host:proxied \
  "$repo_root"