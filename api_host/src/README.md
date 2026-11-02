# API host source

The API host is implemented in Go. From this directory, run `go test ./...` and
`go build -o api-host .`. Runtime configuration and Docker deployment are
documented in [`../doc/deployment.md`](../doc/deployment.md).

The HTTP API exposes unauthenticated `GET /health`, plus job endpoints that
require bearer authentication unless `ALLOW_ANONYMOUS=true`:
`POST /api/v1/jobs`, `GET` and `DELETE /api/v1/jobs/{jobId}`,
`POST /api/v1/jobs/{jobId}/cancel`, `POST /api/v1/jobs/{jobId}/resume`,
`GET /api/v1/jobs/{jobId}/result`, and `POST /api/v1/jobs/cleanup`.
Submissions can upload a `.tar.gz` archive directly; directory-based requests
are disabled unless `ALLOW_DIRECTORY_ACCESS=true`. See the OpenAPI
specification for request and response schemas.
