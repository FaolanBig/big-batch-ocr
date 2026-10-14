# Big Batch OCR API Host

This subproject will provide a Dockerized API for Workato and other automation platforms to submit OCR jobs using the existing Big Batch OCR container. The API host accepts either an input directory already present under a configured data root or a `.tar.gz` archive attached to a multipart API request. It validates the input and output, mounts the input read-only and output read-write in the OCR container, and supports downloading a successful job's processed files as a `.tar.gz` archive using its job ID. Creating a missing output directory must be explicitly requested.

Each request can choose synchronous execution (wait for the OCR result) or asynchronous execution (return a job ID for status checks). Jobs are queued according to a configurable concurrency limit. Bearer-key authentication is required by default; unauthenticated access is available only when explicitly configured.

The roadmap covers the API contract and path validation ([#33](https://github.com/FaolanBig/big-batch-ocr/issues/33)), authentication and configuration ([#34](https://github.com/FaolanBig/big-batch-ocr/issues/34)), safe OCR container execution ([#35](https://github.com/FaolanBig/big-batch-ocr/issues/35)), synchronous/asynchronous jobs and queuing ([#36](https://github.com/FaolanBig/big-batch-ocr/issues/36)), deployment and usage documentation ([#37](https://github.com/FaolanBig/big-batch-ocr/issues/37)), and security-focused testing and release validation ([#38](https://github.com/FaolanBig/big-batch-ocr/issues/38)). Every roadmap issue uses the `dev_api_host` label.

Track the subproject in the [big-batch-ocr_api-host GitHub project](https://github.com/users/FaolanBig/projects/7).

---

## ❤️ Sponsor

- Bitcoin (BTC): `bc1q2ss7n5gv3tr68dfqtmcvy830pj4clf7wqxztk5`
- Ethereum (ETH): `0x8AA598780c6529DCB771B9455E2717043BB1a1e1`
- Solana (SOL): `21BpX9xhkitMEWx2iBxGXEVsFCMF9huViud24NbrPqNC`
