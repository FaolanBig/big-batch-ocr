# API host deployment

## Requirements and configuration

Build the API-host image from the repository root with
`docker compose -f api_host/docker/compose.yaml build`. Before starting, provide:

| Variable | Required | Default | Meaning |
| --- | --- | --- | --- |
| `API_KEY` | Yes unless open access is explicitly enabled | — | Bearer token accepted by job endpoints |
| `ALLOW_ANONYMOUS` | No | `false` | Set to `true` only on a trusted, isolated network |
| `DATA_ROOT` | No | `/data` | Existing data directory inside the API container |
| `DOCKER_DATA_ROOT` | No | `/srv/big-batch-ocr-data` in Compose | Same directory as seen by the Docker daemon |
| `DATABASE_PATH` | No | `/var/lib/api-host/jobs.db` | Persistent SQLite job database |
| `OCR_IMAGE` | Yes | — | OCR image reference with an explicit release tag or digest; do not use `latest` (prefer a digest for immutable pinning) |
| `OCR_JOBS` | No | `3` | OCRmyPDF worker threads passed to the OCR container |
| `MAX_CONCURRENT_JOBS` | No | `1` | Maximum OCR containers running at once |
| `SYNC_WAIT_LIMIT_SECONDS` | No | `30` | Synchronous response wait limit; a timeout does not cancel the job |
| `API_ADDRESS` | No | `:8080` | HTTP listen address |
| `DOCKER_GID` | Compose only | — | Numeric group ID allowed to access the Docker socket |

Use your deployment secret manager to inject `API_KEY`; do not put a production
key in the Compose file or source control. Generate a high-entropy key, restrict
network exposure (the Compose example binds localhost), and terminate TLS at a
trusted reverse proxy when remote clients need access.

## Docker socket and filesystem mapping

API-host access to `/var/run/docker.sock` is effectively **root-equivalent on the
Docker host**. A caller who can submit a job can cause the configured OCR image
to run. Restrict socket access, API network access, and the configured image to
trusted operators. The host only accepts input/output paths relative to
`DATA_ROOT`; it does not expose arbitrary Docker options, images, mounts, or
host paths to callers.

Create the host data directory and make it accessible to the API container
(UID 65532 in the distroless runtime). `DOCKER_DATA_ROOT` is the absolute host
path as seen by the Docker daemon; the Compose bind mounts that host directory
at `/data` inside the API container. Keep the two roots aligned as shown so a
validated relative path maps to the same host files for the OCR container.
Input is bind-mounted read-only at `/source`; output is read-write at
`/destination`.

The named `api-host-state` volume persists the SQLite job database. Back it up
with the host data. It retains container IDs and the last 32 KiB of captured
output per job; these diagnostics are internal and are not returned by the
status API. At startup, jobs left in `starting` or `running` are marked
failed because completion cannot be confirmed; their known managed containers
are stopped and removed when Docker is reachable. Queued jobs remain queued.
Completed OCR containers are removed after their outcome and logs are captured.
The OCR image stores its own processing logs in the mounted destination, so
apply retention controls to output directories as appropriate.

Example startup:

```sh
export API_KEY="$(openssl rand -hex 32)"
export OCR_IMAGE="ghcr.io/faolanbig/big-batch-ocr:v1.2.3"
export DOCKER_GID="$(stat -c '%g' /var/run/docker.sock)"
export DOCKER_DATA_ROOT="/srv/big-batch-ocr-data"
sudo install -d -m 0750 -o 65532 -g 65532 "$DOCKER_DATA_ROOT"
docker compose -f api_host/docker/compose.yaml up --build -d
```

Use an existing release tag in `OCR_IMAGE`; `v1.2.3` above is an example.

## API usage

The full request/response contract is in
[`../api/api_specification/main_api_specification_api_host.json`](../api/api_specification/main_api_specification_api_host.json).
Health is unauthenticated and reports process liveness only:

```sh
curl --fail http://127.0.0.1:8080/health
```

Submit asynchronously and poll the returned status URL:

```sh
curl --fail-with-body -i \
  -H "Authorization: Bearer $API_KEY" \
  -H "Content-Type: application/json" \
  -d '{"inputPath":"incoming/invoices","outputPath":"processed/invoices","mode":"asynchronous"}' \
  http://127.0.0.1:8080/api/v1/jobs

curl --fail-with-body \
  -H "Authorization: Bearer $API_KEY" \
  http://127.0.0.1:8080/api/v1/jobs/job_ID_FROM_RESPONSE
```

For synchronous execution, set `"mode":"synchronous"`. On success the host
returns `200`; a container failure returns a safe `500` problem response. At
the configured wait limit it returns `504` with `jobId` and `statusUrl`, and
the job continues. Poll that URL rather than resubmitting. The request's
disconnect likewise does not cancel an accepted job.

By default, both paths must exist as directories beneath `DATA_ROOT`. To
explicitly create a missing output path and its parent directories, include
`"createDestination":true`. The host rejects traversal, symlink escapes,
unreadable input, identical directories, and either directory nested inside
the other before accepting a job.

### Workato-style HTTP actions

Configure a POST action with the API URL, JSON content type, and a bearer
credential stored in a Workato connection/secret field. For asynchronous
processing, use this body:

```json
{
  "inputPath": "incoming/invoices",
  "outputPath": "processed/invoices",
  "mode": "asynchronous",
  "createDestination": true
}
```

Save `jobId` and `statusUrl` from the `202` response. Add an HTTP GET action to
`statusUrl` with the same bearer credential and repeat it on a Workato
wait/retry step until `status` is `succeeded` or `failed`. For a bounded
request-response recipe, submit `"mode":"synchronous"` instead; handle `200`,
`500`, and `504` explicitly. On `504`, retain and poll the returned job ID and
URL rather than retrying the POST, which would create duplicate work.
