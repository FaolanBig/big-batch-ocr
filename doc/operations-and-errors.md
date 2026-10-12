# Operational behavior, logs, and failure modes

## Progress reporting

The script creates a progress file at `/tmp/ocr_status_<run-id>`. This file is updated on every processed file via `update_status()`.

The content includes:

- percentage complete
- total file count
- processed file count
- remaining files
- total PDF count
- processed PDF count
- remaining PDFs
- failed PDFs
- elapsed time
- ETA estimate
- current file being handled

Example structure:

```text
Progress:         42%

Total files:      120
Processed files:  50
Remaining files:  70

PDFs total:       30
PDFs processed:   12
PDFs remaining:   18
PDFs failed:      0

Elapsed:          55s
ETA:              38s

Current file:     archive/scan_007.pdf
```

For native runs, the status file is consumed by a tmux progress pane, which clears the screen and prints its latest content once per second. The Docker entry point also maintains this status file, but does not display a progress pane.

## Log management

Both entry points maintain two logs:

- `OCR_LOG`: receipt of OCRmyPDF output and per-file start markers
- `ERROR_LOG`: warnings, errors, interruption notices, and failed operations

### OCR log

The OCR log captures:

- start markers for each PDF
- the full output from OCRmyPDF
- any page-cleaning or OCR processing trace information

A new block begins with:

```text
=================================================
START: Wed Oct 5 12:34:56 UTC 2026
FILE : some/relative/path.pdf
=================================================
```

This makes it easy to correlate output with source files.

### Error log

The error log captures all non-success conditions, including:

- failed copy operations
- failed OCR runs
- unreadable completion markers (these generate a warning and trigger reprocessing)
- interrupted signals
- invalid output publication

The final job status prints both log paths at the end of the run.

For the native script, both logs are stored in `/tmp` and remain there after the run. The Docker script stores them in `$LOG_DIR`, which defaults to `<target>/.ocr_sequencial_state/logs`, so they persist with the mounted output. In Docker, script output is also streamed to the terminal and appended to the OCR log.

## Temporary files and cleanup

The script creates temporary files in several contexts:

- file-list snapshot
- destination copy temp files
- OCR output temp files
- marker temp files

The cleanup routine removes them when the script exits via `trap cleanup EXIT`.

This coverage includes:

- `CURRENT_TMP`
- `CURRENT_MARKER_TMP`
- `STATUS_FILE`
- `FILE_LIST_TMP`
- tmux panes in native runs

The script also removes any temp file it created if a copy or OCR step fails.

## Signal interruption behavior

The script registers handlers for:

- `INT`
- `TERM`
- `HUP`

and logs the current file before exiting:

```bash
handle_signal() {
    local signal_name="$1"
    local exit_code="$2"

    echo "[INTERRUPTED] Received SIG${signal_name} while processing: ${CURRENT_FILE:-unknown}" \
        | tee -a "$ERROR_LOG" >&2
    exit "$exit_code"
}
```

This makes interruption events readable in the logs and keeps the terminal state informative.

## Exit status semantics

At the end of the run:

```bash
if (( FAILED_PDFS > 0 || FAILED_FILES > 0 )); then
    echo "[ERROR] Finished with $FAILED_PDFS failed PDF(s) and $FAILED_FILES other file error(s)."
    exit 1
fi
```

The script returns:

- `0` if all work succeeded
- `1` if any item failed

This is a batch-level success/failure signal rather than a per-file one.

## Failure classification

The script distinguishes between:

- PDF failures: OCR errors, publish failures, marker save failures
- non-PDF failures: copy failure, directory preparation failure

The summary prints both categories separately:

```text
PDFs failed:      3
Other files failed: 1
```

This makes it easier to interpret whether the issue came from OCR or file copying.

## Strengths of this design

This operational model is useful for long-running OCR jobs because it provides:

- real-time progress updates
- persistent log output
- resumable file-level processing
- safe publication semantics
- explicit failure accounting
- cleanup of transient artifacts

## Limitations and caveats

The native script makes several assumptions that are important to understand:

- it is designed for a POSIX-like Linux environment
- it depends on tmux being active; the Docker entry point does not
- it heavily assumes `ocrmypdf` is installed and configured correctly
- it only checks file-extension suffixes to decide what is a PDF
- it uses a hidden `.ocr_sequencial_state` directory as the resume store, so deleting it forces reprocessing of PDFs

## Summary

The script’s error and operational model is built around observability and predictable cleanup. It prioritizes progress transparency, safe final publication, and clear batch-level exit semantics over raw parallelism or a more complex state model.
