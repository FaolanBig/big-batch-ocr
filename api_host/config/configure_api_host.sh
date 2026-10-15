#!/usr/bin/env bash
set -euo pipefail

script_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
api_host_dir="$(cd -- "$script_dir/.." && pwd)"
env_dir="$api_host_dir/env"
env_file="$env_dir/.env"
template="$env_dir/.env_template"

if [[ ! -r "$template" ]]; then
  printf 'Environment template is missing or unreadable: %s\n' "$template" >&2
  exit 1
fi
if [[ ! -t 0 || ! -r /dev/tty || ! -w /dev/tty ]]; then
  printf 'This configurator requires an interactive terminal.\n' >&2
  exit 1
fi

mkdir -p -- "$env_dir"
if [[ -e "$env_file" ]]; then
  read -r -p "$env_file already exists. Replace it? [y/N] " answer </dev/tty
  case "$answer" in
    y|Y|yes|YES|Yes) ;;
    *) printf 'Leaving the existing environment file unchanged.\n'; exit 0 ;;
  esac
fi

read_value() {
  local prompt="$1"
  local default="$2"
  local value
  read -r -p "$prompt [$default]: " value </dev/tty
  REPLY="${value:-$default}"
}

read_positive_integer() {
  local prompt="$1"
  local default="$2"
  while true; do
    read_value "$prompt" "$default"
    if [[ "$REPLY" =~ ^[1-9][0-9]*$ ]]; then
      printf -v "$3" '%s' "$REPLY"
      return
    fi
    printf 'Enter a positive whole number.\n' >/dev/tty
  done
}

read_ocr_optimize() {
  while true; do
    read_value "OCRmyPDF optimization level (0-3)" "3"
    if [[ "$REPLY" =~ ^[0-3]$ ]]; then
      ocr_optimize="$REPLY"
      return
    fi
    printf 'Enter an integer from 0 to 3.\n' >/dev/tty
  done
}

ocr_jobs=""
ocr_language=""
ocr_optimize=""
max_concurrent_jobs=""
sync_wait_limit=""
read_value "Versioned OCR image (do not use the latest tag)" "ghcr.io/faolanbig/big-batch-ocr:v1.0.0"
ocr_image="$REPLY"
while true; do
  image_name="${ocr_image%@*}"
  image_leaf="${image_name##*/}"
  if [[ "$ocr_image" != *[[:space:]]* && ( "$image_leaf" == *:* || "$ocr_image" =~ @sha256:[0-9a-fA-F]{64}$ ) && "$image_leaf" != *:latest ]]; then
    break
  fi
  printf 'Enter an image reference with a version tag or digest; the latest tag is not allowed.\n' >/dev/tty
  read_value "Versioned OCR image" "ghcr.io/faolanbig/big-batch-ocr:v1.0.0"
  ocr_image="$REPLY"
done

read -r -s -p "API key (press Enter to generate a secure random key): " api_key </dev/tty
printf '\n' >/dev/tty
if [[ -z "$api_key" ]]; then
  if ! command -v openssl >/dev/null 2>&1; then
    printf 'openssl is required to generate an API key. Install it or rerun and provide a key.\n' >&2
    exit 1
  fi
  api_key="$(openssl rand -hex 32)"
  printf 'Generated a random API key.\n'
fi

read_value "Docker daemon data root (absolute host path)" "/srv/big-batch-ocr-data"
docker_data_root="$REPLY"
while [[ "$docker_data_root" != /* || "$docker_data_root" == *:* ]]; do
  printf 'Enter an absolute path without a colon.\n' >/dev/tty
  read_value "Docker daemon data root" "/srv/big-batch-ocr-data"
  docker_data_root="$REPLY"
done
if ! sudo install -d -m 0750 -o 65532 -g 65532 -- "$docker_data_root"; then
  printf 'Could not create the Docker data root with permissions for the API and OCR containers: %s\n' "$docker_data_root" >&2
  exit 1
fi

default_docker_gid=""
if [[ -S /var/run/docker.sock ]]; then
  default_docker_gid="$(stat -c '%g' /var/run/docker.sock 2>/dev/null || true)"
fi
if [[ ! "$default_docker_gid" =~ ^[0-9]+$ ]]; then
  default_docker_gid=""
fi
while true; do
  if [[ -n "$default_docker_gid" ]]; then
    read_value "Docker socket group ID (used by Compose; launcher detects it automatically)" "$default_docker_gid"
  else
    read_value "Docker socket group ID (used by Compose; launcher detects it automatically)" "0"
  fi
  docker_gid="$REPLY"
  if [[ "$docker_gid" =~ ^[0-9]+$ ]]; then
    break
  fi
  printf 'Enter a numeric group ID.\n' >/dev/tty
done

read_positive_integer "OCR worker threads per container" "3" ocr_jobs
while true; do
  read_value "Default OCR language codes (join multiple with +)" "eng"
  if [[ "$REPLY" =~ ^[A-Za-z0-9_]+(\+[A-Za-z0-9_]+)*$ && ${#REPLY} -le 128 ]]; then
    ocr_language="$REPLY"
    break
  fi
  printf "Use language codes containing only letters, digits, or underscores, joined by '+'.\n" >/dev/tty
done
read_ocr_optimize
read_positive_integer "Maximum concurrent OCR containers" "1" max_concurrent_jobs
read_positive_integer "Synchronous wait limit in seconds" "30" sync_wait_limit

while true; do
  read_value "Allow unauthenticated requests? (not recommended)" "false"
  case "${REPLY,,}" in
    true|yes|y) allow_anonymous=true; break ;;
    false|no|n) allow_anonymous=false; break ;;
    *) printf 'Answer yes or no.\n' >/dev/tty ;;
  esac
done

while true; do
  read_value "Allow directory-based job submissions? (otherwise require archive uploads)" "false"
  case "${REPLY,,}" in
    true|yes|y) allow_directory_access=true; break ;;
    false|no|n) allow_directory_access=false; break ;;
    *) printf 'Answer yes or no.\n' >/dev/tty ;;
  esac
done

tmp_file="$(mktemp "$env_dir/.env.XXXXXX")"
trap 'rm -f -- "$tmp_file"' EXIT
chmod 600 "$tmp_file"
{
  printf 'API_KEY=%s\n' "$api_key"
  printf 'OCR_IMAGE=%s\n' "$ocr_image"
  printf 'ALLOW_ANONYMOUS=%s\n' "$allow_anonymous"
  printf 'ALLOW_DIRECTORY_ACCESS=%s\n' "$allow_directory_access"
  printf 'DOCKER_GID=%s\n' "$docker_gid"
  printf 'DATA_ROOT=/data\n'
  printf 'DOCKER_DATA_ROOT=%s\n' "$docker_data_root"
  printf 'DATABASE_PATH=/var/lib/api-host/jobs.db\n'
  printf 'OCR_JOBS=%s\n' "$ocr_jobs"
  printf 'OCR_LANGUAGE=%s\n' "$ocr_language"
  printf 'OCR_OPTIMIZE=%s\n' "$ocr_optimize"
  printf 'MAX_CONCURRENT_JOBS=%s\n' "$max_concurrent_jobs"
  printf 'SYNC_WAIT_LIMIT_SECONDS=%s\n' "$sync_wait_limit"
} > "$tmp_file"
mv -f -- "$tmp_file" "$env_file"
trap - EXIT

printf 'Configuration saved to %s (permissions: 600).\n' "$env_file"
printf 'Protect this file; it contains the API key. Create the data root if needed: %s\n' "$docker_data_root"
