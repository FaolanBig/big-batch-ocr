# Big Batch OCR

Big Batch OCR recursively processes document folders. It OCRs PDF files with
[OCRmyPDF](https://ocrmypdf.readthedocs.io/) and copies non-PDF files while
preserving their relative directory structure. Run the batcher directly on
Linux, in Docker, or submit jobs to the API host.

## Features

- PDFs are recognized by a case-insensitive `.pdf` filename suffix and
  processed sequentially. OCRmyPDF may use multiple worker threads per PDF.
- The batcher rotates, deskews, cleans, optimizes, and skips pages that already
  contain text. Language and optimization level are configurable.
- Files are published from temporary paths. PDF completion markers include
  the source checksum and OCR language/optimization settings, enabling safe
  resumable runs. Identical non-PDF files are skipped.
- The API host accepts direct `.tar.gz` uploads by default, persists jobs in
  SQLite, and runs them through a bounded queue on Docker Engine or Kubernetes.
  Directory-based requests are optional and disabled by default.

## Choose how to run it

| Option | Best for | Requirements |
| --- | --- | --- |
| [Terminal script](#run-the-batcher-in-a-terminal) | Local or server-side folder batches | Linux, Bash, OCRmyPDF and dependencies, tmux |
| [Docker image](#run-the-batcher-in-docker) | Reproducible runs without installing OCR tools on the host | Docker |
| [API host](#run-the-api-host) | Automation and queued, trackable jobs | Docker Engine, or Kubernetes with shared persistent storage |

## Run the batcher in a terminal

The current native entry point is
[`ocr_batcher/src/ocr_sequencial.sh`](./ocr_batcher/src/ocr_sequencial.sh).
Start it from an existing tmux session:

```bash
git clone https://github.com/FaolanBig/big-batch-ocr.git
cd big-batch-ocr
tmux new -s ocr
```

In that tmux session, run:

```bash
bash ocr_batcher/src/ocr_sequencial.sh "/path/to/input" "/path/to/output"
```

Set OCR worker threads and OCR options as needed:

```bash
OCR_JOBS=4 bash ocr_batcher/src/ocr_sequencial.sh \
  --language deu+eng --optimize 2 \
  "/path/to/input" "/path/to/output"
```

`OCR_JOBS` defaults to 75% of the available logical CPUs. `OCR_LANGUAGE` and
`OCR_OPTIMIZE` default to `eng` and `3`; command-line options override these
environment values. The source must exist, and the target must not be the same
as or inside the source. The target is created if needed. The script displays
progress and OCR output in tmux panes. See the [batcher guide](./ocr_batcher/README.md)
and [batcher documentation index](./ocr_batcher/doc/HOME.md) for requirements,
binary builds, logs, failures, and resume behavior.

The native script can offer to install missing dependencies on supported
Linux distributions. Git and tmux must be installed before it can run; install
dependencies with
[`ocr_batcher/dependencies/install_dependencies.sh`](./ocr_batcher/dependencies/install_dependencies.sh)
if required. See the [dependency notes](./ocr_batcher/dependencies/README.md).

## Run the batcher in Docker

The published container starts OCR automatically. Replace the example tag
with a release available in the
[GitHub Container Registry](https://github.com/FaolanBig/big-batch-ocr/pkgs/container/big-batch-ocr_ocr-sequencial);
there is no `latest` tag.

```bash
TAG=v1.2.9
docker run --rm -it \
  -v "/path/to/input:/source:ro" \
  -v "/path/to/output:/destination" \
  -e OCR_JOBS=4 \
  -e OCR_LANGUAGE=deu+eng \
  -e OCR_OPTIMIZE=2 \
  "ghcr.io/faolanbig/big-batch-ocr_ocr-sequencial:$TAG"
```

Input is mounted read-only at `/source`; output, state, and logs are stored
under `/destination`. For local builds and additional options, see
[Run OCR in Docker](./ocr_batcher/doc/run_with_docker.md).

## Run the API host

The same API-host image supports Docker and Kubernetes. It detects Kubernetes
from its in-cluster service account; otherwise it runs OCR through Docker
Engine. For Kubernetes deployment, use
[`api_host/k8s/api-host.yaml`](./api_host/k8s/api-host.yaml) and the
[Kubernetes deployment guide](./api_host/doc/deployment.md#kubernetes-deployment).
The cluster must provide shared storage mounted at `DATA_ROOT` for the API pod
and OCR Job pods.

For a Docker deployment on a Linux host, create the runtime configuration:

```bash
./api_host/config/configure_api_host.sh
```

The configurator prompts for a versioned OCR image and API key, creates the
selected data root with permissions for UID/GID `65532`, and writes a private
`api_host/env/.env` file. Directory submissions default to disabled. The API
image can be pulled from the
[API-host package registry](https://github.com/FaolanBig/big-batch-ocr/pkgs/container/big-batch-ocr_api-host)
and started with:

```bash
TAG=v1.2.9
API_IMAGE="ghcr.io/faolanbig/big-batch-ocr_api-host:$TAG"
sudo docker pull "$API_IMAGE"
./api_host/run/run_container.sh "$API_IMAGE"
```

The configured OCR image is pulled by Docker on first use if it is not already
available. For self-built images, API settings, Compose, Kubernetes, TLS, and
security requirements, follow the [API-host deployment guide](./api_host/doc/deployment.md)
and [quickstart](./api_host/doc/quickstart-and-worker-setup.md).

### Submit an archive directly to the API

Create a `.tar.gz` archive of the input directory and upload it in the request.
The API creates an isolated workspace; no input/output directories need to be
pre-staged under the server's data root. The command example uses `curl` and
`jq`.

```bash
read -r -s -p "API key: " API_KEY
printf '\n'

tar -czf /tmp/scans.tar.gz -C "/path/to/scans" .
response="$(
  curl --fail-with-body --silent --show-error \
    --oauth2-bearer "$API_KEY" \
    -F "inputArchive=@/tmp/scans.tar.gz;type=application/gzip" \
    -F "mode=asynchronous" \
    -F "language=deu+eng" \
    -F "optimize=2" \
    http://127.0.0.1:8080/api/v1/jobs
)"
printf '%s\n' "$response" | jq .
JOB_ID="$(printf '%s\n' "$response" | jq -r .jobId)"
STATUS_URL="http://127.0.0.1:8080$(printf '%s\n' "$response" | jq -r .statusUrl)"
```

Poll until the job succeeds or fails, then download the result archive:

```bash
while :; do
  status="$(curl --fail-with-body --silent --show-error \
    --oauth2-bearer "$API_KEY" "$STATUS_URL")"
  printf '%s\n' "$status" | jq .
  state="$(printf '%s\n' "$status" | jq -r .status)"
  case "$state" in
    succeeded|failed) break ;;
    *) sleep 2 ;;
  esac
done

curl --fail-with-body --silent --show-error \
  --oauth2-bearer "$API_KEY" \
  "http://127.0.0.1:8080/api/v1/jobs/$JOB_ID/result" \
  -o "result-$JOB_ID.tar.gz"
```

The upload body is limited to 1 GiB and extracted archive contents to 10 GiB.
Downloads do not delete job data; remove a terminal uploaded job with
`DELETE /api/v1/jobs/{jobId}`, or clean up all terminal uploaded jobs with
`POST /api/v1/jobs/cleanup` and `{"scope":"uploads"}`. Synchronous mode returns
the result archive directly; if its wait limit expires, the job continues and
the response includes a status URL. Poll it rather than submitting again.
See the [API specification](./api_host/api/api_specification/main_api_specification_api_host.json)
for all fields and endpoints.

Shared-directory submissions are still supported when
`ALLOW_DIRECTORY_ACCESS=true`; keep them disabled unless trusted callers
specifically need them. The API validates such paths beneath `DATA_ROOT`.
The API host has access to Docker or can create Kubernetes Jobs, so restrict
network exposure and protect the API key. Do not run multiple API hosts
against one database; the queue and concurrency limit belong to one process.

## Documentation

- [OCR batcher guide](./ocr_batcher/README.md)
- [OCR batcher documentation index](./ocr_batcher/doc/HOME.md)
- [Docker usage](./ocr_batcher/doc/run_with_docker.md)
- [API-host quickstart](./api_host/doc/quickstart-and-worker-setup.md)
- [API-host deployment and configuration](./api_host/doc/deployment.md)
- [OpenAPI contract](./api_host/api/api_specification/main_api_specification_api_host.json)

## License

This project is licensed under the [GNU Affero General Public License,
version 3](./LICENSE).

Copyright © Carl Öttinger 2026
