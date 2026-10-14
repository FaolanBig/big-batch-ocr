# Big Batch OCR: `ocr_sequencial.sh`

The OCR application is in `ocr_batcher/`. Run the commands below from that directory.

## Installation

Use either the Bash script at `src/ocr_sequencial.sh` or the compiled binary at `bin/ocr_sequencial`. The binary is also available from the [GitHub Releases](https://github.com/FaolanBig/big-batch-ocr/releases) page. Both provide the current OCR workflow.

`src/ocr.sh` is deprecated and should no longer be used. Use `ocr_sequencial.sh` or the `ocr_sequencial` binary instead; they provide a more modern and robust workflow, including safe output publishing and resumable PDF processing.

For bare-metal installation of the Bash script's dependencies on Debian or Ubuntu, run [`dependencies/install_dependencies.sh`](dependencies/install_dependencies.sh). See [`dependencies/README.md`](dependencies/README.md) for the package, license, and copyright details.

`ocr_sequencial.sh` recursively processes a source directory and writes results to a target directory, preserving the directory structure. Non-PDF files are copied; PDF files are processed one at a time with OCRmyPDF.

## Docker

Prebuilt Docker images are published to [GitHub Container Registry](https://github.com/FaolanBig/big-batch-ocr/pkgs/container/big-batch-ocr). See [Run OCR in Docker](doc/run_with_docker.md) for pull and run instructions.

## Quick Start

Requirements: Bash, OCRmyPDF and its required OCR tools, and `tmux`. The usual GNU/Linux utilities `find`, `realpath`, `sha256sum`, `cmp`, `mktemp`, `cp`, `mv`, and `tail` must also be available. At startup, the script explicitly checks for `ocrmypdf`, `tmux`, `realpath`, `sha256sum`, and `cmp`.

To build the binary from the Bash script, install `shc` and run:

```bash
./build/build.sh
```

The compiled binary is written to `bin/ocr_sequencial`. Build it on the platform where it will be run.

1. Start a tmux session:

   ```bash
   tmux new -s ocr
   ```

2. In the tmux window, change to the repository directory and run either the script or binary with the source and target directories:

   ```bash
   bash src/ocr_sequencial.sh "/path/to/source" "/path/to/target"
   ```

   Or run the binary:

   ```bash
   ./bin/ocr_sequencial "/path/to/source" "/path/to/target"
   ```

   If the script is executable, you can use `./src/ocr_sequencial.sh` instead.

3. Monitor progress in the tmux panes that open automatically. When processing is complete, exit the tmux session with `exit`.

You can optionally set the number of OCRmyPDF worker threads. The default is 3/4 of the logical CPU count reported by `nproc --all`:

```bash
OCR_JOBS=4 bash src/ocr_sequencial.sh "/path/to/source" "/path/to/target"
```

`OCR_JOBS` must be a positive integer. Files are processed sequentially, but OCRmyPDF can use multiple threads internally according to this setting.

## Directories and Limitations

- Both arguments are required: `source_dir` and `target_dir`.
- The source directory must exist. The target directory is created if needed.
- The source and target must be different. The target must not be inside the source directory.
- The script scans all regular files in the source directory and its subdirectories.
- A file is treated as a PDF if its name ends in `.pdf`; the extension match is case-insensitive.
- The relative directory structure is preserved in the target. Existing destination files with the same names may be replaced.

## How It Works

### 1. Setup and Display

The script validates its arguments, dependencies, and directory paths, then creates `.ocr_sequencial_state` in the target directory to store resume markers. It must be run from inside an existing tmux session. Two additional panes show a progress summary and the latest lines from the OCR log. The script closes these panes when it exits.

Before processing, the script builds a list of files in the source directory. Filenames containing spaces or other special characters are handled safely. It then processes the files in two passes.

### 2. Copying Non-PDF Files

Non-PDF files are copied first. If a destination file already exists and has identical contents, it is skipped. Otherwise, the script copies the file to a temporary file and then moves it to its final destination, so a partially copied file is not published as complete. The copy uses `cp -a`.

### 3. Processing PDFs with OCRmyPDF

PDF files are processed one at a time. For each file, the script runs OCRmyPDF with these options:

- `--rotate-pages` and `--deskew` correct page orientation and skew.
- `--clean` and `--clean-final` clean page images, including in the final document.
- `--optimize 3` enables optimization level 3.
- `--output-type pdf` specifies the output format.
- `--skip-text` skips pages that already contain text.
- `--jobs` uses the positive-integer value of `OCR_JOBS` when provided; otherwise, it defaults to 3/4 of the logical CPU count reported by `nproc --all`.

The script does not specify an OCR language; OCRmyPDF therefore uses its configured default language. Output is first written to a temporary file in the target directory and moved to its final path only after OCR completes successfully.

### 4. Recognizing Completed PDFs

After publishing a successful result, the script writes a marker under `.ocr_sequencial_state`. The marker contains the absolute source path and the source file's SHA-256 checksum. On a later run, a PDF is skipped only when its output file exists and the marker matches the current source path and file contents.

If the marker is missing or does not match, or if the source file has changed, the PDF is processed again. An existing output without a matching marker is also considered unverified and is regenerated. Deleting the state directory therefore forces PDFs to be processed again.

### Progress, Logs, and Errors

The progress display reports the total number and status of all files, PDF counters, elapsed time, and an estimated remaining time. The estimate is based on the average processing rate so far and is only a guideline. The current file is also displayed.

Each run creates three files in `/tmp`:

- `ocr_output_<run-id>.log`: full OCRmyPDF output and per-PDF start information; the log pane continuously shows the last 50 lines.
- `ocr_errors_<run-id>.log`: errors and interruptions.
- `ocr_status_<run-id>`: temporary status data for the progress pane; this file is removed when the script exits.

The OCR and error logs remain in `/tmp` after the run. Their paths are printed at the end. Temporary output and marker files are cleaned up when the script exits. If a file fails, the script continues with the remaining files and exits with status code `1`; a successful run exits with status code `0`. The final summary includes counts of failed PDFs and non-PDF files.

---

## ❤️ Sponsor

- Bitcoin (BTC): `bc1q2ss7n5gv3tr68dfqtmcvy830pj4clf7wqxztk5`
- Ethereum (ETH): `0x8AA598780c6529DCB771B9455E2717043BB1a1e1`
- Solana (SOL): `21BpX9xhkitMEWx2iBxGXEVsFCMF9huViud24NbrPqNC`
