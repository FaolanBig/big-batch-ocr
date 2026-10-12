# Architecture and runtime model

## File-level structure

The native script is organized into a compact but layered structure:

1. shell options and global state
2. helper functions
3. validation and setup
4. tmux pane creation
5. file discovery and classification
6. non-PDF copy pass
7. PDF OCR pass
8. final summary and exit status

At a high level, each entry point is a single Bash program with procedural control flow rather than a modular library or service-oriented architecture. The native entry point adds tmux panes for progress and OCR output; the Docker entry point writes its logs to the mounted destination and runs without tmux.

## Global configuration and runtime variables

The script defines several top-level variables immediately after the shell options:

```bash
SCRIPT_NAME="$(basename "$0")"

SOURCE_DIR="${1:-}"
TARGET_DIR="${2:-}"
CURRENT_TMP=""
CURRENT_MARKER_TMP=""
FILE_LIST_TMP=""
PROGRESS_PANE=""
LOG_PANE=""
```

These variables serve as the global runtime state for the entire batch.

### Main variables

- `SCRIPT_NAME`: used in usage messages.
- `SOURCE_DIR`: input directory entered by the user.
- `TARGET_DIR`: output directory entered by the user.
- `CURRENT_TMP`: current temporary file created for copying or OCRing.
- `CURRENT_MARKER_TMP`: temporary marker file being written.
- `FILE_LIST_TMP`: the temporary file containing the file list produced by `find`.
- `PROGRESS_PANE`: tmux pane ID for the progress display.
- `LOG_PANE`: tmux pane ID for the log display.

Later the native script creates additional runtime variables such as:

- `RUN_ID`
- `STATUS_FILE`
- `OCR_LOG`
- `ERROR_LOG`
- `CURRENT_PANE`
- `TOTAL_FILES`
- `TOTAL_PDFS`
- `PROCESSED_FILES`
- `PROCESSED_PDFS`
- `FAILED_PDFS`
- `FAILED_FILES`
- `START_TIME`
- `END_TIME`
- `CPU_THREADS`
- `STATE_DIR`

These values are updated as the job progresses and are used by the status display and final summary.

## Core helper functions

### `die()`

```bash
die() {
    echo "[ERROR] $*" >&2
    exit 1
}
```

This function emits an error message to stderr and exits immediately. It is used for invalid arguments, missing dependencies, or impossible runtime conditions.

### `update_status()`

This function recomputes progress counters and writes a status snapshot to `/tmp/ocr_status_<run-id>`.

It computes:

- elapsed runtime since `START_TIME`
- remaining file count
- remaining PDF count
- percentage complete
- ETA based on the current processing rate
- current file under active processing

It then writes a structured text block with all these values into the status file. The progress pane polls this file once per second.

### `cleanup()`

The cleanup function is registered with:

```bash
trap cleanup EXIT
```

It removes:

- the progress pane if it exists
- the log pane if it exists
- the current temporary copy or OCR file
- the current temporary marker file
- the status file
- the temporary file list file

This ensures that transient job state is cleaned up after the script ends, even on abnormal exit paths.

### `marker_id_for_path()`

This function converts a relative path into a deterministic hash:

```bash
marker_id_for_path() {
    local digest
    digest=$(printf '%s' "$1" | sha256sum) || return 1
    printf '%s' "${digest%% *}"
}
```

It takes the relative path string and computes a SHA-256 digest. The digest is then used as the filename for the marker file under `.ocr_sequencial_state`.

### `handle_signal()`

This function handles Unix signals such as `INT`, `TERM`, `HUP` and logs the fact that processing was interrupted.

It writes a line to the error log that looks like this:

```text
[INTERRUPTED] Received SIGTERM while processing: some/file.pdf
```

Then it exits with the appropriate code:

- `INT` -> 130
- `TERM` -> 143
- `HUP` -> 129

## Signal handling model

The native script installs traps for process termination:

```bash
trap 'handle_signal INT 130' INT
trap 'handle_signal TERM 143' TERM
trap 'handle_signal HUP 129' HUP
```

This allows the script to register a clear interruption record instead of silently terminating in the middle of a file. The cleanup trap still runs on exit, which removes UI panes and temporary files. The Docker entry point also handles these signals, but has no panes to remove.

## Validation stage

Before doing any real work, the native script validates conditions in this order:

1. both command-line arguments are provided
2. required commands exist
3. input and output paths are normalized
4. source and target are not identical
5. target is not inside source
6. target directory is created
7. state directory is created
8. script is running in tmux

### Required commands

It explicitly checks for:

- `ocrmypdf`
- `tmux`
- `realpath`
- `sha256sum`
- `cmp`

The script also uses `find`, `nproc`, and other common GNU/Linux utilities. `nproc` is used to calculate the default OCRmyPDF job count, although it is not part of the explicit dependency checks.

The command validation is intentionally strict, and each missing binary causes a hard exit.

## Directory normalization

The script resolves the source path and creates the target path with `realpath` and `realpath -m`:

```bash
SOURCE_DIR="$(realpath "$SOURCE_DIR")"
TARGET_DIR="$(realpath -m "$TARGET_DIR")"
```

This ensures consistent path comparisons and prevents accidental path confusion when the user passes relative or symlinked values.

## CPU thread configuration

The native script sets:

```bash
export CPU_THREADS="${OCR_JOBS:-$(($(nproc --all) * 3 / 4))}"
```

If `OCR_JOBS` is set to a non-empty value, that value is used; otherwise the script uses roughly 75% of the machine’s total logical CPU count. Both entry points validate that the resulting value is a positive integer.

That value is passed to OCRmyPDF via:

```bash
--jobs "$CPU_THREADS"
```

This is a process-level concurrency setting inside OCRmyPDF, not a loop-level parallelism setting for this script itself.

The Docker entry point uses the same `OCR_JOBS` override and 75%-of-logical-CPUs default.

## File list generation

The script enumerates all files with:

```bash
find "$SOURCE_DIR" -type f -print0 > "$FILE_LIST_TMP"
mapfile -d '' -t ALL_FILES < "$FILE_LIST_TMP"
```

This creates a null-delimited list of all regular files, ensuring support for filenames containing newlines, tabs, or spaces.

It then partitions them into:

```bash
PDF_FILES=()
for file in "${ALL_FILES[@]}"; do
    if [[ "$file" =~ \.[Pp][Dd][Ff]$ ]]; then
        PDF_FILES+=("$file")
    fi
done
```

This classification is based on the file name suffix only. The script does not inspect file content to validate actual PDF format beyond the extension suffix.

## Output publication model

The script distinguishes between:

- a temporary file for the destination artifact
- a final destination path
- a state marker stored under the target metadata directory

This design is important because it decouples the produce-and-validate step from the publish step. A file is only considered complete when it exists at the destination and the marker has been successfully written.

## Summary

The architecture is intentionally straightforward: both entry points perform one sequential batch process with careful file publication and resumable PDF verification. Native execution adds tmux UI updates; container execution writes its output and error logs under `.ocr_sequencial_state/logs` by default (configurable with `LOG_DIR`). Neither entry point parallelizes files, though OCRmyPDF can use multiple jobs within each PDF.
