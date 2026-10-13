# Docker deployment

`Dockerfile` builds a minimal non-root API-host image. `compose.yaml` shows the
required Docker socket, daemon-visible data-root mapping, persistent database,
and pinned OCR image configuration. See [`../doc/deployment.md`](../doc/deployment.md)
before granting Docker socket access.
