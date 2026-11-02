# API host deployment

The API host has one image for Docker and Kubernetes deployments. It detects
Kubernetes in-cluster credentials at startup; otherwise, it uses Docker
Engine. There is no platform-selection environment variable.

## Docker image and runtime configuration

Use a published API-host image from
[GitHub Container Registry](https://github.com/FaolanBig/big-batch-ocr/pkgs/container/big-batch-ocr_api-host)
with an explicit release tag or immutable digest. There is no `latest` tag:

```sh
TAG=v1.2.9
API_IMAGE="ghcr.io/faolanbig/big-batch-ocr_api-host:$TAG"
sudo docker pull "$API_IMAGE"
```

### Build the image locally

From the repository root, run one build script:

```sh
# Standard build.
bash api_host/build/build_dockerfile.sh

# Or, when building behind a TLS-inspection proxy:
bash api_host/build/build_dockerfile_proxied_cert.sh
```

The standard script tags the image `big-batch-ocr-api-host:direct`; the
proxied script tags it `big-batch-ocr-api-host:proxied`. The proxy build
passes `/usr/local/share/ca-certificates/root_cert.crt` as a temporary
BuildKit secret only for Go module downloads; it is not copied into the image.
Neither build script reads runtime settings. The image includes `/bin/sh` and
`curl` for network troubleshooting and runs the API host as a non-root user.

Start a precompiled image by passing its tag to the launcher. For a local
image, omit the argument if exactly one local build is available, or specify
the desired tag if both are present:

```sh
./api_host/run/run_container.sh "$API_IMAGE"
# Or:
./api_host/run/run_container.sh big-batch-ocr-api-host:proxied
```

### Environment variables

Create `api_host/env/.env` with
[`configure_api_host.sh`](../config/configure_api_host.sh), or copy and edit
[`env/.env_template`](../env/.env_template). Protect the file; it contains the
API credential. The configurator creates the Docker data-root directory for
UID/GID `65532` and saves the environment file with mode `0600`.

| Variable | Required | Default | Meaning |
| --- | --- | --- | --- |
| `API_KEY` | Yes unless anonymous access is enabled | — | API key used to authenticate job requests. |
| `ALLOW_ANONYMOUS` | No | `false` | Allow requests without a key. Do not enable on an exposed network. |
| `ALLOW_DIRECTORY_ACCESS` | No | `false` | Permit shared-directory submissions. When false, submit an archive. |
| `LOG_LEVEL` | No | `INFO` | Structured JSON log verbosity: `DEBUG`, `INFO`, `WARN`, or `ERROR`. |
| `DATA_ROOT` | No | `/data` | Existing data directory inside the API container and OCR runtime. |
| `DOCKER_DATA_ROOT` | Docker only | `/srv/big-batch-ocr-data` in Compose | Absolute host path mounted at `DATA_ROOT`; ignored in Kubernetes. |
| `DATABASE_PATH` | No | `/var/lib/api-host/jobs.db` | SQLite job database; mount persistent storage at its parent directory. |
| `OCR_IMAGE` | Yes | — | OCR image reference with explicit version tag or digest; `latest` is rejected. |
| `OCR_JOBS` | No | `3` | OCRmyPDF threads inside each OCR container. |
| `OCR_LANGUAGE` | No | `eng` | Default language code or `+`-separated codes, for example `deu+eng`. |
| `OCR_OPTIMIZE` | No | `3` | Default OCRmyPDF optimization level (`0`–`3`). |
| `MAX_CONCURRENT_JOBS` | No | `1` | Maximum active OCR containers/Jobs for this API-host process. |
| `SYNC_WAIT_LIMIT_SECONDS` | No | `30` | Synchronous response wait limit; expiry does not cancel the job. |
| `API_ADDRESS` | No | `:8080` | HTTP listen address. |
| `DOCKER_GID` | Compose only | — | Numeric Docker socket group ID; the run launcher detects it. |
| `OCR_CPU_REQUEST` / `OCR_CPU_LIMIT` | Kubernetes only | unset | OCR Job CPU resources. |
| `OCR_MEMORY_REQUEST` / `OCR_MEMORY_LIMIT` | Kubernetes only | unset | OCR Job memory resources. |

Set all four `OCR_*_REQUEST` and `OCR_*_LIMIT` resource variables together or
leave them all unset. They use Kubernetes resource quantity syntax, must be
positive, and requests cannot exceed limits.

Use a secret manager for production API credentials. Restrict network exposure
and terminate TLS at a trusted reverse proxy for remote clients. The included
Compose service binds to localhost by default.

### Docker Compose and data paths

The Compose file at [`../docker/compose.yaml`](../docker/compose.yaml) mounts
the Docker socket, host data root at `/data`, and a named volume at
`/var/lib/api-host`. It uses `DOCKER_GID` for socket access. The launcher
calculates the socket group and creates/reuses the `api-host-state` named
volume automatically.

`DOCKER_DATA_ROOT` must be the same absolute host directory that the Docker
daemon can bind-mount and the API sees at `DATA_ROOT`. The configurator
creates it with mode `0750`, owned by UID/GID `65532`, used by the API and OCR
containers. Do not make the Docker socket world-writable: socket access is
effectively root-equivalent on the host. A caller who can submit jobs can
cause the configured OCR image to run, so restrict API callers and trusted
image sources.

The API database volume preserves the queue and job records across normal
container replacement. Back it up together with job data. On startup, queued
jobs remain queued; jobs left in `starting` or `running` are marked failed
because completion cannot be confirmed. Completed OCR containers are removed
after their outcome and up to 32 KiB of runtime output are recorded. The OCR
image stores its processing logs under the job output directory.

## Kubernetes deployment

[`../k8s/api-host.yaml`](../k8s/api-host.yaml) deploys one API-host pod with a
ClusterIP Service, namespace-scoped Role/RoleBinding, a NetworkPolicy for OCR
Job pods, a data PVC, and a separate SQLite-state PVC. Update the image
references and storage class names for your release and cluster before
applying the manifest. The sample requests:

- `ReadWriteMany` data storage (`api-host-data`, 50 GiB), mounted by the API
  and OCR pods.
- `ReadWriteOnce` SQLite state storage (`api-host-state`, 1 GiB).
- Four CPUs and 4 GiB memory for the API pod.
- 10 CPUs and 2 GiB memory requested per OCR Job, with limits of 16 CPUs and
  4 GiB.
- `MAX_CONCURRENT_JOBS=10` and `OCR_JOBS=12` in each OCR container.

The sample StorageClass names are cluster-specific; replace them with classes
that provide the required access modes. The API and its OCR pods must be able
to read/write the data PVC with the configured UID/GID. Keep `replicas: 1`:
the queue and concurrency limit are local to one API-host process. The
included NetworkPolicy denies ingress and egress for OCR Job pods and requires
a CNI that enforces NetworkPolicy. OCR pods have no service-account token.

Create an API-key Secret in the deployment namespace:

```sh
kubectl create secret generic api-host-secrets \
  --from-literal=API_KEY="$(openssl rand -hex 32)"
```

Store production credentials in your cluster's secret-management system.
Update the API-host and OCR image references in the manifest, check its
resource requests, storage class names, and networking for your cluster, then
apply and test:

```sh
kubectl apply -f api_host/k8s/api-host.yaml
kubectl rollout status deployment/api-host
kubectl port-forward service/api-host 8080:8080
curl --fail http://127.0.0.1:8080/health
```

The API host uses its service account and Kubernetes environment to identify
the platform and create OCR Jobs in the same namespace. The cluster pulls the
OCR image when scheduling a Job; ensure the image is accessible to the nodes
or provide image-pull credentials. Kubernetes deployments do not use
`DOCKER_GID`, `DOCKER_DATA_ROOT`, or a Docker socket.

## API usage

The complete contract, including all endpoints and schemas, is in the
[OpenAPI specification](../api/api_specification/main_api_specification_api_host.json).
Health is unauthenticated and reports process liveness:

```sh
curl --fail http://127.0.0.1:8080/health
```

### Recommended: upload an archive

Create a `.tar.gz` file from the input directory and attach it directly to the
job submission. No shared input/output directories need to be prepared on the
server:

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

Poll `statusUrl` until the job reaches `succeeded` or `failed`, then download
the result:

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

curl --fail-with-body --silent --show-error \
  --oauth2-bearer "$API_KEY" \
  "http://127.0.0.1:8080/api/v1/jobs/${job_id}/result" \
  -o "result-${job_id}.tar.gz"
```

Multipart requests are limited to 1 GiB and extracted archive contents to
10 GiB. Only regular files and directories are accepted in an archive. The
API retains each uploaded-job workspace after download so clients can
download again. Delete one terminal job with
`DELETE /api/v1/jobs/{jobId}`, or remove every terminal uploaded job with
`POST /api/v1/jobs/cleanup` and `{"scope":"uploads"}`. Cancellation also
retains data until cleanup.

For synchronous execution, send the same multipart fields with
`mode=synchronous`. Success returns the output archive and the job ID in
`X-Job-ID`. If `SYNC_WAIT_LIMIT_SECONDS` expires, the job continues and a
`504` response includes its job ID and status URL; poll the job instead of
submitting it again.

Multipart clients that do not send a file object can use
`inputArchiveContents` for raw archive bytes or
`inputArchiveContentsBase64` for standard Base64-encoded archive bytes. For
example, automation platforms can supply either field from their own file or
compression action.

### Optional: shared-directory submissions

Set `ALLOW_DIRECTORY_ACCESS=true` only when trusted callers specifically need
to use pre-staged data. JSON requests then identify input and output
directories relative to `DATA_ROOT`:

```sh
curl --fail-with-body --silent --show-error \
  --oauth2-bearer "$API_KEY" \
  -H "Content-Type: application/json" \
  -d '{"inputPath":"incoming/invoices","outputPath":"processed/invoices","mode":"asynchronous"}' \
  http://127.0.0.1:8080/api/v1/jobs
```

Paths must remain within `DATA_ROOT`. The input directory must exist and be
readable. The output must exist unless `createDestination: true` is included.
Traversal, symlink escapes, identical paths, and nested input/output paths
are rejected. `DELETE /api/v1/jobs/{jobId}` does not remove shared
directories. Cleanup scope `all` does remove the entire caller-specified
input/output directories for eligible terminal jobs; treat it as destructive.

## Webhooks

Submissions can set `webhookUrl` to receive one POST with the terminal job
resource as JSON. Optional `webhookSecret` and `rootPath` are sent as
`X-Webhook-Secret` and `X-Root-Path` headers; `rootPath` is also included in
the JSON body. These fields require `webhookUrl`. HTTPS certificate
verification is on by default. `webhookInsecureSkipVerify: true` disables
verification only for that job's webhook request and should be used only for
a trusted self-signed endpoint. Delivery is best-effort, with one attempt,
10-second timeout, and no redirect following; failures do not change the job
outcome.

## Security and operations

Keep the API private and protect the bearer key. API access to the Docker
socket is root-equivalent on the Docker host. Restrict API access, configured
OCR images, and data-root permissions. Terminate TLS at a trusted reverse
proxy if clients connect remotely.

The API writes structured JSON logs to standard output. Inspect them with
`docker logs api-host` or `kubectl logs deployment/api-host`. The public job
resource reports safe error details; captured runtime output is retained
internally in the persistent database. Back up the database volume and data
root, and define retention for job workspaces.
