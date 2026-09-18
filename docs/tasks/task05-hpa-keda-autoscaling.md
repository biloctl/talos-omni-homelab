# Task 5: Workload Autoscaling — Native HPA → KEDA (on Prometheus metrics, via Argo GitOps)

> **Goal:** Learn workload autoscaling on the real stack. Start with native Kubernetes HPA on CPU to understand the baseline mechanism, then extend to KEDA (confirmed production stack) scaling on a live Prometheus metric. Stand up the missing metrics substrate (metrics-server + Prometheus/Grafana) on `omni-cluster`, and use the task to learn BOTH Helm (imperative) and Argo CD (declarative GitOps) by doing one install each way.
>
> **Status:** ✅ Complete. metrics-server + native HPA CPU demo proven; scoped kube-prometheus-stack (Grafana on, Alertmanager off) on Ceph storage; Argo CD bootstrapped; KEDA installed via Argo Application (declarative, self-heal verified); ScaledObject with a Prometheus request-rate trigger created and confirmed to generate a managed HPA (`keda-hpa-web`). Live scale-up under load not captured (demo-app friction — see gotchas); pipeline verified via the KEDA-generated HPA. All git-managed on branch `task05-hpa-keda`.

---

## Environment Facts

| Thing | Value |
|---|---|
| Cluster | `omni-cluster` (Omni-managed), k8s 1.36.2 / Talos 1.13.6 |
| kubeconfig context | `omni-cluster` |
| Nodes | masters `master01/02/03` (2 vCPU / ~3.75Gi), workers `worker01/02/03` (4 vCPU / ~7.7Gi) |
| Repo / branch | `example-user/homelab-k8s`, branch `task05-hpa-keda` |
| metrics-server | Helm chart `3.13.0` (app v0.8.0), ns `kube-system` |
| kube-prometheus-stack | Helm chart `87.17.0`, ns `monitoring`, Grafana ON, Alertmanager OFF |
| Prometheus TSDB | PVC on `rook-ceph-block` (10Gi, 7d retention) — Task 2 storage |
| In-cluster Prometheus URL | `http://kube-prometheus-stack-prometheus.monitoring.svc:9090` |
| Argo CD | Helm chart `10.2.0` (app v3.4.x), ns `argocd`, non-HA, `server.insecure=true` |
| KEDA | Helm chart `2.20.1` via Argo Application, ns `keda` |
| Repo cred | read-only single-repo SSH **deploy key** (`~/.ssh/argocd_deploy`), stored as labeled Secret in `argocd` ns |


**Network personality note:** friendly to the locked-down VLAN. All image pulls + chart fetches are TCP/443 (`ghcr.io`, `quay.io`, `registry.k8s.io`, helm repos). No outbound UDP / external DNS / NTP dependency. Grafana/Argo/Prometheus UIs reached via `kubectl port-forward` (no ingress/MetalLB on omni-cluster at this point — both arrive in Task 10 with the Istio ingress gateway).

---

## Core concept: HPA vs KEDA — KEDA DRIVES HPA, it does NOT replace it

The `HorizontalPodAutoscaler` **[controller]** (built into `kube-controller-manager`) is the ONLY thing that changes replica counts. Each ~15s it reads a metric, compares to a target, computes `desiredReplicas`, patches the Deployment's scale. But HPA can only read from three k8s metrics APIs — it can't talk to Prometheus/Kafka/etc. directly. So the question is always "who fills those APIs?"

- `metrics.k8s.io` (resource metrics) ← **metrics-server** [workload]. CPU/memory only. The native-HPA baseline.
- `custom.metrics.k8s.io` / `external.metrics.k8s.io` ← an **adapter** you install.

**KEDA** is two things: a `keda-operator` **[controller]** + a `keda-operator-metrics-apiserver` **[metrics adapter]**. When you write a `ScaledObject` **[CRD]**, the operator *reads it and creates a plain HPA on your behalf*, pointed at KEDA's external-metrics endpoint. So:

```
you author ScaledObject → KEDA operator generates + owns an HPA
                        → KEDA adapter answers that HPA's metric queries (by querying Prometheus)
                        → HPA does its normal scaling arithmetic → scales the Deployment
```

KEDA adds two things raw HPA can't do: **event-source scalers** (60+: Prometheus, Kafka, cron…) and **scale-to-zero** (raw HPA floor is 1; KEDA's operator handles 0↔1 "activation" itself).

**The full pipeline (what Task 5 lit up):**
```
traffic → app (/metrics) → Prometheus scrapes → KEDA adapter queries Prometheus
        → KEDA operator creates+feeds an HPA → HPA scales the Deployment
```

**Key API fact:** KEDA registers `v1beta1.external.metrics.k8s.io`, and k8s allows only ONE owner of that APIService cluster-wide. So with KEDA you do NOT also run prometheus-adapter for external metrics (they'd collide). This is the same collision Datadog's Cluster Agent external-metrics feature would cause → Task 7 watch-point.

---

## Two decisions settled up front

**1. Metrics source = scoped kube-prometheus-stack (Grafana ON, Alertmanager OFF).**
- Native HPA baseline needs ONLY metrics-server (no Prometheus). Prometheus is the KEDA half.
- Decision was deferred one step ON PURPOSE: `kubectl top` doesn't work until metrics-server is in, so we installed metrics-server first, THEN read real usage to size the Prometheus scope. Result: workers at 3–8% CPU / 21–38% mem = plenty of headroom → scoped full stack with Grafana (visual payoff) was safe. Alertmanager dropped (dead weight for autoscaling).
- Lesson: Ceph is cheap at rest (`HEALTH_OK`); its CPU cost is during recovery/rebalance, not idle.

**2. Delivery = BOTH Helm and Argo (learn both; it's the industry-standard pattern).**
- "Helm vs Argo" is a FALSE choice — Argo *uses* Helm internally to render charts. Helm = packager/templater (one-shot, like `terraform apply`). Argo CD = GitOps **[controller]** that continuously reconciles git→cluster.
- `helm install` : Argo CD ≈ `terraform apply` : the Omni server (one-shot vs continuous reconcile loop).
- So: metrics-server + Prometheus via plain `helm install` (imperative), then KEDA via an Argo Application (declarative). Mixed estate is realistic; adopting Prometheus into Argo later is a brownfield lesson (mirrors Task 4 `import`).

---

## PART 1 — metrics-server via Helm (imperative) — the native-HPA prerequisite

**Why first:** HPA is blind without the resource metrics API. Omni's bootstrap manifests ship CoreDNS/kube-proxy/flannel but NOT metrics-server. Verify proved it absent.

**Talos gotcha (the WHY):** metrics-server scrapes each kubelet over HTTPS :10250. Talos kubelets present a **self-signed** serving cert (no kubelet-serving-cert rotation + CSR approval by default) → metrics-server fails TLS verify with `x509: certificate signed by unknown authority` → `kubectl top` stays broken though the pod looks healthy. Fix = `--kubelet-insecure-tls` (lab-acceptable; docs call it "testing only"). Proper prod path = enable kubelet cert rotation + a CSR approver. Also set `--kubelet-preferred-address-types=InternalIP,...` so it connects by IP (Talos hostnames don't always resolve on dnsmasq).

```bash
kubectl config current-context      
helm repo add metrics-server https://kubernetes-sigs.github.io/metrics-server/
helm repo update metrics-server
helm search repo metrics-server/metrics-server --versions | head   # pin latest
```

`clusters/omni-cluster/metrics-server/values.yaml`:
```yaml
args:
  - --kubelet-insecure-tls
  - --kubelet-preferred-address-types=InternalIP,ExternalIP,Hostname
  - --metric-resolution=15s
resources:
  requests: { cpu: 100m, memory: 200Mi }
  limits:   { memory: 300Mi }
```

```bash
helm install metrics-server metrics-server/metrics-server \
  --namespace kube-system --version 3.13.0 \
  -f clusters/omni-cluster/metrics-server/values.yaml \
  --kube-context omni-cluster

# verify (wait ~20-30s for first scrape to populate)
kubectl -n kube-system rollout status deploy/metrics-server
kubectl get apiservice v1beta1.metrics.k8s.io      # AVAILABLE True
kubectl top nodes                                  # now returns real numbers
```

---

## PART 2 — Native HPA baseline (CPU) — throwaway demo

Exercises the LEFT lane: metrics-server → resource metrics API → HPA → Deployment. No Prometheus/KEDA. **Throwaway (not committed)** — proves the mechanism.

**THE concept to internalize:** HPA measures CPU as a **% of the pod's `requests`**, NOT limits, NOT node capacity. Pod requests `200m`, target 50% → HPA aims to hold each pod near `100m` actual.
`desiredReplicas = ceil( currentReplicas × (currentUtilization / targetUtilization) )`, where `currentUtilization = actual ÷ requests`.
→ **`requests` is the denominator of the whole autoscaling decision.** Same real load looks like "on fire" (low requests) or "asleep" (high requests). Right-sizing requests = calibrating the autoscaler. (This limitation is *why* KEDA exists — event metrics use an ABSOLUTE target, no requests denominator.)

```bash
# demo app (php-apache), Service, and an autoscaling/v2 HPA (hand-written on purpose —
# it's the exact object KEDA generates for you later)
kubectl apply -f - <<'EOF'
apiVersion: apps/v1
kind: Deployment
metadata: { name: php-apache }
spec:
  selector: { matchLabels: { run: php-apache } }
  template:
    metadata: { labels: { run: php-apache } }
    spec:
      containers:
      - name: php-apache
        image: registry.k8s.io/hpa-example
        ports: [{ containerPort: 80 }]
        resources: { requests: { cpu: 200m }, limits: { cpu: 500m } }  # HPA % is vs THIS
---
apiVersion: v1
kind: Service
metadata: { name: php-apache, labels: { run: php-apache } }
spec: { ports: [{ port: 80 }], selector: { run: php-apache } }
---
apiVersion: autoscaling/v2
kind: HorizontalPodAutoscaler
metadata: { name: php-apache }
spec:
  scaleTargetRef: { apiVersion: apps/v1, kind: Deployment, name: php-apache }
  minReplicas: 1
  maxReplicas: 10
  metrics:
  - type: Resource
    resource: { name: cpu, target: { type: Utilization, averageUtilization: 50 } }
EOF

kubectl get hpa php-apache --watch     # TARGETS <unknown>/50% then ~1%/50%
# load (separate terminal):
kubectl run -it --rm load-generator --image=busybox:1.36 --restart=Never -- \
  /bin/sh -c "while sleep 0.01; do wget -q -O- http://php-apache; done"
# TARGETS crosses 50%, REPLICAS climbs toward 10.
```
**Observed asymmetry:** scale-UP is fast; scale-DOWN waits a **5-minute stabilization window** (anti-flap). Ctrl-C load → ~5 min later returns to 1.
Cleanup: `kubectl delete hpa/deploy/svc php-apache`.

---

## PART 3 — kube-prometheus-stack via Helm (imperative) — KEDA's metric source

**Two Talos gotchas handled BEFORE install:**
1. **PodSecurity (Task 2 callback):** node-exporter [workload, DaemonSet] needs `hostPath`/`hostNetwork`/`hostPID`, which Talos's cluster-wide `baseline` PSA forbids → pods rejected at **admission**. Fix = label the ns `privileged` (same as `rook-ceph`). The DaemonSet object is accepted; its *pods* get bounced — read the `FailedCreate` event, not logs.
2. **Talos control-plane targets show "down":** chart ships ServiceMonitors for kube-controller-manager/scheduler/etcd/proxy that Talos doesn't expose to scraping → perpetual red targets (cosmetic). Disable them in values.

```bash
helm repo add prometheus-community https://prometheus-community.github.io/helm-charts
helm repo update prometheus-community
kubectl create namespace monitoring
kubectl label namespace monitoring pod-security.kubernetes.io/enforce=privileged --overwrite
```

`clusters/omni-cluster/monitoring/values.yaml`:
```yaml
alertmanager: { enabled: false }
grafana:
  enabled: true
  persistence: { enabled: false }   # dashboards from ConfigMaps; no durable PVC; admin pw auto-gens into a Secret (never commit)
kubeControllerManager: { enabled: false }
kubeScheduler:         { enabled: false }
kubeEtcd:              { enabled: false }
kubeProxy:             { enabled: false }
prometheus:
  prometheusSpec:
    retention: 7d
    retentionSize: 8GB
    resources: { requests: { cpu: 250m, memory: 512Mi }, limits: { memory: 1536Mi } }
    storageSpec:
      volumeClaimTemplate:
        spec:
          storageClassName: rook-ceph-block     # Task 2 Ceph storage
          accessModes: ["ReadWriteOnce"]
          resources: { requests: { storage: 10Gi } }
    serviceMonitorSelectorNilUsesHelmValues: false   # <-- scrape ANY ServiceMonitor (needed for the KEDA demo app)
    podMonitorSelectorNilUsesHelmValues: false
```

```bash
helm install kube-prometheus-stack prometheus-community/kube-prometheus-stack \
  --namespace monitoring --version 87.17.0 \
  -f clusters/omni-cluster/monitoring/values.yaml \
  --kube-context omni-cluster

# verify
kubectl -n monitoring get pods      # operator, prometheus-0 (2/2), grafana (3/3), kube-state-metrics, node-exporter ×6 (one per NODE — masters too!)
kubectl -n monitoring get pvc       # prometheus PVC Bound on rook-ceph-block
# Grafana:
kubectl get secret -n monitoring kube-prometheus-stack-grafana -o jsonpath="{.data.admin-password}" | base64 -d; echo
kubectl -n monitoring port-forward svc/kube-prometheus-stack-grafana 3000:80   # http://localhost:3000 user admin
```
Note: node-exporter count = NODE count (DaemonSet), so 6 pods on a 3+3 cluster, not 3.

---

## PART 4 — Argo CD bootstrap via Helm (the imperative step that's imperative ON PURPOSE)

**Bootstrap paradox:** Argo installs things *from git*, but can't install *itself* from git (no Argo running yet to read the repo). So Argo's own install is a ONE-TIME imperative `helm install`. After that: declarative forever.

**Components (workload vs controller):**
- `argocd-application-controller` **[controller]** — the reconcile loop (the heart).
- `argocd-repo-server` **[workload]** — clones repo, renders Helm/Kustomize → manifests.
- `argocd-server` **[workload]** — API + UI.
- `argocd-redis` **[workload]** — cache. Plus `applicationset-controller`, `notifications-controller`, `dex`.

**Reconcile loop:** Git (desired) → repo-server renders → app-controller diffs desired-vs-live → sync/apply → cluster → app-controller keeps observing live → re-syncs on drift. **Never exits** (vs `helm install` firing once). PSA note: `argocd` ns does NOT need the privileged label (Argo touches no host resources → passes `baseline`). Label isn't a reflex; it's a response to a specific need.

```bash
helm repo add argo https://argoproj.github.io/argo-helm
helm repo update argo
kubectl create namespace argocd
```
`clusters/omni-cluster/argocd/values.yaml`:
```yaml
crds: { install: true, keep: true }     # keep CRDs on uninstall so Applications aren't nuked
configs:
  params:
    server.insecure: true               # plain HTTP for localhost port-forward (Argo's TLS fights the tunnel)
```
```bash
helm install argocd argo/argo-cd --namespace argocd --version 10.2.0 \
  -f clusters/omni-cluster/argocd/values.yaml --kube-context omni-cluster

kubectl -n argocd get pods
kubectl -n argocd get secret argocd-initial-admin-secret -o jsonpath="{.data.password}" | base64 -d; echo
kubectl -n argocd port-forward svc/argocd-server 8080:80    # http://localhost:8080 user admin (empty Applications page = correct)
```

---

## PART 5 — Connect the private repo (read-only deploy key, DECLARATIVE Secret)

**Concept:** Argo needs a credential to READ git — the one secret you can't put in git (bootstrap paradox again). Applied out-of-band, never committed. Least-privilege (Task 2/4 reflex): a **read-only, single-repo SSH deploy key**, not a personal key or broad PAT.

```bash
ssh-keygen -t ed25519 -f ~/.ssh/argocd_deploy -N "" -C "argocd-deploy-key omni-cluster"
cat ~/.ssh/argocd_deploy.pub
# GitHub: repo -> Settings -> Deploy keys -> Add -> paste PUBLIC key -> "Allow write access" UNCHECKED
```

**Robust path = declarative Secret (skip the argocd CLI/port-forward entirely).** The label `argocd.argoproj.io/secret-type: repository` is HOW Argo recognizes a plain Secret as a repo cred and picks it up (no restart). `argocd repo add` just creates this exact Secret under the hood — doing it declaratively is seeing the real mechanism.

```bash
cat <<EOF | kubectl apply -f -
apiVersion: v1
kind: Secret
metadata:
  name: homelab-k8s-repo
  namespace: argocd
  labels: { argocd.argoproj.io/secret-type: repository }
stringData:
  type: git
  url: git@github.com:example-user/homelab-k8s.git
  sshPrivateKey: |
$(sed 's/^/    /' ~/.ssh/argocd_deploy)
EOF

kubectl -n argocd get secret homelab-k8s-repo
kubectl -n argocd get secret homelab-k8s-repo -o jsonpath='{.data.sshPrivateKey}' | base64 -d | head -1
# expect: -----BEGIN OPENSSH PRIVATE KEY-----
```
(Definitive connection proof = the KEDA Application going `Synced`; a bad key shows `permission denied (publickey)`.)

---

## PART 6 — KEDA via Argo Application (DECLARATIVE) — the payoff

`clusters/omni-cluster/argocd/apps/keda.yaml`:
```yaml
apiVersion: argoproj.io/v1alpha1
kind: Application
metadata: { name: keda, namespace: argocd }
spec:
  project: default
  source:
    repoURL: https://kedacore.github.io/charts     # KEDA's PUBLIC helm repo (no deploy key needed)
    chart: keda
    targetRevision: 2.20.1                          # pin (confirm latest)
    helm:
      values: |
        resources:
          operator: { requests: { cpu: 100m, memory: 128Mi } }
  destination: { server: https://kubernetes.default.svc, namespace: keda }
  syncPolicy:
    automated: { prune: true, selfHeal: true }
    syncOptions: [ CreateNamespace=true ]
```
```bash
helm repo add kedacore https://kedacore.github.io/charts && helm repo update kedacore
helm search repo kedacore/keda --versions | head        # confirm 2.20.1
kubectl apply -f clusters/omni-cluster/argocd/apps/keda.yaml   # first Application always bootstrapped by hand

kubectl -n argocd get application keda    # Synced / Healthy
kubectl -n keda get pods                  # keda-operator, keda-operator-metrics-apiserver, keda-admission-webhooks
kubectl get crd | grep keda.sh
kubectl api-versions | grep external.metrics   # external.metrics.k8s.io/v1beta1 <-- KEDA claimed the empty slot from step 0
```

**Self-heal test (proves controller vs one-shot):**
```bash
kubectl -n keda delete deployment keda-operator
kubectl -n keda get pods -w      # Argo re-applies it within seconds (selfHeal: true)
```
vs `helm install`: deleting would leave it dead until a manual `helm upgrade`. THAT difference is the whole reason GitOps exists. ✅ Verified live.

---

## PART 7 — ScaledObject on a live Prometheus metric

**Concept:** KEDA doesn't know your metric; Prometheus only scrapes what a **ServiceMonitor [CRD]** tells it to. App must (a) expose a Prometheus metric and (b) have a ServiceMonitor. Then KEDA's trigger queries it.

Demo app (**throwaway**, not committed) + Service + ServiceMonitor in ns `scaling-demo`. **The ScaledObject IS committed** (durable desired-state policy) at `clusters/omni-cluster/autoscaling/web-scaledobject.yaml`:
```yaml
apiVersion: keda.sh/v1alpha1
kind: ScaledObject
metadata: { name: web, namespace: scaling-demo }
spec:
  scaleTargetRef: { name: web }
  minReplicaCount: 1
  maxReplicaCount: 10
  cooldownPeriod: 60
  triggers:
  - type: prometheus
    metadata:
      serverAddress: http://kube-prometheus-stack-prometheus.monitoring.svc:9090
      metricName: http_requests_per_second
      query: sum(rate(http_requests_total{app="web"}[1m]))
      threshold: "10"    # ~10 req/s PER replica
```
```bash
kubectl apply -f clusters/omni-cluster/autoscaling/web-scaledobject.yaml
kubectl -n scaling-demo get scaledobject web    # READY=True
kubectl -n scaling-demo get hpa                 # keda-hpa-web APPEARS — KEDA created it, you didn't  ✅ PROOF
```
`keda-hpa-web` existing = the "KEDA drives+creates HPA" concept made physical, AND it's why the generated HPA is deliberately NOT committed (KEDA owns it → committing would be a second owner, violating one-resource-one-owner).

**HPA TARGETS readout meaning:** `<unknown>/10` = KEDA got NO data from Prometheus (empty query window); `0/10` = got data but rate is 0 (metric exists, not incrementing = no traffic). A real number = traffic flowing.

Cleanup throwaway: `kubectl delete namespace scaling-demo --ignore-not-found`.

---

## GOTCHAS HIT (real, do these right next time)

1. **metrics-server on Talos → `--kubelet-insecure-tls`** (self-signed kubelet certs). Without it: healthy-looking pod, dead `kubectl top`, `x509` in logs.
2. **PSA `privileged` label on `monitoring`** (node-exporter hostPath). Rejected at ADMISSION (creation time, by API server) — DaemonSet exists, pods bounced. Read `FailedCreate` events, not logs.
3. **`serviceMonitorSelectorNilUsesHelmValues: false`** or Prometheus ignores your ServiceMonitor. (Verify: `kubectl -n monitoring get prometheus -o yaml | grep -A2 serviceMonitorSelector` → `{}` means match-all = good.)
4. **port-forward is fragile:** foreground process, dies on Ctrl-C / new terminal; `localhost`→IPv6 `[::1]` can miss the IPv4 bind (`connection refused` / `connection reset by peer`). Use `127.0.0.1`, keep the tunnel in its own terminal, or background it. For repo cred, the declarative Secret avoids the tunnel entirely.
5. **argocd CLI login didn't persist** through the resets → `Argo CD server address unspecified`. Declarative Secret path sidesteps the CLI.
6. **ImagePullBackOff — READ THE MESSAGE:** `pull access denied / does not exist` = wrong image name/tag (a *reference* problem — `brancz/prometheus-example-app:v0.5.0` isn't on docker.io; use `quay.io/brancz/prometheus-example-app:v0.5.0`). vs `i/o timeout / connection refused` = can't REACH registry (a *network* problem). Different root causes, different fixes.
7. **Counter metrics only exist after the first request.** `http_requests_total` is absent on an idle pod → empty Prometheus result is CORRECT, not a bug. Traffic creates the signal.
8. **`kubectl run` busybox load pod hit PSA `restricted`** (default enforce for `kubectl run`), pod never started → no traffic → `<unknown>` HPA. Either give it a compliant `securityContext` via `--overrides`, or generate load without a pod. Your APP came up fine under baseline — different workloads, different privilege needs (PSA working as designed).
9. **`exec format error` exec-ing into the app** = distroless/scratch image, no `/bin/sh`. Can't `exec` a shell into it; use a separate load pod.
10. **`^[[A` garbling a `-w` watch** = up-arrow injected an escape sequence; the watch isn't hung. Ctrl-C, re-run.
11. **Live scale-up not captured** — combination of #6/#7/#8/#9 ate the time; the pipeline was proven by `keda-hpa-web` being generated + all links verified (app exposes metric, Service labeled, ServiceMonitor matches, Prometheus match-all). Load animation is theater, not the deliverable.

---

## KEY LESSONS

1. **KEDA drives HPA, doesn't replace it** — ScaledObject → KEDA generates a managed HPA + feeds it via `external.metrics.k8s.io`.
2. **`requests` is the denominator of native CPU HPA** — right-sizing requests calibrates the autoscaler. KEDA's absolute event thresholds sidestep this (its reason to exist).
3. **Helm and Argo aren't competitors** — Argo uses Helm to render. `helm install` = one-shot (like `terraform apply`); Argo = continuous reconcile loop (like the Omni server). Learn both by doing one each way.
4. **Bootstrap paradox** — the GitOps tool, and its repo credential, must be introduced imperatively; everything after is declarative.
5. **Declarative repo cred > CLI** — a Secret labeled `argocd.argoproj.io/secret-type: repository` is the real mechanism; robust, no tunnel.
6. **Verify, don't assume** — checked metrics-server/Prometheus/Argo/KEDA absence + the empty external-metrics slot before touching anything. Deferred the Prometheus-scope decision until `kubectl top` worked.
7. **Sizing the Prometheus scope on real numbers** — workers had headroom → Grafana ON was safe; Alertmanager OFF (dead weight).
8. **Pin every chart version** (metrics-server 3.13.0, kps 87.17.0, argo-cd 10.2.0, keda 2.20.1) — same discipline as omnictl/talosctl/Ceph.
9. **Prometheus TSDB on Ceph** — Task 2 storage layer earning its keep via `rook-ceph-block` PVC.
10. **Diagnose the chain, don't guess** — walk app→Service→ServiceMonitor→Prometheus→KEDA→HPA link by link; the failing link tells you the fix.

---

## Git / GitHub workflow used (prod-style)

```
branch → per-step edit → helm/kubectl apply → verify healthy → commit (WHY-body) → push → [end: PR → review → merge → delete]
```
One commit per step (readable history), one PR at task end (Tasks 3/4 rhythm):
```bash
git checkout main && git pull
git checkout -b task05-hpa-keda
# step 1: git add clusters/omni-cluster/metrics-server/values.yaml; commit; git push -u origin task05-hpa-keda
# step 4: git add clusters/omni-cluster/monitoring/values.yaml; commit; git push
# step 5: git add clusters/omni-cluster/argocd/values.yaml; commit; git push
# step 6: git add clusters/omni-cluster/argocd/apps/keda.yaml; commit; git push
# step 7: git add clusters/omni-cluster/autoscaling/web-scaledobject.yaml; commit; git push
# NOT committed: throwaway demos (php-apache, scaling-demo app/Service/ServiceMonitor) and the KEDA-generated HPA (KEDA owns it)
```
**Committed-vs-not sorting rule:** durable desired-state (want it reconciled) → repo. Proving/probing then tearing down → inline/`/tmp`. Never manage what another controller manages (the generated `keda-hpa-web`).

Finish prod-style:
```bash
git push -u origin task05-hpa-keda      # (already set on step 1)
# GitHub: open PR task05-hpa-keda -> main, review own diff, Merge (squash), Delete branch
git checkout main && git pull
git branch -d task05-hpa-keda
git push origin --delete task05-hpa-keda
git fetch --prune
```

---

## Conceptual Q&A

**Q: Does KEDA replace HPA or drive it?**
Drives it. You write a ScaledObject; KEDA's operator generates a normal HPA (you'll see `keda-hpa-<name>`) and its metrics adapter answers that HPA's queries by querying Prometheus. HPA still does the scaling math. KEDA adds event scalers + scale-to-zero. Delete the ScaledObject → KEDA deletes the HPA it created (proof it's driving, not replacing).

**Q: Helm vs Argo — which do I use?**
Both — they're different layers. Helm packages/renders; Argo continuously delivers from git (using Helm under the hood). One-shot vs continuous reconcile. The prod pattern is Argo Applications referencing Helm charts.

**Q: Why is native HPA "blind" without metrics-server, but KEDA needs Prometheus?**
HPA can only read k8s metrics APIs. metrics-server fills the resource API (CPU/mem) for native HPA. For app/event metrics, an adapter fills the external-metrics API — KEDA is that adapter, backed by Prometheus (or Kafka, etc.).

**Q: Why did the demo never visibly scale up?**
Traffic never reliably reached the app (dead port-forwards, PSA-blocked load pod, appless image for exec, counter needs traffic to exist). The pipeline was still proven: KEDA generated `keda-hpa-web` from the ScaledObject and every scrape link verified. The replica animation is a demo nicety, not the engineering deliverable.

**Q: Why commit the ScaledObject but not the generated HPA?**
The ScaledObject is desired state you authored → git. The HPA is created and owned by KEDA → committing it would make you a second owner and cause drift fights (one-resource-one-owner). Same rule that kept `php-apache`/`ceph-test-pvc` out of git: manage what you own, not what a controller manages.

**Q: What's the Datadog watch-point for Task 7?**
KEDA owns `external.metrics.k8s.io` (single owner allowed cluster-wide). Datadog's Cluster Agent external-metrics feature would contend for the same APIService. Prometheus itself doesn't collide with Datadog (pull vs push). Bonus: KEDA has a Datadog scaler — you could re-point the same ScaledObject from a Prometheus trigger to a Datadog one later.
