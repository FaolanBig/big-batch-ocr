# Script overview

> :information_source: everything in here applies to the folder `[ocr_batcher](../../ocr_batcher/)`, not the repos root folder.

## Supported entry points

The current native workflow is implemented in [`src/ocr_sequencial.sh`](../src/ocr_sequencial.sh). The Docker image runs [`docker/ocr_sequencial_docker.sh`](../docker/ocr_sequencial_docker.sh), a container-oriented variant of the same workflow. [`src/ocr.sh`](../src/ocr.sh) is deprecated.

Both current entry points recursively scan a source directory, preserve relative paths in the target, copy non-PDF files, and OCR PDF files one at a time with OCRmyPDF. They publish copied and OCR-processed files from temporary paths, record per-PDF completion markers, continue after per-file failures, and return a nonzero status if any file fails.

## Native execution

The native script requires Bash, OCRmyPDF, tmux, and common Linux utilities. It must be launched from inside an existing tmux session. It creates a progress pane and a pane following the OCR log. Native OCR and error logs are created in `/tmp` and their paths are printed at the end of the run.

```bash
tmux new -s ocr
# In the tmux session:
bash src/ocr_sequencial.sh "/path/to/source" "/path/to/target"
```

The native script uses a positive-integer `OCR_JOBS` value when provided; otherwise, it sets OCRmyPDF's `--jobs` value to 75% of the logical CPU count reported by `nproc --all`.

## Docker execution

The container runs automatically without tmux. Mount the source at `/source` and the destination at `/destination`; the container script uses these fixed paths. It writes logs under `<destination>/.ocr_sequencial_state/logs` by default. Set `LOG_DIR` to select a different log directory.

```bash
docker run --rm -it \
  -v /path/to/source:/source:ro \
  -v /path/to/target:/destination \
  -e OCR_JOBS=4 \
  ghcr.io/faolanbig/big-batch-ocr_ocr-sequencial:<tag>
```

The Docker entry point uses the same positive-integer `OCR_JOBS` override and defaults to 75% of the logical CPU count when it is unset.

## File handling and resume behavior

PDFs are identified by a case-insensitive `.pdf` suffix; file contents are not checked during discovery. All other regular files are copied. Existing non-PDF outputs are skipped when `cmp` finds identical contents. Otherwise, they are copied to a temporary destination file and moved into place.

For each PDF, the script calculates a SHA-256 checksum of the source and checks for both an output PDF and a completion marker matching the source path and checksum. A match skips OCR. Missing, unreadable, or non-matching state causes the PDF to be processed again.

The state directory is `<target>/.ocr_sequencial_state`. Marker files are named using a hash of each PDF's source-relative path. OCR output is first written to a temporary file and moved to its destination only after OCRmyPDF succeeds. The marker is published after the output; if marker publication fails, the batch records a failure and a later run will process that PDF again.

## Constraints and outcomes

- The source directory must exist; the target is created if necessary.
- Source and target must differ, and the target must not be inside the source.
- Processing is sequential across files, although OCRmyPDF may use multiple jobs for an individual PDF.
- A failed item does not stop later items from being processed.
- Exit status is `0` when all items succeed and `1` when one or more items fail. Signal interruptions use the corresponding conventional status (`129`, `130`, or `143`).

See [architecture](./architecture.md), [processing flow](./processing-flow.md), [state and recovery](./state-and-recovery.md), and [operations and errors](./operations-and-errors.md) for details.
