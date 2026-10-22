# Documentation

- [`quickstart-and-worker-setup.md`](quickstart-and-worker-setup.md): complete
  single-host setup, worker model, API smoke test, and troubleshooting.
- [`deployment.md`](deployment.md): configuration reference, deployment,
  automatic Docker/Kubernetes runtime detection, Docker socket risk, path
  mapping, and API usage.
- [`workato-connector.md`](workato-connector.md): Workato custom connector
  setup, authentication, actions, recipe patterns, and troubleshooting.
- [`../k8s/api-host.yaml`](../k8s/api-host.yaml): single API-host Kubernetes
  deployment with HTTPS Ingress, service account RBAC, shared data storage,
  and persistent job state.
- [`../run/run_container.sh`](../run/run_container.sh): starts a pre-pulled
  GHCR API-host image or either locally built API-host image using `env/.env`.
- [`../config/configure_api_host.sh`](../config/configure_api_host.sh):
  interactively creates `../env/.env` from the requested settings.
