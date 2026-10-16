# API host source

The API host is implemented in Go. From this directory, run `go test ./...` and
`go build -o api-host .`. Runtime configuration and Docker deployment are
documented in [`../doc/deployment.md`](../doc/deployment.md).

The executable exposes `GET /health`, `POST /api/v1/jobs`,
`GET /api/v1/jobs/{jobId}`, and `POST /api/v1/jobs/{jobId}/resume` as defined
by the OpenAPI specification.
