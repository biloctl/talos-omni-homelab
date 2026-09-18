# Cluster manifests (`omni-cluster`)

The git-managed manifests for `omni-cluster`, laid out as the real GitOps tree. Most
directories are reconciled by Argo CD; `istio/` and `kubevirt/` mix Helm values files
(`*-values.yaml`, applied with `helm upgrade`) with hand-applied manifests, and adopting
them into Argo is tracked as open work in tasks 10–11. The runbooks under [`../docs/tasks/`](../docs/tasks/) narrate how each was
built and *why*.

## Reference, not directly `kubectl apply`-able

A few files assume live infrastructure or runtime-materialized credentials:

- `omni-cluster-template.yaml` references machine IDs (placeholders here).
- The Argo Applications point `repoURL` at a git repo, and some charts pin versions.
- `argocd/vault/*` and the `ghcr-pull` / `datadog-secret` references expect **External
  Secrets Operator + Vault** to materialize the actual Secret at runtime.
- `istio/demo-app-gateway.yaml` references a TLS Secret (`example-com-wildcard`) created
  out-of-band from a Let's Encrypt wildcard; `kubevirt/` expects nested virtualization on
  the worker VMs and the KubeVirt/CDI operators installed first (task 11).

Treat this tree as an accurate structural reference, not a one-command deploy.

## Why there are (almost) no secrets in here

That's the design, not an omission. Every credential is **referenced by name/path**, never
stored:

- `ExternalSecret` objects name a Vault key; ESO fetches it and writes the k8s Secret at
  runtime (see `argocd/vault/`).
- `imagePullSecrets` / `apiSecret` name a Secret that ESO/Vault populates.

The only places credentials ever live — Talos machine config, Terraform state, actual
Secret objects — are deliberately **excluded** from git (see [`../.gitignore`](../.gitignore)).
So the high-signal architecture files are safe to publish precisely *because* of the
secrets design shown in tasks 7–8.
