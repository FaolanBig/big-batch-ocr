#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/../.." && pwd)"
cd -- "$repo_root"

sudo docker buildx build --load \
  -f api_host/docker/Dockerfile \
  -t big-batch-ocr-api-host:testing \
  "$repo_root"