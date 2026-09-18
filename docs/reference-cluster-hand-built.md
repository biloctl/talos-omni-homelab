# Reference Cluster — Hand-Built Talos HA + Control-Plane VIP

> **What this is:** a standalone, hand-built Talos cluster (`talos-cluster`) put together with `talosctl` to learn Kubernetes HA fundamentals from the ground up, and kept around afterward as a throwaway testbed / reference. It is **not** part of the main platform build — the production-style stack (Omni, Ceph, GitOps, etc.) lives on a separate cluster and is documented under [`tasks/`](./tasks/). This doc stands on its own.
>
> **Goal:** take a single Talos node to a highly-available cluster — 3 control-plane + 3 workers — then make the *API endpoint itself* highly available with a floating control-plane VIP.
>
> **Status:** ✅ Complete. 3 CP + 3 workers with a 3-member etcd quorum; control-plane VIP live on `192.0.2.10`; cert SAN correct; failover proven by powering off the VIP owner and continuing to manage the cluster.
>
> **Scope note:** The node expansion itself was mechanically simple (clone/boot more Talos VMs, apply configs, watch etcd quorum form) and wasn't captured step-by-step — it's summarized here. The **VIP sub-step** is documented in full, because that's where the real concept and the fiddly cert detail live.

---

## Environment Facts

| Thing | Value |
|---|---|
| Cluster | `talos-cluster` — hand-built with `talosctl` |
| OS / orchestrator | Talos Linux 1.13.4 / Kubernetes |
| Control-plane nodes | `master01` `192.0.2.3`, `master02` `192.0.2.5`, `master03` `192.0.2.6` (static) |
| Workers | 3 × worker nodes |
| Control-plane VIP | `192.0.2.10` (added this task) |
| API endpoint | was `https://192.0.2.3:6443` (master01 only) → `https://192.0.2.10:6443` (VIP) |
| Tooling | `talosctl` (imperative apply-config); initial configs delivered via the vSphere **guestinfo** method |
| Network | segregated VLAN `192.0.2.0/24` |

---

## Core concept: node redundancy ≠ endpoint HA (what this is really about)

Expanding 1 → 3 masters gives you a **redundant control plane**: three API servers and a 3-member **etcd** quorum. But that's redundancy of the *nodes*. If every client is still pointed at `https://192.0.2.3:6443` (master01 alone), then three live masters behind one dead endpoint is **still a single point of failure** — master01 dies and you lose API access despite two healthy masters.

The **VIP** [virtual IP — a floating address bound to no single node] closes that gap by making the *endpoint* highly available.

```
BEFORE (expanded, but endpoint = SPOF)          AFTER (VIP floats to a live node)
  clients ─► .3 master01  ✗ dies                clients ─► .10  (floating)
            .5 master02   alive but unaddressed             ├─ .3 master01  ← owns .10 now
            .6 master03   alive but unaddressed             ├─ .5 master02
            → API access LOST                               └─ .6 master03
                                                   .3 dies → .5 grabs .10 within seconds → API stays up
```

**Mechanism:** the three control-plane nodes run a **leader election** through the etcd they already share; the winner "owns" `.10` and answers for it. If the owner dies, the survivors detect it and one claims `.10` within seconds. Clients point at `.10` and never care which physical master is behind it.

It's the same "floating address that lands on a live node" idea as **MetalLB**'s L2 mode (which this cluster also ran — see below, and Task 10 for the same pattern on `omni-cluster`) — except MetalLB is a separate [controller] you install for *LoadBalancer Services*, whereas the Talos control-plane VIP is a **built-in OS feature**, coordinated natively through etcd, with no extra workload to run.

---

## Part 1 — HA expansion (summarized)

1. Stand up two more control-plane VMs (`master02` `.5`, `master03` `.6`) and three worker VMs from the Talos image.
2. Apply the shared `controlplane.yaml` to the new masters and `worker.yaml` to the workers (initial delivery via guestinfo; static IPs set per node).
3. The new masters join etcd; quorum becomes **2 of 3** (survives losing one member).
4. `kubectl get nodes` shows 3 CP + 3 workers `Ready`.

At this point the *cluster* is HA, but the *endpoint* is not — hence Part 2.

---

## Part 2 — Add the VIP + cert SAN (the detailed part)

The fiddly part, and the reason this was staged separately: the API server's TLS certificate must list `.10` as a valid name (a **cert SAN** [Subject Alternative Name]), or clients hitting `.10` get a TLS error. So the VIP and the SAN go in **together**.

### Step 1 — Config additions

**(a) The VIP — lives inside the control-plane interface, which is PER-NODE**, so it goes in each master's patch (`master01/02/03-patch.yaml`):

```yaml
machine:
  network:
    interfaces:
      - deviceSelector:
          physical: true
        # ... existing address / routes / dhcp MUST stay present ...
        vip:
          ip: 192.0.2.10
```

**(b) The cert SANs — cluster-wide**, so they go in the shared base config:

```yaml
machine:
  certSANs:
    - 192.0.2.10
cluster:
  apiServer:
    certSANs:
      - 192.0.2.10
```

### Step 2 — Apply to the running masters

The masters are already up and reachable at their static IPs, so apply over the network (no guestinfo needed for an *update*, no `--insecure` since they have configs):

```bash
talosctl -n 192.0.2.3 apply-config --file controlplane.yaml --config-patch @master01-patch.yaml
talosctl -n 192.0.2.5 apply-config --file controlplane.yaml --config-patch @master02-patch.yaml
talosctl -n 192.0.2.6 apply-config --file controlplane.yaml --config-patch @master03-patch.yaml
```

Preview each merged config first (`talosctl machineconfig patch ... -o`) to confirm the `vip` and `certSANs` are both present before applying.

### Step 3 — Verify the VIP is live (THE GATE before re-pointing anything)

```bash
ping 192.0.2.10
talosctl -n 192.0.2.10 version   # talk to the cluster THROUGH the VIP
```

If `.10` answers and `talosctl` works against it, the VIP is up **and** the cert SAN is correct. Do not re-point clients until this passes.

### Step 4 — Re-point your clients to the VIP

```bash
talosctl config endpoint 192.0.2.10
kubectl config set-cluster talos-cluster --server=https://192.0.2.10:6443
kubectl get nodes   # still works, now flowing through the VIP
```

### Step 5 — Point the workers at the VIP too

For full HA, the workers' kubelets should reach the API via the VIP, not `.3`. Update each worker's `cluster.controlPlane.endpoint` to `https://192.0.2.10:6443` and apply. This is what makes **worker → API survive master01 dying**.

### Step 6 — Test the failover (the satisfying part)

Power off `master01` in vCenter, then run `kubectl get nodes` a few times. After a few seconds' blip (while another master claims the VIP), it keeps working — you're managing the cluster with master01 dead. Power it back on and it rejoins.

---

## Part 3 — What else ran on this cluster (summarized)

Before the Omni-managed platform existed, this cluster was where the basic add-on stack was
learned by hand with Helm, one piece at a time. None of it is git-managed in this repo (the
Omni-era tree under `clusters/omni-cluster/` is the real platform), but it's the reason the
later tasks could move fast:

| Layer | What ran here | Where it reappears |
|---|---|---|
| Load balancing | **MetalLB** (L2 mode) handing VLAN IPs to `LoadBalancer` Services | Task 10, fronting the Istio ingress gateway |
| Ingress | **ingress-nginx** routing by host/path | replaced by the Istio gateway on `omni-cluster` |
| Storage | **Longhorn** distributed block storage — required the `iscsi-tools` and `util-linux-tools` Talos system extensions, built into the image via the Image Factory | Rook-Ceph on `omni-cluster` (Task 2) |
| Observability | **kube-prometheus-stack** (Prometheus, Grafana, Alertmanager) | Task 5 onward |
| GitOps | first **Argo CD** install and `Application` objects | Task 5 onward |
| Scheduling | control-plane taint patch (masters don't run workloads), a topology-spread test deployment | default posture on `omni-cluster` |

Two things this cluster taught that carried straight over: a Talos machine-config **patch is a delta
against a pristine base** (never edit the generated `controlplane.yaml`/`worker.yaml` in place), and
Talos's default `enforce: baseline` / `warn: restricted` PodSecurity posture, which every later task
ran into in some form.

---

## KEY LESSONS / GOTCHAS

1. **Node redundancy is not endpoint HA.** Three masters behind one hard-coded endpoint is still a SPOF. The VIP is the piece that actually removes it.
2. **VIP and cert SAN are one change, not two.** Re-pointing a client to `.10` before `.10` is in the cert SAN gives a TLS error — and `ping` won't warn you, because ICMP has nothing to do with certificates (see Q&A). Add both together.
3. **Know WHERE each field lives:** the `vip` is inside the interface block = **per-node** (each master's patch); `certSANs` are **cluster-wide** (shared base). Putting the VIP in the base, or the SANs in one node's patch, silently misconfigures HA.
4. **List-replacement trap (Talos):** the `vip` sits inside the `interfaces` list, and Talos patches **replace lists wholesale** — the patched interface must carry the *full* block (`address`, `routes`, `dhcp`, **and** `vip`) or you drop whatever you omitted. Always preview the merged config.
5. **Go one master at a time and keep `.3` as a fallback** until `.10` is confirmed — if the SAN is wrong you don't want to lose every path to the API at once.

---

## Conceptual Q&A

**Q: If I re-pointed kubeconfig to `.10` *before* adding `.10` to the cert SAN, what breaks — and would `ping 192.0.2.10` catch it?**
The TLS handshake fails: `kubectl`/`talosctl` present `.10` as the server name, the API server's cert doesn't list `.10` as a SAN, so the client rejects it as an invalid cert (name mismatch). `ping` would **not** catch it — ICMP operates at L3 and knows nothing about TLS. A successful `ping` only proves some node is answering at `.10`; it says nothing about whether the cert is valid for that name. That's exactly why Step 3 uses `talosctl -n 192.0.2.10 version` (a real TLS call) as the gate, not `ping`.

**Q: How is the Talos control-plane VIP different from MetalLB?**
Same floating-IP idea (an address that lands on a live node), different scope and mechanism. MetalLB is a [controller] you install to hand out external IPs for **LoadBalancer Services** (workloads). The Talos VIP is a built-in feature for the **control-plane API endpoint only**, elected through the existing etcd quorum — no extra software.

**Q: Why does losing one master not take down the cluster?**
etcd needs a **quorum** (strict majority) to accept writes. 3 members → majority is 2 → survives losing 1. The VIP failover rides on the same health signal: the survivors that still form quorum are the ones eligible to claim `.10`.

**Q: Why build this by hand instead of with a management tool?**
To learn the fundamentals without a black box in the way. Placing the `vip`/`certSANs` yourself, adding a node to etcd by hand, and watching a VIP fail over builds the mental model you need to *read* and troubleshoot any higher-level tooling that automates the same steps later. It also makes a handy disposable testbed for trying risky changes without touching the real platform.
