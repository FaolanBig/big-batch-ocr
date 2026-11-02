# Operations, logs, and errors

## Progress

Both entry points update a status file under `/tmp` while processing. It
contains total and processed file counts, PDF counts, failures, elapsed time,
an estimated remaining time, and the current file.

The native script displays status in a tmux pane and opens a second pane
following the OCR log. The Docker entry point does not use a progress pane;
status remains internal while script messages are streamed to the container
output.

## Logs

Each run creates an OCR output log and an error log. The OCR log includes a
start marker for each PDF and OCRmyPDF's output. The error log records failed
copies, OCR failures, marker/publication problems, and interruption notices.
Both paths are printed when the run finishes.

- Native logs are stored in `/tmp` and remain after the run.
- Docker logs are stored in `LOG_DIR`, which defaults to
  `<target>/.ocr_sequencial_state/logs`. Set `LOG_DIR` to use another
  directory. Script output is also streamed to the container terminal.

For the API-host workflow, the managed OCR runtime's stdout/stderr is captured
in the job database (up to 32 KiB). The batcher's own Docker logs and marker
files remain in the mounted job output directory.

## Temporary files and cleanup

The scripts create a temporary file list and temporary destination files for
copies, OCR output, and markers. An `EXIT` trap removes outstanding temporary
files and the status file. The native script also closes the progress and log
panes. Published files and completion markers are retained.

## Failure and interruption behavior

An item failure is logged and counted; later files are still processed. Exit
status is:

- `0` when all copy and OCR operations succeed
- `1` when one or more files fail
- `129`, `130`, or `143` for `HUP`, `INT`, or `TERM`

Signal handlers record the current file in the error log. The temporary
output for an unsuccessful OCR run is removed. If output publication
succeeded but marker publication failed, the next run treats the output as
unverified and retries the PDF.

## Operational limits

- The native entry point is for Linux and requires an existing tmux session.
- The Docker entry point uses fixed `/source` and `/destination` mounts and
  does not need tmux on the host.
- PDF identification is based on the filename suffix; file contents are not
  inspected during discovery.
- Files are processed sequentially. `OCR_JOBS` controls OCRmyPDF threads
  within a single PDF, not file-level parallelism.
- Deleting `.ocr_sequencial_state` removes PDF markers and forces
  reprocessing. Docker logs below this directory are deleted with it.

See [state and recovery](state-and-recovery.md) for marker behavior and
[Run OCR in Docker](run_with_docker.md) for container logging details.
