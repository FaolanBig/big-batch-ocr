# Script overview

This guide covers the OCR batcher in [`../`](../), not the repository as a
whole.

## Current entry points

- [`../src/ocr_sequencial.sh`](../src/ocr_sequencial.sh): native Linux script;
  pass source and target directories as arguments and run inside tmux.
- [`../docker/ocr_sequencial_docker.sh`](../docker/ocr_sequencial_docker.sh):
  container entry point; source and target are fixed at `/source` and
  `/destination`, so no directory arguments are accepted.
- [`../bin/ocr_sequencial`](../bin/ocr_sequencial): optional compiled native
  binary built from the Bash script with `shc`.

[`../src/ocr.sh`](../src/ocr.sh) is deprecated.

## Shared behavior

Both current scripts recursively scan regular files, preserve relative paths,
copy non-PDF files, and process PDFs sequentially with OCRmyPDF. PDFs are
identified by case-insensitive `.pdf` suffix, not by inspecting their
contents. Files are published from temporary paths. A failed file is logged,
but does not stop later items.

`OCR_JOBS` controls OCRmyPDF's internal worker threads for each PDF and
defaults to 75% of the logical CPU count reported by `nproc --all`.
`OCR_LANGUAGE` and `OCR_OPTIMIZE` default to `eng` and `3`. The scripts accept
`--language CODE[+CODE...]` (or `-l`) and `--optimize 0..3`; other OCRmyPDF
options are fixed.

PDF markers in `.ocr_sequencial_state` include the source path, SHA-256
checksum, language, and optimization setting. A PDF is skipped only when its
output exists and the marker matches. Non-PDF files are skipped if their
existing destination has identical contents.

## Native execution

The source directory must exist. The target is created if needed and must not
be the same as or inside the source. Start the script from a tmux session:

```bash
tmux new -s ocr
# In the tmux session:
bash src/ocr_sequencial.sh "/path/to/source" "/path/to/target"
```

The native workflow requires Bash, OCRmyPDF and dependencies, tmux, and
utilities checked or used by the script. It offers to run the included
installer for missing dependencies when started interactively. See the
[dependency guide](../dependencies/README.md).

## Docker execution

The published container starts OCR automatically without tmux. Mount the
input read-only at `/source` and the output at `/destination`:

```bash
TAG=v1.2.9
docker run --rm -it \
  -v /path/to/source:/source:ro \
  -v /path/to/target:/destination \
  -e OCR_JOBS=4 \
  "ghcr.io/faolanbig/big-batch-ocr_ocr-sequencial:$TAG"
```

Container logs are stored under
`/destination/.ocr_sequencial_state/logs` by default; `LOG_DIR` can override
the location. See [Run OCR in Docker](run_with_docker.md).

## Constraints and outcomes

- Source and target must differ, and target must not be inside source.
- File processing is sequential; only OCRmyPDF's per-PDF workers are
  configurable.
- Exit status is `0` if all items succeed and `1` if any file fails.
- `INT`, `TERM`, and `HUP` interruptions use exit status `130`, `143`, and
  `129`, respectively.

See [architecture](architecture.md), [processing flow](processing-flow.md),
[state and recovery](state-and-recovery.md), and
[operations and errors](operations-and-errors.md) for details.
