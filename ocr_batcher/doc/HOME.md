# Big Batch OCR documentation

This directory documents the current OCR workflow in this repository. The supported native entry point is [`src/ocr_sequencial.sh`](../src/ocr_sequencial.sh); the Docker image runs its container-specific counterpart, [`docker/ocr_sequencial_docker.sh`](../docker/ocr_sequencial_docker.sh). The older [`src/ocr.sh`](../src/ocr.sh) is deprecated.

Both current entry points scan a source directory recursively, preserve its relative directory structure in the destination, copy non-PDF files, and process PDFs sequentially with OCRmyPDF. They use temporary output files and per-PDF SHA-256 markers so successful work can be reused on a later run.

## Documentation

- [Script overview](./script_overview.md): supported entry points, requirements, and high-level behavior.
- [Architecture and runtime model](./architecture.md): components, runtime configuration, and differences between native and Docker execution.
- [Execution flow and file processing](./processing-flow.md): file discovery, copying, OCR, and output publication.
- [State, resume markers, and recovery](./state-and-recovery.md): marker format and skip/reprocess decisions.
- [Operations, logs, and errors](./operations-and-errors.md): progress, logs, cleanup, interruption, and exit statuses.
- [Run OCR in Docker](./run_with_docker.md): published images, local builds, and container usage.

## Quick reference

Native runs require Bash, OCRmyPDF, tmux, and the utilities checked or used by the script (`realpath`, `sha256sum`, `cmp`, `find`, `nproc`, and others). Start the script from inside an existing tmux session:

```bash
bash src/ocr_sequencial.sh "/path/to/source" "/path/to/target"
```

The Docker image does not require tmux on the host. Mount the input at `/source` (read-only is recommended) and the output at `/destination`; see [Run OCR in Docker](./run_with_docker.md).

Both entry points require the source directory to exist and reject identical source/target directories or a target inside the source. PDF detection is based on a case-insensitive `.pdf` suffix. Both accept a positive-integer `OCR_JOBS` override and otherwise use 75% of the logical CPU count reported by `nproc --all`.
