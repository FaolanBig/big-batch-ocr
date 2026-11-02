# Run OCR in Docker

The OCR batcher image starts processing automatically. It expects the input
directory at `/source` and the output directory at `/destination`; mount the
input read-only where possible.

## Use a published image

Images are published to
[GitHub Container Registry](https://github.com/FaolanBig/big-batch-ocr/pkgs/container/big-batch-ocr_ocr-sequencial).
Use an available release tag; there is no `latest` tag:

```bash
TAG=v1.2.9
IMAGE="ghcr.io/faolanbig/big-batch-ocr_ocr-sequencial:$TAG"

docker pull "$IMAGE"
docker run --rm -it \
  -v "/path/to/input:/source:ro" \
  -v "/path/to/output:/destination" \
  -e OCR_JOBS=4 \
  -e OCR_LANGUAGE=deu+eng \
  -e OCR_OPTIMIZE=2 \
  "$IMAGE"
```

Replace the example tag and host paths with values for your deployment. The
source must exist. The target must not be the same as or inside the source.

## Build locally

From the repository root, build the image with:

```bash
cd ocr_batcher/docker
sudo bash build_dockerfile.sh
```

The script builds `ocr_batch_sequencial:v1.2.9`. Run it with the same mounts:

```bash
docker run --rm -it \
  -v "/path/to/input:/source:ro" \
  -v "/path/to/output:/destination" \
  -e OCR_JOBS=4 \
  ocr_batch_sequencial:v1.2.9
```

## Configure the run

`OCR_JOBS` sets the positive-integer worker count used inside OCRmyPDF. When
unset, it defaults to 75% of the logical CPU count reported by
`nproc --all`. `OCR_LANGUAGE` defaults to `eng` and accepts installed
OCRmyPDF/Tesseract language codes joined by `+`. `OCR_OPTIMIZE` defaults to
`3` and accepts `0` through `3`. These values can also be supplied as
entry-point options:

```bash
docker run --rm -it \
  -v "/path/to/input:/source:ro" \
  -v "/path/to/output:/destination" \
  "$IMAGE" --language deu+eng --optimize 2
```

No other OCRmyPDF arguments are configurable. Files remain sequential; the
worker count applies within each PDF.

## Logs and output

The container does not require tmux and does not display a progress pane.
Script messages are printed to the container output; OCRmyPDF's detailed
output is written to the run log. Logs and PDF completion markers are stored
under `/destination/.ocr_sequencial_state/logs` and
`/destination/.ocr_sequencial_state`, respectively. Override the log
directory with `LOG_DIR`. The input directory is not modified when mounted
read-only.
