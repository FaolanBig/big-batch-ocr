# State, resume markers, and recovery

The batcher keeps PDF completion markers beneath the target directory:

```text
<target>/.ocr_sequencial_state/
```

In the Docker image, this directory also contains persistent logs by default.
The native script stores its run logs in `/tmp`.

## PDF marker format and skip decision

Each PDF's marker filename is the SHA-256 hash of its source-relative path.
Its contents identify the absolute source path, source file's SHA-256
checksum, selected OCR language, and optimization level. The marker is
published only after OCRmyPDF succeeds and the temporary output is moved to
its final destination.

A PDF is skipped only when both the output PDF and its marker exist and the
marker exactly matches the current source path, content checksum, and OCR
settings. A changed input, language, or optimization level causes the PDF to
be processed again. An absent, unreadable, or mismatching marker also causes
reprocessing. The script does not use timestamps for this decision.

The output and marker are separate files, not a transactional pair. If a
failure or interruption occurs after publishing the output but before
publishing its marker, a later run treats that output as unverified and
processes the PDF again. Deleting `.ocr_sequencial_state` removes all PDF
markers and therefore forces PDFs to be reprocessed.

## Non-PDF files

Non-PDF files do not use markers. Before copying, the script compares an
existing destination file with the source using `cmp`. Identical files are
skipped; different or missing destination files are copied to a temporary
path and then moved into place.

## Failures and reruns

OCR, copy, and publication failures are logged and counted. Processing
continues with the remaining files, and the script exits nonzero if any item
fails. A failed OCR operation does not publish a completion marker. On a
later run, the source is checked again and the PDF is processed unless its
output and marker validate.

Because markers are part of the output state, retain the hidden state
directory when you expect to resume work. Removing it is a deliberate way to
force OCR to run again for every PDF.
