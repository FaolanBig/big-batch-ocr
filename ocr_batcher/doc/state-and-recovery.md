# State, resume markers, and recovery

## Purpose of the state directory

The script maintains a hidden metadata directory inside the target directory:

```text
<target_dir>/.ocr_sequencial_state
```

This directory is not part of the source tree and is not a user-facing output directory. It is used solely to record which source PDFs have already been successfully OCR-processed and what the expected source signature was at the time of completion.

The directory is created with:

```bash
STATE_DIR="$TARGET_DIR/.ocr_sequencial_state"
mkdir -p "$STATE_DIR"
```

This directory is essential for safe reruns and for avoiding redundant processing.

## Marker model

Each processed PDF gets a marker file in `.ocr_sequencial_state`.

The path is named using the hash of the source-relative path:

```bash
marker_id=$(marker_id_for_path "$rel")
marker="$STATE_DIR/$marker_id"
```

The relative path is turned into a SHA-256 digest with `sha256sum` and the filename becomes that digest.

This model has several useful properties:

- path names are stored in a compact form
- file names remain stable across runs
- the marker is easy to check without scanning the full target directory tree
- the state path does not require a database or a manifest file

## Source signature

The script records a source signature that combines the current PDF path and its content hash:

```bash
source_hash=$(sha256sum -- "$pdf")
source_signature="$SOURCE_DIR/$rel:${source_hash%% *}"
```

This creates a string like:

```text
/source/path/to/file.pdf:<sha256hash>
```

The marker file stores this exact value:

```bash
printf '%s\n' "$source_signature" > "$marker_tmp"
mv -f -- "$marker_tmp" "$marker"
```

This is a highly effective resume token because it indicates both:

- which source file was processed
- which content version of that file was processed

## Skip decision logic

A PDF is skipped if all of the following conditions are true:

1. the output file exists at the destination path
2. a marker file exists for that relative path
3. the stored marker content exactly matches the current `source_signature`

Equivalent logic:

```bash
if [[ -f "$out" && -f "$marker" ]]; then
    if marker_signature=$(cat -- "$marker" 2>/dev/null); then
        if [[ "$marker_signature" == "$source_signature" ]]; then
            marker_matches=1
        fi
    fi
fi
```

If `marker_matches` is 1, the script prints:

```text
[SKIP] <relative-path>
```

and does not rerun OCR for that file.

## Reprocessing conditions

The script marks a PDF as needing OCR again if any of the following are true:

- the output file does not exist
- the marker file does not exist
- the file content was changed since the last successful OCR run
- the output file exists but the marker is missing or corrupted
- the output file exists but the stored marker no longer matches the current source checksum

In these cases, the script explicitly reprocesses the file and replaces the output file.

This behavior is intentionally conservative. A PDF is considered valid only when both the destination and the state marker agree with the current source.

## Handling corrupted or missing markers

The script logs warnings when a marker could not be read:

```bash
echo "[WARN] Could not read completion marker; reprocessing: $rel" \
    | tee -a "$ERROR_LOG"
```

This protects the workflow from stale or invalid state. A marker is treated as untrusted if it cannot be read or does not match the observed file identity.

## Notable robustness property

The script does not rely on modification times. It uses the actual file content hash for decision-making. This is more reliable than relying on timestamps because:

- files may be copied while preserving mtimes in different ways
- metadata can be unreliable across file systems
- a file may be edited but keep the same modification time under certain workflows

By hashing the content, the script correctly detects real content changes.

## Partial failure handling

The script aims to keep the batch running even if an OCR operation fails. If a PDF fails during OCR, the script:

- writes the failure to the error log
- removes the temporary output file
- sets the failure counter
- continues to the next PDF

The state marker is not written unless the OCR output was successfully moved into place and the marker file was successfully saved. This ensures a failed run does not incorrectly mark a file as complete.

## Finalization semantics

The final state marker is only created after all of the following succeed:

1. OCRmyPDF exits successfully
2. the temporary PDF is moved into the final output path
3. the marker contents are written to a temporary marker file
4. the temporary marker file is moved into place as the committed marker

This sequence makes the marker act as a reliable commit point.

## Recovery from a deleted state directory

If `.ocr_sequencial_state` is deleted, the script effectively treats all PDF outputs as unverified. On the next run, each PDF is compared against the current source signature. Since no markers exist, the original OCR work will be rerun unless the output file itself can be recognized as valid by another mechanism.

This is a deliberate and safe design decision: removing the state directory is equivalent to forcing a fresh validation pass.

## Non-PDF copy behavior

Unlike PDF handling, non-PDF files do not use a separate state database. Instead, they are compared directly against the existing destination file using `cmp -s`:

```bash
if [[ -f "$out" ]] && cmp -s -- "$file" "$out"; then
    echo "[SKIP] $rel"
    ((PROCESSED_FILES+=1))
    update_status
    continue
fi
```

This is a content-level equality check. If the content is the same, the file is skipped. Otherwise it is copied as a new version.

## Operational implication

The state design gives the script an accidental but useful “resume without a manifest” behavior. It does not keep a central job list or transaction log; instead, it relies on the final output and the state markers to decide whether work is already complete.

That makes the script easy to reason about at the file level, but it also means the correctness of skip/reprocess decisions depends on the integrity of the state directory and the target output files.

## Summary

The script’s resume mechanism is built around a combination of:

- destination file existence
- per-path marker files
- source-relative path hashing
- SHA-256 content signatures

This is a robust and relatively simple mechanism for preventing duplicate OCR work while still allowing safe reruns after source changes or interrupted jobs.
