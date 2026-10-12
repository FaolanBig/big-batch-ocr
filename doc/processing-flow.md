# Execution flow and file processing

## Overall execution sequence

The script follows a clear linear execution path:

1. start shell options and basic state variables
2. validate arguments and dependencies
3. normalize source and target paths
4. create the target state directory
5. verify tmux runtime and create progress/log panes
6. scan source directory for files
7. copy non-PDF files
8. process PDF files one by one
9. print final summary and exit status

This is a batch job rather than a streaming pipeline: each pass is completed before the next one starts.

## Step 1: startup and validation

At startup, the native script requires non-empty source and target directory arguments. Pass these as the only two arguments; additional arguments are not used.

```bash
if [[ -z "$SOURCE_DIR" || -z "$TARGET_DIR" ]]; then
    die "Usage: $SCRIPT_NAME <source_dir> <target_dir>"
fi
```

It checks for required command-line tools and immediately exits on the first missing requirement. It then normalizes both paths; the source must exist and the target is created if necessary.

It then resolves both paths so comparisons are robust and consistent:

```bash
SOURCE_DIR="$(realpath "$SOURCE_DIR")"
TARGET_DIR="$(realpath -m "$TARGET_DIR")"
```

The script rejects several invalid layouts:

- source and target are the same
- target is inside source
- source directory does not exist

This is a strong guardrail because copy-and-OCR processes should not mutate a directory tree that they are also scanning.

## Step 2: runtime setup

The native script requires an existing tmux session and creates progress and log panes. The Docker entry point runs without tmux and writes logs under the destination's `.ocr_sequencial_state/logs` directory by default (`LOG_DIR` can override this).

For native runs, the script requires `TMUX` to be present:

```bash
if [[ -z "${TMUX:-}" ]]; then
    die "This script must be started from inside a tmux session."
fi
```

It creates a run ID:

```bash
RUN_ID="$(date +%Y%m%d_%H%M%S)_$$"
```

Then it creates three files in `/tmp`:

- `ocr_status_<run-id>`
- `ocr_output_<run-id>.log`
- `ocr_errors_<run-id>.log`

It also captures the active pane ID:

```bash
CURRENT_PANE="$(tmux display-message -p "#{pane_id}")"
```

Then it splits the tmux window into two panes:

- a vertical pane for status/progress
- a horizontal pane for log output

The progress pane runs a loop that clears the terminal and prints the current status file every second.

The log pane runs:

```bash
tail -n 50 -F '$OCR_LOG'
```

This is an operational log monitor that follows the OCR log in real time. It displays the most recent 50 lines and continues following new output as it arrives.

## Step 3: build the file list

The script enumerates all files using `find`:

```bash
find "$SOURCE_DIR" -type f -print0 > "$FILE_LIST_TMP"
mapfile -d '' -t ALL_FILES < "$FILE_LIST_TMP"
```

This is a safe approach for files with special characters, including spaces and newlines. It avoids the common pitfalls of using a newline-delimited file list.

The list is then partitioned by extension:

```bash
for file in "${ALL_FILES[@]}"; do
    if [[ "$file" =~ \.[Pp][Dd][Ff]$ ]]; then
        PDF_FILES+=("$file")
    fi
done
```

This means the script treats a file as a PDF if its final extension is `.pdf` in a case-insensitive match. No attempt is made to validate the actual PDF signature at this stage. The Docker entry point uses the same file discovery and classification logic.

The counts are then computed:

- `TOTAL_FILES=${#ALL_FILES[@]}`
- `TOTAL_PDFS=${#PDF_FILES[@]}`

The accumulation counters are reset:

- `PROCESSED_FILES=0`
- `PROCESSED_PDFS=0`
- `FAILED_PDFS=0`
- `FAILED_FILES=0`

## Step 4: copy non-PDF files

Before OCR work starts, the script scans every regular file and copies non-PDF items. This happens in its first main pass.

For each file in `ALL_FILES`:

```bash
rel="${file#"$SOURCE_DIR"/}"
out="$TARGET_DIR/$rel"
CURRENT_FILE="$rel"
```

The script checks whether the current file is a PDF. If it is, it is skipped in this pass.

For non-PDF files, it computes the destination path and then checks whether a matching output already exists:

```bash
if [[ -f "$out" ]] && cmp -s -- "$file" "$out"; then
    echo "[SKIP] $rel"
    ((PROCESSED_FILES+=1))
    update_status
    continue
fi
```

If the output file already exists and is identical, it is not copied again. This reduces unnecessary churn and preserves target files.

Otherwise, the script ensures the parent directory exists:

```bash
if [[ -d "$out" ]] || ! mkdir -p -- "$(dirname "$out")"; then
    echo "[ERROR] Could not prepare destination: $out" | tee -a "$ERROR_LOG"
    ((FAILED_FILES+=1))
    ((PROCESSED_FILES+=1))
    update_status
    continue
fi
```

Then it writes to a temp copy:

```bash
copy_tmp="${out}.copy-${RUN_ID}.tmp"
CURRENT_TMP="$copy_tmp"
```

And finally:

```bash
if cp -a -- "$file" "$copy_tmp" && mv -f -- "$copy_tmp" "$out"; then
    CURRENT_TMP=""
else
    rc=$?
    echo "[ERROR] Failed to copy: $file (exit code=$rc)" | tee -a "$ERROR_LOG"
    rm -f -- "$copy_tmp"
    CURRENT_TMP=""
    ((FAILED_FILES+=1))
fi
```

The logic is intentionally atomic from the perspective of the final output: the file is only published at the end by `mv`.

## Step 5: OCR PDFs one at a time

Once all non-PDF files are handled, the script begins the PDF processing loop.

For each PDF in `PDF_FILES`, it computes:

- source-relative path `rel`
- final target path `out`
- temporary OCR output `tmp_out`

It then builds a marker identifier:

```bash
marker_id=$(marker_id_for_path "$rel") || {
    echo "[ERROR] Could not create completion marker id: $rel" | tee -a "$ERROR_LOG"
    ((FAILED_PDFS+=1))
    ((PROCESSED_FILES+=1))
    ((PROCESSED_PDFS+=1))
    update_status
    continue
}
```

The script stores the state under:

```bash
marker="$STATE_DIR/$marker_id"
marker_tmp="${marker}.tmp.$$"
```

The source signature is created from the source directory path plus the source file SHA-256:

```bash
source_hash=$(sha256sum -- "$pdf")
source_signature="$SOURCE_DIR/$rel:${source_hash%% *}"
```

This string is the key semantic identifier that allows the script to tell whether the source file has changed since the last successful OCR run.

## Step 6: skip logic for completed PDFs

Before running OCR, the script checks whether the output exists and whether a completion marker is present.

```bash
marker_matches=0
if [[ -f "$out" && -f "$marker" ]]; then
    if marker_signature=$(cat -- "$marker" 2>/dev/null); then
        if [[ "$marker_signature" == "$source_signature" ]]; then
            marker_matches=1
        fi
    else
        echo "[WARN] Could not read completion marker; reprocessing: $rel" \
            | tee -a "$ERROR_LOG"
    fi
fi
```

If both conditions are true and the marker content exactly matches the source signature, the file is skipped entirely:

```bash
if (( marker_matches )); then
    echo "[SKIP] $rel"
    ((PROCESSED_FILES+=1))
    ((PROCESSED_PDFS+=1))
    update_status
    continue
fi
```

This means the script reuses the existing output and avoids redundant OCR if the target output is still valid for the current source file.

If the marker is missing, if the marker does not match, or if a source file changed since the marker was last recorded, the script reprocesses the PDF.

## Step 7: OCR invocation

The script logs entry information for the PDF before starting OCR:

```bash
{
    echo
    echo "================================================="
    echo "START: $(date)"
    echo "FILE : $rel"
    echo "================================================="
    echo
} >> "$OCR_LOG"
```

Then it calls OCRmyPDF with a specific set of OCR cleanup and scanning settings:

```bash
if ocrmypdf \
    --rotate-pages \
    --deskew \
    --clean \
    --clean-final \
    --optimize 3 \
    --output-type pdf \
    --skip-text \
    --jobs "$CPU_THREADS" \
    "$pdf" \
    "$tmp_out" >> "$OCR_LOG" 2>&1
then
```

The options do the following:

- `--rotate-pages`: fix page orientation
- `--deskew`: correct skewed document scans
- `--clean`: clean page images before OCR
- `--clean-final`: apply cleanup to the final output
- `--optimize 3`: optimize PDF output size and structure
- `--output-type pdf`: force PDF output
- `--skip-text`: skip pages already containing text
- `--jobs`: configure parallelism inside OCRmyPDF

The native script derives `CPU_THREADS` as 75% of the logical CPU count and does not currently use the `OCR_JOBS` environment variable. The Docker entry point accepts `OCR_JOBS` as an override and uses the same default when it is unset.

This is a strong cleanup-and-optimization workflow intended for scanned document processing.

## Step 8: publish output only after success

The script writes OCR output into a temporary file first:

```bash
tmp_out="${out}.ocr-${RUN_ID}.tmp.pdf"
CURRENT_TMP="$tmp_out"
CURRENT_MARKER_TMP="$marker_tmp"
```

Only after OCRmyPDF returns success does it move the temporary output into place. It then writes the source signature to a temporary marker and moves that marker into place:

```bash
if mv -f -- "$tmp_out" "$out"; then
    if printf '%s\n' "$source_signature" > "$marker_tmp" \
        && mv -f -- "$marker_tmp" "$marker"; then
        CURRENT_TMP=""
        CURRENT_MARKER_TMP=""
        echo "[SUCCESS] completed OCR for $pdf"
```

The output is published before the marker. If publishing the output fails, the PDF is counted as failed. If writing or publishing the marker fails, the output remains but the PDF is still counted as failed; because it has no matching marker, a later run reprocesses it.

## Step 9: failure handling within the PDF loop

If OCR fails, the script does this:

```bash
else
    rc=$?

    echo "[ERROR] OCR failed: $pdf (exit code=$rc)" \
        | tee -a "$ERROR_LOG"
    rm -f -- "$tmp_out"
    CURRENT_TMP=""
    CURRENT_MARKER_TMP=""
    ((FAILED_PDFS+=1))
fi
```

Similarly, if the temporary output cannot be renamed into place, or if the marker cannot be saved, the script marks the PDF as failed and keeps processing the remaining files.

After either result, the script increments:

```bash
((PROCESSED_FILES+=1))
((PROCESSED_PDFS+=1))
update_status
```

This keeps counters consistent even when a file fails.

## Step 10: final summary

When all files have been processed, the script computes the final runtime:

```bash
END_TIME=$(date +%s)
```

Then it writes a final summary block to the status file:

```bash
cat > "$STATUS_FILE" <<EOF
$JOB_RESULT

Total files:      $TOTAL_FILES
Processed files:  $PROCESSED_FILES

PDFs total:       $TOTAL_PDFS
PDFs processed:   $PROCESSED_PDFS
PDFs failed:      $FAILED_PDFS
Other files failed: $FAILED_FILES

Runtime:          $((END_TIME - START_TIME))s

OCR Log:          $OCR_LOG
Error Log:        $ERROR_LOG
EOF
```

This summary includes:

- batch-level result
- total processed count
- PDF counts
- failed non-PDF count
- runtime
- log file paths

Finally the script exits with:

- `0` when there were no failed PDFs and no failed non-PDF files
- `1` when at least one failure occurred

## Behavior with failed files

The script is designed to continue processing even after an individual file fails.

This is visible in the PDF loop and the copy loop: each error is reported, counted, and then the run proceeds to the next file. The job-level result is only reported at the end.

This is a deliberate operational choice: long-running OCR batches are more useful when they schedule the next file even after a prior one has failed, rather than aborting mid-stream.

## Summary

The execution flow is intentionally conservative and publication-safe. It first preserves non-PDF files, then handles PDFs in a resumable, stateful manner. The script ensures that users can monitor both progress and detailed OCR output in real time, even while the job is still running.
