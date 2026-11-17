# File processing flow

The native and Docker entry points follow the same processing sequence. The
native script reads source and target paths from its positional arguments;
the container script uses the paths mounted at `/source` and `/destination`.

## 1. Validate and scan

The source must exist. The target is created when necessary. The scripts
reject identical source/target paths and a target nested inside the source,
then create `.ocr_sequencial_state` beneath the target.

They enumerate regular files recursively with a null-delimited `find` list,
so spaces, tabs, and newlines in filenames are supported. A filename is
classified as a PDF when it ends in `.pdf`, case-insensitively; file contents
are not inspected during classification. Other regular files are non-PDFs.

## 2. Copy non-PDF files

Non-PDF files are handled before PDFs. The relative path beneath the source is
preserved beneath the target. An existing regular output file with identical
contents is skipped. Otherwise, the script copies to a temporary path with
`cp -a`, then renames that temporary file to the final path. A copy failure
is logged and counted, but does not stop the rest of the batch.

## 3. OCR PDFs

PDFs are processed one at a time. OCRmyPDF receives these fixed options and
the configured values:

- `--language "$OCR_LANGUAGE"` (`eng` by default)
- `--rotate-pages` and `--deskew`
- `--clean` and `--clean-final`
- `--optimize "$OCR_OPTIMIZE"` (`3` by default)
- `--output-type pdf`
- `--skip-text`
- `--jobs "$OCR_JOBS"` (default: 75% of logical CPUs)

The output is first written to a temporary file beside the final destination.
Only after OCRmyPDF exits successfully is that file moved to its final path.
Then the script publishes the completion marker. If OCR or either publication
step fails, the error is logged and the next file is processed.

## 4. Report results

Progress includes file and PDF counts, elapsed time, an estimated remaining
time, and the current file. The native script displays progress and follows
the OCR log in tmux panes. The container script has no progress pane; it
streams script messages to the terminal and writes detailed OCR output to the
destination's log directory.

At the end, the scripts print log locations and counts of failed PDFs and
other files. Exit status is `0` if all items succeed and `1` if any item
fails. See [state and recovery](state-and-recovery.md) for skip decisions and
[operations and errors](operations-and-errors.md) for logs and interruption
behavior.
