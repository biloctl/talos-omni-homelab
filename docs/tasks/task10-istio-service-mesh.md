# Task 10: Istio Service Mesh (install → mTLS → ingress gateway + TLS → canary → egress control → Kiali)

> **Goal:** Add a service mesh to `omni-cluster` and use it for the things a mesh is actually for: identity-based encryption between services, a real north-south front door with TLS, weighted traffic shifting between two versions of a service without touching a pod, deny-by-default egress, and a live topology map drawn from traffic. Install via Helm with pinned versions and values in git; run both sidecar-injection paths so the Talos PodSecurity trade-off is understood rather than worked around.
>
> **Status:** ✅ Complete. Istio **1.30.3** (sidecar mode, CNI injection path). `demo-app` namespace meshed under `baseline` PodSecurity with no privileged label. STRICT mTLS enforced and proven (plaintext refused, meshed traffic served). MetalLB + Istio ingress gateway serving `https://demo.example.com` from a browser with a Let's Encrypt wildcard cert. v1/v2 canary resting at 90/10, shifted live 50/50 → 0/100 → 90/10 with zero pod events. `REGISTRY_ONLY` egress with one approved external destination. Kiali rendering the mesh graph from Envoy metrics via Prometheus. Six sub-tasks (A–G), each its own branch/PR.

---

## Environment Facts

| Thing | Value |
|---|---|
| Cluster | `omni-cluster` (Omni-managed), k8s 1.36.2 / Talos 1.13.6 |
| Istio version (pinned) | **1.30.3** — `istio/base`, `istio/istiod`, `istio/cni`, `istio/gateway` charts + matching `istioctl` |
| Helm repo | `https://istio-release.storage.googleapis.com/charts` |
| Mesh namespaces | `istio-system` (control plane, CNI DaemonSet, ingress gateway) — labeled `enforce=privileged`; `demo-app` (meshed workload) — `istio-injection=enabled`, **no** privileged label |
| Injection path (end state) | `istio-cni` — privilege on one DaemonSet per node, app pods stay unprivileged |
| Primary CNI | flannel; istio-cni chains after it, does not replace it |
| mTLS | `PeerAuthentication` STRICT in `demo-app` |
| Load balancer | MetalLB (L2 mode) via Argo Application; pool `192.0.2.211–221` |
| Ingress gateway | `istio/gateway` chart, Service `type: LoadBalancer`, EXTERNAL-IP `192.0.2.211` |
| Public hostname | `demo.example.com` → Gateway + VirtualService in `demo-app` |
| TLS | Let's Encrypt wildcard `*.example.com` (certbot DNS-01 via Cloudflare), Secret `example-com-wildcard` in `istio-system` |
| Canary | `demo-app` (v1) + `demo-app-v2` Deployments behind one Service; `DestinationRule` subsets; VirtualService weights 90/10 |
| Egress | `meshConfig.outboundTrafficPolicy.mode: REGISTRY_ONLY`; `ServiceEntry` for `api.github.com:443` |
| Observability | `PodMonitor` on sidecar port 15090 → Prometheus; Kiali (anonymous auth, lab only) |
| Files (Helm-owned) | `istio/istiod-values.yaml`, `istio/cni-values.yaml`, `istio/ingress-gateway-values.yaml`, `istio/kiali-values.yaml` |
| Files (kubectl-owned) | `istio/peer-authentication.yaml`, `istio/demo-app-gateway.yaml`, `istio/serviceentry-github.yaml` |
| Files (Argo-owned) | `apps/demo-app/deployment.yaml`, `deployment-v2.yaml`, `destinationrule.yaml`, `podmonitor-envoy.yaml`; `argocd/apps/metallb.yaml`; `metallb/ipaddresspool.yaml` |

**Network note:** VLAN-friendly throughout. Charts and images pull over TCP/443; mesh traffic is east-west LAN TCP. The one network surprise was HSTS, not the VLAN (see gotchas).

---

## Core concepts (settled up front)

### Istio = one control plane + many Envoys
- **istiod** [controller] — watches Istio CRDs, compiles them into Envoy config, pushes it to every proxy over **xDS** (gRPC). Never touches a packet.
- **Envoy** [workload — a proxy] — the data plane. Every sidecar and every gateway is the *same* Envoy binary in a different role. Learning Istio is learning to configure a fleet of Envoys declaratively.
- Author a CRD → istiod compiles → pushes over xDS → Envoy reconfigures **live, no restart**. This is the property everything below depends on.

### Paper vs. infrastructure
Everything in this task sorts into two buckets, and the sort decides the verb you use:
- **Infrastructure** (actual pods): istiod, sidecars, the ingress gateway, the CNI DaemonSet, Kiali. Installed with Helm; changed with `helm upgrade`.
- **Paper** (CRDs istiod compiles): `PeerAuthentication`, `Gateway`, `VirtualService`, `DestinationRule`, `ServiceEntry`. Applied with `kubectl` or reconciled by Argo, depending on which directory the file lives in.

The `Gateway` CRD is the naming trap: it's paper that configures the gateway pod.

### One resource, one owner (repo rule, applied here)
- File has no `kind:` → Helm values → edit + `helm upgrade`. The `-values.yaml` suffix is the tell.
- File has `kind:` and lives in an Argo-watched directory (`apps/demo-app/`, `metallb/`) → git push; Argo applies and self-heals.
- File has `kind:` and lives in `istio/` → `kubectl apply` and commit. (Debt: adopt these into Argo — see open items.)

Going around the owner gets your change erased on the next reconcile.

---

## PART A — Install: both injection paths, on purpose

### Why two paths
Traffic capture (iptables rules redirecting all pod traffic through the sidecar) has to be set up once per pod. Who does it:
- **Classic (`istio-init`)**: a privileged init container inside *every* meshed pod (`NET_ADMIN`/`NET_RAW`). Talos enforces `baseline` PodSecurity on every namespace by default, and `baseline` forbids those capabilities → every meshed namespace must be labeled `privileged`.
- **CNI (`istio-cni`)**: a privileged DaemonSet does the same iptables work once per *node*, via a plugin chained after flannel. App pods get a harmless `istio-validation` init container that only checks the redirect is in place. App namespaces stay `baseline`.

The classic path is still common. The trade-off is not the init container (one second, pod's own netns) — it's the door you open to admit it: `enforce=privileged` turns off *all* pod security checks for the namespace. CNI confines privilege to `istio-system`.

### A.1 — Pin versions, install istioctl
```bash
helm repo add istio https://istio-release.storage.googleapis.com/charts
helm repo update istio
helm search repo istio/istiod --versions | head          # pinned: 1.30.3

# istioctl is a client CLI; the charts never install it. Pin it to the control plane version.
curl -L https://istio.io/downloadIstio | ISTIO_VERSION=1.30.3 TARGET_ARCH=arm64 sh -
sudo cp istio-1.30.3/bin/istioctl /usr/local/bin/istioctl
istioctl version
```

### A.2 — base + istiod
```bash
kubectl create namespace istio-system
helm install istio-base istio/base -n istio-system --version 1.30.3      # CRDs only
```

`clusters/omni-cluster/istio/istiod-values.yaml` (final form, after Parts A and F):
```yaml
pilot:
  resources:
    requests: { cpu: 100m, memory: 256Mi }   # right-sized in Part F
meshConfig:
  accessLogFile: /dev/stdout
  outboundTrafficPolicy:
    mode: REGISTRY_ONLY                      # Part F
# key is 'cni' in chart 1.30.x — 'istio_cni' is silently ignored
cni:
  enabled: true
  provider: default
```
```bash
helm install istiod istio/istiod -n istio-system --version 1.30.3 \
  -f clusters/omni-cluster/istio/istiod-values.yaml
kubectl -n istio-system get pods                          # istiod 1/1
kubectl get mutatingwebhookconfigurations | grep istio    # the injection webhook
```

### A.3 — Classic path: hit the wall
```bash
kubectl label namespace demo-app istio-injection=enabled
kubectl -n demo-app rollout restart deployment demo-app
kubectl -n demo-app get rs                                # new RS: DESIRED 1, CURRENT 0
kubectl -n demo-app describe rs <new-rs> | grep -A6 -i events
# FailedCreate ... violates PodSecurity "baseline:latest":
#   container "istio-init" must not include "NET_ADMIN","NET_RAW" in securityContext.capabilities.add
```
The terminal printed a loud `restricted:latest` *warning* about the app container — noise. The real *block* was silent, on the ReplicaSet's events. The old pod kept serving the whole time (rolling update held the fort).

Classic-shop fix, to prove the path works:
```bash
kubectl label namespace demo-app pod-security.kubernetes.io/enforce=privileged --overwrite
kubectl -n demo-app get pods -w                           # 2/2 Running
kubectl -n demo-app get pod -l app=demo-app -o jsonpath='{.items[0].spec.initContainers[*].name}'
# -> istio-init
istioctl proxy-status                                     # SYNCED  CDS,LDS,EDS,RDS
```

### A.4 — CNI path: take the privilege back
Order matters: CNI in → istiod flipped → app-namespace privilege removed *last*.
```bash
kubectl label namespace istio-system pod-security.kubernetes.io/enforce=privileged --overwrite

# clusters/omni-cluster/istio/cni-values.yaml:
#   cni:
#     resources:
#       requests: { cpu: 50m, memory: 100Mi }
helm install istio-cni istio/cni -n istio-system --version 1.30.3 \
  -f clusters/omni-cluster/istio/cni-values.yaml
kubectl -n istio-system get ds istio-cni-node             # DESIRED 6, READY 6 (masters included)

# verify the values key against the pinned chart, then flip istiod
helm show values istio/istiod --version 1.30.3 | grep -B2 -A4 -i cni
helm upgrade istiod istio/istiod -n istio-system --version 1.30.3 \
  -f clusters/omni-cluster/istio/istiod-values.yaml

# the reveal
kubectl label namespace demo-app pod-security.kubernetes.io/enforce-      # trailing dash = remove
kubectl -n demo-app rollout restart deployment demo-app
kubectl -n demo-app get pod -l app=demo-app -o jsonpath='{.items[0].spec.initContainers[*].name}'
# -> istio-validation istio-proxy                        (native sidecar, no istio-init)
istioctl proxy-status                                     # still SYNCED
```

| | Classic (`istio-init`) | CNI (`istio-cni`) |
|---|---|---|
| Init container | `istio-init` (NET_ADMIN/NET_RAW) | `istio-validation` (unprivileged check) |
| App namespace | `enforce=privileged` required | plain `baseline` |
| Where privilege lives | inside every meshed pod | one DaemonSet per node, `istio-system` only |
| Mesh behavior | identical | identical |

---

## PART B — mTLS: prove the sidecar does something

- **istiod is the mesh CA.** Every sidecar gets an identity cert tied to its ServiceAccount, rotated automatically. No certs to create, no app changes; the app still speaks plain HTTP and the sidecars encrypt between themselves.
- Default mode is **PERMISSIVE** (accept mTLS *and* plaintext — the migration mode). **STRICT** = mTLS only.

`clusters/omni-cluster/istio/peer-authentication.yaml`:
```yaml
apiVersion: security.istio.io/v1
kind: PeerAuthentication
metadata:
  name: strict-mtls
  namespace: demo-app
spec:
  mtls:
    mode: STRICT
```

The proof is the same command run from two namespaces:
```bash
# baseline (permissive): unmeshed pod, plaintext → served
kubectl -n default run curl-test --rm -it --restart=Never --image=curlimages/curl -- \
  curl -s -m 5 http://demo-app.demo-app.svc/

kubectl apply -f clusters/omni-cluster/istio/peer-authentication.yaml

# unmeshed pod, plaintext → refused (no sidecar → no cert → no identity → door slammed)
kubectl -n default run curl-test --rm -it --restart=Never --image=curlimages/curl -- \
  curl -v -m 5 http://demo-app.demo-app.svc/
# -> Connection reset by peer; pod terminated (Error)

# meshed pod (born in demo-app → sidecar injected → automatic mTLS) → served
kubectl -n demo-app run curl-meshed --rm -it --restart=Never --image=curlimages/curl -- \
  curl -s -m 5 http://demo-app/
```
Only the namespace differs — and therefore whether the pod has an identity.

This is the first *enforced* traffic security on the cluster: the Task 9 NetworkPolicies are L3/L4 objects flannel never enforces; this is L7 in the sidecar and works regardless of CNI.

---

## PART C — Ingress gateway: MetalLB + Gateway/VirtualService + TLS

```
Browser: https://demo.example.com
   |
DNS (dnsmasq on the VLAN)                   name -> 192.0.2.211
   |
MetalLB [controller + speaker DaemonSet]    the IP ASSIGNER: hands the gateway
   |                                         Service a VLAN IP, answers ARP for it.
   |                                         Not an ingress — never reads HTTP.
   |
Istio ingress gateway [Envoy]               the INGRESS: terminates TLS, routes.
   |                                         Empty (zero listeners) until a Gateway CRD binds.
   |
demo-app sidecar (mTLS hop, Part B) -> demo-app
```

### C.1 — MetalLB via Argo
`clusters/omni-cluster/argocd/apps/metallb.yaml` — standard Application (public chart, pinned `targetRevision`, `CreateNamespace=true`, `ServerSideApply=true`, automated prune + selfHeal), destination `metallb-system`.

The speaker DaemonSet needs host networking → `privileged` label:
```bash
kubectl label namespace metallb-system pod-security.kubernetes.io/enforce=privileged --overwrite
kubectl -n metallb-system rollout restart ds metallb-speaker      # kick the FailedCreate backoff
```

`clusters/omni-cluster/metallb/ipaddresspool.yaml`:
```yaml
apiVersion: metallb.io/v1beta1
kind: IPAddressPool
metadata: { name: lab-pool, namespace: metallb-system }
spec:
  addresses: ["192.0.2.211-192.0.2.221"]      # outside DHCP range and static infra
---
apiVersion: metallb.io/v1beta1
kind: L2Advertisement
metadata: { name: lab-l2, namespace: metallb-system }
spec:
  ipAddressPools: [lab-pool]
```
Pool = "here are the addresses." L2Advertisement = "announce them by ARP." Both required.

### C.2 — Ingress gateway (a third costume for the same Envoy)
`clusters/omni-cluster/istio/ingress-gateway-values.yaml`:
```yaml
service:
  type: LoadBalancer
resources:
  requests: { cpu: 100m, memory: 128Mi }
```
```bash
helm install istio-ingressgateway istio/gateway -n istio-system --version 1.30.3 \
  -f clusters/omni-cluster/istio/ingress-gateway-values.yaml
kubectl -n istio-system get svc istio-ingressgateway     # EXTERNAL-IP: 192.0.2.211
```
`<pending>` → a pool IP is MetalLB doing its one job.

### C.3 — Gateway + VirtualService
`clusters/omni-cluster/istio/demo-app-gateway.yaml` (final form, with Part D weights):
```yaml
apiVersion: networking.istio.io/v1
kind: Gateway
metadata: { name: demo-app-gateway, namespace: demo-app }
spec:
  selector: { istio: ingressgateway }
  servers:
    - port: { number: 80, name: http, protocol: HTTP }
      hosts: ["demo.example.com"]
    - port: { number: 443, name: https, protocol: HTTPS }
      hosts: ["demo.example.com"]
      tls:
        mode: SIMPLE                            # terminate TLS at the gateway
        credentialName: example-com-wildcard    # TLS Secret in istio-system
---
apiVersion: networking.istio.io/v1
kind: VirtualService
metadata: { name: demo-app, namespace: demo-app }
spec:
  hosts: ["demo.example.com"]
  gateways: [demo-app-gateway]
  http:
    - route:
        - destination: { host: demo-app, subset: v1, port: { number: 80 } }
          weight: 90
        - destination: { host: demo-app, subset: v2, port: { number: 80 } }
          weight: 10
```
- **Gateway** = open the door (listen on this port for this hostname). Becomes an Envoy listener.
- **VirtualService** = the directions (that hostname → this Service). Becomes Envoy routes.
- Door without directions = 404. Directions without a door = unreachable.

```bash
kubectl apply -f clusters/omni-cluster/istio/demo-app-gateway.yaml
curl -v -H "Host: demo.example.com" http://192.0.2.211/    # 200 — test before DNS exists
```

### C.4 — The cert
Three steps: get the files, put them in a drawer, point the Gateway at the drawer.
```bash
# 1. certbot with the Cloudflare DNS plugin — DNS-01 is the only challenge that allows wildcards,
#    and nothing needs to be internet-reachable. One wildcard covers every future hostname.
sudo certbot certonly --dns-cloudflare --dns-cloudflare-credentials ~/cloudflare.ini \
  -d '*.example.com' --agree-tos -m <email> --no-eff-email

# 2. the drawer — MUST live in istio-system; the gateway Envoy reads credentials from its own namespace
kubectl -n istio-system create secret tls example-com-wildcard \
  --cert=fullchain.pem --key=privkey.pem

# 3. credentialName: in the Gateway (above). istiod pushes the cert to the gateway live — no restart.
```
This cert and the Part B mTLS certs are unrelated systems: this is the gateway's public face for browsers (north-south, Let's Encrypt, manual, 90-day). mTLS certs are sidecar identity inside the mesh (east-west, istiod, automatic, rotated invisibly). One request uses both.

### C.5 — DNS + browser
dnsmasq on the VLAN: `address=/demo.example.com/192.0.2.211`. Off-VLAN workstation: `/etc/hosts`. Then `https://demo.example.com` → padlock → hello.

---

## PART D — Canary: two versions, one Service, weighted routing

### D.1 — Ship v2 through the CI loop (Task 6)
One-line change in `src/demo-app/main.go` on a branch → PR → squash-merge → CI builds, pushes by SHA, bumps `deployment.yaml` → Argo rolls it. This is the *100% rollout* — the canary replaces it.

### D.2 — Build the two-version world
`deployment.yaml` (v1): pin the image back to the known-good v1 SHA explicitly (not `git revert` — the previous bump was a broken build; explicit beats clever) and add `version: v1` to the **template** labels only. A Deployment's `spec.selector` is immutable.

`clusters/omni-cluster/apps/demo-app/deployment-v2.yaml`: a second Deployment, `name: demo-app-v2`, selector and template labels `{ app: demo-app, version: v2 }`, v2 image SHA. The Service selects `app: demo-app` and matches both.

`clusters/omni-cluster/apps/demo-app/destinationrule.yaml`:
```yaml
apiVersion: networking.istio.io/v1
kind: DestinationRule
metadata: { name: demo-app, namespace: demo-app }
spec:
  host: demo-app
  subsets:
    - { name: v1, labels: { version: v1 } }
    - { name: v2, labels: { version: v2 } }
```
The DestinationRule is the seating chart — it names the groups. It routes nothing itself. The VirtualService is the host using the chart. All three files sit in the Argo-owned directory, so Argo deploys and self-heals them together.

```bash
kubectl -n demo-app get pods -L version          # demo-app (v1) 2/2, demo-app-v2 (v2) 2/2
for i in $(seq 1 10); do curl -s https://demo.example.com/; done
# BEFORE weights: the two hellos mix ~evenly — the k8s Service round-robins blind across both.
```

### D.3 — Weights, and the live shift
Add subsets + weights to the VirtualService (final form shown in C.3), `kubectl apply`, commit.
```bash
for i in $(seq 1 30); do curl -s https://demo.example.com/; done | sort | uniq -c
# ~27 v1 / ~3 v2. It's a weighted coin per request, not a turnstile — 26/4 or 29/1 are correct.
```
The payoff, in three terminals: a continuous curl loop; `kubectl -n demo-app get pods -w`; and edits to the two `weight:` numbers followed by `kubectl apply`:
1. `50/50` → the stream flips to alternating within ~2 s
2. `0/100` → v1 vanishes ("canary promoted")
3. `90/10` → v1 dominates again ("rollback") — resting state

The pod watch shows **zero events** throughout. Weight changes are pure route pushes: VirtualService → istiod → RDS over xDS → gateway Envoy → per-request weighted choice. No pods involved.

Standing tripwire while the canary rests: any merge touching `src/demo-app/` triggers the CI bump, which rewrites v1's image and destroys the demo. `deployment-v2.yaml` is hand-pinned; CI doesn't know it exists.

---

## PART E — Egress control: deny by default, allow by name

- Default mesh behavior: any pod can call any external destination. **`REGISTRY_ONLY`** flips it: sidecars refuse destinations the mesh doesn't know.
- **`ServiceEntry`** registers an external destination — the allowlist entry.
- Enforcement is each pod's *own* sidecar. Pure policy, no new pods. (An egress *gateway* — one audited exit pod all approved outbound routes through — is an optional funnel on top; concept covered, not built.)
- **Egress vs. ingress is about who dialed, not which way bytes flow.** A reply to an end user rides the original inbound call; `REGISTRY_ONLY` never touches it.

Flip the default in `istiod-values.yaml` (shown in A.2) and `helm upgrade`. Then:

`clusters/omni-cluster/istio/serviceentry-github.yaml`:
```yaml
apiVersion: networking.istio.io/v1
kind: ServiceEntry
metadata: { name: github-api, namespace: demo-app }
spec:
  hosts: ["api.github.com"]
  ports: [{ number: 443, name: https, protocol: TLS }]
  resolution: DNS
```

Prove both directions — the lesson is the pair:
```bash
kubectl -n demo-app run curl-egress --rm -it --restart=Never --image=curlimages/curl -- \
  curl -sv -m 5 https://api.github.com/ 2>&1 | tail -3       # JSON body — allowed
kubectl -n demo-app run curl-egress2 --rm -it --restart=Never --image=curlimages/curl -- \
  curl -sv -m 5 https://example.org/ 2>&1 | tail -3          # connection closed — refused by its own sidecar
```

---

## PART F — Kiali: the mesh as a map

Kiali is visualization only: not a control plane, not enforcement. Every Envoy counts what passes through it (`istio_requests_total` with source/destination/version/mTLS labels); Prometheus scrapes those; Kiali queries Prometheus and draws. **Nobody tells Kiali the topology** — it's inferred from observed traffic, which is why the map is always true.

### F.1 — Feed Prometheus the sidecar metrics (Kiali draws nothing without this)
kube-prometheus-stack does not scrape sidecars by default. One PodMonitor, targeting port 15090 (`http-envoy-prom`), which *is* declared on injected pods so plain port-name matching works:

`clusters/omni-cluster/apps/demo-app/podmonitor-envoy.yaml`:
```yaml
apiVersion: monitoring.coreos.com/v1
kind: PodMonitor
metadata: { name: demo-app-envoy, namespace: demo-app }
spec:
  selector:
    matchLabels: { app: demo-app }
  podMetricsEndpoints:
    - { port: http-envoy-prom, path: /stats/prometheus, interval: 30s }
```
Verify in Prometheus: `istio_requests_total` returns series whose labels *are* the graph edges — `source_workload=istio-ingressgateway`, `destination_workload=demo-app|demo-app-v2`, `connection_security_policy=mutual_tls`. First read: 16 v1 / 2 v2 — the canary, measured by Envoy itself.

### F.2 — Install
`clusters/omni-cluster/istio/kiali-values.yaml`:
```yaml
auth:
  strategy: anonymous            # lab only — real deployments use openid/token
external_services:
  prometheus:
    url: http://kube-prometheus-stack-prometheus.monitoring.svc:9090
```
```bash
helm repo add kiali https://kiali.org/helm-charts && helm repo update kiali
helm install kiali kiali/kiali-server -n istio-system --version <PINNED> \
  -f clusters/omni-cluster/istio/kiali-values.yaml
kubectl -n istio-system port-forward svc/kiali 20001:20001
```
Traffic Graph → namespace `demo-app` → Display → **Traffic Distribution** + **Security**. Within a minute of a curl loop: gateway → Service (triangle) → v1 and v2 (workloads), percentages converging on 90/10, a padlock on every hop. The fork *after* the triangle is the canary made visible — one Service, two versions, split by Envoy.

Two maps to keep separate: **Traffic Graph** = your applications (incident triage). **Mesh** page = Istio's own anatomy (is the control plane healthy). **Istio Config** = your CRDs, linted.

---

## KEY LESSONS / GOTCHAS (do these right next time)

**Install / PodSecurity**
1. **warn prints loudly and blocks nothing; enforce blocks silently and prints nowhere you're looking.** The `restricted:latest` terminal warning was noise about the wrong container at the wrong level. The real rejection was a `FailedCreate` event on the *new ReplicaSet* — an object you have to go interrogate.
2. **A blocked rollout is not an outage.** The old pod served the entire time admission rejected the new one. Rolling-update contract, working.
3. **PSA labels don't grant anything — they pick the rulebook.** `enforce=privileged` = "stop checking this namespace." Admission is checked at creation only; running pods are never re-checked, which is why removing the label doesn't kill the old pod and a restart is needed.
4. **Sidecar injection is a mutating webhook at pod creation, gated on the namespace label.** Existing pods can never gain a sidecar. `rollout restart` is the mechanism, not a workaround. `kubectl get ns -L istio-injection` shows who's in.
5. **CNI-flip order: cni chart → istiod values → then remove the app-namespace privilege.** Wrong order re-manufactures the classic-path rejection.
6. **`istio-validation` is fail-closed.** A pod stuck `Init:0/1` means the CNI redirect isn't in place — check the `istio-cni-node` pod on that node, don't blame the app.
7. **`istio-proxy` in `initContainers` is a native sidecar, not a bug.** Newer Kubernetes starts sidecars as a special init container that runs for the pod's life.

**Helm**
8. **Helm silently ignores unknown values keys.** `istio_cni: enabled: true` did nothing on chart 1.30.x, which wants `cni:`. No error, no warning — the webhook kept injecting `istio-init` and PSA kept rejecting it. Diagnose with `helm get values <release>` (what's live) vs `helm show values <chart> --version <pinned>` (what the chart reads). Read the schema, not recall.
9. **A Helm values file is the complete desired state.** Every `helm upgrade -f` must carry all settings; anything omitted reverts to chart defaults. That's how the CNI setting got lost once.
10. **A stray character in YAML can ride silently.** A leftover `\` before `pilot:` lived in a values file undetected. Read what ships.

**Networking / gateway**
11. **MetalLB speakers blocked by PSA look like success.** Controller Running, IPs *assigned*, `kubectl` perfect — and curls hang, because no speaker is answering ARP. `DESIRED 6 / CURRENT 0` on the DaemonSet is the tell.
12. **REFUSED vs. TIMEOUT tells you which layer failed.** Refused = something answered with a reset → ARP/MetalLB worked, no listener on the port. Timeout = nothing answered → speaker/ARP problem. A gateway Envoy has *zero* listeners until a Gateway CRD binds; the pre-CRD refusal was Envoy behaving correctly.
13. **`Synced/Degraded` with every pod green — read once, then stop tuning.** No degraded resource, no condition message, data path proven. Recorded as an open item (frr-k8s health in L2-only mode) rather than chased.
14. **The entire `.dev` TLD is HSTS-preloaded.** Browsers refuse plain HTTP to any `.dev` name and silently force HTTPS. `curl` on 80 works; a browser never will. The fix is real TLS on 443 — the production pattern anyway.
15. **The TLS Secret must live in `istio-system`.** The gateway Envoy reads credentials from its own namespace, not the Gateway CRD's namespace.
16. **Static credentials in files rot.** A DNS-API token used by certbot was dead when needed — and it had also silently doomed an unrelated cert's auto-renewal. Same bug class as expired PATs. Migrate to Vault; check `df -h` before long-lived hosts do anything important (`cat > file` truncates before writing — a full disk emptied a config file).
17. **`nslookup` ignores `/etc/hosts` on macOS.** `dscacheutil -q host -a name <host>` shows what curl and the browser actually resolve.

**Canary / GitOps**
18. **Argo `Unknown` = repo fetch failing.** Read the condition (`kubectl -n argocd get application <app> -o jsonpath='{range .status.conditions[*]}{.type}: {.message}{"\n"}{end}'`). Ours: a dead repo PAT.
19. **A healthy delivery chain says nothing about the credential inside it.** Vault unsealed, ESO `SecretSynced`, Secret present — and the token was still dead. Vault stores; it doesn't renew. Acceptance gate: pull the cluster's *own stored bytes* and test them against the real endpoint (`git ls-remote https://$U:$P@github.com/...`) before anything else consumes them.
20. **Never hand-paste credentials into pod shells.** A PAT pasted inside `kubectl exec -- sh` arrived corrupted while passing both the prefix check and the length check. Only content comparison caught it. Write secrets via UI or API with variable expansion.
21. **Dead pull secrets hide behind image caches.** v1 ran for a month on a cached image; the first *new* pull exposed the dead GHCR PAT (`403` at the token endpoint). GHCR wants a classic PAT with `read:packages`.
22. **Robots commit to main → humans never work on main.** A fix committed on local `main` collided with the CI bot's bump. Recovery: `git branch save-work` → `git reset --hard origin/main` → rebase → PR.
23. **Never hand-type existing refs.** `git push -u origin HEAD`; `gh pr create --head "$(git branch --show-current)"`. Three typos in one task paid for this rule.
24. **A Deployment's `spec.selector` is immutable.** Add `version` labels to the template only; new Deployments (v2) may and should include `version` in their selector so the two never claim each other's pods.
25. **Explicit image SHA beats `git revert`.** The revert would have restored the *previous* bump — also a broken build.
26. **Pod-failure taxonomy** (all four seen in one task):

| Status | Meaning | Diagnose with |
|---|---|---|
| `ImagePullBackOff` | can't GET the image (auth/name) | pod events — registry error verbatim |
| `Init:0/1` stuck | init container blocked | that container's logs (`istio-validation` = CNI issue) |
| `RunContainerError` | process never started (exec/perms) | `describe` → OCI runtime message; logs empty |
| `CrashLoopBackOff` | started, then died | `kubectl logs --previous` + exit code (137 OOM, 255 died at startup) |

**Scheduling**
27. **`0/6 nodes available: 3 Insufficient memory, 3 untolerated taints` — decode per bucket.** Masters tainted (by design); workers out of *requested* headroom. Scheduling is bookkeeping: `kubectl top nodes` (actual) vs `kubectl describe nodes | grep -A6 "Allocated resources"` (promises). On a busy cluster "Insufficient X" usually means requests exhausted, not resources. Fix: right-size the request (istiod 512Mi → 256Mi). Note the chicken-and-egg: the old istiod's reservation is part of what blocks its replacement — rolling upgrades need one pod of slack.
28. **A Pending replacement during a stuck rollout is safe-but-urgent.** Nothing is broken, but you're one old-pod crash from no control plane.

**Observability**
29. **kube-prometheus-stack does not scrape sidecars by default.** `istio_requests_total` is empty until a PodMonitor targets 15090. Kiali installed without this renders a blank map.
30. **15090 vs. 15020.** 15090 = Envoy's raw stats, *declared* port, plain `port:` matching, Envoy metrics only. 15020 = merged Envoy + app endpoint, *undeclared*, needs relabeling. For Kiali, 15090 is sufficient and simple.
31. **Kiali only maps what Prometheus hears.** One PodMonitor on `demo-app` = only `demo-app`'s sidecars witnessed. Mesh more namespaces and they need scraping too or they're invisible.
32. **Kiali's Grafana warning is absence, not failure.** It reports every unconfigured integration as unhealthy.

---

## Conceptual Q&A

**Q: Why did only `demo-app` get a sidecar?**
The injection webhook's first question is "does this pod's *namespace* carry `istio-injection=enabled`?" Only `demo-app` does. Infrastructure namespaces (monitoring, vault, argocd) stay out of the mesh unless deliberately pulled in. Opt-in by design.

**Q: Is there a real security reason to avoid `istio-init`?**
The container itself, no — one second, its own netns. The cost is the door: admitting it requires `enforce=privileged` on every meshed namespace, which turns off all pod security checks there. CNI keeps guardrails on app namespaces and confines privilege to `istio-system`. Defense-in-depth trade; many teams accept the classic path, auditors tend to prefer CNI.

**Q: If MetalLB exists, why an ingress gateway too?**
Different questions. MetalLB: "how does traffic reach the cluster at all?" (an address). Gateway: "what happens to it once it arrives?" (routing, TLS, mesh rules). Address vs. front desk — the same pairing as MetalLB + ingress-nginx on the reference cluster.

**Q: DestinationRule vs. VirtualService?**
DR = the seating chart (subset `v1` = pods labeled `v1`). VS = the host using it (send 90% to subset `v1`). The DR routes nothing by itself.

**Q: Why did traffic mix ~50/50 before weights?**
The Kubernetes Service is version-blind round-robin. Istio adds the finer grain — one Service carved internally into subsets that only Envoy can see.

**Q: Where do weight changes "run"?**
You apply the VirtualService → istiod compiles it → pushes routes (RDS) over xDS into the gateway Envoy → per-request weighted choice. No pods restart; the pod watch is silent.

**Q: Why zero downtime through six broken v2 builds?**
Rolling update: the old pod isn't killed until the new one is Ready. Every broken v2 left v1 serving.

**Q: Does `REGISTRY_ONLY` break the demo site?**
No. Inbound conversations (user → gateway → app, replies included) are ingress. `REGISTRY_ONLY` only affects calls pods *initiate* outward.

**Q: Is Kiali part of Istio?**
Separate project, purpose-built companion. Installs from its own Helm repo, reads Istio's metrics and CRDs. Kiali is mesh-shaped (services, edges, mTLS, config validation); Grafana is general dashboards. Complementary.

**Q: `helm upgrade` or `kubectl apply` — how do I tell?**
Ownership decides the verb. No `kind:` → Helm values → `helm upgrade`. Has `kind:` → a manifest → if its directory is Argo-watched, git push; otherwise `kubectl apply`. One owner per resource.

---

## Git / GitHub workflow used (prod-style)

```
Task A  branch taskA-istio-install    -> istiod-values, cni-values          -> PR -> squash
Task B  branch taskB-mtls             -> peer-authentication.yaml           -> PR -> squash
Task C  branch ingress-gateway        -> metallb app + pool, gateway values, demo-app-gateway.yaml -> PR -> squash
Task D  branch taskd-v2 (app change)  -> CI builds v2, bot bumps manifest
        branch taskd-canary           -> deployment.yaml (v1 pin), deployment-v2.yaml, destinationrule.yaml -> PR -> squash
        gateway weights                -> kubectl apply + commit (hand-owned istio/ family)
Task F  istiod-values (REGISTRY_ONLY, cni key fix, right-size) + serviceentry-github.yaml -> helm upgrade / kubectl apply + commit
Task G  branch taskg-envoy-metrics    -> podmonitor-envoy.yaml (Argo-owned) -> PR -> squash
        kiali-values.yaml              -> helm install + commit
```

Not committed (imperative, out-of-band — noted as debt): the namespace labels (`istio-injection=enabled` on `demo-app`; `enforce=privileged` on `istio-system` and `metallb-system`), the TLS Secret, the `istioctl` install.

## Open items
- Adopt the four Helm releases (`base`, `istiod`, `cni`, `gateway`, and `kiali`) into Argo Applications with `ServerSideApply=true` (Istio CRDs are large). Move the hand-applied `istio/` manifests under Argo too — weight changes then become git commits, which is the doorstep of Argo Rollouts.
- Promote namespace labels into git-managed manifests.
- **cert-manager** to issue and renew the wildcard in-cluster; the Secret is currently a 90-day snapshot.
- Migrate the DNS-API token to Vault.
- Part D follow-ons: fault injection on v2, retries and timeouts at the gateway, circuit breaking via `DestinationRule.trafficPolicy`.
- Grafana, Argo CD, and Kiali behind the ingress gateway (the wildcard already covers the names) — kills port-forwarding for good.
- Kiali → Grafana integration (`external_services.grafana.internal_url`, cosmetic).
- Investigate or disable `frr-k8s` in the MetalLB chart (L2-only; likely source of the `Degraded` status).
