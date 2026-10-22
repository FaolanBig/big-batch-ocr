# Workato connector

The custom connector definition is
[`../ext/con/workato_connector.rb`](../ext/con/workato_connector.rb). It
connects Workato recipes to the API host using HTTPS and bearer-key
authentication. It does not deploy the API host or expose a Kubernetes
service; configure external HTTPS access separately as described in the
[Kubernetes Ingress guide](deployment.md#external-access-through-https-ingress).

## Connection setup

Create a Workato custom connector using the Ruby source in
[`../ext/con/workato_connector.rb`](../ext/con/workato_connector.rb), then
configure its connection fields:

| Field | Value |
| --- | --- |
| API host URL | The origin URL, for example `https://ocr.example.invalid`. Do not include `/api/v1` or `/health`. |
| Bearer API key | The `API_KEY` configured on the API host. Store it in Workato's connection/secret field, not in recipe data or source control. |

The URL must be reachable from Workato, use a valid TLS certificate, and route
`/health` unchanged to the API host. The connector's connection test calls
`GET /health`; this liveness endpoint is unauthenticated. API job operations
send the configured key as `Authorization: Bearer <key>`. Leave the key blank
only if the API operator has explicitly enabled anonymous access.

Use the public HTTPS hostname from the Ingress, not the Kubernetes ClusterIP,
pod IP, or port-forward address. Do not use a malformed scheme such as
`htttps://`, and do not append an API path to the base URL.

## Available actions

| Action | Purpose |
| --- | --- |
| Check API health | Calls `GET /health` to verify reachability. |
| Submit a directory OCR job | Submits `inputPath` and `outputPath` under the API host's data root. The host must have `ALLOW_DIRECTORY_ACCESS=true`; this is disabled by default. `createDestination` optionally requests creation of a missing output directory. |
| Submit an archive OCR job | Uploads an input `.tar.gz` file as multipart form data. The API creates a private per-job workspace; no output path is supplied. This is the recommended option for Workato and does not require directory access. |
| Get job status | Reads the current job state, timestamps, exit code, failure details, and status URL. |
| Cancel an OCR job | Cancels a queued job or requests cancellation of a running job. Poll status until it becomes `cancelled`. |
| Resume a failed or cancelled job | Requeues an eligible job with its saved input and OCR settings. |
| Download processed files | Downloads a succeeded job's output as a `.tar.gz` archive. |
| Delete a terminal job | Deletes the job record and, for archive submissions, its private workspace. Shared input/output directories are preserved. |
| Clean up terminal job data | `uploads` removes archive-submission data; `all` can also permanently delete shared input/output directories for eligible terminal jobs. Use `all` with care. |

Both submit actions accept optional OCR language and optimization settings.
Language must be at most 128 characters and match language codes separated by
`+` (for example `deu+eng`). Optimization must be an integer from 0 to 3.
Leaving either value blank uses the API host's configured default.

## Synchronous and asynchronous recipes

Both submission actions default to asynchronous mode. The response includes a
job ID, status, and status URL. Use the **Get job status** action to poll that
job; when status becomes `succeeded`, use **Download processed files** to
retrieve its result.

Synchronous mode waits for OCR completion and returns the result archive in the
submission action's `resultArchive` output. Prefer asynchronous mode for
potentially long jobs: the API host's synchronous wait limit does not cancel an
accepted job. If a synchronous request times out, use its returned job ID or
status URL to continue polling rather than submitting the job again.

Archive uploads are limited to a 1 GiB compressed request body and 10 GiB of
extracted contents. Handle result archives as binary data in subsequent recipe
steps; the connector's result output includes the suggested
`Content-Disposition` value.

## Troubleshooting

- A connection-test **404** generally means Workato reached an endpoint that
  does not route `/health` to the API host. Verify the base URL, DNS, Ingress
  class/address, and that the Ingress does not require a different path prefix.
- A TLS or certificate error means the Workato runtime cannot validate the
  endpoint certificate; use a certificate trusted by Workato and matching the
  hostname.
- A connection test can succeed while job actions fail with **401** if the API
  key is absent or incorrect. Check the configured secret and bearer
  authentication.
- **403** for directory submission means directory access is disabled. Use
  archive submission, or have the operator deliberately enable directory
  access for trusted callers.
- **400** indicates an invalid request or OCR option. Check the language and
  optimization constraints above and the API host's response details.

For API request and response schemas, see the
[OpenAPI specification](../api/api_specification/main_api_specification_api_host.json).
