# Architecture and runtime model

The OCR batcher has two current Bash entry points that implement the same
sequential file-processing workflow:

- [`../src/ocr_sequencial.sh`](../src/ocr_sequencial.sh) runs on a Linux host
  and uses tmux for progress and live OCR-log panes.
- [`../docker/ocr_sequencial_docker.sh`](../docker/ocr_sequencial_docker.sh)
  runs in the container with `/source` and `/destination` as fixed paths.
  It does not use tmux and stores logs below the mounted destination.

The older [`../src/ocr.sh`](../src/ocr.sh) is deprecated. The compiled
[`../bin/ocr_sequencial`](../bin/ocr_sequencial) is generated from the native
script using `shc`; it is not a separate implementation.

## Run configuration

Both current scripts support these settings:

| Setting | Default | Purpose |
| --- | --- | --- |
| `OCR_JOBS` | 75% of logical CPUs from `nproc --all` | Positive integer passed to OCRmyPDF's `--jobs`. |
| `OCR_LANGUAGE` | `eng` | Installed language code(s), joined with `+`. |
| `OCR_OPTIMIZE` | `3` | OCRmyPDF optimization level `0`–`3`. |

The native script accepts `--language`/`-l` and `--optimize` options, which
override their environment values. The Docker entry point accepts those same
options. Other OCRmyPDF arguments are fixed in the scripts.

`OCR_JOBS` controls OCRmyPDF's internal workers for one PDF; it does not
parallelize files. The API host can run multiple batcher containers or
Kubernetes Jobs concurrently, subject to its own `MAX_CONCURRENT_JOBS`
setting.

## Processing stages

Both entry points validate their arguments/settings and normalize the source
and target paths. The source must exist; the target is created if needed.
They reject identical source and target paths and a target inside the source.
The scripts create `.ocr_sequencial_state` in the target.

They then take a null-delimited snapshot of regular files, classify PDFs by
case-insensitive `.pdf` suffix, copy non-PDF files first, and OCR PDFs
sequentially. Temporary output files are moved into place only after a
successful copy or OCR operation. Each PDF's completion marker is written
after its output is published.

The marker signature includes the absolute source path, source SHA-256,
language, and optimization setting. A subsequent run skips a PDF only when
both its output and matching marker exist. Non-PDF outputs are skipped when
`cmp` finds identical contents.

## Runtime differences

The native script checks dependencies and can offer to run the included
distribution-aware installer. It must be started inside an existing tmux
session. It writes OCR and error logs in `/tmp`; its progress and log panes are
closed on exit.

The Docker entry point uses fixed mount paths and needs no host OCR
installation or tmux. It writes logs to
`/destination/.ocr_sequencial_state/logs` by default; `LOG_DIR` can override
that location. Container output is streamed to the terminal, while
OCRmyPDF's detailed output is written to its log.

Both variants continue after per-file failures, summarize failures, and exit
nonzero if any file failed. They install signal handlers for `INT`, `TERM`,
and `HUP`; see [operations and errors](operations-and-errors.md).
