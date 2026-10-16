# API host deployment

## Build and runtime configuration

From the repository root, build either image:

```sh
# Standard build; does not pass a CA certificate secret.
bash api_host/build/build_dockerfile.sh

# Corporate TLS-inspection proxy build; passes the company CA as a BuildKit secret.
bash api_host/build/build_dockerfile_proxied_cert.sh
```

The standard script builds `big-batch-ocr-api-host:direct`. The proxied
script builds `big-batch-ocr-api-host:proxied` and uses the readable
certificate at `/usr/local/share/ca-certificates/root_cert.crt` as a
BuildKit secret. The CA is mounted only while Go downloads modules; it is not
copied into the resulting image. Neither script reads the runtime `.env`.

The same runtime setup works for either image because the CA secret only
changes how dependencies are downloaded during the build. The launcher below
selects the sole locally available image automatically, or accepts an image
tag when both are present.

Before starting, configure the runtime variables. These can be passed using a
protected `.env` file:

| Variable | Required | Default | Meaning |
| --- | --- | --- | --- |
| `API_KEY` | Yes unless open access is explicitly enabled | — | Bearer token accepted by job endpoints |
| `ALLOW_ANONYMOUS` | No | `false` | Set to `true` only on a trusted, isolated network |
| `ALLOW_DIRECTORY_ACCESS` | No | `false` | Set to `true` to allow jobs that refer to existing directories under `DATA_ROOT`; otherwise input must be uploaded as an archive |
| `DATA_ROOT` | No | `/data` | Existing data directory inside the API container |
| `DOCKER_DATA_ROOT` | No | `/srv/big-batch-ocr-data` in Compose | Same directory as seen by the Docker daemon |
| `DATABASE_PATH` | No | `/var/lib/api-host/jobs.db` | Persistent SQLite job database |
| `OCR_IMAGE` | Yes | — | OCR image reference with an explicit release tag or digest; do not use `latest` (prefer a digest for immutable pinning) |
| `OCR_JOBS` | No | `3` | OCRmyPDF worker threads passed to the OCR container |
| `OCR_LANGUAGE` | No | `eng` | Default OCRmyPDF language code or `+`-separated language codes, such as `deu+eng` |
| `OCR_OPTIMIZE` | No | `3` | Default OCRmyPDF optimization level (`0`–`3`) |
| `MAX_CONCURRENT_JOBS` | No | `1` | Maximum OCR containers running at once |
| `SYNC_WAIT_LIMIT_SECONDS` | No | `30` | Synchronous response wait limit; a timeout does not cancel the job |
| `API_ADDRESS` | No | `:8080` | HTTP listen address |
| `DOCKER_GID` | Compose only | — | Numeric Docker socket group ID; the run script derives it automatically |

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

`DOCKER_DATA_ROOT` is the absolute host path as seen by the Docker daemon; the
Compose bind mounts that host directory at `/data` inside the API container.
Keep the two roots aligned as shown so a validated relative path maps to the
same host files for the OCR container. The configurator creates the selected
data-root directory with mode `0750` and owner UID/GID `65532`, used by the API
and managed OCR containers. OCR containers run as UID/GID `65532` so files
they create can be removed by the non-root API host. If you use the manual
`.env` template workflow, create the root yourself with the same ownership and
permissions. Existing output files created by older root-running OCR
containers may still need a one-time ownership/permission repair before API
cleanup can remove them. Input is bind-mounted read-only at `/source`; output
is read-write at `/destination`.

For shared-directory jobs, the data-root ownership does not automatically
apply to existing child directories. The API can remove a shared input/output
directory during `POST /api/v1/jobs/cleanup` with `{"scope":"all"}` only if
UID/GID `65532` has write and execute permission on the directory's parent.
Recursive removal also requires write and execute permission on directories
inside the tree. Owning the target directory alone is not enough to remove it
from a root-owned parent. For example, for
`/srv/big-batch-ocr-data/incoming/sample`, make the parent and job directory
accessible to the API host:

```sh
sudo chown 65532:65532 /srv/big-batch-ocr-data/incoming
sudo chown -R 65532:65532 /srv/big-batch-ocr-data/incoming/sample
sudo chmod 0750 /srv/big-batch-ocr-data/incoming
sudo find /srv/big-batch-ocr-data/incoming/sample -type d -exec chmod 0750 {} +
```

Apply ownership changes only to directories you intend the API host to manage;
do not recursively change the entire data root just to enable cleanup. If
other users or services need access, use a shared group and grant the required
group write/execute permissions instead. Archive-upload workspaces are created
under `.api-host/jobs` for the API and managed OCR containers; files left
behind by earlier root-running OCR containers may need a targeted ownership
repair before `scope:"uploads"` cleanup can remove them.

The named `api-host-state` volume persists the SQLite job database. Back it up
with the host data. It retains container IDs and the last 32 KiB of captured
output per job; these diagnostics are internal and are not returned by the
status API. At startup, jobs left in `starting` or `running` are marked
failed because completion cannot be confirmed; their known managed containers
are stopped and removed when Docker is reachable. Queued jobs remain queued.
Completed OCR containers are removed after their outcome and logs are captured.
The OCR image stores its own processing logs in the mounted destination, so
apply retention controls to output directories as appropriate.

Create the runtime environment file with the guided configurator:

```sh
./api_host/config/configure_api_host.sh
```

The configurator prompts for an OCR image, API key (or generates one),
Docker-visible data root, worker settings, anonymous-access choice, and whether
directory-based submissions are allowed. The latter defaults to `false`, so
only uploaded archive submissions are accepted unless explicitly enabled. It
saves the file as `api_host/env/.env` with mode `600`. The data-root directory
is created by the configurator and made accessible to both the API and OCR
containers. The configurator detects the socket group ID when available; the
run launcher detects it at startup.
The selected OCR image does not need to be present locally when configuring:
the API host checks for it when the first job starts and pulls it if needed.
The Docker daemon must be able to reach the image registry for this first pull.

Alternatively, manually copy and edit the template:

```sh
cp api_host/env/.env_template api_host/env/.env
chmod 600 api_host/env/.env
```

Edit `api_host/env/.env`, replacing the sample `API_KEY`, `OCR_IMAGE`, and
`DOCKER_DATA_ROOT` values. Keep `DATA_ROOT=/data`. If using Compose, also
replace `DOCKER_GID` with `stat -c '%g' /var/run/docker.sock`. Use plain
`KEY=VALUE` syntax without `export`. Keep the secret file private and do not
commit it.

Start with the single launcher for either image:

```sh
./api_host/run/run_container.sh
```

If both image tags are installed, specify the desired tag:

```sh
./api_host/run/run_container.sh big-batch-ocr-api-host:proxied
```

The launcher uses the same runtime arguments for both images, calculates the
Docker socket group ID, and preserves job state in the `api-host-state` named
volume. If `api-host` is already running, it leaves it untouched. If that
container exists but is stopped, it replaces it so the current `env/.env`
settings are applied. For Compose deployment and its configuration, see
[`../docker/compose.yaml`](../docker/compose.yaml).

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

Each submission may include `language` and/or `optimize` to override these
defaults for that job. Language values are limited to letters, digits, and
underscores in one or more codes joined with `+`; optimization must be an
integer from `0` through `3`. The API rejects invalid values and does not
accept arbitrary OCRmyPDF or Docker arguments. For example, use
`"language":"deu+eng","optimize":2` in JSON, or the corresponding multipart
fields for an archive upload.

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

To upload the input instead of staging it on the shared volume, send a
multipart form request with `inputArchive` set to a `.tar.gz` file. Provide
`mode`; omit both `inputPath` and `outputPath`. The API creates an isolated
`.api-host/jobs/<jobId>/` workspace with separate `input` and `output`
directories:

```sh
curl --fail-with-body -i \
  -H "Authorization: ******" \
  -F "inputArchive=@invoices.tar.gz" \
  -F "mode=asynchronous" \
  http://127.0.0.1:8080/api/v1/jobs
```

The complete multipart request is limited to 1 GiB and archive extraction to
10 GiB. The server rejects links and archive paths that escape the extraction
directory. The JSON request with `inputPath` remains supported for files
already present under `DATA_ROOT`.

After polling until `status` is `succeeded`, download the output bundle using
the `jobId` returned by submission:

```sh
curl --fail-with-body \
  -H "Authorization: ******" \
  "http://127.0.0.1:8080/api/v1/jobs/job_ID_FROM_RESPONSE/result" \
  -o result.tar.gz
```

The result endpoint returns all files under the job's output directory as a
`.tar.gz` archive. It returns `409 JOB_NOT_COMPLETED` until the job succeeds.
Downloading does not delete job data: uploaded-job workspaces (including the
extracted input) remain available, and results can be downloaded repeatedly.
To delete an uploaded job's workspace and metadata, explicitly call the
terminal-job DELETE endpoint:

```sh
curl --fail-with-body -X DELETE \
  -H "Authorization: ******" \
  "http://127.0.0.1:8080/api/v1/jobs/job_ID_FROM_RESPONSE"
```

Alternatively, call `POST /api/v1/jobs/cleanup` with `{"scope":"uploads"}` to
delete all terminal uploaded jobs and their workspaces. Cancelling an uploaded
job also preserves its workspace until one of these cleanup API calls is made.
Existing jobs submitted with `inputPath` continue using the shared output
directory; deleting their job record does not remove those caller-managed
directories.

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
