# Task 7: Datadog (SaaS Observability) — Agent on omni-cluster, compared to self-hosted Prometheus/Grafana

> **Goal:** Learn Datadog as the production observability tool by contrast with the self-hosted Prometheus/Grafana stack from Task 5 — SaaS **push-model** agent vs self-hosted **pull-model** scraping. Deploy the Datadog Agent onto `omni-cluster` via the Datadog Operator (GitOps, mirroring the Task-5 KEDA pattern), get telemetry flowing to the Datadog SaaS, and settle the KEDA `external.metrics.k8s.io` collision by design.
>
> **Status:** ✅ **Complete** (node-agent on Talos deferred — optional, see Part 6). Datadog Operator installed via Argo (`Synced/Healthy`); `DatadogAgent` CR git-managed; **Cluster Agent forwarding to the SaaS** (cluster-level metrics live in `app.datadoghq.com`). KEDA collision avoided by design and **proven** (KEDA still owns the APIService; Datadog's provider confirmed disabled). Branch `task07-datadog` → PR #5 squash-merged to `main`. **The node Agent DaemonSet is cleanly disabled** (`override.nodeAgent.disabled: true`) pending a documented Talos-vs-Datadog host-assumption fix — not blocking; the task goal is fully met by the Cluster Agent. Resume steps below.

---

## Environment Facts

| Thing | Value |
|---|---|
| Cluster | `omni-cluster` (Omni-managed), k8s 1.36.2 / Talos 1.13.6 |
| kubeconfig context | `omni-cluster` |
| Repo / branch | `example-user/homelab-k8s`, branch `task07-datadog` |
| Datadog site | **US1** → `site: datadoghq.com` (no subdomain — US1 quirk; US5/EU carry a prefix) |
| Datadog account URL | `app.datadoghq.com` |
| API key | 32 hex chars, stored as Secret `datadog-secret` (key `api-key`) in ns `datadog` — **never committed** |
| App key | **not needed** (only external-metrics requires one; we left that off) |
| Namespace | `datadog` — labeled `pod-security.kubernetes.io/enforce=privileged` (earned from a real denial) |
| Datadog Operator | Helm chart via Argo Application, ns `datadog` |
| DatadogAgent CR | `clusters/omni-cluster/apps/datadog/datadog-agent.yaml` (git-managed) |
| Argo Application | `clusters/omni-cluster/argocd/apps/datadog-operator.yaml` |
| Agent image | `registry.datadoghq.com/agent:7.80.2` |
| Cluster Agent | `v7.80.2`, `1/1 Running`, **forwarding to SaaS** ✅ |
| Node Agent DaemonSet | ⚠️ DISABLED (`override.nodeAgent.disabled: true`) — Talos host-mount deferred, see Part 6 |
| External metrics owner | `keda/keda-operator-metrics-apiserver` (UNCHANGED — Datadog did not clobber it) ✅ |

**Network personality note:** friendly to the locked-down VLAN in the end, but this is the first task where the *cluster itself* had to reach a **SaaS** intake. Datadog Agent → SaaS is all **outbound TCP/443** (`api.datadoghq.com`, `agent.datadoghq.com`), which the VLAN allows; DNS resolves via OpenDNS. No outbound UDP / non-443 dependency. Proven with a `netshoot` pod before installing anything (see Part 2).

---

## Core concepts (settled up front, before touching anything)

### Push vs pull — the COLLECTION axis (a different axis than Task 6's DEPLOY axis)

The trick to keeping the two straight: ask **"who initiates the connection?"** in each case.

- **Task 6 (deploy axis):** pull = Argo, inside the cluster, reaches out to git and pulls desired state. Push would be CI reaching *into* the cluster with `kubectl apply`.
- **Task 7 (collection axis):** pull = Prometheus [workload], inside the cluster, reaches out to each pod's `/metrics` and scrapes it. Push = the Datadog Agent [workload, DaemonSet] collects locally, then reaches *out* to Datadog's SaaS and ships the data.

Same word, opposite direction of the useful arrow — name the axis every time.

**What changes when you flip to push:**

- **Network path.** Prometheus keeps all metric traffic inside the VLAN (scraper → pod, east-west). Datadog opens an *outbound* connection across the VLAN boundary to the SaaS — which is why the locked-down network is back in play this task.
- **Failure modes (cuts both ways):**
  - *Pull's superpower:* absence is a signal. If Prometheus can't scrape a target, it records `up == 0` — you *know* it's down, from one central place. Cost: pull needs reachability to every target (painful across firewalls/NAT).
  - *Push's superpower:* targets only need *outbound* reachability — an agent behind three firewalls just needs a route out on 443, no inbound holes. That's why SaaS observability scales across messy networks. Cost: absence is *ambiguous* — silence could mean the node died, the network to Datadog broke, or the agent crashed. (Heartbeats/agent-status metadata soften this, not eliminate it.)

Not "push good, pull bad" — pull gives crisp local truth, push gives reach + a managed backend you don't operate.

### Agent vs Cluster Agent — why there are two

- **Datadog Agent** [workload, DaemonSet] — one pod per node (same shape as node-exporter). Collects that node's host/container metrics, logs, and (if enabled) APM traces, and pushes them out. A tenant doing a job on each node.
- **Datadog Cluster Agent** [controller] — a small Deployment (1, HA-2) between the node Agents and the kube API server. The coordinator.

Why the second tier?
1. **API-server load.** Without it, all 6 node Agents independently hammer the kube API for cluster-level data. The Cluster Agent queries once and fans answers out. One reader instead of N.
2. **Cluster-level features live here** — including the external-metrics provider that would collide with KEDA. The collision is *entirely* a Cluster Agent concern; node Agents have nothing to do with it.

Workload-vs-controller test: node Agent = doing a job (collect + ship); Cluster Agent = watching + coordinating = controller.

### What Datadog replaces vs complements (evenhanded)

Datadog *can* fully replace Prometheus/Grafana (collection + storage + dashboards + alerting in one SaaS). It also *complements*:
- Its Agent has an **OpenMetrics/Prometheus check** — it can scrape the same `/metrics` endpoints Prometheus does (so the Task-6 demo-app `/metrics` is already a valid Datadog target), and it can consume Prometheus `remote_write`.
- Common "run both" split: Prometheus for cheap, high-cardinality, short-retention in-cluster metrics + fast local alerting; Datadog for cross-system correlation, APM/tracing, logs, long retention, one pane across many clusters.

Deciding factor is usually **cost, not capability** — Datadog bills per host + per ingested custom metric / log volume / span. That's why teams keep Prometheus underneath for the bulk and send only what's worth the money. A production environment runs both — this hybrid.

### The KEDA collision (the task's central risk)

`external.metrics.k8s.io` is an **APIService** — Kubernetes allows exactly **one** owner per group/version cluster-wide. KEDA's metrics-apiserver already claimed it in Task 5, and demo-app's ScaledObject depends on it. Datadog's Cluster Agent external-metrics feature would register the *same* APIService → collision.

**Decision: leave Datadog external-metrics OFF** (`features.externalMetricsServer.enabled: false` — also the chart default). If Datadog-driven autoscaling is ever wanted, use KEDA's **Datadog scaler** (KEDA queries the Datadog API directly and keeps owning the slot — no contention). Prometheus itself never collides (it registers no APIService — pull vs push).

---

## PART 0 (preliminary) — restore the CD baseline (dead Argo PAT)

On starting, `kubectl get application -n argocd` showed `demo-app  Unknown  Healthy` while `keda  Synced`. **`Unknown` ≠ `OutOfSync`** (Task-6 lesson): `OutOfSync` = compared and differ; `Unknown` = *couldn't compare at all* (git fetch/auth failed).

**Diagnosis (repo-server log named it):**
`failed to list refs: authentication required: Invalid username or token. Password authentication is not supported for Git operations.`
The `GenerateManifest` call returned in ~278ms → Argo *reached* GitHub over 443 and GitHub *rejected the token* (not the Task-6 SSH/22 black-hole, which shows `context deadline exceeded` with no response). **The fine-grained PAT had expired** (fixed expiry; demo-app hadn't synced since Task 6).

**Fix (pure out-of-band credential rotation — nothing to commit):**
```bash
# mint a fresh fine-grained PAT: owner example-user, only homelab-k8s, Contents: Read-only
read -rs "GH_PAT?new PAT: "; echo
echo "starts: ${GH_PAT:0:11}   length: ${#GH_PAT}"   # expect github_pat_ / ~93 chars

kubectl -n argocd delete secret homelab-k8s-repo
cat <<EOF | kubectl apply -f -
apiVersion: v1
kind: Secret
metadata:
  name: homelab-k8s-repo
  namespace: argocd
  labels: { argocd.argoproj.io/secret-type: repository }
stringData:
  type: git
  url: https://github.com/example-user/homelab-k8s.git
  username: example-user
  password: ${GH_PAT}
EOF
unset GH_PAT

kubectl -n argocd annotate application demo-app argocd.argoproj.io/refresh=hard --overwrite
kubectl -n argocd get application demo-app -w   # -> Synced / Healthy
```
**Lesson banked:** a git-managed cluster reading a private repo has a credential that silently dies on a timer. This is exactly what Vault (Task 8) kills — short-lived auto-issued creds instead of hand-minted PATs. This dead PAT is a live advertisement for Task 8.

---

## PART 1 — Datadog account, site, API key (the SaaS half — the one billing-sensitive step)

- Sign up at `datadoghq.com` → Free Trial (14 days, full-feature). Trial does **not** require a card to start; if a flow demands one, that's not the normal trial path.
- **Note the site.** Browser URL after signup = your site. `app.datadoghq.com` → **US1** → config value `site: datadoghq.com` (no subdomain). US5/EU carry a prefix. If `site` is wrong, agents authenticate fine but ship to the wrong intake and data silently never appears.
- Mint an **API key** (Organization Settings → API Keys). This is the write-intake credential. **No App key needed** (App key is only for external-metrics, which we skip).

Gate before storing (Task-6 reflex):
```bash
read -rs "DD_API_KEY?Datadog API key: "; echo
echo "starts: ${DD_API_KEY:0:6}   length: ${#DD_API_KEY}"   # expect 32 hex chars
```
A Datadog API key is **32 hex chars**. If length ≠ 32 you grabbed the wrong thing (App keys are longer, adjacent in the UI).

---

## PART 2 — prove the outbound 443 path BEFORE installing anything

Same discipline that diagnosed the demo-app PAT: verify the network so a later failure can't hide behind "config or network?" Run from *inside the cluster* (the agent's real path), not the Mac (off-VLAN, proves nothing).

```bash
kubectl -n default run nettest --rm -it --restart=Never --image=nicolaka/netshoot -- \
  sh -c '
    echo "== DNS =="; nslookup app.datadoghq.com | tail -4
    echo "== api 443 ==";      nc -zv -w5 api.datadoghq.com 443
    echo "== agent intake =="; nc -zv -w5 agent.datadoghq.com 443
  '
```
**Result:** both `443 succeeded`; DNS resolved (`orange.intake.datadoghq.com` — US1 intake CNAME target; name resolving confirms OpenDNS returns public records). Agent push path is open.

Notes:
- The `Warning: would violate PodSecurity "restricted:latest"` on the `kubectl run` pod is expected (`default` ns enforces `restricted` for `kubectl run`) — but it's a `Warning:`, not a hard `FailedCreate`, so the pod still ran. `warn`-level = noise; `enforce`-level = real block (Ceph/Task-5 distinction).
- DNS came back IPv6 (`2600:1f18:…`), `nc` connected IPv4 (`3.233.158.x`) — dual-stack, harmless.

---

## PART 3 — API-key Secret, out-of-band

```bash
kubectl create namespace datadog

kubectl -n datadog create secret generic datadog-secret \
  --from-literal api-key="$DD_API_KEY"

kubectl -n datadog get secret datadog-secret \
  -o jsonpath='{.data.api-key}' | base64 -d | wc -c   # expect 32

unset DD_API_KEY
```
- Secret name `datadog-secret` / key `api-key` are the names the `DatadogAgent` CR references (`credentials.apiSecret.secretName` / `keyName`) — name to the convention now so the CR reads clean.
- `base64 -d | wc -c` → `32` is the "gate the stored value, don't trust it landed right" habit (the Task-6 poisoned-`read` scar).

**PSA label — decided wait-and-see, not reflex.** We deliberately did NOT pre-label the namespace `privileged`, to learn whether the Operator's pod spec was sufficient. It was not — see Part 5.

---

## PART 4 — Datadog Operator via an Argo Application (GitOps, mirrors KEDA)

`clusters/omni-cluster/argocd/apps/datadog-operator.yaml`:
```yaml
apiVersion: argoproj.io/v1alpha1
kind: Application
metadata:
  name: datadog-operator
  namespace: argocd
spec:
  project: default
  source:
    repoURL: https://helm.datadoghq.com        # PUBLIC helm repo (443, no cred — NOT the private repo)
    chart: datadog-operator
    targetRevision: 2.9.0                        # PIN — confirm latest first
    helm:
      values: |
        replicaCount: 1
  destination:
    server: https://kubernetes.default.svc
    namespace: datadog
  syncPolicy:
    automated: { prune: true, selfHeal: true }
    syncOptions:
      - CreateNamespace=true
      - ServerSideApply=true                     # pre-empt the 256KB CRD annotation wall (Task-6 scar)
```
Two load-bearing lines:
- **`ServerSideApply=true`** — the Task-6 KEDA-CRD fix applied *before* it bites. The Operator ships a large `DatadogAgent` CRD; client-side apply would overflow the 262144-byte annotation ceiling. SSA tracks ownership server-side (no annotation, no cap). Confirmed working — 8 CRDs registered, no `Too long` error.
- **`repoURL: https://helm.datadoghq.com`** — public chart over 443, like KEDA's `kedacore.github.io`. Does NOT touch the private-repo PAT. (The `DatadogAgent` CR in Part 5 comes from the private repo and DOES ride the PAT — keep the two straight.)

Confirm the version, apply (first Application of a pattern is bootstrapped by hand), verify:
```bash
helm repo add datadog https://helm.datadoghq.com 2>/dev/null; helm repo update datadog
helm search repo datadog/datadog-operator --versions | head   # adjust targetRevision to current

kubectl apply -f clusters/omni-cluster/argocd/apps/datadog-operator.yaml
kubectl -n argocd get application datadog-operator -w          # -> Synced / Healthy
kubectl -n datadog get pods                                    # datadog-operator-... Running
kubectl get crd | grep datadoghq.com                           # datadogagents.datadoghq.com now exists
```
The CRD registering = the Operator did its job (new noun, same as Rook giving you `CephCluster`). The Operator is a **controller with no CR to act on yet** — idle, watching. Part 5's CR wakes it.

```bash
git add clusters/omni-cluster/argocd/apps/datadog-operator.yaml
git commit -m "task 8: install Datadog Operator via Argo Application (SSA pre-empts large-CRD annotation wall)"
git push -u origin task07-datadog
```

---

## PART 5 — the DatadogAgent CR (collection comes alive) + the PSA denial

`clusters/omni-cluster/apps/datadog/datadog-agent.yaml` (final working-toward version — see Part 6 for the node-agent overrides that are still being tuned):
```yaml
apiVersion: datadoghq.com/v2alpha1
kind: DatadogAgent
metadata:
  name: datadog
  namespace: datadog
spec:
  global:
    site: datadoghq.com                    # US1 — matches app.datadoghq.com
    credentials:
      apiSecret:
        secretName: datadog-secret         # out-of-band Secret from Part 3
        keyName: api-key
  features:
    logCollection:
      enabled: true
      containerCollectAll: false           # SCOPED — not every pod's logs (billing dimension)
    apm:
      enabled: false                       # off — traces are fastest billable volume
    externalMetricsServer:
      enabled: false                       # DELIBERATE — KEDA owns external.metrics.k8s.io
    liveProcessCollection:
      enabled: false
```
Every non-default line is a decision:
- `site: datadoghq.com` — US1 no-subdomain. Wrong = agents auth but ship to the void.
- `credentials.apiSecret` — points at the Secret by *name*, not value (git carries the reference; cluster holds the contents).
- `logCollection.containerCollectAll: false` — billing guardrail; `true` slurps every pod cluster-wide.
- `apm.enabled: false` — the other fast-billing dimension.
- `externalMetricsServer.enabled: false` — the line that keeps Datadog's Cluster Agent from fighting KEDA for the single APIService slot.

The Operator stands up **both tiers** from this one CR — node Agent DaemonSet + Cluster Agent Deployment. You do NOT configure the Cluster Agent separately.

**The PSA denial (wait-and-see paid off):** node agent pods were `Forbidden` at admission — hard `enforce`-level block citing `hostPath` / `baseline` (the DaemonSet mounts `/var/log/pods` etc.). This is the earned finding: the node Agent genuinely needs elevation, unlike the `nonroot` demo-app. Fix:
```bash
kubectl label namespace datadog pod-security.kubernetes.io/enforce=privileged --overwrite
```
Read the *actual* denial, not `warn`-level noise. If pods still don't schedule after labeling: `kubectl -n datadog rollout restart daemonset datadog-agent` (Task-5 "label sometimes doesn't take first try").

**IaC-honesty note:** the `kubectl label` is an imperative out-of-band act the repo doesn't reproduce (same category as the Task-5 first-Application bootstrap and Task-6 server-side CRD bootstrap). Option to promote it into git later: commit the namespace as a manifest with the label baked in so Argo owns it (deferred).

---

## PART 6 — Node Agent on Talos: the host-assumption saga (⚠️ DEFERRED — cleanly disabled, optional)

**Current state:** the node Agent DaemonSet is disabled in the CR (`override.nodeAgent.disabled: true`) — no crashloop, Cluster Agent unaffected, task goal met without it. The saga below is the diagnosis if/when you want to finish it.

**The pattern:** the Datadog node Agent assumes a conventional Linux host. Talos is deliberately not one (minimal, immutable, read-only host fs, no shell). So each fix cleared one host-assumption and revealed the next. **These are not bugs — they're an architectural impedance mismatch**, documented upstream (DataDog/helm-charts issue #273: "attempted to mount /etc/passwd from the host, which does not exist on Talos").

**Method that worked throughout:** read the container STATE, not the symptom string. The status string (`CreateContainerError` → `RunContainerError` → `CrashLoopBackOff`) stayed similar while the *cause* changed under it — same trap as the Task-6 KEDA CRD saga. The single load-bearing command:
```bash
P=$(kubectl -n datadog get pod -l app.kubernetes.io/component=agent -o name | head -1)
kubectl -n datadog get "$P" -o jsonpath='{range .status.containerStatuses[?(@.name=="agent")]}{.lastState.terminated.reason}{" exit="}{.lastState.terminated.exitCode}{" msg="}{.lastState.terminated.message}{"\n"}{end}'
```

**Layers peeled (each with its real message):**

1. **AppArmor red herring.** First `describe` surfaced `forbidden AppArmor profile ... system-probe unconfined`. Looked like the cause; wasn't — it was a `warn`-level artifact. Talos ships no AppArmor, but that wasn't the blocker. *Lesson: don't act on the admission warning; pull the container message.*
2. **Host `/etc/passwd` mount (the real first blocker).** `failed to apply OCI options: failed to mkdir "/etc/passwd": read-only file system`. The Agent bind-mounts the host's `/etc/passwd`; Talos has no such file, so the hostPath source is created as an empty *dir*, and the read-only host fs can't build the mount target. **`DD_KUBERNETES_USE_HOST_ETC=false` did NOT remove the volume on this Operator version**, and `readOnlyRootFilesystem: false` was irrelevant (the read-only fs is the *host*, not the container). Both confirmed ineffective by reading the live pod (`ro=false useHostEtc=false` present, still failing).
3. **emptyDir shape mismatch.** Overrode the `passwd` volume to `emptyDir: {}`. The volume schema is a **list-map keyed on `name`** (`x-kubernetes-list-map-keys: [name]`), so redefining `passwd` *replaces* the hostPath by name (confirmed via the live CRD schema — no duplicate). But new error: `error mounting ".../empty-dir/passwd" to rootfs at "/etc/passwd": not a directory` — an emptyDir is a **directory**, the target `/etc/passwd` is a **file**. Can't mount a dir onto a file.
4. **ConfigMap still projects a directory.** Swapped to a `configMap` volume with `items` (a `datadog-passwd` ConfigMap holding a minimal `passwd`). Still `not a directory` — a ConfigMap-with-`items` mounts as a *directory containing* the files (`/etc/passwd/passwd`), not a bare file.

**The unfinished fix (resume here):** to project a single **file** onto `/etc/passwd`, the container's *volumeMount* needs a **`subPath`** (`subPath: passwd`), not just `items` on the volume. That is a container-level `volumeMounts` override, to be set against the live CRD schema. Also `system-probe` (the NPM/CWS/kernel container) is independently crashlooping and is **not needed** for log-only collection — disable it explicitly. Likely order:
   1. Add `override.nodeAgent.containers.agent.volumeMounts` entry for `passwd` with `subPath: passwd` (file-shape the ConfigMap mount).
   2. Disable `system-probe` (e.g. keep NPM/CWS/`liveProcessCollection` off; if the container still appears, override its command/disable via the relevant feature flag).
   3. Re-roll, read `lastState` again; expect either green or the *next* discrete host-assumption.

**Scope decision (deliberate, SRE-correct):** stopped grinding the DaemonSet here. Rationale — the core objective (Datadog live beside Prometheus, push-vs-pull demonstrable, KEDA collision avoided) is met by the **Cluster Agent**, which forwards cleanly. The node agent is an open-ended Talos-tuning exercise with diminishing returns. "Know when to stop tuning and ship the working scope" is the skill. Node-agent config is committed (real progress) and marked in-progress.

---

## VERIFICATION (what actually works — closing evidence)

```bash
# 1. KEDA still owns the slot — Datadog did NOT clobber it (the task's central risk, handled)
kubectl get apiservices | grep -i external.metrics
#  -> v1beta1.external.metrics.k8s.io   keda/keda-operator-metrics-apiserver   True

# 2. Cluster Agent confirms it stood down from external-metrics (two views agreeing)
kubectl -n datadog exec deploy/datadog-cluster-agent -- agent status | grep -iA2 'external metrics'
#  -> "Disabled: The external metrics provider is not enabled on the Cluster Agent"

# 3. Cluster Agent healthy + forwarding
kubectl -n datadog exec deploy/datadog-cluster-agent -- agent status | grep -A4 -iE 'cluster agent|forwarder'
#  -> Cluster Agent (v7.80.2), Forwarder active

# 4. In the SaaS: app.datadoghq.com -> Infrastructure -> omni-cluster visible (cluster-level metrics)
```

---

## KEY LESSONS / GOTCHAS (do these right next time)

1. **Two push/pull axes — name which one.** Deploy (Task 6): who applies to the cluster. Collection (Task 7): who initiates the metric connection. Prometheus pulls (in-cluster, absence = signal); Datadog Agent pushes (outbound 443, absence = ambiguous).
2. **Datadog = two tiers.** Agent [workload, DaemonSet, per-node collect+push] + Cluster Agent [controller, coordinator + cluster-level features]. The KEDA collision lives ONLY in the Cluster Agent.
3. **KEDA collision is avoided by DESIGN, not luck.** `externalMetricsServer.enabled: false`. Single-owner APIService `external.metrics.k8s.io`. Prometheus never collides (pull, no APIService). For DD-driven autoscaling, use KEDA's Datadog *scaler* (queries DD API directly, keeps the slot).
4. **Datadog site must match the account.** US1 = `datadoghq.com` (no subdomain). Wrong site = silent no-data (auth succeeds, ships nowhere).
5. **Verify the outbound path first (netshoot + `nc -zv 443`)** so a later failure isn't ambiguous. Everything Datadog needs is 443 → VLAN-OK. Different from Task-6 SSH/22.
6. **API key = 32 hex; no App key needed** (App key only for external-metrics). Gate every secret before storing (`echo starts/length`; `base64 -d | wc -c`).
7. **`ServerSideApply=true` on the DD Operator Application** — pre-empt the 256KB CRD annotation wall (Task-6 KEDA scar, applied proactively).
8. **Operator repo = public helm (443, no cred); the CR = your private repo (rides the PAT).** Keep the two credential paths straight.
9. **Node Agent on Talos is known-hard.** Host-assumption mismatches, one at a time: host `/etc/passwd` mount (Talos has none), single-file mount needs `subPath` (not emptyDir, not bare configMap+items), `system-probe` wants kernel access Talos restricts (disable if log-only). READ `lastState.terminated.message`, not the status string — same cause-changes-under-same-symptom trap as the Task-6 CRD saga.
10. **Volume override schema is a list-map keyed on `name`** (`x-kubernetes-list-map-keys`) → redefining a volume by its exact name REPLACES it (no duplicate). Confirm the live name first (`get pod -o jsonpath` on `.spec.volumes`).
11. **`Unknown` ≠ `OutOfSync` (Argo).** `Unknown` = couldn't fetch/compare. A rejected token returns fast (`auth required`), vs SSH/22 black-hole (`context deadline exceeded`, no response).
12. **Fine-grained PATs expire on a timer** → a private-repo GitOps cred silently dies. The advertisement for Vault (Task 8).
13. **Know when to stop tuning.** Shipping the working scope (Cluster Agent) + an honest in-progress finding beats brute-forcing a DaemonSet. Scope is an SRE decision.

---

## Conceptual Q&A

**Q: Push vs pull — how is this different from Task 6?**
Different axis. Task 6 = *deploy* (Argo pulls git into the cluster vs CI pushing `kubectl apply`). Task 7 = *collection* (Prometheus pulls `/metrics` inside the cluster vs the Datadog Agent pushing out to SaaS). Ask "who initiates the connection." Pull = absence is a signal but needs reachability to targets; push = only needs outbound 443 but absence is ambiguous.

**Q: Does Datadog replace Prometheus/Grafana?**
It can (one SaaS for collection/storage/dashboards/alerting), but many shops run both: Prometheus for cheap in-cluster bulk metrics + local alerting, Datadog for cross-system correlation, APM, logs, long retention. The deciding factor is cost (Datadog bills per host + ingest volume), not capability. Datadog can even ingest *from* Prometheus (OpenMetrics check, remote_write).

**Q: Why two agents?**
Node Agent (DaemonSet) collects per-node and pushes. Cluster Agent (controller) sits between the node agents and the kube API — one reader of cluster-level data instead of N, plus it hosts cluster-level features (including the external-metrics provider). Splitting them cuts API-server load and isolates cluster concerns.

**Q: Why not enable Datadog external-metrics?**
`external.metrics.k8s.io` is a single-owner APIService and KEDA already owns it (demo-app's ScaledObject depends on it). Enabling Datadog's provider would fight KEDA for the slot. We leave it off; for DD-driven autoscaling later, KEDA's Datadog scaler queries the DD API directly without contending for the APIService.

**Q: Why did the node agents fail when the cluster agent worked fine?**
The Cluster Agent is a normal Deployment with no host mounts. The node Agent DaemonSet makes host-shaped assumptions (mount host `/etc/passwd`, kernel access for `system-probe`, host log paths) that Talos — minimal/immutable/read-only host — refuses. Each is a discrete override; it's a documented Talos integration friction, not a config error.

**Q: Why stop before the node agents were green?**
The task's goal — stand Datadog beside Prometheus, compare push vs pull, avoid the KEDA collision — is met by the working Cluster Agent. The node agent adds per-node host metrics/logs but is an open-ended Talos-tuning loop. Scoping it as a diagnosed in-progress finding is the honest, higher-value outcome than brute force.

---

## Git / GitHub workflow used (prod-style)

```
branch task07-datadog
  -> Part 0: rotate Argo PAT (out-of-band, nothing committed)
  -> Part 4: commit datadog-operator Application  -> push -u
  -> Part 5: commit datadog-agent CR + passwd-configmap (node-agent config, in-progress)
  -> [pending] finish node-agent subPath fix -> commit -> PR -> squash-merge -> delete branch
```
Committed: `argocd/apps/datadog-operator.yaml`, `apps/datadog/datadog-agent.yaml`, `apps/datadog/passwd-configmap.yaml`.
NOT committed: the API key (Secret contents), the `kubectl label` (out-of-band, note it).
**PR/merge deferred** until the node-agent scope is either finished or explicitly closed as in-progress in the merge commit message.
