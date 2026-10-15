# Big Batch OCR API Host

This subproject provides a Dockerized API for Workato and other automation platforms to submit OCR jobs using the Big Batch OCR container. The API host accepts either an input directory already present under a configured data root or a `.tar.gz` archive attached to a multipart API request. Uploaded archives are extracted into isolated per-job input/output folders; the processed output can be downloaded as a `.tar.gz` using the job ID, after which that uploaded job's workspace is deleted. Shared-directory jobs continue to use caller-specified paths, and creating a missing output directory must be explicitly requested. The setup configurator creates the selected data-root directory; the API host pulls the configured OCR image on first use when it is not available locally.

Each request can choose synchronous execution (wait for the OCR result) or asynchronous execution (return a job ID for status checks). Jobs are queued according to a configurable concurrency limit. Use `POST /api/v1/jobs/{jobId}/cancel` to cancel a queued job immediately or request that a running job's managed container be stopped. Running jobs pass through `cancelling` before reaching the terminal `cancelled` state. Cancelling a job submitted with an uploaded archive removes its private input and output workspace after the container has stopped; shared-directory inputs and outputs are not removed. Bearer-key authentication is required by default; unauthenticated access is available only when explicitly configured.

Use `DELETE /api/v1/jobs/{jobId}` to delete a terminal job's persisted metadata and, for uploaded-archive jobs, its private workspace. Active jobs must first be cancelled and reach a terminal state. Deleting a job that used shared directories never removes those caller-managed input or output directories.

The roadmap covers the API contract and path validation ([#33](https://github.com/FaolanBig/big-batch-ocr/issues/33)), authentication and configuration ([#34](https://github.com/FaolanBig/big-batch-ocr/issues/34)), safe OCR container execution ([#35](https://github.com/FaolanBig/big-batch-ocr/issues/35)), synchronous/asynchronous jobs and queuing ([#36](https://github.com/FaolanBig/big-batch-ocr/issues/36)), deployment and usage documentation ([#37](https://github.com/FaolanBig/big-batch-ocr/issues/37)), and security-focused testing and release validation ([#38](https://github.com/FaolanBig/big-batch-ocr/issues/38)). Every roadmap issue uses the `dev_api_host` label.

Track the subproject in the [big-batch-ocr_api-host GitHub project](https://github.com/users/FaolanBig/projects/7).

---

## ❤️ Sponsor

- Bitcoin (BTC): `bc1q2ss7n5gv3tr68dfqtmcvy830pj4clf7wqxztk5`
- Ethereum (ETH): `0x8AA598780c6529DCB771B9455E2717043BB1a1e1`
- Solana (SOL): `21BpX9xhkitMEWx2iBxGXEVsFCMF9huViud24NbrPqNC`
