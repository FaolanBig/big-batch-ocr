# Big Batch OCR API Host

This subproject provides one API host for Docker and Kubernetes deployments. It automatically detects an in-cluster Kubernetes service account and starts OCR jobs as Kubernetes Jobs; otherwise, it uses Docker Engine. The API contract and job behavior are the same on both platforms. For security, use `.tar.gz` archives attached to multipart API requests and leave directory-based requests disabled (`ALLOW_DIRECTORY_ACCESS=false`) unless they are specifically required for trusted callers. Uploaded archives are extracted into isolated per-job input/output folders; results can be downloaded repeatedly, and the workspace is retained until explicitly removed through the job DELETE or cleanup API. When enabled, shared-directory jobs use caller-specified paths, and creating a missing output directory must be explicitly requested. Docker deployments use the setup configurator to create the selected data-root directory. Kubernetes deployments use a shared PersistentVolumeClaim mounted at `DATA_ROOT`; their OCR image is pulled by the cluster when it schedules a job.

For Kubernetes deployment, set the API image in [`k8s/api-host.yaml`](k8s/api-host.yaml) to a release containing Kubernetes support, then apply it after creating the `api-host-secrets` Secret with an `API_KEY`. The API host discovers Kubernetes automatically; no platform-selection setting is needed. See [`doc/deployment.md`](doc/deployment.md#kubernetes-deployment) for storage, permissions, and RBAC details.

Each request can choose synchronous execution (wait for OCR completion and receive the result `.tar.gz` archive in the response body) or asynchronous execution (return a job ID for status checks). Synchronous responses include the job ID in the `X-Job-ID` header; if the synchronous wait limit expires, the job continues and the error response includes a status URL. Asynchronous results can be downloaded from `GET /api/v1/jobs/{jobId}/result`. Jobs are queued according to a configurable concurrency limit. Use `POST /api/v1/jobs/{jobId}/cancel` to cancel a queued job immediately or request that a running job's managed container or Kubernetes Job be stopped. Running jobs pass through `cancelling` before reaching the terminal `cancelled` state; cancellation does not remove job data. Bearer-key authentication is required by default; unauthenticated access is available only when explicitly configured.

Submissions may include `webhookUrl` to receive a POST containing the final job status as JSON when processing finishes. Optional `webhookSecret` and `rootPath` values are sent to the callback as `X-Webhook-Secret` and `X-Root-Path` headers; both require `webhookUrl`. Delivery is best-effort with one attempt, and callback failures do not change the job outcome. See [`doc/quickstart-and-worker-setup.md`](doc/quickstart-and-worker-setup.md#completion-webhooks) for an example.

Use `POST /api/v1/jobs/{jobId}/resume` to retry a failed or cancelled job. The existing job ID, input and output paths, and OCR settings are reused; the previous attempt's status and result metadata are cleared, and the job is added to the end of the queue. Jobs in any other state cannot be resumed.

Use `DELETE /api/v1/jobs/{jobId}` to delete a terminal job's persisted metadata and, for uploaded-archive jobs, its private workspace. Active jobs must first be cancelled and reach a terminal state. Deleting a job that used shared directories never removes those caller-managed input or output directories.

Use `POST /api/v1/jobs/cleanup` with `{"scope":"uploads"}` to remove metadata and private workspaces for terminal jobs submitted with uploaded archives. Use `{"scope":"all"}` to also delete the entire caller-specified input and output directories for terminal shared-directory jobs. Queued, starting, running, and cancelling jobs are ignored; terminal jobs whose directories overlap active jobs are also left untouched. Unsafe or unresolvable terminal paths are preserved and counted in `jobsSkippedForUnsafePaths`. The `all` scope is destructive and permanent.

The roadmap covers the API contract and path validation ([#33](https://github.com/FaolanBig/big-batch-ocr/issues/33)), authentication and configuration ([#34](https://github.com/FaolanBig/big-batch-ocr/issues/34)), safe OCR container execution ([#35](https://github.com/FaolanBig/big-batch-ocr/issues/35)), synchronous/asynchronous jobs and queuing ([#36](https://github.com/FaolanBig/big-batch-ocr/issues/36)), deployment and usage documentation ([#37](https://github.com/FaolanBig/big-batch-ocr/issues/37)), and security-focused testing and release validation ([#38](https://github.com/FaolanBig/big-batch-ocr/issues/38)). Every roadmap issue uses the `dev_api_host` label.

Track the subproject in the [big-batch-ocr_api-host GitHub project](https://github.com/users/FaolanBig/projects/7).

---

## ❤️ Sponsor

- Bitcoin (BTC): `bc1q2ss7n5gv3tr68dfqtmcvy830pj4clf7wqxztk5`
- Ethereum (ETH): `0x8AA598780c6529DCB771B9455E2717043BB1a1e1`
- Solana (SOL): `21BpX9xhkitMEWx2iBxGXEVsFCMF9huViud24NbrPqNC`
