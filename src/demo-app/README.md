# demo-app

A tiny Go (stdlib-only) HTTP service used to exercise the CI/CD + GitOps loop end to end.

- Serves `/` (returns its build version) and `/metrics` (a hand-rolled Prometheus
  `http_requests_total` counter — the scrape target for the KEDA/Prometheus demo).
- Built by [`../../.github/workflows/build-demo-app.yaml`](../../.github/workflows/build-demo-app.yaml):
  a **multi-stage Dockerfile** (Go toolchain in the build stage; only the static
  `CGO_ENABLED=0` binary copied into a `distroless/static:nonroot` final image — a few MB,
  no shell, passes Talos `baseline` PodSecurity with no privileged label).
- Tagged by **immutable commit SHA** (never `latest`); the workflow commits the new tag into
  `clusters/omni-cluster/apps/demo-app/deployment.yaml`, which Argo CD then syncs — the seam
  where CI hands off to CD through git.

Full loop — push → build → GHCR → manifest bump → Argo sync — in
[`../../docs/tasks/task06-github-actions-cicd.md`](../../docs/tasks/task06-github-actions-cicd.md).
