# API host quickstart and worker setup

This guide starts the API host on one Linux Docker machine and submits a
`.tar.gz` input archive directly to the API. For Kubernetes, use the
[deployment guide](deployment.md#kubernetes-deployment).

## Worker model

The API host starts `MAX_CONCURRENT_JOBS` in-process workers. Each worker
claims the oldest queued job, starts one OCR container (or Kubernetes Job)
using `OCR_IMAGE`, waits for it, records its outcome, and removes the managed
runtime resource. `MAX_CONCURRENT_JOBS` limits jobs at once;
`OCR_JOBS` controls OCRmyPDF's internal threads in each job.

As a starting estimate, plan for up to
`MAX_CONCURRENT_JOBS * OCR_JOBS` OCR threads, in addition to memory and CPU
used by the rest of the OCR pipeline. Start with one concurrent job and a
small thread count, then monitor resource use.

Run one API-host instance against its database. Queue workers and concurrency
are local to one process; this implementation does not coordinate multiple
API hosts. On startup, queued jobs remain queued, while jobs left in `starting`
or `running` are marked failed because their completion cannot be confirmed.

## Docker quickstart

### 1. Check prerequisites

Use a Linux machine with Docker Engine, `curl`, `jq`, and `sudo` access.
`openssl` is needed only if you ask the configurator to generate the API key;
the Docker Compose plugin is needed only if you choose to start the service
through Compose. The operator must be allowed to access the Docker daemon.
Docker socket access is effectively root-equivalent on the host, so run this
API only on a trusted machine and keep it private. The included Compose
configuration binds to `127.0.0.1:8080`.

```sh
docker version
```

### 2. Configure the API host

Run the interactive configurator from the repository root:

```sh
./api_host/config/configure_api_host.sh
```

It asks for a versioned OCR image, API key, Docker-visible data root, worker
settings, and access options. Press Enter at the API-key prompt to generate a
random key. Directory-based job submissions default to disabled; leave them
disabled to require archive uploads. The configurator creates the selected
data-root directory with mode `0750` and ownership UID/GID `65532`, and writes
`api_host/env/.env` with mode `0600`.

The OCR image does not have to be pre-pulled. Docker pulls it when the first
job starts if it is not already local. Choose an absolute host path for the
Docker data root; it is mounted at `/data` in the API and OCR containers.
Archive uploads create their own workspace under this root, so no input or
output subdirectories need to be created manually.

Keep the API key private. Do not commit `api_host/env/.env`, place secrets in
build arguments, or enable anonymous access on a reachable network.

For manual configuration, copy `api_host/env/.env_template` to
`api_host/env/.env`, set restrictive file permissions, and edit the values.
Keep `DATA_ROOT=/data`; set `DOCKER_DATA_ROOT` to the same host directory
mounted into the API container. Manual setup also requires creating the host
directory for UID/GID `65532`. When using Compose directly, set `DOCKER_GID`
to the numeric group ID of `/var/run/docker.sock`; the launcher derives it
automatically.

### 3. Start the API host

Use a published API-host image:

```sh
TAG=v1.2.9
API_IMAGE="ghcr.io/faolanbig/big-batch-ocr_api-host:$TAG"
sudo docker pull "$API_IMAGE"
./api_host/run/run_container.sh "$API_IMAGE"
```

Use a tag present in the
[API-host package registry](https://github.com/FaolanBig/big-batch-ocr/pkgs/container/big-batch-ocr_api-host).
There is no `latest` tag. To build locally, use
`bash api_host/build/build_dockerfile.sh` or, behind a TLS-inspection proxy,
`bash api_host/build/build_dockerfile_proxied_cert.sh`; then run
`./api_host/run/run_container.sh` without an image argument. If both local
images exist, pass the desired image tag explicitly. When neither local build
is available, the launcher uses `API_IMAGE` from `api_host/env/.env` and asks
before pulling it. If that environment file does not exist, the launcher asks
whether to use an existing environment file at another path or run the
configurator to create one.

For a Compose deployment, the host does not need the repository source. Keep
the Compose file and `api_host/env/.env`. The API-host and OCR images default
to v1.2.9 from GHCR; set `API_IMAGE` or `OCR_IMAGE` in that file to override
them. Run:

```sh
cd api_host/docker
docker compose --env-file ../env/.env -f compose.yaml pull
docker compose --env-file ../env/.env -f compose.yaml up -d
```

The Dockerfiles are only needed for local or CI image builds; they copy
`api_host/src` from a source checkout and are not used by Compose.

The launcher uses `api_host/env/.env`, binds the Docker socket and data root,
and persists SQLite state in the `api-host-state` named volume. It leaves an
already-running API container unchanged. Check startup and liveness:

```sh
sudo docker logs --tail=100 api-host
curl --fail http://127.0.0.1:8080/health
```

`/health` is unauthenticated and reports process liveness only; it does not
check Docker Engine or OCR-image availability.

### 4. Upload an archive and submit a job

Create a compressed tar archive from the directory to process. Submit it as
the `inputArchive` multipart field; do not send `inputPath` or `outputPath`.
The following example submits asynchronously and reads the returned job ID
and status URL:

```sh
read -r -s -p "API key: " API_KEY
printf '\n'

tar -czf /tmp/invoices.tar.gz -C ./invoices .
response="$(
  curl --fail-with-body --silent --show-error \
    --oauth2-bearer "$API_KEY" \
    -F "inputArchive=@/tmp/invoices.tar.gz;type=application/gzip" \
    -F "mode=asynchronous" \
    -F "language=deu+eng" \
    -F "optimize=2" \
    http://127.0.0.1:8080/api/v1/jobs
)"
printf '%s\n' "$response" | jq .
job_id="$(printf '%s\n' "$response" | jq -r .jobId)"
status_url="$(printf '%s\n' "$response" | jq -r .statusUrl)"
```

Poll the status URL until `status` is `succeeded` or `failed`:

```sh
while :; do
  status="$(curl --fail-with-body --silent --show-error \
    --oauth2-bearer "$API_KEY" \
    "http://127.0.0.1:8080${status_url}")"
  printf '%s\n' "$status" | jq .
  state="$(printf '%s\n' "$status" | jq -r .status)"
  case "$state" in
    succeeded|failed) break ;;
    *) sleep 2 ;;
  esac
done
```

After success, download the result archive:

```sh
curl --fail-with-body --silent --show-error \
  --oauth2-bearer "$API_KEY" \
  "http://127.0.0.1:8080/api/v1/jobs/${job_id}/result" \
  -o "result-${job_id}.tar.gz"
```

The API request body is limited to 1 GiB and extracted archive contents to
10 GiB. The archive may contain regular files and directories only; symbolic
links and paths that escape the archive root are rejected. Language values
must use installed OCRmyPDF/Tesseract codes separated by `+`; optimization
must be an integer from `0` to `3`. Omitted options use the server defaults.

### Synchronous submissions

Use `mode=synchronous` to wait for completion and download the result archive
in the submit response. Save the response body as a file:

```sh
curl --fail-with-body --silent --show-error \
  -D /tmp/ocr-response-headers.txt \
  --oauth2-bearer "$API_KEY" \
  -F "inputArchive=@/tmp/invoices.tar.gz;type=application/gzip" \
  -F "mode=synchronous" \
  http://127.0.0.1:8080/api/v1/jobs \
  -o synchronous-result.tar.gz
grep -i '^X-Job-ID:' /tmp/ocr-response-headers.txt
```

The response includes the completed job ID in `X-Job-ID`. If the configured
`SYNC_WAIT_LIMIT_SECONDS` is reached first, the API returns `504` with the job
ID and status URL; it does not cancel the job. Poll that URL and download the
result after the job succeeds. Do not repeat the submission after a timeout.

### Completion webhooks

Any multipart submission may include `webhookUrl`. The API makes one POST with
the terminal job resource as JSON. Optional `webhookSecret` and `rootPath`
values become `X-Webhook-Secret` and `X-Root-Path` headers; `rootPath` is also
included in the webhook JSON body. They require a webhook URL.

```sh
curl --fail-with-body --silent --show-error \
  --oauth2-bearer "$API_KEY" \
  -F "inputArchive=@/tmp/invoices.tar.gz;type=application/gzip" \
  -F "mode=asynchronous" \
  -F "webhookUrl=https://automation.example.com/ocr/finished" \
  -F "webhookSecret=replace-with-a-shared-secret" \
  -F "rootPath=/srv/automation/ocr-results" \
  http://127.0.0.1:8080/api/v1/jobs
```

HTTPS certificate verification is enabled by default. Set
`webhookInsecureSkipVerify=true` only for a trusted endpoint using a
self-signed certificate. Delivery is best-effort: there is one attempt, a
10-second timeout, no redirect following, and callback failures do not change
the job outcome. Webhooks supplement rather than replace status polling and
result download.

For integrations that provide archive bytes rather than a file object, the
multipart fields `inputArchiveContents` (raw bytes) and
`inputArchiveContentsBase64` (standard Base64) are also supported.

## Retain or remove job data

Downloading a result does not delete the job record or uploaded workspace;
results can be downloaded again. After the job reaches a terminal state,
delete a single job and its uploaded workspace:

```sh
curl --fail-with-body --silent --show-error -X DELETE \
  --oauth2-bearer "$API_KEY" \
  "http://127.0.0.1:8080/api/v1/jobs/${job_id}"
```

Alternatively, remove metadata and workspaces for every terminal uploaded
job:

```sh
curl --fail-with-body --silent --show-error \
  --oauth2-bearer "$API_KEY" \
  -H "Content-Type: application/json" \
  -d '{"scope":"uploads"}' \
  http://127.0.0.1:8080/api/v1/jobs/cleanup
```

Queued, starting, running, and cancelling jobs are not removed. Cancellation
does not delete data. Cleanup scope `all` is destructive: it also deletes
caller-specified shared input and output directories for eligible terminal
directory jobs. See [Docker data paths](deployment.md#docker-compose-and-data-paths)
for permissions and safeguards.

### Optional shared-directory submissions

For trusted callers only, an operator can set `ALLOW_DIRECTORY_ACCESS=true`.
Then a request can refer to existing input and output directories relative to
`DATA_ROOT`. Missing output directories still require
`createDestination=true`. This mode is disabled by default and is not needed
for direct archive uploads.

## Monitor and size workers

The API logs structured JSON to standard output:

```sh
sudo docker logs --follow api-host
sudo docker ps --filter label=com.faolanbig.big-batch-ocr.managed=true
```

Completed OCR containers are removed after the host records their outcome and
captures up to 32 KiB of runtime output in the job database. OCR processing
logs are stored under the output directory inside the job workspace. Keep
appropriate retention and backups for the data root and `api-host-state`
volume.

Change `MAX_CONCURRENT_JOBS` and `OCR_JOBS` in the environment file, then
recreate the single API-host container to apply the settings. Increasing both
values increases resource use; monitor CPU, memory, and I/O before increasing
capacity. For Kubernetes, also set appropriate OCR Job resource requests and
limits. The four `OCR_CPU_REQUEST`, `OCR_CPU_LIMIT`, `OCR_MEMORY_REQUEST`,
and `OCR_MEMORY_LIMIT` settings must be specified together.

## Troubleshooting

| Symptom | Checks |
| --- | --- |
| API container exits at startup | Check logs and required settings, especially `API_KEY`, `OCR_IMAGE`, `DATA_ROOT`, and `DATABASE_PATH`. |
| `permission denied` on Docker socket | Check socket access and `DOCKER_GID`; never make the socket world-writable. |
| Archive upload rejected | Verify it is a valid `.tar.gz`, under the request and extraction limits, and contains only regular files/directories with safe paths. |
| Job remains `queued` | Check API-host logs, Docker daemon health, and whether worker slots are occupied. |
| Job is `failed` | Inspect the job's `error` and `exitCode`, API logs, and OCR output logs in its workspace. |
| Docker cannot access a job path | Confirm `DOCKER_DATA_ROOT` is the Docker daemon's host path corresponding to `/data`. |

For all environment variables, Docker socket risks, Kubernetes setup, and the
full API contract, see [deployment](deployment.md) and the
[OpenAPI specification](../api/api_specification/main_api_specification_api_host.json).
