# Big Batch OCR: functionality ideas

This is a brainstorm, not a committed roadmap. Ideas are grounded in the current
Linux/Docker batch processor and its Docker-backed API, which already support
resumable folder processing, progress and logs, API authentication, queued
sync/async jobs, archive uploads, status polling, and result downloads.

## Best next additions

| Priority | Idea | Why it could help | A useful first version |
| --- | --- | --- | --- |
| High | Per-file result manifest | Batch exit status says whether anything failed, but callers need to know exactly which files succeeded, failed, or were skipped. | Write a machine-readable JSONL or JSON report with relative path, outcome, error category, and elapsed time. Keep it separate from OCR output and define whether it is replaced or appended on reruns. |
| High | Configurable OCR language and profiles | The batcher currently relies on OCRmyPDF's configured default language and fixed processing options, which may not suit multilingual or different-quality scans. | Add validated options for language and a small set of named profiles (for example, standard, archival, fast). Expose the same supported options through CLI, Docker, and API without allowing arbitrary command arguments. |
| High | API job cancellation | Long jobs cannot currently be stopped through the API after acceptance. Operators may need to cancel a mistaken or obsolete submission. | Add a cancellation endpoint, a terminal `cancelled` state, and explicit handling for queued versus running jobs. Ensure cancellation reaches the managed container and records the final outcome. |
| High | API idempotency for submissions | A client timeout can leave it unclear whether a submission was accepted; resubmitting can create duplicate work. | Support an `Idempotency-Key` with a documented retention window and return the original job for a repeated key/request. |
| High | Better operational health and capacity visibility | `/health` reports liveness only; operators cannot readily distinguish a healthy service from a full queue or unavailable Docker daemon. | Add a protected readiness/status endpoint with queue depth, active workers, and dependency reachability. Keep secrets, paths, and sensitive job details out of the response. |
| Medium | Per-job OCR options in API | API users currently choose input, output, and execution mode, while OCR settings are configured at deployment. | Allow a narrow, validated allowlist of settings (starting with language and profile) and persist the effective settings with the job for reproducibility. |
| Medium | Retry failed files or jobs | A per-file failure currently makes a batch exit nonzero, but users must rerun the batch and rely on resume markers to bypass completed work. | Add an explicit retry-failed-only mode based on a prior manifest; distinguish retryable infrastructure errors from invalid or corrupt input. |

## OCR and batch-processing ideas

- **Dry run / plan mode:** scan and classify files, show expected PDF and copy
  counts, destination paths, and conflicts without writing outputs.
- **Input validation and file diagnostics:** detect files with a `.pdf` suffix
  that are not readable PDFs, report encrypted or malformed PDFs clearly, and
  optionally inspect content instead of relying on the extension alone.
- **Page-level controls:** allow a page range or maximum page count for targeted
  work and previews. Define how partial results are named and whether they can
  share resume state with a complete document.
- **Optional OCRmyPDF switches:** expose common, safe choices such as output
  PDF/A mode, image optimization level, deskew/rotation, and sidecar text
  generation. Validate combinations and keep defaults backward-compatible.
- **Preserve or emit searchable text:** optionally create a `.txt` sidecar for
  each PDF, or extract recognized text into a structured report for indexing.
- **Resource-aware parallelism:** support a bounded number of files in flight
  in addition to per-document `OCR_JOBS`. Avoid oversubscribing CPU and memory;
  define how progress and failure counts behave under concurrency.
- **Watch mode:** monitor an incoming directory and process files once they are
  fully written. Include a stable-file check, shutdown behavior, and a way to
  avoid repeatedly retrying a bad file.
- **Selectable inputs:** accept include/exclude glob patterns or a manifest of
  relative paths, useful for splitting large collections into deliberate
  batches.
- **Output verification:** optionally verify that each published PDF opens and
  has expected pages/text before saving the resume marker.
- **Progress/report export:** provide a stable JSON progress format for scripts
  and dashboards instead of relying only on human-readable tmux output.

## API and service ideas

- **Job listing and filtering:** add a paginated endpoint to list recent jobs
  by status, creation time, or caller-supplied label. This is more manageable
  than tracking every returned status URL.
- **Job metadata and labels:** let clients attach a bounded external reference
  or label, such as an invoice batch ID, and return it in status/results.
- **Webhooks:** notify a caller on completion or failure, with signed payloads,
  retry/backoff, and a way to inspect delivery failures. Keep polling available.
- **Result lifecycle controls:** support explicit result deletion and
  configurable retention/cleanup for uploaded-job workspaces and completed job
  records. Document which data is removed and when.
- **Admission limits:** cap queued jobs, archive size, extracted file count,
  total extracted size, and per-job runtime. Return actionable errors when
  limits are reached.
- **Queue controls:** let an operator pause/resume intake or queued work and
  expose estimated queue wait based on observed runtimes.
- **Per-job diagnostics:** return a safe summary of file outcomes and error
  categories. Keep raw container logs restricted to operators unless a
  deliberate redaction/access model is added.
- **Multi-worker deployment:** allow workers on multiple hosts with shared
  storage and coordinated leases/locking. This is a larger architectural step;
  the current single-process queue and SQLite assumptions should not be scaled
  by simply adding API replicas.
- **Tenant or caller separation:** if the service is used by multiple teams,
  add scoped credentials and ownership checks so clients can only inspect,
  cancel, or download their own jobs.
- **Operational metrics:** export queue depth, job durations, failure counts,
  OCR throughput, and worker utilization for Prometheus or another metrics
  backend.

## Usability and integration ideas

- **Small web dashboard:** submit jobs, view queue and progress, inspect
  per-file outcomes, and download completed results. Reuse the API as the source
  of truth rather than adding a separate job store.
- **CLI/API parity:** provide a command-line API client for submit, wait,
  download, and cancel workflows, with API keys read from an environment
  variable or protected config file.
- **Configuration validation command:** check OCR image availability, Docker
  access, data-root mappings and permissions, API settings, and OCR language
  packs before accepting production jobs.
- **Container and dependency updates:** document supported OCRmyPDF versions
  and language packs, and automate image rebuilds or vulnerability reporting
  while retaining immutable release tags/digests.
- **Accessible progress output:** add a non-tmux terminal mode and structured
  logs so native runs work in CI, systemd services, and environments without
  interactive terminal panes.

## Suggested sequencing

1. Add a per-file machine-readable manifest and use it to guide a failed-file
   retry mode.
2. Introduce validated OCR profiles and language selection consistently across
   native, Docker, and API execution.
3. Improve API operability with idempotent submission, job listing, and
   cancellation; keep lifecycle states and restart behavior explicit.
4. Add retention limits, admission control, and readiness/metrics before
   increasing deployment scale.
5. Consider a dashboard, webhooks, and multi-host workers after the API
   lifecycle and ownership model are stable.

## Questions to resolve before turning ideas into issues

- Which users are the priority: individual operators, automation clients, or
  multi-team service operators?
- Is the most important pain point OCR accuracy/configuration, visibility into
  individual file outcomes, or API job lifecycle?
- Should OCR settings be fixed by administrators, selectable from approved
  profiles, or freely configurable per job within a strict allowlist?
- What retention period and storage limits are acceptable for uploaded inputs,
  outputs, job records, and logs?
- Is single-host deployment sufficient, or is multi-host processing a real
  near-term requirement?
