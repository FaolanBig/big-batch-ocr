#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/../.." && pwd)"
certificate=/usr/local/share/ca-certificates/vector_root_cert.crt # Path to the company CA certificate
cd -- "$repo_root"

if ! sudo test -r "$certificate"; then
  printf 'Cannot read company CA certificate: %s\n' "$certificate" >&2
  exit 1
fi

sudo docker buildx build --load \
  --secret id=root_cert,src="$certificate" \
  -f api_host/docker/Dockerfile_proxied_cert \
  -t big-batch-ocr-api-host:proxied \
  "$repo_root"