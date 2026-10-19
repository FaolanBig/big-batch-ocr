# Run OCR in Docker

## Use the published image

Images are published to GitHub Container Registry as `ghcr.io/faolanbig/big-batch-ocr_ocr-sequencial`. Each published Git tag has a matching image tag; for stability and security reasons there is no `latest` tag. Replace `TAG` below with the tag of the release you want to use:

```bash
TAG=v1.1.0
IMAGE="ghcr.io/faolanbig/big-batch-ocr_ocr-sequencial:$TAG"

docker pull "$IMAGE"
docker run --rm -it \
	-v /path/to/input:/source:ro \
	-v /path/to/output:/destination \
	-e OCR_JOBS=4 \
	-e OCR_LANGUAGE=deu+eng \
	-e OCR_OPTIMIZE=2 \
	"$IMAGE"
```

The example `v1.1.0` is a release tag; use a tag that exists in the [package registry](https://github.com/FaolanBig/big-batch-ocr/pkgs/container/big-batch-ocr_ocr-sequencial). The image starts OCR automatically. The input directory is mounted read-only at `/source`, and OCR output, state markers, and logs are written under `/destination`.

## Build the image

From the `ocr_batcher/` directory (change into it first if you're at the repository root):

```bash
cd docker
sudo bash build_dockerfile.sh
```

This builds the image `ocr_batch_sequencial:v1.1.0`.

## Start a container

Mount the input directory read-only at `/source` and an output directory at `/destination`:

```bash
docker run --rm -it \
	-v /path/to/input:/source:ro \
	-v /path/to/output:/destination \
	-e OCR_JOBS=4 \
	ocr_batch_sequencial:v1.1.0
```

Replace the example host paths and adjust `OCR_JOBS` to set the number of OCR threads. If omitted, the script uses 75% of the logical CPU count reported by `nproc --all`.
`OCR_LANGUAGE` defaults to `eng` and accepts installed OCRmyPDF/Tesseract
language codes joined by `+`; `OCR_OPTIMIZE` defaults to `3` and accepts `0`
through `3`. The container entrypoint also accepts the explicit
`--language CODE` and `--optimize LEVEL` options, which override these
environment defaults. Other OCRmyPDF arguments are not accepted.

For example, the same configuration can be supplied as entrypoint options:

```bash
docker run --rm -it \
	-v /path/to/input:/source:ro \
	-v /path/to/output:/destination \
	"$IMAGE" --language deu+eng --optimize 2
```

## Run the script

The container starts the script automatically (no tmux needed). It scans `/source`, OCRs PDF files, and copies other files to `/destination`. Script messages are shown in the terminal and also written to a log file; OCRmyPDF output is written to the log file only. The container does not display a progress pane. Logs are stored in `/destination/.ocr_sequencial_state/logs` (override with `-e LOG_DIR=...`). Completed OCR files and state markers are written to the output directory.
