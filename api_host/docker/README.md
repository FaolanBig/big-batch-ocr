# Docker deployment

`compose.yaml` runs a published, non-root API-host image from GitHub Container
Registry. It does not build from the repository or require the repository
source to be present on the deployment host. The image defaults to
`ghcr.io/faolanbig/big-batch-ocr_api-host:v1.2.9`; set `API_IMAGE` to override
it with another release tag or digest. Pass the runtime environment file to
Compose. The OCR image defaults to
`ghcr.io/faolanbig/big-batch-ocr_ocr-sequencial:v1.2.9`; set `OCR_IMAGE` to
override it.

```sh
docker compose --env-file ../env/.env -f compose.yaml pull
docker compose --env-file ../env/.env -f compose.yaml up -d
```

The Compose file shows the Docker socket, daemon-visible data-root mapping,
persistent database, and configured OCR image. See
[`../doc/deployment.md`](../doc/deployment.md) before granting Docker socket
access.

`Dockerfile` remains available for maintainers who need to build the image
locally. A local build requires the API-host source checkout because the
Dockerfile copies `api_host/src`; it is not part of the runtime Compose
deployment.
