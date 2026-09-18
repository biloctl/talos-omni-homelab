# Task 9: StrongDM + RBAC + NetworkPolicies (the human-access & authorization planes)

> **Goal:** Settle the last three security control planes as *documentation*: StrongDM (how humans get a brokered, audited, short-lived session), RBAC (what an identity may do to the k8s API), and NetworkPolicies (which workloads may talk to which). No live install — StrongDM is trial/billing-gated and its egress model doesn't fit the VLAN without an external gateway; RBAC + NetworkPolicies are authored/explained rather than enforced (NetworkPolicies are a no-op on flannel).
>
> **Status:** ✅ Document-only complete. Architecture + authored config + gotchas captured for all three planes. Decision to scope-and-stop the StrongDM build recorded (trial clock + external-gateway requirement). **The build sequence is now complete.**
>
> **Scope note:** The original plan had RBAC + NetworkPolicies as a task and StrongDM as a separate task. Folded back together here as one authorization/access chapter, all document-only, to close the sequence cleanly.

---

## THE ONE MENTAL MODEL — the five control planes, now all accounted for

From Task 8. These are **independent** planes (not a dependency stack), each answering a different question. Task 9 closes the three that were still open:

| Question | Plane | Tool | Status after Task 9 |
|---|---|---|---|
| How do humans get in? (audited, short-lived?) | human access / audit | **StrongDM** | documented (build scoped-and-stopped) |
| Who are you? | authentication | Auth0 / k8s SA auth | built (Tasks 2, 6–9) |
| What may you **do** to the k8s API? | authorization | **RBAC** | documented + authored |
| What secrets can you **get**, for how long? | secrets | Vault + ESO | built (Task 8) |
| Which workloads may **talk** to which? | network segmentation | **NetworkPolicy** | documented (no-op on flannel) |

Read it as five locks on five doors. StrongDM wraps *around* authN/authZ — it does not replace them. A brokered StrongDM session still lands you at the k8s API as some identity, and RBAC still decides what that identity can do.

---

## Environment Facts

| Thing | Value |
|---|---|
| Cluster | `omni-cluster` (Omni-managed), k8s 1.36.2 / Talos 1.13.6 |
| kubeconfig context | `omni-cluster` |
| Repo / branch | `example-user/homelab-k8s`, branch `task09-access-authz` |
| StrongDM control plane | `app.strongdm.com` — SaaS, egress **443** required |
| StrongDM node listener | default **TCP 5000** (configurable) — the reason a naive install fails on this VLAN |
| StrongDM pricing | quote-based enterprise PAM; **14-day trial**, no standing free tier |
| StrongDM brokerable resource (chosen) | omni-cluster **k8s API** (first-class); SSH to a lab host = secondary |
| StrongDM NON-brokerable (flagged) | talosconfig (gRPC/mTLS, not a native SDM type), Omni API (Auth0/gRPC) |
| Required topology (if ever built) | **gateway OUTSIDE the VLAN** (listen 443) + **relay INSIDE** (egress-only) |
| RBAC target (authored) | read-only role in `demo-app` ns + cluster-wide read ClusterRole |
| NetworkPolicy reality | **flannel [CNI] does NOT enforce NetworkPolicies** → objects are no-ops here |
| NetworkPolicy enforcement path | needs a policy-capable CNI

**Network personality note:** StrongDM is the FIRST SaaS-brokered tool whose control-plane link is **not** 443 by default — node-to-node is TCP 5000. The VLAN blocks non-443 outbound TCP (same wall that killed SSH/22 in Task 6). RBAC + NetworkPolicies are in-cluster only — no egress concern.

---

## PART 1 — StrongDM (human access / audit plane)

### 1.1 What it is (and isn't)

StrongDM is a Zero-Trust **PAM** (Privileged Access Management) / dynamic access platform. It brokers, authorizes, and **audits** human sessions to infrastructure (k8s, SSH, RDP, databases, web apps), granting access for the precise time it's needed and logging every action.

- It is the **human-side mirror of Vault.** Vault (Task 8) killed static, long-lived secrets for *workloads* (ESO authenticates by ServiceAccount, gets a short-lived token). StrongDM does the analogous thing for *humans*: no long-lived kubeconfig/SSH key on the laptop; the session is brokered, short-lived, and audited.
- It does **NOT** replace Auth0, k8s SA auth, or RBAC. It sits *in front of* them. authN answers "who are you"; RBAC answers "what may you do"; StrongDM answers "should you get a logged pipe to this resource at all, and for how long."

### 1.2 Architecture — who DIALS whom (the load-bearing concept)

Three pieces of StrongDM software + the resource. Categorize gateway/relay as **[workloads / relays]** (they proxy traffic; they do NOT reconcile desired→actual, so they are not [controllers]).

- **Control plane** `[SaaS — app.strongdm.com]` — policy, identity federation, audit log. Never dials into your network. Everything authenticates OUT to it on **443**.
- **Gateway** `[self-hosted node]` — the entry point clients connect to. Clients must reach a gateway on **TCP 5000** (or a configured port). Gateways LISTEN for inbound client connections and dial OUT to resources (or to relays).
- **Relay** `[self-hosted node, egress-only]` — same binary, different role. Relays do NOT listen for client connections; they open a **reverse tunnel out to a gateway**, preserving the egress-only nature of a firewall. A relay only needs egress to a gateway (5000) + egress to the resources it fronts.

**The whole task turns on one property:** the arrowhead sits on the *receiving* side; the tail is whoever *dials*. Relays are designed for "a secure subnet where you can't expose ports" — which is your VLAN described back to you.

```
OUTSIDE THE VLAN
  [ Mac client (SDM app) ] --login 443--> [ StrongDM control plane (SaaS) ]
            |                                        ^
        session (443/5000)                           | 443
            v                                         |
  [ Gateway  listens :443 ] ------------------443-----+
            ^
============|===== egress-only wall: only 443 crosses =====================
            | relay dials OUT :443 (reverse tunnel)
INSIDE THE VLAN 192.0.2.0/24
  [ Relay (egress-only) ] --LAN--> [ omni-cluster k8s API ]  (the brokered resource)
```

### 1.3 How this replaces the laptop-kubeconfig model

Today: a long-lived kubeconfig (cluster CA + token) sits on the Mac. Lost laptop = standing key until someone rotates it; nothing logs what you did. Same anti-pattern Vault taught you to distrust.

Under StrongDM:
1. You authenticate to the control plane (SSO) → policy check.
2. The real cluster credential lives with StrongDM / the relay — **never on the laptop**.
3. Your local kubeconfig points at `127.0.0.1:<port>` that the SDM client proxies; the credential is injected at the gateway/relay; the session is short-lived; every request is logged.

Endpoint holds no durable secret → session-scoped access → audit trail. The human-side of "ESO materializes a short-lived, SA-authed cred so no static token sits in the workload."

### 1.4 The Talos/Omni flag (know what a tool can't cleanly do)

- **k8s API** — first-class SDM resource type. Brokers cleanly; you'd get a kubeconfig-shaped experience. **This is the demo to author.**
- **SSH to a lab host** — first-class. Good secondary (e.g. the dnsmasq box).
- **talosctl / talosconfig** — Talos speaks its own gRPC API over mTLS with a client cert bundle. NOT a native SDM protocol. Best case a raw-TCP "server" resource = a dumb pipe with no protocol-aware credential injection or command-level audit (the whole value). **Do not plan to broker talosconfig.**
- **Omni API** — HTTPS/gRPC behind Auth0; already has its own auth story (Task 1). Not a native SDM type. Out of scope.

Net: broker the **k8s API**; note *why* Talos/Omni are excluded. That "cleanly-supported vs dumb-tunnel" judgement is the SRE lesson.

### 1.5 The two reality checks that forced document-only

**Free-tier reality.** StrongDM is quote-based enterprise PAM (contract values commonly tens of thousands/yr); there's a **14-day trial** but **no standing free plan**. So any build is time-boxed to two weeks, then the trial org disappears. Emphasis therefore shifts to *authored, git-committed config you can re-stand-up* over a permanently-running service.

**Egress reality (the real fork).** StrongDM node listeners default to **TCP 5000** — used for client→gateway AND relay→gateway. The VLAN blocks all non-443 outbound TCP (same wall as SSH/22 in Task 6). A relay inside the VLAN dialing a gateway on 5000 is **dead on arrival**.
- Escape hatch (the recurring "get onto 443 or stay internal" lesson): configure the gateway to LISTEN on 443; the relay's egress rides 443. But that forces the gateway to live **outside** the VLAN on a routable host both the Mac and the relay can reach on 443 — in practice a small cloud VM (or public IP + port-forward). The relay goes inside; the gateway does not.
- Consequence: unlike Datadog (one egress-only agent → done), a working StrongDM demo needs a trial org **+** an external 443 gateway host **+** an in-VLAN relay **+** the registered resource — more moving parts than any prior task, on a 14-day clock.

**Decision: scope-and-stop → document the architecture.** The Task-7 node-agent lesson applied at the task level: the learning goal (where StrongDM sits, the broker/relay model, how it replaces static kubeconfig, why the VLAN reshapes it) is fully met without burning a cloud VM and a trial clock on an ephemeral install.

### 1.6 Authored config sketch (what you WOULD commit)

Even document-only, capture the shape so it's re-stand-up-able. StrongDM has a Terraform provider (`strongdm/terraform-provider-sdm`) and a Helm chart for k8s-hosted nodes — meaning the whole thing fits the Task-4 "Terraform owns SaaS/identity" and the Task-5 "Argo Application for a Helm chart" patterns you already know.

- **Node (relay) in-cluster via Helm/Argo** — a relay can run as a Kubernetes container. It would be an Argo Application referencing StrongDM's node Helm chart, ns `strongdm`, with the node's registration token as an out-of-band Secret (Vault-sourced via ESO — the Task-8 pattern extends here).
- **Resource registration via Terraform** (`sdm_resource` / `sdm_node`) — declare the gateway, the relay, and the k8s API resource as HCL, exactly like the Cloudflare record in Task 4. The gateway `listen_address` would be set to `:443`.
- **Git placement:** `terraform/strongdm/` for the SDM resources; `clusters/omni-cluster/argocd/apps/strongdm-relay.yaml` for the relay Application. Node join token = Vault → ESO, never committed.

(All authored/illustrative — not applied, since there's no live org.)

### 1.7 StrongDM gotchas (banked for a future real build)

1. **Node listener is TCP 5000, NOT 443** — verify the real egress with a `netshoot` pod + `nc -zv <gateway-host> 5000` (or your configured port) BEFORE installing. On this VLAN you MUST configure the gateway on 443 and put it OUTSIDE the VLAN.
2. **Trial clock** — 14 days, no free tier. Build only when you can finish the loop inside the window.
3. **Relay is the right node type for an egress-only subnet** — it dials out and never listens; that's the textbook firewalled-subnet pattern.
4. **talosconfig doesn't broker cleanly** — raw-TCP tunnel loses the audit/credential-injection value. Broker the k8s API instead.
5. **Node join token is a secret** — Vault → ESO, same as every other cred since Task 6. Never in git.

---

## PART 2 — RBAC (authorization plane)

### 2.1 Concept

RBAC (Role-Based Access Control) answers "what may this identity DO to the k8s API?" It's pure authorization — it assumes authN already happened (a cert, an OIDC token, a ServiceAccount JWT). Four object types, two pairs:

- **Role** `[namespaced]` / **ClusterRole** `[cluster-scoped]` — a set of *permissions*: `verbs` (get/list/watch/create/update/patch/delete) on `resources` (pods, secrets, deployments…) in `apiGroups`. A grant attached to nothing yet — **exactly like a Vault policy** (Task 8).
- **RoleBinding** `[namespaced]` / **ClusterRoleBinding** `[cluster-scoped]` — ties a **subject** (a user, group, or ServiceAccount) to a Role/ClusterRole — **exactly like a Vault k8s-auth role** binding an SA to a policy.

Mental map to Vault (Task 8): Role ≈ Vault policy (the grant); RoleBinding ≈ the k8s-auth role (the binding). Same "grant is inert until bound to an identity" shape.

**Scope combinations:**
- Role + RoleBinding → permissions in ONE namespace.
- ClusterRole + ClusterRoleBinding → permissions cluster-wide.
- ClusterRole + RoleBinding → reuse a cluster-wide *definition* but grant it only in ONE namespace (common for "read-only" reused per team).

### 2.2 How it ties to StrongDM

StrongDM brokers the *pipe* to the API and injects an identity; RBAC constrains what that identity may do once connected. They're orthogonal: StrongDM = "you get an audited session as identity X"; RBAC = "identity X may only `get`/`list` in `demo-app`." Both are least-privilege reflexes (Task 4/8 theme) applied to different planes.

### 2.3 Authored example — read-only SRE identity

Namespaced read-only Role + binding (grounded in your stack: read `demo-app`):

```yaml
# clusters/omni-cluster/rbac/demo-app-readonly-role.yaml
apiVersion: rbac.authorization.k8s.io/v1
kind: Role
metadata:
  name: demo-app-readonly
  namespace: demo-app
rules:
  - apiGroups: ["", "apps"]                 # "" = core (pods, services), "apps" = deployments
    resources: ["pods", "services", "deployments", "replicasets"]
    verbs: ["get", "list", "watch"]         # NO create/delete = read-only
---
apiVersion: rbac.authorization.k8s.io/v1
kind: RoleBinding
metadata:
  name: demo-app-readonly
  namespace: demo-app
subjects:
  - kind: Group                              # an OIDC/SSO group (StrongDM-federated) or an SA
    name: sre-readonly
    apiGroup: rbac.authorization.k8s.io
roleRef:
  kind: Role
  name: demo-app-readonly
  apiGroup: rbac.authorization.k8s.io
```

Cluster-wide read (reuse a ClusterRole, bind cluster-wide):

```yaml
# clusters/omni-cluster/rbac/cluster-readonly.yaml
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRole
metadata:
  name: cluster-readonly
rules:
  - apiGroups: ["", "apps", "batch"]
    resources: ["*"]
    verbs: ["get", "list", "watch"]
---
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRoleBinding
metadata:
  name: cluster-readonly
subjects:
  - kind: Group
    name: sre-readonly
    apiGroup: rbac.authorization.k8s.io
roleRef:
  kind: ClusterRole
  name: cluster-readonly
  apiGroup: rbac.authorization.k8s.io
```

### 2.4 Verification (the trophy command — no guessing)

```bash
# impersonate to test a grant WITHOUT switching identities:
kubectl auth can-i list pods --namespace demo-app --as-group sre-readonly     # -> yes
kubectl auth can-i delete pods --namespace demo-app --as-group sre-readonly   # -> no
kubectl auth can-i create secrets -A --as-group sre-readonly                  # -> no

# see what a subject actually has:
kubectl get rolebinding,clusterrolebinding -A -o wide | grep sre-readonly
```

`kubectl auth can-i ... --as`/`--as-group` is the read-only seatbelt — it answers "would this be allowed?" without you needing the identity. Same discipline as `terraform plan` / `omnictl ... diff`.

### 2.5 RBAC gotchas

1. **RBAC is additive-only, default-deny.** No `deny` rules exist; if nothing grants a verb, it's denied. You never "block" — you simply don't grant. (Contrast with NetworkPolicy, which has an explicit default-deny you must *author*.)
2. **`apiGroups: [""]`** is the CORE group (pods, services, secrets, configmaps). Deployments live in `apps`, jobs in `batch`. Forgetting the group = the rule silently matches nothing.
3. **ClusterRole + RoleBinding** grants a cluster-wide *definition* in ONE namespace — the idiomatic way to reuse "view"/"edit"/"admin" per team without duplicating rules.
4. **Built-in ClusterRoles exist** (`view`, `edit`, `admin`, `cluster-admin`) — prefer binding these over hand-rolling for common cases; hand-roll only for tighter scopes.
5. **Verify with `kubectl auth can-i --as`**, don't assume. A missing verb or wrong apiGroup is invisible until a request 403s.

---

## PART 3 — NetworkPolicies (network segmentation plane) — OVERVIEW / AUTHORING ONLY

### 3.1 The reality that makes this document-only

`omni-cluster` runs **flannel [CNI]**, confirmed in Task 8 (6 flannel pods, no Calico/Cilium). **flannel does NOT enforce NetworkPolicies.** So any `NetworkPolicy` object you apply here is a **no-op** — the API accepts it, nothing enforces it. Live enforcement needs a **policy-capable CNI** (Calico or Cilium), which is a CNI swap on a live cluster — deferred.

So this part is: understand the model, author correct policies, know exactly why they don't bite here.

### 3.2 Concept

A `NetworkPolicy` `[namespaced object]` controls which pods may talk to which, at L3/L4 (IP + port), by pod/namespace label selectors.

- **Default is ALLOW-ALL.** With no policy, every pod can reach every other pod. This is the opposite of RBAC's default-deny — a common trip-up.
- **The moment ONE policy selects a pod, that pod flips to default-DENY** for the direction(s) the policy covers (ingress and/or egress) — then only the listed rules are allowed. Selecting a pod with an empty rule set = deny all of that direction.
- Selectors: `podSelector` (by pod labels), `namespaceSelector` (by ns labels), `ipBlock` (CIDR). `policyTypes: [Ingress, Egress]` says which directions the policy governs.

### 3.3 Authored example — default-deny + a scoped allow (grounded in your stack)

Deny all ingress in `demo-app`, then allow only Prometheus (in `monitoring`) to scrape `/metrics`:

```yaml
# clusters/omni-cluster/netpol/demo-app-default-deny.yaml   (NO-OP on flannel — authored for portability)
apiVersion: networking.k8s.io/v1
kind: NetworkPolicy
metadata:
  name: default-deny-ingress
  namespace: demo-app
spec:
  podSelector: {}          # {} = ALL pods in the namespace
  policyTypes: [Ingress]   # empty ingress rules below = deny all inbound
---
apiVersion: networking.k8s.io/v1
kind: NetworkPolicy
metadata:
  name: allow-prometheus-scrape
  namespace: demo-app
spec:
  podSelector:
    matchLabels: { app: demo-app }
  policyTypes: [Ingress]
  ingress:
    - from:
        - namespaceSelector:
            matchLabels: { kubernetes.io/metadata.name: monitoring }
      ports:
        - protocol: TCP
          port: 8080          # demo-app's /metrics port (Task 6)
```

### 3.4 Why author a no-op?

- **It documents intent** even unenforced — a reviewer sees "demo-app should only accept Prometheus ingress."
- **Same one-resource-one-owner discipline** — but mark clearly in the repo (comment/README) that these are inert on flannel so nobody assumes protection that isn't there.

### 3.5 NetworkPolicy gotchas

1. **flannel = no enforcement.** The single most important fact. Applying a policy gives false confidence. VERIFY the CNI (`kubectl -n kube-system get pods | grep -E 'flannel|calico|cilium'`) before trusting any policy.
2. **Default allow-all**, unlike RBAC. You must author an explicit default-deny to get a deny-by-default posture.
3. **Selecting a pod flips it to deny for that direction** — an allow policy without a matching default-deny still narrows, because the first selecting policy already denies everything not listed.
4. **Ingress and egress are independent** — a default-deny-ingress does nothing to egress; author both if you want full lockdown.
5. **`namespaceSelector` needs ns labels** — modern k8s auto-labels namespaces with `kubernetes.io/metadata.name`, which is the reliable selector.

---

## Git / GitHub workflow used (doc-only, still prod-style)

```
branch task09-access-authz
  -> add Task-9 runbook + authored (inert/illustrative) RBAC + NetPol manifests + StrongDM config sketch
  -> commit (WHY-body: "document-only closeout; StrongDM scoped-and-stopped, NetPol inert on flannel")
  -> push -u
  -> review own diff (git diff --stat main...HEAD)
  -> PR -> squash-merge -> delete branch -> prune
```
- **Nothing is `sync`'d/applied** — this is a documentation + authored-config commit, not a live change. Note that explicitly in the PR body (an honest IaC record: these manifests are authored, RBAC is applyable, NetPol is inert on flannel, StrongDM has no live org).
- RBAC manifests are genuinely applyable if you want to exercise `kubectl auth can-i`; NetPol + StrongDM are illustrative.

---

## Conceptual Q&A

**Q: Does StrongDM replace my kubeconfig auth or RBAC?**
No. It wraps around them. StrongDM brokers an audited, short-lived *session* and injects an identity; authN still verifies that identity; RBAC still limits what it can do. Five independent planes — StrongDM is the human-access/audit one, not authN or authZ.

**Q: Why can't I just install a StrongDM gateway inside the VLAN like the Datadog agent?**
Two reasons. The node listener is TCP 5000, not 443, and the VLAN blocks non-443 outbound TCP — so a relay can't dial a gateway on 5000. And a gateway must be reachable by clients; inside an egress-only VLAN it isn't. The fix is a gateway OUTSIDE the VLAN listening on 443 + a relay inside dialing out on 443 — more infra than a single egress-only agent.

**Q: Why is RBAC default-deny but NetworkPolicy default-allow?**
Different designs. RBAC grants are purely additive — no verb granted means denied, so you never write a deny. NetworkPolicy starts allow-all; the first policy selecting a pod flips that pod to deny-for-that-direction, so you must *author* a default-deny to lock down. Opposite defaults — a classic trip-up.

**Q: Why author NetworkPolicies that don't do anything?**
flannel doesn't enforce them, so they're inert on omni-cluster. But they're correct k8s, version-controlled intent, and enforce the moment a policy-capable CNI (Calico/Cilium) is in place — the bare-metal direction likely runs one. IaC honesty means labelling them clearly as inert-on-flannel so nobody assumes protection.

**Q: How is RBAC like Vault?**
Role ≈ Vault policy (an inert grant of capabilities on paths/resources); RoleBinding ≈ the Vault k8s-auth role (binds an identity to the grant). Same "grant does nothing until bound to a subject" shape, one for the k8s API, one for Vault secrets.

**Q: Why scope-and-stop StrongDM instead of finishing it?**
Same judgement as the Task-7 node-agent, at task level. The learning goal — where it sits, the broker/relay model, how it replaces static kubeconfig, why the VLAN reshapes it — is met by the architecture. A live build needs a cloud VM + a 14-day trial clock for an ephemeral result. Shipping the documented architecture is the higher-value SRE call.

---

## New glossary terms (appended to SRE-Terminology-Glossary.md)

StrongDM, PAM, control plane (SaaS), gateway, relay, node (SDM), brokered session, just-in-time access, audit log, RBAC, Role, ClusterRole, RoleBinding, ClusterRoleBinding, subject, verb/resource/apiGroup, `kubectl auth can-i`, NetworkPolicy, podSelector/namespaceSelector, default-deny vs default-allow, policy-capable CNI (Calico/Cilium).

---
