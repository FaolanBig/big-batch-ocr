# Big Batch OCR

Big Batch OCR processes document folders in batches. It recursively finds PDF
files, applies OCR with [OCRmyPDF](https://ocrmypdf.readthedocs.io/), and copies
non-PDF files to a separate destination while preserving the directory
structure. Run the batcher directly on Linux, in Docker, or submit jobs through
the Docker-based API host.

## What it does

- **Batch folder processing:** scans nested directories and recognizes PDFs by
  their case-insensitive `.pdf` filename extension.
- **OCR and file copying:** processes PDFs with page rotation and deskewing,
  image cleanup, optimization, and skipping of pages that already contain
  text. Non-PDF files are copied without changing their contents.
- **Safe output publishing:** writes files to temporary paths before moving
  completed results into place, helping avoid partial output files.
- **Resume support:** records a SHA-256 signature for each completed PDF and
  skips it on later runs only when the output and source signature still match.
  Identical non-PDF files are also skipped.
- **Progress and diagnostics:** native runs display progress and OCR output in
  tmux panes; Docker runs write logs alongside the output by default.
- **API job handling:** the API host authenticates requests by bearer key,
  validates paths under a configured data root, persists jobs in SQLite, and
  runs OCR containers through a configurable queue.

Files are processed sequentially; `OCR_JOBS` controls OCRmyPDF's internal
worker threads for each PDF, not the number of files processed at once. The API
host can run multiple OCR containers at once with `MAX_CONCURRENT_JOBS`.

## Choose how to run it

| Option | Best for | Requirements |
| --- | --- | --- |
| [Terminal script](#1-run-the-batcher-in-a-terminal) | Local or server-side folder batches | Linux, Bash, OCRmyPDF and dependencies, tmux |
| [Docker image](#2-run-the-batcher-in-docker) | Reproducible runs without installing OCR tools on the host | Docker |
| [API host](#3-run-the-api-host) | Automation platforms and queued, trackable jobs | Linux Docker host and trusted Docker access |

## Quick start

### 1. Run the batcher in a terminal

The native script is [`ocr_batcher/src/ocr_sequencial.sh`](./ocr_batcher/src/ocr_sequencial.sh).
It requires Bash, OCRmyPDF and its dependencies, `tmux`, and common Linux
utilities. Start it from an existing tmux session:

```bash
git clone https://github.com/FaolanBig/big-batch-ocr.git
cd big-batch-ocr
tmux new -s ocr
```

In the tmux session, run:

```bash
bash ocr_batcher/src/ocr_sequencial.sh "/path/to/input" "/path/to/output"
```

You can set the number of OCRmyPDF worker threads with `OCR_JOBS` (default:
75% of the available logical CPUs):

```bash
OCR_JOBS=4 bash ocr_batcher/src/ocr_sequencial.sh "/path/to/input" "/path/to/output"
```

To select installed OCRmyPDF/Tesseract languages and the optimization level,
use `--language CODE[+CODE...]` and `--optimize 0..3` (or set
`OCR_LANGUAGE`/`OCR_OPTIMIZE`; defaults are `eng` and `3`):

```bash
bash ocr_batcher/src/ocr_sequencial.sh --language deu+eng --optimize 2 "/path/to/input" "/path/to/output"
```

The source directory must exist. The output directory is created if needed.
They must be different directories, and the output must not be inside the
input. Progress and live OCR output appear in tmux panes. See the
[batcher guide](./ocr_batcher/README.md) for details, including binary builds,
logs, error handling, and resume behavior.

For this **bare-metal Bash script only**, missing dependencies are detected at
startup and the script asks before running the distro-specific installer. The
installer supports Debian/Ubuntu, openSUSE, Fedora, RHEL, and Arch Linux;
administrator privileges may be required (Arch requires running as a regular
user with `sudo` configured). If you decline, install the dependencies
manually before retrying. This does not apply to the Docker image or API host,
which use their own dependencies. See
[`ocr_batcher/dependencies/README.md`](./ocr_batcher/dependencies/README.md)
for package, license, and copyright details.

Before the bare-metal script can operate, install **Git** and **tmux**. The
script must run inside a tmux session, so if tmux is missing, run
`ocr_batcher/dependencies/install_dependencies.sh` manually first, then start
tmux and launch the script from that session. Git is needed by the Arch
dependency-install path to fetch OCRmyPDF from the AUR.

### 2. Run the batcher in Docker

The published image starts OCR automatically. The tag used here will pull the base release. 
Replace the example tag with a version available in the
[GitHub Container Registry](https://github.com/FaolanBig/big-batch-ocr/pkgs/container/big-batch-ocr) if you want a specific release;
published tags correspond to Git tags, and there is no `latest` tag.

```bash
TAG=v1.1.0
docker run --rm -it \
  -v "/path/to/input:/source:ro" \
  -v "/path/to/output:/destination" \
  -e OCR_JOBS=4 \
  -e OCR_LANGUAGE=deu+eng \
  -e OCR_OPTIMIZE=2 \
  "ghcr.io/faolanbig/big-batch-ocr:$TAG"
```

The paths shown are examples; choose a published version and replace the host
directories. The input is mounted read-only, while OCR output, resume state,
and logs are stored under the output directory. If you prefer to build locally,
use the batcher's
[`Dockerfile`](./ocr_batcher/docker/Dockerfile) from `ocr_batcher/docker`:

```bash
cd ocr_batcher/docker
sudo bash build_dockerfile.sh
```

Then run the local `ocr_batch_sequencial:v1.1.0` image with the same volume
mounts. More options are in [Run OCR in Docker](./ocr_batcher/doc/run_with_docker.md).

### 3. Run the API host

The API host accepts either a shared-directory input or an uploaded `.tar.gz`
archive, starts the configured OCR image using Docker, records jobs in a
persistent SQLite database, and exposes synchronous and asynchronous modes.
Uploaded jobs receive isolated input/output folders, and the result can be
downloaded by job ID; their workspace is removed after a successful download.
The following is a quick outline; follow the complete
[API host deployment guide](./api_host/doc/deployment.md) before exposing or
operating the service.

Configure the API on a Linux Docker host:

```bash
./api_host/config/configure_api_host.sh
```

The configurator asks for a versioned OCR image, creates or accepts an API key,
creates the data root with permissions for UID/GID 65532, and writes a private
`api_host/env/.env` file. If you plan to use shared-directory submissions,
create input/output child folders and place source PDFs there:

```bash
sudo install -d -m 0750 -o 65532 -g 65532 \
  /srv/big-batch-ocr-data/incoming/scans \
  /srv/big-batch-ocr-data/processed
```

Replace the example root if you selected a different one. Archive uploads do
not need these child folders; the API creates isolated per-job input/output
directories automatically. The configured OCR image is pulled on first use if
it is not already available locally. Build the API image and start the host:

```bash
bash api_host/build/build_dockerfile.sh
./api_host/run/run_container.sh
```

The API listens on `127.0.0.1:8080` by default. Check liveness and submit an
asynchronous job (replace the paths and key):

```bash
read -r -s -p "API key: " API_KEY
printf '\n'

curl --fail http://127.0.0.1:8080/health

curl --fail-with-body -i \
  -H "Authorization: Bearer $API_KEY" \
  -H "Content-Type: application/json" \
  -d '{"inputPath":"incoming/scans","outputPath":"processed/scans","mode":"asynchronous","createDestination":true,"language":"deu+eng","optimize":2}' \
  http://127.0.0.1:8080/api/v1/jobs
```

The accepted response includes a `jobId` and `statusUrl`. Copy that URL into
`STATUS_URL` and poll it using the same bearer authorization header until the
job reaches `succeeded` or `failed`:

```bash
STATUS_URL='http://127.0.0.1:8080/api/v1/jobs/<job-id-from-response>'
curl --fail-with-body \
  -H "Authorization: Bearer $API_KEY" \
  "$STATUS_URL"
```

  For uploaded archive submissions, send `inputArchive` and `mode` as multipart
  form data and omit `inputPath` and `outputPath`. After the job succeeds,
  download `/api/v1/jobs/<jobId>/result` to retrieve a `.tar.gz` of the processed
  files. See the [API host quickstart](./api_host/doc/quickstart-and-worker-setup.md)
  for complete upload, polling, and download examples.

  To wait for completion in the submit request instead, use
`"mode":"synchronous"`; if the configured wait limit is reached, the job
continues and the response provides a status URL to poll.

The API rejects paths outside the configured data root, path traversal and
symlink escapes, unreadable inputs, and identical or nested input/output
directories. Missing output directories are created only when
`"createDestination":true` is provided. Restrict network access and protect
the API key: access to the Docker socket grants broad control over the Docker
host. See the [API contract](./api_host/api/api_specification/main_api_specification_api_host.json)
and [deployment guide](./api_host/doc/deployment.md) for configuration,
security, and operational limits.

## Behavior and limitations

- Directory structure is preserved in the output. PDFs are processed one at
  a time; files are identified by extension, not by inspecting PDF contents.
- A failed file does not stop the rest of the batch. The process exits
  nonzero if any file fails.
- Deleting `.ocr_sequencial_state` removes PDF completion markers and causes
  those PDFs to be processed again.
- The API host's queue and concurrency limit are local to one API-host
  instance. Do not run multiple API hosts against the same job database.
- The API host controls Docker and should run only on a trusted host and
  network; use a protected secret for `API_KEY` and terminate TLS at a trusted
  reverse proxy for remote access.

## Documentation

- [Batcher guide](./ocr_batcher/README.md)
- [Batcher documentation index](./ocr_batcher/doc/HOME.md)
- [Docker usage](./ocr_batcher/doc/run_with_docker.md)
- [API host quickstart and worker setup](./api_host/doc/quickstart-and-worker-setup.md)
- [API host deployment](./api_host/doc/deployment.md)
- [API specification](./api_host/api/api_specification/main_api_specification_api_host.json)

## License

This project is licensed under the [GNU Affero General Public License,
version 3](./LICENSE).

## Copyright

Copyright © Carl Öttinger 2026
