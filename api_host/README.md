# Big Batch OCR API Host

The API host accepts OCR jobs and runs the configured OCR image through Docker
Engine or Kubernetes Jobs. It selects Kubernetes automatically when it has
in-cluster service-account credentials; otherwise it uses Docker Engine. The
same API and job behavior apply on both backends.

## Input and output

The recommended and default input method is to upload a `.tar.gz` archive as
multipart form data. The API extracts it into an isolated per-job input/output
workspace. The complete request is limited to 1 GiB and extracted contents to
10 GiB. Archive entries must be regular files or directories; unsafe paths and
other entry types are rejected.

For asynchronous jobs, download results with
`GET /api/v1/jobs/{jobId}/result` after success. Synchronous submissions
return the result archive directly. Results may be downloaded repeatedly.
The workspace remains until explicitly removed through the job DELETE or
cleanup endpoint.

Directory-based submissions are available only when
`ALLOW_DIRECTORY_ACCESS=true`; this setting defaults to `false`. Such
submissions identify existing input and output paths relative to `DATA_ROOT`.
Missing output paths can be created only when the caller sets
`createDestination: true`.

## Job lifecycle

Submissions select `synchronous` or `asynchronous` mode. Synchronous requests
wait up to `SYNC_WAIT_LIMIT_SECONDS` and return a `.tar.gz` result on success;
the completed job ID is in the `X-Job-ID` header. If the wait limit expires,
the job continues and the response provides its ID and status URL.
Asynchronous requests return a job ID and status URL immediately. Jobs are
queued FIFO and run up to `MAX_CONCURRENT_JOBS` at once. `OCR_JOBS` controls
OCRmyPDF threads inside each OCR container, not the queue concurrency.

Use `GET /api/v1/jobs/{jobId}` to poll, `POST .../{jobId}/cancel` to request
cancellation, and `POST .../{jobId}/resume` to retry a failed or cancelled
job. Cancellation does not delete data. `DELETE /api/v1/jobs/{jobId}` removes
terminal job metadata and, for uploaded jobs, its private workspace. Cleanup
with scope `uploads` removes terminal uploaded jobs; scope `all` also removes
caller-managed directories recorded for eligible terminal directory jobs.
Scope `all` is destructive; unsafe paths and directories overlapping active
jobs are preserved.

Bearer-key authentication is required unless `ALLOW_ANONYMOUS=true` is
explicitly configured. Keep the API private and protect its key. Docker socket
access grants broad control over the Docker host. Kubernetes deployments use
the provided single-replica manifest, namespace-scoped RBAC, a shared data PVC,
and a separate persistent database PVC.

Optional `webhookUrl` submissions receive one best-effort POST with the
terminal job resource. `webhookSecret` and `rootPath` are sent as callback
headers; `rootPath` is also present in the webhook JSON body. HTTPS
verification is enabled unless the per-job
`webhookInsecureSkipVerify: true` option is used for a trusted self-signed
endpoint.

## Documentation

- [Quickstart and worker model](doc/quickstart-and-worker-setup.md)
- [Configuration and deployment](doc/deployment.md)
- [Docker image and Compose](docker/README.md)
- [Kubernetes manifest](k8s/api-host.yaml)
- [OpenAPI contract](api/api_specification/main_api_specification_api_host.json)
