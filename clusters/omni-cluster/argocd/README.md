# Argo CD (GitOps control plane)

`values.yaml` installs Argo CD itself (a one-time imperative `helm install` — the
**bootstrap paradox**: the tool that syncs from git can't sync *itself* from git before it
exists). Everything after is declarative.

## Individual Applications today; app-of-apps is next

`apps/` holds one Argo `Application` per component (KEDA, Datadog operator, Vault, ESO,
MetalLB, demo-app). Each was bootstrapped once by hand (`kubectl apply -f`), then self-manages.

There is intentionally **no app-of-apps root** yet — it's the natural next step (a single
`Application` pointing at `apps/` so even the first apply is git-managed). It's called out
as open work in the runbooks rather than faked here, because the honest current state is
"individual Applications, hand-bootstrapped."

## `vault/` — the secrets bridge

`SecretStore` + `ExternalSecret` pairs that let External Secrets Operator authenticate to
Vault (by its own ServiceAccount — no static token) and materialize native k8s Secrets:
the Argo repo credential and the GHCR image-pull credential. See
[`../../../docs/tasks/task08-vault-eso.md`](../../../docs/tasks/task08-vault-eso.md).
