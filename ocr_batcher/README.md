# OCR batcher

The batcher recursively processes a source directory and writes results to a
target directory with the relative folder structure preserved. It processes
PDFs one at a time with OCRmyPDF and copies non-PDF files unchanged.

## Supported entry points

- Native Bash: [`src/ocr_sequencial.sh`](src/ocr_sequencial.sh)
- Compiled native binary: [`bin/ocr_sequencial`](bin/ocr_sequencial), built
  from the Bash script with `shc`
- Docker: [`docker/ocr_sequencial_docker.sh`](docker/ocr_sequencial_docker.sh),
  started automatically by the published image

[`src/ocr.sh`](src/ocr.sh) is deprecated. Use one of the current entry points.

## Quick start: native script

The script requires Linux, Bash, OCRmyPDF and its dependencies, tmux, and
common utilities. It must run in an existing tmux session:

```bash
tmux new -s ocr
# In the tmux session:
bash src/ocr_sequencial.sh "/path/to/source" "/path/to/target"
```

The source directory must exist. The target is created if needed, and must
not be the same as or inside the source. The script opens tmux panes for
progress and OCR output.

The defaults are `OCR_JOBS` = 75% of logical CPUs,
`OCR_LANGUAGE=eng`, and `OCR_OPTIMIZE=3`. Set them using environment variables
or the supported command-line options:

```bash
OCR_JOBS=4 bash src/ocr_sequencial.sh \
  --language deu+eng --optimize 2 \
  "/path/to/source" "/path/to/target"
```

`--language` (or `-l`) accepts installed OCRmyPDF/Tesseract language codes
joined by `+`; `--optimize` accepts `0` through `3`. These are the only
configurable OCRmyPDF options. `OCR_JOBS` must be a positive integer. Files
are sequential; `OCR_JOBS` controls the internal OCRmyPDF worker threads.

When required commands are missing, the native script offers to run the
distro-specific installer if an interactive terminal is available. See
[`dependencies/README.md`](dependencies/README.md) for supported Linux
distributions and package details. Git and tmux must be installed before
starting the native workflow. To build the optional binary, install `shc`
and run `./build/build.sh`; build it on the platform where it will be used.

## Quick start: Docker

The published image starts OCR automatically and does not require tmux on the
host. Replace the example release tag with one available in the
[container registry](https://github.com/FaolanBig/big-batch-ocr/pkgs/container/big-batch-ocr_ocr-sequencial).
There is no `latest` tag.

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

The container uses fixed paths `/source` and `/destination`. Logs and resume
state are stored under the destination directory. See
[Run OCR in Docker](doc/run_with_docker.md) for local builds, logging, and
runtime options.

## Processing and resume behavior

PDF detection uses a case-insensitive `.pdf` suffix, not file-content
inspection. All regular files are discovered recursively. Non-PDF files are
copied first; identical existing outputs are skipped. PDFs are processed
afterward with page rotation, deskew, image cleanup, optimization, and
`--skip-text`. Successful output is published from a temporary file.

The hidden target directory `.ocr_sequencial_state` stores a marker for each
PDF. A marker matches the source path, SHA-256 checksum, OCR language, and
optimization level. A PDF is skipped only if its output exists and the marker
matches; changing source content or either OCR setting causes it to be
processed again. Deleting the state directory forces PDFs to be reprocessed.

Per-file errors do not stop the rest of the batch. The script exits `0` if all
files succeed and `1` if any copy or OCR operation fails. Signal interruptions
use conventional statuses `129`, `130`, or `143`.

## Documentation

- [Documentation index](doc/HOME.md)
- [Script overview](doc/script_overview.md)
- [Architecture and runtime model](doc/architecture.md)
- [File processing flow](doc/processing-flow.md)
- [State and recovery](doc/state-and-recovery.md)
- [Operations, logs, and errors](doc/operations-and-errors.md)
- [Docker usage](doc/run_with_docker.md)
