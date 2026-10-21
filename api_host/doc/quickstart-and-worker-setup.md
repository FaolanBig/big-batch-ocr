# Docker quickstart and worker setup

This guide takes you from a clean checkout to a running Docker deployment that
starts OCR containers for accepted jobs. For Kubernetes, use the
[`deployment.md`](deployment.md#kubernetes-deployment) guide; the same API host
automatically selects Kubernetes Jobs when it runs in a Kubernetes pod.

## How the worker is included

There is no separate worker binary or worker container to build or start. The
API host starts `MAX_CONCURRENT_JOBS` in-process worker goroutines when it
starts. Each worker takes the oldest queued job, starts one OCR container using
the configured `OCR_IMAGE`, waits for it, records its result, and removes the
container. For example, `MAX_CONCURRENT_JOBS=2` means this API-host process can
run at most two OCR containers at once; further accepted jobs wait in the
persistent queue.

`OCR_JOBS` is a separate setting: it controls OCRmyPDF's internal worker
threads **inside each OCR container**. Increasing both settings increases
resource use: as a first estimate, plan for up to
`MAX_CONCURRENT_JOBS * OCR_JOBS` OCR threads, plus memory and CPU used by
scanning and processing individual documents. Start with one concurrent job
and three OCR threads, monitor the host, and adjust deliberately.
The API host's `OCR_LANGUAGE` and `OCR_OPTIMIZE` settings provide defaults
(`eng` and `3`); each job may override them with validated `language` and
`optimize` fields.

Run one API-host instance against its database. Do not scale this Compose
service to multiple replicas or run multiple API hosts against one database:
the queue workers and concurrency limit are local to a process, and startup
recovery treats jobs left running in the database as interrupted. A
multi-host worker service needs coordinated locking/leases and shared storage,
which this implementation does not provide.

## Quickstart: one Linux Docker host

### 1. Install prerequisites

Use a Linux machine with Docker Engine, the Docker Compose plugin, `curl`,
`openssl`, `jq`, and `sudo` access. The operator must be allowed to access the
Docker daemon.
Docker socket access is effectively root-equivalent on that host; only deploy
this API on a trusted machine and keep the API private. The Compose example
publishes the API on `127.0.0.1:8080`, not on every network interface.

From the repository root, confirm Docker access:

```sh
docker version
docker compose version
```

### 2. Choose a data root and a published OCR image

Choose an absolute directory on the Docker daemon's host for
`DOCKER_DATA_ROOT` (the default is `/srv/big-batch-ocr-data`) and a versioned
OCR image from the [Big Batch OCR package registry](https://github.com/FaolanBig/big-batch-ocr/pkgs/container/big-batch-ocr_ocr-sequencial).
For example, `ghcr.io/faolanbig/big-batch-ocr_ocr-sequencial:v1.2.1`; replace this with a
published tag, or use a digest-pinned reference for stronger immutability.
The OCR image does not need to be pre-pulled: the API host pulls the configured
image automatically when the first job needs it, if it is not already local.

`DOCKER_DATA_ROOT` is the absolute path on the Docker daemon's host. The
container mounts this same host directory at `/data`, and the API maps
validated relative paths back to that host path for the OCR container.
Do not point it at a path that is available only inside the API container.

When you use the configurator in step 3, it creates the selected data-root
directory and sets it to mode `0750`, owned by UID/GID `65532` for the API and
OCR containers. You do not need to create the root manually. To use the
shared-directory submission method, enable directory-based submissions in the
configurator, then create the input/output subdirectories inside that root and
put source files in the input directory. Directory submissions are disabled
by default. For security, keep them disabled and submit input as `.tar.gz`
archives through the API; each archive gets an isolated per-job workspace.
Enable shared-directory submissions only for trusted callers when they are
specifically needed.

### 3. Configure runtime credentials

Create the runtime environment file with the interactive configurator:

```sh
./api_host/config/configure_api_host.sh
```

The script prompts for the OCR image, API key (press Enter to generate one),
Docker data root, worker settings, and whether directory-based submissions
should be allowed. That option defaults to disabled, so use archive uploads
unless you explicitly opt in to directory submissions. The script creates the
data-root directory with permissions for the containers, then saves
`api_host/env/.env` with mode `600`. Enter the image and data-root values
selected in step 2.
For the shared-directory example later in this guide, create its child
directories after configuration (replace the root if you selected a different
one):

```sh
sudo install -d -m 0750 -o 65532 -g 65532 \
  /srv/big-batch-ocr-data/incoming/sample \
  /srv/big-batch-ocr-data/processed
```

The API host and managed OCR containers run as UID/GID `65532`. If you later
use `POST /api/v1/jobs/cleanup` with `{"scope":"all"}`, that user must have
write and execute permission on each shared job directory's parent to remove
the directory, and on directories inside it to remove their contents. For
example, owning `incoming/sample` does not let the API delete it if
`incoming` is owned by root and is not writable by UID/GID `65532`. See
[`deployment.md`](deployment.md#docker-socket-and-filesystem-mapping) for a
targeted permission-repair example. Avoid recursively changing permissions on
the whole data root.

Skip this for archive uploads; they do not require pre-created input or output
directories.
Keep `API_KEY` protected; do not commit it or put it into a Docker build
argument. Authentication is required unless `ALLOW_ANONYMOUS=true` is
explicitly set. Do not enable anonymous mode on an exposed network. If you use
Compose instead of the run script, set `DOCKER_GID` to the numeric output of
`stat -c '%g' /var/run/docker.sock`; the run launcher calculates it for you.

Alternatively, create the same file by manually editing the template:

```sh
cp api_host/env/.env_template api_host/env/.env
chmod 600 api_host/env/.env
```

Edit `api_host/env/.env`, replacing the sample `API_KEY`, `OCR_IMAGE`, and
`DOCKER_DATA_ROOT` values. Keep `DATA_ROOT=/data`; use plain `KEY=VALUE`
entries without `export` or shell command substitutions. The API key can be
generated with `openssl rand -hex 32`.
The manual template workflow does not create the host data root; create it
with permissions for UID/GID 65532 before starting the service:

```sh
sudo install -d -m 0750 -o 65532 -g 65532 /srv/big-batch-ocr-data
```

If Docker socket permissions prevent the API container from connecting,
resolve access through your host's Docker administration policy. Do not make
the socket world-writable. Granting socket access grants broad control over
the Docker host.

### 4. Start the API host

Use the pre-compiled API-host image from
[GitHub Container Registry](https://github.com/FaolanBig/big-batch-ocr/pkgs/container/big-batch-ocr_api-host).
Select a published release tag (the example below uses `v1.2.1`), then pull and
start it:

```sh
TAG=v1.2.1
API_IMAGE="ghcr.io/faolanbig/big-batch-ocr_api-host:$TAG"
sudo docker pull "$API_IMAGE"
./api_host/run/run_container.sh "$API_IMAGE"
```

To compile the API host yourself instead, run either build script from the
repository root:

```sh
# Standard build; does not pass a CA certificate secret.
bash api_host/build/build_dockerfile.sh

# OR: proxy build; uses the company CA as a temporary BuildKit secret.
bash api_host/build/build_dockerfile_proxied_cert.sh
```

The proxy script reads the certificate at
`/usr/local/share/ca-certificates/root_cert.crt` as a BuildKit secret.
The CA is used only during module download and is not copied into the image.
Neither build script reads the runtime `.env` file. Start the image using the
launcher, which works identically for either build:

```sh
./api_host/run/run_container.sh
```

If both images are available, pass the desired image:

```sh
./api_host/run/run_container.sh big-batch-ocr-api-host:proxied
```

Check startup and health:

```sh
sudo docker ps --filter name=api-host
sudo docker logs --tail=100 api-host
```

The launcher reads `api_host/env/.env`, binds `DOCKER_DATA_ROOT` at `/data`,
and creates/reuses the persistent `api-host-state` volume. It leaves an
already-running API host unchanged; if the named container is stopped, it
recreates it with the current environment file.

The image contains the API host only. It connects to the Docker Engine through
the mounted socket and creates OCR containers when jobs are accepted. The
SQLite database is persisted in the `api-host-state` named volume, so queued
jobs and completed job records survive a normal API-container replacement.
On restart, queued work can resume; work that was already starting/running is
marked failed if completion cannot be confirmed.

Confirm process liveness:

```sh
curl --fail http://127.0.0.1:8080/health
```

Expected response:

```json
{"status":"ok"}
```

Health is unauthenticated and reports process liveness only; it does not
confirm that the Docker daemon or OCR image is available.

### 5. Submit a job and poll its status

Paths are relative to `DATA_ROOT` (`/data` in Compose), not arbitrary host
paths. Existing input and output directories are required by default. Set
`createDestination` to `true` only when the API should create a missing output
directory beneath the data root. Alternatively, upload a `.tar.gz` archive
and let the API create per-job input/output folders without specifying either
path.

Submit asynchronously:

```sh
response="$(
  curl --fail-with-body --silent --show-error \
    -H "Authorization: Bearer $API_KEY" \
    -H "Content-Type: application/json" \
    -d '{"inputPath":"incoming/sample","outputPath":"processed/sample","mode":"asynchronous","createDestination":true,"language":"deu+eng","optimize":2}' \
    http://127.0.0.1:8080/api/v1/jobs
)"
printf '%s\n' "$response" | jq .
job_id="$(printf '%s\n' "$response" | jq -r .jobId)"
status_url="$(printf '%s\n' "$response" | jq -r .statusUrl)"
```

The API returns `202 Accepted` with a job ID and status URL. Poll that URL
until the status is `succeeded` or `failed`:

```sh
while :; do
  status="$(curl --fail-with-body --silent --show-error \
    -H "Authorization: Bearer $API_KEY" \
    "http://127.0.0.1:8080${status_url}")"
  printf '%s\n' "$status" | jq .
  state="$(printf '%s\n' "$status" | jq -r .status)"
  case "$state" in
    succeeded|failed) break ;;
    *) sleep 2 ;;
  esac
done
```

Use the status URL returned by the API rather than constructing a host path
from untrusted input. A `failed` job is a valid job resource; check its `error`
and `exitCode` fields. Do not resubmit just because a client times out or
disconnects after acceptance.

For a synchronous test instead, submit `"mode":"synchronous"`. The call waits
up to `SYNC_WAIT_LIMIT_SECONDS` (30 seconds by default), then either returns
the processed `.tar.gz` archive (with the job ID in the `X-Job-ID` header) or a
`504` containing the accepted job ID and status URL. The job is not cancelled
at the wait limit; poll its status URL and download its result after it
succeeds.

Instead of placing input files on the shared data volume manually, upload a
`.tar.gz` archive as multipart form data. Set `mode`; do not set `inputPath` or
`outputPath`. The API creates a
private workspace under `.api-host/jobs/<jobId>/` with separate `input` and
`output` directories:

```sh
tar -czf /tmp/invoices.tar.gz -C ./invoices .
response="$(
  curl --fail-with-body --silent --show-error \
    -H "Authorization: ******" \
    -F "inputArchive=@/tmp/invoices.tar.gz" \
    -F "mode=asynchronous" \
    http://127.0.0.1:8080/api/v1/jobs
)"
printf '%s\n' "$response" | jq .
job_id="$(printf '%s\n' "$response" | jq -r .jobId)"
status_url="$(printf '%s\n' "$response" | jq -r .statusUrl)"
```

The `language` and `optimize` fields are optional. `language` accepts one or
more installed OCRmyPDF/Tesseract language codes joined by `+`; `optimize` is
an integer from `0` to `3`. Omitted fields use the server defaults. Unknown
OCR command arguments cannot be supplied.

The archive request body is limited to 1 GiB and its extracted contents to
10 GiB. Archive entries must be files or directories; links and paths that
escape the archive are rejected. The existing JSON request with `inputPath`
continues to use the shared-directory workflow and has no uploaded archive.

After a job succeeds, download its processed files by job ID. The endpoint
returns a `.tar.gz` archive; polling and downloading both require the same
bearer credential:

```sh
curl --fail-with-body --silent --show-error \
  -H "Authorization: ******" \
  "http://127.0.0.1:8080/api/v1/jobs/${job_id}/result" \
  -o "result-${job_id}.tar.gz"
```

The result is generated from the job's output directory when requested.
Downloading does not remove the job-specific workspace or its extracted
input; the result can be downloaded again later. Remove uploaded-job data
explicitly with `DELETE /api/v1/jobs/${job_id}` after the job is terminal, or
use `POST /api/v1/jobs/cleanup` with `{"scope":"uploads"}` to delete all
terminal uploaded jobs. Cancelling a job also preserves its workspace until
explicit cleanup. For jobs submitted with the original `inputPath` JSON
request, result downloads do not remove the shared output directory, and
deleting the job record does not remove shared input/output directories.

### 6. Monitor and stop the service

Inspect API logs and running containers:

```sh
sudo docker logs --follow api-host
sudo docker ps --filter label=com.faolanbig.big-batch-ocr.managed=true
```

The API removes completed OCR containers after recording their exit result and
capturing up to 32 KiB of container output in the job database. The OCR image
also writes its own processing logs/state below each destination directory.
Plan retention and backups for both the host data directory and the Compose
`api-host-state` volume.

Stop with:

```sh
sudo docker stop api-host
sudo docker rm api-host
```

Stopping/removing the container preserves the `api-host-state` named volume.
Do not run `docker volume rm api-host-state` unless you intend to delete
persisted job history and queued work.

## Adjusting worker capacity

Change `MAX_CONCURRENT_JOBS` and/or `OCR_JOBS` in `.env`, then recreate the
single API-host container with the same image tag and mounts:

```sh
sudo docker rm -f api-host
sudo docker run -d --name api-host --restart unless-stopped \
  --env-file .env \
  -p 127.0.0.1:8080:8080 \
  --group-add "$(stat -c '%g' /var/run/docker.sock)" \
  -v /var/run/docker.sock:/var/run/docker.sock \
  -v /srv/big-batch-ocr-data:/data:rw \
  -v api-host-state:/var/lib/api-host \
  big-batch-ocr-api-host:direct
```

This configures two in-process queue workers, with each OCR container using
two OCRmyPDF threads. Watch CPU, memory, I/O, and the number of running
containers before increasing capacity further. Jobs are persisted in FIFO
order and no more than the configured number of workers will start jobs in
this API-host instance.

## Troubleshooting

| Symptom | Checks |
| --- | --- |
| API container exits during startup | Check `sudo docker logs api-host`; verify `API_KEY`, `OCR_IMAGE`, `DATA_ROOT`, and `DATABASE_PATH` configuration. |
| `permission denied` accessing Docker | Check `/var/run/docker.sock` permissions and `DOCKER_GID`; do not loosen socket permissions to world-writable. |
| Job rejected with `INVALID_PATH` | Use paths relative to `/data`; check existence, directory type, access permissions, and symlink targets. |
| Destination does not exist | Create it on the host or request `"createDestination":true`. |
| Job is still `queued` | Check that the API host is running and inspect its logs and Docker daemon availability. Workers pick up queued jobs when a slot becomes available. |
| Job is `failed` | Check the safe `error` and `exitCode` in the job resource, API logs, Docker daemon health, OCR image reference, and OCR logs under the destination. |
| Docker cannot bind a validated path | Confirm `DOCKER_DATA_ROOT` is the daemon-host absolute path and matches the host directory mounted at `/data`. |

For the full configuration reference, API examples, and Docker-socket security
notes, see [`deployment.md`](deployment.md).
