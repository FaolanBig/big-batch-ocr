# Run OCR in Docker

## Use the published image

Images are published to GitHub Container Registry as `ghcr.io/faolanbig/big-batch-ocr`. Each published Git tag has a matching image tag; for stability and security reasons there is no `latest` tag. Replace `TAG` below with the tag of the release you want to use:

```bash
TAG=v1.2.3
IMAGE="ghcr.io/faolanbig/big-batch-ocr:$TAG"

docker pull "$IMAGE"
docker run --rm -it \
	-v /path/to/input:/source:ro \
	-v /path/to/output:/destination \
	-e OCR_JOBS=4 \
	"$IMAGE"
```

The example `v1.2.3` is a placeholder; use a tag that exists in the [package registry](https://github.com/FaolanBig/big-batch-ocr/pkgs/container/big-batch-ocr). The image starts OCR automatically. The input directory is mounted read-only at `/source`, and OCR output, state markers, and logs are written under `/destination`.

## Build the image

From the `docker` directory (the build script uses the current directory as its build context):

```bash
cd docker
sudo bash build_dockerfile.sh
```

This builds the image `ocr_batch_sequencial:1.0.0`.

## Start a container

Mount the input directory read-only at `/source` and an output directory at `/destination`:

```bash
docker run --rm -it \
	-v /path/to/input:/source:ro \
	-v /path/to/output:/destination \
	-e OCR_JOBS=4 \
	ocr_batch_sequencial:1.0.0
```

Replace the example host paths and adjust `OCR_JOBS` to set the number of OCR threads. If omitted, the script uses 75% of the available CPUs.

## Run the script

The container starts the script automatically (no tmux needed). It scans `/source`, OCRs PDF files, and copies other files to `/destination`. Script messages are shown in the terminal and also written to a log file; the ocrmypdf output and progress are only written to the log file. Logs are stored in `/destination/.ocr_sequencial_state/logs` (override with `-e LOG_DIR=...`). Completed OCR files and state markers are written to the output directory.
