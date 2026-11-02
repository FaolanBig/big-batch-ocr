# Docker deployment

`Dockerfile` builds a non-root API-host image with a shell and `curl` for
troubleshooting outbound connections from the container. `compose.yaml` shows
the Docker socket, daemon-visible data-root mapping, persistent database, and
configured OCR image. See
[`../doc/deployment.md`](../doc/deployment.md) before granting Docker socket
access.
