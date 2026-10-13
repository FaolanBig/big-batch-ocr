#!/usr/bin/env bash
set -euo pipefail

script_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
repo_root="$(cd -- "$script_dir/../.." && pwd)"
env_file="$repo_root/api_host/env/.env"
container_name=api-host
stopped_container=false

usage() {
  printf 'Usage: %s [api-host-image]\n' "$0" >&2
  printf 'Image defaults to the only locally available testing/proxied image.\n' >&2
}

if (( $# > 1 )); then
  usage
  exit 2
fi

if [[ ! -f "$env_file" || ! -r "$env_file" ]]; then
  printf 'Runtime environment file is missing or unreadable: %s\n' "$env_file" >&2
  printf 'Create it from api_host/docker/.env_template and secure it with chmod 600.\n' >&2
  exit 1
fi

data_root=""
data_root_count=0
data_container_root=""
api_key=""
allow_anonymous=""
ocr_image=""
while IFS= read -r line || [[ -n "$line" ]]; do
  line="${line%$'\r'}"
  [[ "$line" =~ ^[[:space:]]*(#|$) ]] && continue
  if [[ "$line" != *=* ]]; then
    printf 'Invalid .env line; expected KEY=VALUE: %s\n' "$line" >&2
    exit 1
  fi
  key="${line%%=*}"
  value="${line#*=}"
  if [[ ! "$key" =~ ^[A-Za-z_][A-Za-z0-9_]*$ ]]; then
    printf 'Invalid environment variable name in %s\n' "$env_file" >&2
    exit 1
  fi
  if [[ "$key" == "DOCKER_DATA_ROOT" ]]; then
    data_root="$value"
    ((data_root_count += 1))
  elif [[ "$key" == "DATA_ROOT" ]]; then
    data_container_root="$value"
  elif [[ "$key" == "API_KEY" ]]; then
    api_key="$value"
  elif [[ "$key" == "ALLOW_ANONYMOUS" ]]; then
    allow_anonymous="$value"
  elif [[ "$key" == "OCR_IMAGE" ]]; then
    ocr_image="$value"
  fi
done < "$env_file"

if (( data_root_count != 1 )) || [[ -z "$data_root" || "$data_root" != /* || "$data_root" == *:* ]]; then
  printf 'DOCKER_DATA_ROOT must be an absolute host path without a colon in %s\n' "$env_file" >&2
  exit 1
fi
if [[ -n "$data_container_root" && "$data_container_root" != "/data" ]]; then
  printf 'DATA_ROOT must be /data when using this launcher (got %s).\n' "$data_container_root" >&2
  exit 1
fi
if [[ -z "$ocr_image" ]]; then
  printf 'OCR_IMAGE must be set in %s\n' "$env_file" >&2
  exit 1
fi
if [[ "$api_key" == "replace-with-a-long-random-secret" ]]; then
  printf 'Replace the sample API_KEY in %s before starting the API host.\n' "$env_file" >&2
  exit 1
fi
if [[ -z "$api_key" && ! "$allow_anonymous" =~ ^(1|[tT]|[Tt][Rr][Uu][Ee])$ ]]; then
  printf 'Set API_KEY in %s or explicitly enable ALLOW_ANONYMOUS=true.\n' "$env_file" >&2
  exit 1
fi
if [[ ! -d "$data_root" ]]; then
  printf 'DOCKER_DATA_ROOT does not exist or is not a directory: %s\n' "$data_root" >&2
  printf 'Create it first and ensure it is accessible to the API and OCR containers.\n' >&2
  exit 1
fi

docker_cmd=(sudo docker)
if ! "${docker_cmd[@]}" info >/dev/null; then
  printf 'Cannot access the Docker daemon. Check Docker service and sudo permissions.\n' >&2
  exit 1
fi

if container_state="$("${docker_cmd[@]}" container inspect --format '{{.State.Status}}' "$container_name" 2>/dev/null)"; then
  if [[ "$container_state" == "running" || "$container_state" == "restarting" || "$container_state" == "paused" ]]; then
    printf 'Container %s is already %s; leaving it unchanged.\n' "$container_name" "$container_state"
    exit 0
  fi
  stopped_container=true
fi

if (( $# == 1 )); then
  image="$1"
  if ! "${docker_cmd[@]}" image inspect "$image" >/dev/null 2>&1; then
    printf 'API host image is not available locally: %s\n' "$image" >&2
    exit 1
  fi
else
  standard_image=big-batch-ocr-api-host:testing
  proxied_image=big-batch-ocr-api-host:proxied
  has_standard=false
  has_proxied=false
  "${docker_cmd[@]}" image inspect "$standard_image" >/dev/null 2>&1 && has_standard=true
  "${docker_cmd[@]}" image inspect "$proxied_image" >/dev/null 2>&1 && has_proxied=true
  if [[ "$has_standard" == true && "$has_proxied" == true ]]; then
    printf 'Both standard and proxied images are available. Choose one explicitly:\n' >&2
    printf '  %s %s\n  %s %s\n' "$0" "$standard_image" "$0" "$proxied_image" >&2
    exit 1
  elif [[ "$has_standard" == true ]]; then
    image="$standard_image"
  elif [[ "$has_proxied" == true ]]; then
    image="$proxied_image"
  else
    printf 'No API host image found. Build one first (testing or proxied tag).\n' >&2
    exit 1
  fi
fi

if [[ ! -S /var/run/docker.sock ]]; then
  printf 'Docker socket not found: /var/run/docker.sock\n' >&2
  exit 1
fi
docker_gid="$(stat -c '%g' /var/run/docker.sock)"
if [[ ! "$docker_gid" =~ ^[0-9]+$ ]]; then
  printf 'Could not determine numeric Docker socket group ID.\n' >&2
  exit 1
fi

if [[ "$stopped_container" == true ]]; then
  printf 'Removing stopped %s container so the current .env settings are applied.\n' "$container_name"
  "${docker_cmd[@]}" container rm "$container_name" >/dev/null
fi

"${docker_cmd[@]}" volume create api-host-state >/dev/null
"${docker_cmd[@]}" run -d \
  --name "$container_name" \
  --restart unless-stopped \
  --env-file "$env_file" \
  -p 127.0.0.1:8080:8080 \
  --group-add "$docker_gid" \
  -v /var/run/docker.sock:/var/run/docker.sock \
  -v "$data_root:/data:rw" \
  -v api-host-state:/var/lib/api-host \
  "$image"

printf 'Started %s using %s and %s\n' "$container_name" "$image" "$env_file"
