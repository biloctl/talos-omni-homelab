# Task 8: HashiCorp Vault + External Secrets Operator (secrets management)

> **Goal:** Learn Vault as a production-standard secrets manager by migrating the hand-created Kubernetes Secrets from earlier tasks (the Argo repo PAT that expired and broke CD in Task 7, and the GHCR image-pull secret from Task 6) to Vault-sourced credentials, delivered into the cluster by the External Secrets Operator. Prove the loop end-to-end (Argo re-authenticates to git; a private image pulls) with no hand-minted static token left in the credential path — all git-managed.
>
> **Status:** ✅ **Core complete.** Self-hosted Vault (single-node Raft on Ceph) installed via Argo; initialized + unsealed (manual Shamir); Kubernetes auth method wired so ESO authenticates by ServiceAccount (no static Vault token anywhere); kv-v2 engine holds both secrets; least-privilege per-secret Vault policies + roles. ESO installed via Argo Application. **Both migrations proven:** the Argo `repository` Secret (Opaque) and the GHCR `dockerconfigjson` pull Secret rebuilt from Vault, Argo stays `Synced/Healthy`, private image pulls `1/1 Running`. Branch `task08-vault` → PR → squash-merged to `main`.
>
> **Scope note:** This "task" in the original plan was four things (StrongDM + RBAC + NetworkPolicies + Vault). **Split by decision:** Task 8 = Vault only. StrongDM + RBAC + NetworkPolicies = Task 9 (NetworkPolicies overview-only — see the flannel finding below; StrongDM documented as architecture).

---

## Environment Facts

| Thing | Value |
|---|---|
| Cluster | `omni-cluster` (Omni-managed), k8s 1.36.2 / Talos 1.13.6 |
| kubeconfig context | `omni-cluster` |
| Repo / branch | `example-user/homelab-k8s`, branch `task08-vault` |
| Vault Helm chart | `hashicorp/vault` **chart `0.34.0`** (ships Vault app **`v2.0.3`**) |
| Vault mode | single-node **Raft** (integrated storage), PVC 2Gi on `rook-ceph-block`, injector **disabled** |
| Vault namespace / service | `vault` / `http://vault.vault.svc:8200` (plain HTTP — lab; prod terminates TLS) |
| Seal | **Shamir**, 3 shares / threshold 2, **manual unseal** (re-seals on every pod restart) |
| Vault auth method | `kubernetes` at `auth/kubernetes`, `kubernetes_host=https://10.96.0.1:443`, token-reviewer auto (Vault's own SA) |
| kv engine | kv-**v2** at mount path `secret/` |
| ESO Helm chart | `external-secrets/external-secrets` **chart `2.8.0`** |
| ESO namespace / SA | `external-secrets` / ServiceAccount `external-secrets` (confirmed off the running Deployment) |
| Secret 1 (repo PAT) | Vault `secret/argocd/repo` (`username`,`password`) → k8s `argocd/homelab-k8s-repo` (Opaque, `repository` label) |
| Secret 2 (GHCR pull) | Vault `secret/ghcr` (`username`,`password`) → k8s `demo-app/ghcr-pull` (`kubernetes.io/dockerconfigjson`) |
| Vault policies | `argocd-repo-ro` (read `secret/data/argocd/*`), `ghcr-ro` (read `secret/data/ghcr` **and** `secret/data/ghcr/*`) |
| Vault k8s-auth roles | `argocd-repo` → policy `argocd-repo-ro`; `ghcr-pull` → policy `ghcr-ro`; both bound to SA `external-secrets`/ns `external-secrets`, `ttl=1h` |
| Git files added | `argocd/apps/vault.yaml`, `argocd/apps/eso.yaml`, `argocd/vault/{store,repo-externalsecret,ghcr-store,ghcr-externalsecret}.yaml`, `.gitignore` fix |

**Network personality note:** friendly to the locked-down VLAN. Vault is **in-cluster**, so the credential traffic (ESO→Vault, Vault↔k8s API) is all internal LAN TCP; image/chart pulls are TCP/443. **No** outbound UDP / external DNS / non-443 dependency, and **no SSH/22** in this task. (StrongDM, when it comes in Task 9, is SaaS-brokered and WILL need egress verification — that's a Task-9 problem, not here.)

---

## THE ONE MENTAL MODEL — five orthogonal control planes

The confusing thing going in: StrongDM, RBAC, NetworkPolicies, and Vault feel like one topic ("security"). They are **four independent control planes**, each answering a different question — NOT a vertical dependency stack like the Task-4 layer cake. This is the "what owns what" that settles the whole multi-task arc:

| Question | Plane | Tool | Object type |
|---|---|---|---|
| How do humans get in? (audited?) | human access / audit | **StrongDM** (Task 9) | SaaS access broker |
| Who are you? | authentication | Auth0 / k8s auth (**already built**) | — |
| What may you **do** to the k8s API? | authorization | **RBAC** (Task 9) | `Role`/`ClusterRole` + `Binding` |
| What secrets can you **get**, for how long? | secrets | **Vault** (this task) | secrets engine, leases |
| Which workloads may **talk** to which? | network segmentation | **NetworkPolicy** (Task 9, overview) | namespaced policy object |

Read that as five locks on five different doors, not a stack. Only authentication existed before Task 8.

---

## Core concepts (settled up front, before touching anything)

### Vault is NOT a password vault
A password vault (1Password, a k8s Secret) *stores* a credential you made and hands the same bytes back forever. Vault does that too (its `kv` engine), but its real trick is **manufacturing** credentials on demand that didn't exist a second ago and are dead a minute later.

- **Secrets engine** [Vault plugin] — a mount that knows how to produce one *kind* of secret. `kv` stores/returns static values; `database` creates a brand-new DB user per request with a TTL; `pki` mints certs; `transit` = encryption-as-a-service. Conceptually the same "config → call an API → make reality match" shape as a Terraform provider, but issuing creds.
- **Static vs dynamic secret** — the whole point.
  - *Static* (`kv`): value in, same value out. Your Cloudflare/GHCR/repo PATs are static; moving them to Vault adds central storage + audit + access policy, but the credential is still long-lived + hand-minted.
  - *Dynamic* (`database`/`pki`/…): Vault **generates** the credential, attaches a **lease/TTL**, and **revokes it at the source** when the lease ends. Nobody typed it; it can't leak from a screenshot because it didn't exist then and is dead now.
- **Lease / TTL** — the timer on a dynamic secret; Vault tracks every lease and can mass-revoke ("something's breached — kill everything issued in the last hour").

### How this kills the Task-7 bug class
Task-7's outage was a fine-grained PAT that expired **silently on a fixed calendar date**, and Argo went `Unknown` until hand-rotated. That's the failure mode of *hand-minted static creds with a fixed expiry and no renewal*. Vault's dynamic model inverts it: the credential is *designed* short-lived and the client **continuously renews its lease** while healthy — so expiry is the normal handled case, not a surprise.

**Honest limit (where Vault is partly overkill here):** GitHub/Cloudflare PATs have **no Vault dynamic engine** that issues short-lived versions the way `database` does for Postgres. For *these* migration candidates the realistic step is `kv` — static secrets, centrally managed. Real wins (one audited source of truth, access policy, no secret sprawl in `kubectl`, rotate-in-one-place), but the dramatic "auto-issued short-lived creds" payoff shows up fully on **databases and PKI**, not third-party SaaS PATs. Don't oversell it on the PAT.

### The Vault ↔ Kubernetes integration pattern (the flagged unknown)
Vault holds the secret; the pod needs it as a file, env var, or native k8s Secret. Four bridges:

- **Vault Agent Injector** [controller / mutating admission webhook] — annotate a pod, webhook adds a sidecar that logs in and drops secret *files* into an in-mem volume. Great for app config; **bad for our candidates** (produces files in the pod, not a labeled k8s Secret).
- **Vault CSI provider** [per-node driver, DaemonSet] — secrets as a mounted volume via Secrets Store CSI. Same file-into-pod limitation for our case.
- **External Secrets Operator (ESO)** [controller/operator] — watches an `ExternalSecret` [CRD] and **materializes a native k8s Secret** synced from Vault. **Natural fit here** because our targets MUST be native Secrets (Argo `repository` label; GHCR `dockerconfigjson`). Community/CNCF, multi-backend (Vault is one of many).
- **Vault Secrets Operator (VSO)** [controller/operator] — HashiCorp's own ESO-equivalent, newer. Same CRD→native-Secret idea. 

**Chosen: ESO**, because the proof secret (Argo repo PAT) must become a labeled k8s Secret — the one pattern that closes the exact Task-7 loop.

Sort: injector/CSI **deliver into a pod at mount time**; ESO/VSO are **reconciling controllers** keeping a native Secret in sync (self-heal on drift — same category as Argo/KEDA/Rook).

### The elegant core — ServiceAccount auth, no static Vault token
We do **not** hand ESO a static Vault token (that recreates the Task-7 problem). We enable Vault's **Kubernetes auth method**: ESO presents its own ServiceAccount JWT, Vault calls the k8s TokenReview API to verify it, maps the identity to a **role → policy**, and issues a **short-lived Vault token** scoped to exactly what the role allows. **The pod's existing k8s identity IS the credential.** Nothing static to rotate, expire, or leak.

---

## THE DATA FLOW (where things live)

```
 Vault server ──read──► ESO ──write──► k8s Secret ──read──► Argo CD ──auth──► GitHub
 (kv holds PAT)      (controller)    (labeled/native)                        (private repo)
        ▲                 │
        └── ESO authenticates to Vault with its OWN k8s ServiceAccount
            (role argocd-repo → policy → short-lived Vault token). No static token anywhere.
```

The credential now flows Vault → ESO → k8s Secret → Argo → GitHub with no hand-minted static token in the middle. Same shape for the GHCR pull path (target ns `demo-app`, role `ghcr-pull`, kubelet pulls the image).

---

## PART 1 — Install Vault server via Argo (Helm)

Same pattern as KEDA/Datadog: public Helm chart delivered by an Argo Application. Vault has **no CRDs**, so no `ServerSideApply` needed for the server itself (ESO does — see Part 6).

Confirm the chart version (pin-every-chart habit; note BOTH columns):
```bash
helm repo add hashicorp https://helm.releases.hashicorp.com 2>/dev/null; helm repo update hashicorp
helm search repo hashicorp/vault --versions | head
#   CHART VERSION -> targetRevision (the packaging);  APP VERSION -> which Vault binary (informational)
#   picked chart 0.34.0 = Vault app v2.0.3
```

`clusters/omni-cluster/argocd/apps/vault.yaml`:
```yaml
apiVersion: argoproj.io/v1alpha1
kind: Application
metadata: { name: vault, namespace: argocd }
spec:
  project: default
  source:
    repoURL: https://helm.releases.hashicorp.com     # public helm, 443, no cred
    chart: vault
    targetRevision: 0.34.0                            # CHART version (ships Vault v2.0.3)
    helm:
      values: |
        server:
          ha:
            enabled: true
            raft: { enabled: true }
            replicas: 1                # single-node Raft; scale later
          dataStorage:
            enabled: true
            size: 2Gi
            storageClassName: rook-ceph-block   # Task-2 storage earns its keep again
          extraEnvironmentVars:
            VAULT_DISABLE_MLOCK: "true"           # HashiCorp guidance w/ integrated storage; also dodges IPC_LOCK vs Talos baseline PSA
        injector: { enabled: false }              # ESO route, not the sidecar injector
  destination: { server: https://kubernetes.default.svc, namespace: vault }
  syncPolicy:
    automated: { prune: true, selfHeal: true }
    syncOptions: [ CreateNamespace=true ]
```
```bash
kubectl config current-context      
git add clusters/omni-cluster/argocd/apps/vault.yaml && git commit -m "task 9: install Vault via Argo (Raft, ESO route, mlock off)"
git push -u origin task08-vault
kubectl apply -f clusters/omni-cluster/argocd/apps/vault.yaml   # first Application per pattern = hand-applied
kubectl -n argocd get application vault -w
kubectl -n vault get pods,pvc,svc
```

**Expect `vault-0` `Running` but `0/1` Ready and the Argo app `Progressing`, NOT `Healthy`.** That is CORRECT — a fresh Vault is uninitialized + **sealed**, and its readiness probe fails while sealed. Unsealing (Part 2) flips it to `1/1`. Four services appear (`vault`, `vault-active`, `vault-standby`, `vault-internal` on 8200/8201).

---

## PART 2 — Init + unseal (the DANGEROUS step)

**`vault operator init` prints the unseal key shares + initial root token EXACTLY ONCE and they are never recoverable. Lose the shares → the Raft data is cryptographically dead — no reset, no support, gone.** This is the one homelab step where a mistake is not a `git revert` away. Output goes to a **password manager**, never the repo / a plaintext file / a screenshot. Redact key material when sharing terminal output.

**Seal/unseal model (the WHY):** Vault encrypts everything with a master key it never stores on disk. At init that master key is split via **Shamir's Secret Sharing** [threshold crypto] into N shares; you need K to reconstruct it. Vault boots **sealed** (has ciphertext, not the key) and is useless until K shares are fed in to rebuild the key in memory. In prod, N shares go to N humans so no one can unseal alone.

```bash
kubectl -n vault exec -it vault-0 -- vault operator init -key-shares=3 -key-threshold=2
#   >>> COPY all 3 unseal keys + the root token into a password manager NOW <<<

kubectl -n vault exec -it vault-0 -- vault operator unseal <UNSEAL_KEY_1>   # Sealed true, progress 1/2
kubectl -n vault exec -it vault-0 -- vault operator unseal <UNSEAL_KEY_2>   # Sealed false

kubectl -n vault get pods                                 # vault-0 -> 1/1 Ready
kubectl -n vault exec -it vault-0 -- vault status         # Initialized true, Sealed false, Storage raft, HA active
```

**Two operational realities (real costs, not bugs):**
- **Every `vault-0` restart re-seals Vault** (node reboot, pod eviction, upgrade) → you must manually unseal again, and until you do, **ESO stops syncing → Argo loses its repo cred on next refresh.** Auto-unseal (cloud KMS or a second Vault's `transit` engine) removes the chore but adds a dependency — the prod-vs-simple trade we took deliberately. **If CD mysteriously breaks after a reboot: unseal Vault first.**
- This chart runs the listener over **plain HTTP, no TLS** (lab). Fine internally on the segregated VLAN; prod terminates TLS on the listener.

---

## PART 3 — Kubernetes auth method

```bash
kubectl -n vault exec -it vault-0 -- sh
```
```sh
export VAULT_ADDR=http://127.0.0.1:8200
vault login <ROOT_TOKEN>

vault auth enable kubernetes
vault write auth/kubernetes/config kubernetes_host="https://$KUBERNETES_PORT_443_TCP_ADDR:443"
vault read auth/kubernetes/config    # kubernetes_host resolved to https://10.96.0.1:443
exit
```
- **Minimal config is correct on Vault 1.9+/2.0:** `token_reviewer_jwt_set:false` / `kubernetes_ca_cert:n/a` in the read-back is *expected* — Vault uses its **own** mounted SA token + CA to do the TokenReview at request time. Don't add the old `token_reviewer_jwt`/`kubernetes_ca_cert` flags.
- `$KUBERNETES_PORT_443_TCP_ADDR` is the API-server ClusterIP env var k8s injects into every pod (avoids hardcoding). `https://kubernetes.default.svc:443` also works.
- Vault **2.0 is past training cutoff** — if a flag behaves differently, **read the command output, not recall.** Fresh install = no migration-gate pain (unlike the Omni climb).

---

## PART 4 — kv engine + write the secrets

**Decision: mint FRESH PATs, don't copy the old ones.** Copying leaves the old hand-minted token live + unaudited (you moved the secret without rotating it). Fresh = the old one gets retired and proves the Vault path end-to-end. Gate every captured secret (Task-6 poisoned-`read` scar):
```bash
GH_PAT='github_pat_...'                              # fine-grained, owner example-user, only homelab-k8s, Contents: Read-only
echo "starts: ${GH_PAT:0:11} length: ${#GH_PAT}"     # expect github_pat_ / ~93 chars
```
```bash
# enable kv v2 at secret/ (skip if "path is already in use" — some installs pre-mount it)
kubectl -n vault exec -it vault-0 -- sh -c 'export VAULT_ADDR=http://127.0.0.1:8200; vault login "$0" >/dev/null; vault secrets enable -path=secret kv-v2' '<ROOT_TOKEN>'

# write repo PAT (username + password = the two fields the Argo repository Secret needs)
kubectl -n vault exec -i vault-0 -- sh -c 'export VAULT_ADDR=http://127.0.0.1:8200; vault login "$0" >/dev/null; vault kv put secret/argocd/repo username=example-user password="$1"' '<ROOT_TOKEN>' "$GH_PAT"
unset GH_PAT

kubectl -n vault exec -it vault-0 -- sh -c 'export VAULT_ADDR=http://127.0.0.1:8200; vault login "$0" >/dev/null; vault kv get -field=username secret/argocd/repo' '<ROOT_TOKEN>'   # -> example-user
```
- **kv-v2 vs v1:** v2 adds versioning + soft-delete (current default). Its API path injects a hidden `data/` segment (`secret/data/argocd/repo` at the raw level) — matters in POLICIES (Part 5), NOT in ESO config (Part 7).
- Passing token/PAT as positional `"$0"`/`"$1"` keeps them out of shell history literals — lab-grade, not airtight. The clean version uses the k8s-auth path (which ESO does), not the root token.

---

## PART 5 — Vault policy + role (least privilege)

Two objects, mirroring k8s Role/RoleBinding:
- **Vault policy** [ACL object] = a grant (path + capabilities), attached to nothing yet.
- **Kubernetes auth role** [binding] = ties a specific ServiceAccount to that policy + a token TTL.

**kv-v2 footgun #1:** the policy path needs the `data/` segment → `secret/data/argocd/*`, NOT `secret/argocd/*`.
```bash
kubectl -n vault exec -it vault-0 -- sh
```
```sh
export VAULT_ADDR=http://127.0.0.1:8200
vault login <ROOT_TOKEN>

vault policy write argocd-repo-ro - <<'EOF'
path "secret/data/argocd/*" { capabilities = ["read"] }
EOF

vault write auth/kubernetes/role/argocd-repo \
  bound_service_account_names=external-secrets \
  bound_service_account_namespaces=external-secrets \
  policies=argocd-repo-ro \
  ttl=1h

vault policy read argocd-repo-ro
vault read auth/kubernetes/role/argocd-repo
exit
```
- `bound_service_account_names`/`_namespaces` = the identity gate: **only** SA `external-secrets` in ns `external-secrets` can assume this role.
- `ttl=1h` = the Vault token ESO gets lives 1h then it re-auths with its SA JWT. Short-lived by design.
- `WARNING ... does not have an audience configured` on the role write is **informational** (optional stricter JWT claim), not an error.
- **Verify the SA name after ESO installs** (Part 6) — the role's `bound_service_account_names` must match the operator's real SA or you get `403 permission denied` at sync.

---

## PART 6 — Install ESO via an Argo Application

`ServerSideApply=true` up front — ESO ships CRDs and the 256KB client-side-apply annotation wall bit KEDA in Task 6.
```bash
helm repo add external-secrets https://charts.external-secrets.io 2>/dev/null; helm repo update external-secrets
helm search repo external-secrets/external-secrets --versions | head    # picked chart 2.8.0
```
`clusters/omni-cluster/argocd/apps/eso.yaml`  *(filename must AVOID the word "secret" — see gotcha #1)*:
```yaml
apiVersion: argoproj.io/v1alpha1
kind: Application
metadata: { name: eso, namespace: argocd }
spec:
  project: default
  source:
    repoURL: https://charts.external-secrets.io
    chart: external-secrets
    targetRevision: 2.8.0
    helm: { values: "installCRDs: true" }
  destination: { server: https://kubernetes.default.svc, namespace: external-secrets }
  syncPolicy:
    automated: { prune: true, selfHeal: true }
    syncOptions: [ CreateNamespace=true, ServerSideApply=true ]
```
```bash
git add clusters/omni-cluster/argocd/apps/eso.yaml && git commit -m "task 9: install ESO via Argo (SSA pre-empts CRD annotation wall)"
git push
kubectl apply -f clusters/omni-cluster/argocd/apps/eso.yaml
kubectl -n argocd get application eso -w        # transient Degraded during webhook-cert startup, then Healthy
kubectl -n external-secrets get pods            # external-secrets [controller] + -cert-controller + -webhook, all 1/1
# CONFIRM the SA the controller actually runs as (de-risks the Vault role):
kubectl -n external-secrets get deploy external-secrets -o jsonpath='{.spec.template.spec.serviceAccountName}{"\n"}'   # -> external-secrets
```
- Initial `Synced/Degraded` right after install is usually the **transient webhook-cert startup gap** (cert-controller mints the webhook's TLS cert before the webhook goes Ready). Self-resolves; read the pods, not the status string.
- ESO runs unprivileged (no host mounts) → **no `privileged` PSA label needed** (unlike node-exporter/Ceph/Datadog node-agent).

---

## PART 7 — SecretStore + ExternalSecret (the loop closes)

- **SecretStore** [namespaced CRD] = *how/where* to reach Vault + *who I am* (connection + auth). Lives in the ns where the target Secret must land.
- **ExternalSecret** [namespaced CRD] = *which* Vault key → *which* k8s Secret, shaped *how*. ESO reconciles it (self-heal on drift).

**kv-v2 footgun #2 (mirror of #1):** the SecretStore uses `path: "secret"` + `version: "v2"` and the ExternalSecret uses `key: argocd/repo` — **NO `data/` anywhere here.** ESO inserts `data/` itself. `data/` belongs in the POLICY, never in ESO config. (Double-`data/` = 404.)

**The SecretStore's `role:` selects which Vault role → which policy → which paths.** Get it wrong and you 403.

`clusters/omni-cluster/argocd/vault/store.yaml`:
```yaml
apiVersion: external-secrets.io/v1
kind: SecretStore
metadata: { name: vault, namespace: argocd }
spec:
  provider:
    vault:
      server: "http://vault.vault.svc:8200"   # in-cluster, plain HTTP
      path: "secret"                            # kv-v2 MOUNT path (NO data/)
      version: "v2"
      auth:
        kubernetes:
          mountPath: "kubernetes"               # auth/kubernetes
          role: "argocd-repo"                   # Part-5 role; picks policy argocd-repo-ro
          # ESO uses its OWN serviceaccount token by default — no secretRef needed
```
`clusters/omni-cluster/argocd/vault/repo-externalsecret.yaml`:
```yaml
apiVersion: external-secrets.io/v1
kind: ExternalSecret
metadata: { name: homelab-k8s-repo, namespace: argocd }
spec:
  refreshInterval: 1h
  secretStoreRef: { name: vault, kind: SecretStore }
  target:
    name: homelab-k8s-repo                 # SAME name Argo already reads
    creationPolicy: Owner                        # ESO creates + fully owns it
    template:
      metadata:
        labels: { argocd.argoproj.io/secret-type: repository }   # makes Argo treat it as a repo cred
      data:
        type: "git"
        url: "https://github.com/example-user/homelab-k8s.git"   # HTTPS (cluster can't SSH — Task 6)
        username: "{{ .username }}"
        password: "{{ .password }}"
  data:
    - secretKey: username
      remoteRef: { key: argocd/repo, property: username }        # Vault path UNDER the mount (no data/)
    - secretKey: password
      remoteRef: { key: argocd/repo, property: password }
```
Apply — clean ownership hand-off (delete the old hand-made Secret so ESO owns the recreation; one-resource-one-owner):
```bash
git add clusters/omni-cluster/argocd/vault/            # NOTE dir is argocd/vault/ NOT .../secrets/ (gitignore glob)
git check-ignore -v clusters/omni-cluster/argocd/vault/repo-externalsecret.yaml   # expect NO output
git commit -m "task 9: source Argo repo cred from Vault via ESO" && git push

kubectl -n argocd delete secret homelab-k8s-repo
kubectl apply -f clusters/omni-cluster/argocd/vault/store.yaml
kubectl apply -f clusters/omni-cluster/argocd/vault/repo-externalsecret.yaml

kubectl -n argocd get secretstore vault                      # STATUS Valid, READY True  <- ESO authed to Vault
kubectl -n argocd get externalsecret homelab-k8s-repo   # STATUS SecretSynced, READY True
kubectl -n argocd get secret homelab-k8s-repo           # recreated, Opaque, 4 keys (fresh AGE)
```
**The real proof — Argo re-authenticates to git with the Vault-sourced cred:**
```bash
kubectl -n argocd annotate application demo-app argocd.argoproj.io/refresh=hard --overwrite
kubectl -n argocd get application demo-app -w                # -> Synced / Healthy
kubectl -n argocd get secret homelab-k8s-repo -o jsonpath='{.metadata.labels}'; echo
#   shows argocd.argoproj.io/secret-type=repository AND reconcile.external-secrets.io/managed=true (ESO owns it)
```
`SecretStore Valid` = ESO authenticated to Vault by ServiceAccount (no static token). `demo-app Synced/Healthy` = Argo fetched the private repo with the Vault-sourced credential. **The Task-7 loop is closed.**

---

## PART 8 — Second migration: GHCR pull secret (dockerconfigjson)

Proves the `docker-registry` Secret shape + a **second scoped policy/role** (one identity, different least-privilege roles). GHCR pull needs a **classic** PAT (`ghp_`/40, `read:packages`) — fine-grained 403s on GHCR (Task 6).
```bash
GHCR_PAT='ghp_...'; echo "starts: ${GHCR_PAT:0:4} length: ${#GHCR_PAT}"    # ghp_ / 40
```
```sh
# inside vault-0, logged in as root:
vault kv put secret/ghcr username=example-user password=<GHCR_PAT>
vault policy write ghcr-ro - <<'EOF'
path "secret/data/ghcr"   { capabilities = ["read"] }
path "secret/data/ghcr/*" { capabilities = ["read"] }
EOF
vault write auth/kubernetes/role/ghcr-pull \
  bound_service_account_names=external-secrets \
  bound_service_account_namespaces=external-secrets \
  policies=ghcr-ro ttl=1h
```
`clusters/omni-cluster/argocd/vault/ghcr-store.yaml` (SecretStore `vault` in ns `demo-app`, `role: ghcr-pull`).
`clusters/omni-cluster/argocd/vault/ghcr-externalsecret.yaml` — the dockerconfigjson template:
```yaml
apiVersion: external-secrets.io/v1
kind: ExternalSecret
metadata: { name: ghcr-pull, namespace: demo-app }
spec:
  refreshInterval: 1h
  secretStoreRef: { name: vault, kind: SecretStore }
  target:
    name: ghcr-pull
    creationPolicy: Owner
    template:
      type: kubernetes.io/dockerconfigjson       # makes it a docker-registry Secret, not Opaque
      data:
        .dockerconfigjson: |
          {"auths":{"ghcr.io":{"username":"{{ .username }}","password":"{{ .password }}","auth":"{{ printf "%s:%s" .username .password | b64enc }}"}}}
  data:
    - secretKey: username
      remoteRef: { key: ghcr, property: username }
    - secretKey: password
      remoteRef: { key: ghcr, property: password }
```
```bash
git add clusters/omni-cluster/argocd/vault/ghcr-*.yaml && git commit -m "task 9: source GHCR pull secret from Vault via ESO" && git push
kubectl -n demo-app delete secret ghcr-pull
kubectl apply -f clusters/omni-cluster/argocd/vault/ghcr-store.yaml
kubectl apply -f clusters/omni-cluster/argocd/vault/ghcr-externalsecret.yaml
kubectl -n demo-app get externalsecret ghcr-pull            # SecretSynced / Ready True
kubectl -n demo-app get secret ghcr-pull                    # TYPE kubernetes.io/dockerconfigjson
kubectl -n demo-app delete pod -l app=demo-app              # force fresh pull (kubelet caches — Task 6)
kubectl -n demo-app get pods -w                             # 1/1 Running = private pull worked from Vault-sourced secret
```

**This is where the leaf-path 403 was hit and fixed — see gotcha #4.**

---

## KEY LESSONS / GOTCHAS (do these right next time)

1. **`.gitignore` should encode file SHAPE, not vocabulary.** A bare `*secret*` glob matched every path containing "secret" (`external-secrets.yaml`, `argocd/secrets/`) and silently un-staged safe manifests. Tightened to `*.secret` / `secrets.yaml` / `*-secret-values.yaml` — matching the extension/name convention of the other rules (`*.key`, `*.asc`, `*.tfstate`). The real defense against committing a credential is the `git status` eyeball + gating values into Vault, not the glob. Use `git check-ignore -v <path>` (authoritative) instead of eyeballing the ignore file.
2. **kv-v2 needs `data/` in the POLICY, and must NOT have it in the ESO config.** Policy: `secret/data/argocd/*`. SecretStore/ExternalSecret: `path: secret`, `key: argocd/repo`. Double-`data/` = 404; missing `data/` in the policy = 403.
3. **A Vault policy `path` ending in `/*` matches DESCENDANTS, not the node.** `secret/data/ghcr/*` does NOT cover a read of the leaf `secret/data/ghcr` → `403 permission denied`. A leaf secret needs its **exact path** granted. (`secret/argocd/repo` worked under `secret/data/argocd/*` because it's a child; `secret/ghcr` is a leaf.) Grant both `secret/data/ghcr` and `secret/data/ghcr/*` when unsure of shape.
4. **`SecretSyncedError` is a status, not a cause — read the ExternalSecret events.** `kubectl -n <ns> describe externalsecret <n>` (or `get ... -o jsonpath='{.status.conditions[*].message}'`) named it: `GET .../secret/data/ghcr → 403 permission denied`. Same "read the STATE not the symptom string" trap as the Task-6 CRD saga and Task-7 node-agent.
5. **401 vs 403 (Vault, same as GHCR).** 403 = authenticated, wrong scope (policy/role). 401 would be no/invalid identity. The 403 URL tells you the exact denied path.
6. **The SecretStore's `role:` selects the Vault role → policy → paths.** A wrong/misscoped role is a `403` on an otherwise-correct path. Verify policy + role in Vault (`vault policy read`, `vault read auth/kubernetes/role/<n>`) before blaming ESO.
7. **Confirm ESO's real ServiceAccount** (`get deploy external-secrets -o jsonpath=...serviceAccountName`) against the role's `bound_service_account_names` — a mismatch is a silent auth failure later.
8. **Vault re-seals on every pod restart** (manual Shamir). Until unsealed, ESO stops syncing → Argo loses its repo cred on next refresh. **If CD breaks after a reboot, unseal Vault first.** Auto-unseal removes this but adds a dependency.
9. **The unseal shares + root token are the single most dangerous artifact in the lab** — lose them and the Raft data is unrecoverable. Password manager only; never repo/plaintext/screenshot. Redact in any pasted output.
10. **Vault chart-version vs app-version drift.** `targetRevision` = CHART version (packaging); APP version = the Vault binary — record BOTH. Chart `0.34.0` shipped Vault `v2.0.3`; the app crossed a major (`1.21.2`→`2.0.x`) between chart minors while packaging barely changed.
11. **A namespace typo becomes a `--server` flag.** `kubectl -n external -secrets ...` (stray space) → kubectl parsed `-secrets` where `-s` = `--server`, tried to reach host `ecrets` → `dial tcp: lookup ecrets: no such host`. `dial tcp: lookup <weird-host>` almost always = an arg got parsed as `--server`. (Same family as the Task-6 zsh gotchas — the shell mangles the command before kubectl runs.)
12. **Vault 2.0 is past the training cutoff** — for exact init/unseal/auth flags, trust the command output over recall. Fresh install = no upgrade-gate pain.
13. **flannel does NOT enforce NetworkPolicies** — confirmed 6 flannel pods, no Calico/Cilium. Any NetworkPolicy on omni-cluster is a no-op. This is why Task 9 does NetworkPolicies as **overview/authoring only** (enforcement would need a CNI swap on a live cluster).

---

## Conceptual Q&A

**Q: How does Vault kill the class of bug that broke CD in Task 7?**
Task 7 = a hand-minted PAT that expired silently on a fixed date → Argo `Unknown`. Vault's model makes short-lived-and-renewed the *normal* case: ESO authenticates to Vault by its k8s ServiceAccount and gets a 1h Vault token it auto-renews. For dynamic engines (database/PKI) Vault issues the credential itself with a lease and revokes it at expiry — the credential literally can't outlive its lease. Caveat: for third-party SaaS PATs (GitHub/Cloudflare) there's no dynamic engine, so the win is "one audited source of truth + rotate-in-one-place," not auto-issued short-lived creds.

**Q: Static vs dynamic secret?**
Static (`kv`) = you store a value, Vault returns the same bytes (our repo/GHCR PATs). Dynamic (`database`/`pki`) = Vault generates a fresh credential per request with a TTL/lease and revokes it at the source when the lease ends. Dynamic is where the "no credential to leak/expire-surprise" magic lives.

**Q: Why ESO and not the Vault Agent Injector?**
The Injector drops secret files into the consuming pod — great for app config, useless for our targets, which MUST be native k8s Secrets (the Argo `repository`-labeled Secret; the `dockerconfigjson` pull Secret). ESO materializes exactly those from Vault and keeps them synced.

**Q: How does ESO authenticate to Vault without a stored Vault token?**
Vault's Kubernetes auth method. ESO presents its own ServiceAccount JWT; Vault verifies it via the k8s TokenReview API, maps the identity (SA `external-secrets`/ns `external-secrets`) to a role → policy, and issues a short-lived Vault token. The pod's k8s identity IS the credential — nothing static stored.

**Q: Why did the repo secret sync but GHCR 403 with the "same" setup?**
Policy path shape. `secret/argocd/repo` is a child of `argocd`, so `secret/data/argocd/*` matched it. `secret/ghcr` is a leaf — `secret/data/ghcr/*` matches children of `ghcr`, not `ghcr` itself. Fix: grant the exact path `secret/data/ghcr` too.

**Q: Why is Vault installed by Argo but init/unseal done by hand?**
Bootstrap paradox again (like Argo's own install and the first KEDA/Datadog Application). The server Deployment is declarative; but initialization generates irreplaceable key material and unsealing needs a human with the shares — that can't be GitOps'd. Everything after (engines, policies, roles, ESO CRs) is declarative/committed.

**Q: What's the operational cost of manual Shamir unseal?**
Every Vault pod restart re-seals it; you must manually feed K shares again, and ESO (hence Argo's repo cred) is down until you do. Auto-unseal (cloud KMS / transit) fixes the chore at the cost of an external dependency. Lab chose manual to learn the model.

---

## Git / GitHub workflow used (prod-style)

```
branch task08-vault
  -> Part 1: commit vault Application -> push -u
  -> Part 2-5: init/unseal/auth/kv/policy/role  (imperative bootstrap — nothing committable except later CRs)
  -> Part 6: commit eso Application -> push
  -> fix: tighten over-broad *secret* .gitignore glob -> commit -> push
  -> Part 7: commit SecretStore + ExternalSecret (repo) -> push
  -> Part 8: commit SecretStore + ExternalSecret (ghcr) -> push
  -> review own diff (git diff --stat main...HEAD) -> PR -> squash-merge -> delete branch -> prune
```
Committed: the two Argo Applications (`vault.yaml`, `eso.yaml`), the four Vault CRs under `argocd/vault/`, the `.gitignore` fix.
**NOT committed (out-of-band, imperative — note it):** the unseal keys/root token (password manager), the `vault operator init/unseal`, the `vault auth enable`/`kv put`/`policy write`/`role write` (Vault server state, not repo-reproducible), the fresh PATs. Same category as the Task-5 first-Application bootstrap / Task-6 server-side CRD bootstrap.

**Open items / debt (honest IaC notes):**
- SecretStore + ExternalSecret CRs are **hand-applied**, not wrapped in an Argo Application (app-of-apps gap — same as first KEDA/Datadog Applications). Bootstrap ordering: Vault must be init+unsealed and the k8s-auth role must exist *before* an ExternalSecret can sync — can't be fully GitOps'd.
- Consider promoting the Vault server bootstrap toward **auto-unseal** if reboots become painful.
- The Cloudflare token (Task 4, used from the Mac by Terraform) is the obvious 3rd migration if Vault's use expands off-cluster (Task-4 endgame: "token comes from Vault instead of `export`").
