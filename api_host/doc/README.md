# API-host documentation

- [`quickstart-and-worker-setup.md`](quickstart-and-worker-setup.md): single
  Docker-host setup, worker model, direct archive upload, polling, result
  download, webhooks, cleanup, and troubleshooting.
- [`deployment.md`](deployment.md): runtime configuration, Docker and
  Kubernetes deployment, API contract examples, storage, and security.
- [`../k8s/api-host.yaml`](../k8s/api-host.yaml): single-replica Kubernetes
  deployment with scoped RBAC, network policy, shared data storage, and
  persistent job state.
- [`../run/run_container.sh`](../run/run_container.sh): starts a local API-host
  build or prompts before pulling the `API_IMAGE` configured in an environment
  file; if the default file is missing, it offers to locate or create one.
- [`../config/configure_api_host.sh`](../config/configure_api_host.sh):
  interactively creates `../env/.env`, including an API key and a
  Docker-visible data root.
