# Task 2: Rook-Ceph Distributed Storage

> **Goal:** Stand up Rook-managed Ceph on the Omni-managed `omni-cluster` as distributed block storage, entirely as git-managed infra. Prove it end-to-end (PVC binds) and verify self-heal on node failure.
>
> **Status:** ✅ Core complete. Healthy Ceph cluster (3 mon / 2 mgr / 3 osd, `HEALTH_OK`), replicated pool, StorageClass, test PVC bound, self-heal cycle confirmed.

---

## Environment Facts

| Thing | Value |
|---|---|
| Cluster | `omni-cluster` (Omni-managed), k8s 1.30.1 / Talos 1.7.4 |
| kubeconfig context | `omni-cluster` (Omni-name + cluster-name) |
| Workers | `worker01/02/03` → `192.0.2.145/115/102` |
| Ceph disk (added this task) | `/dev/sdb`, 50 GB thin, one per worker |
| OS disk (untouched) | `/dev/sda`, 50 GB (Talos-claimed) |
| Worker specs (after this task) | **4 vCPU** (bumped from 2), 8 GB RAM |
| Rook version | operator chart `v1.19.7` (min k8s for v1.19 = 1.30) |
| Ceph image | `quay.io/ceph/ceph:v19.2.4` (Squid; matches Rook v1.19.7 default) |
| Pool / StorageClass | `replicapool` (size 3, failureDomain host) / `rook-ceph-block` (default) |
| Repo | `example-user/homelab-k8s`, manifests in `clusters/omni-cluster/storage/` |
| vSphere CLI | `govc` (env-var config, `GOVC_INSECURE=1`) |

**Network personality note:** This task is fully internal — Ceph inter-node traffic is LAN TCP, image pulls are TCP/443. No outbound UDP / external DNS / NTP dependency, so the VLAN lockdown does **not** interfere (unlike the Omni/NTP work in Task 1).

**SAN caveat:** All three `sdb` disks live on the same SAN Appliance underneath, so replica-3 here tests Ceph's *software* self-heal logic, not true independent-disk fault tolerance. Mechanics are identical to bare metal; only disk independence differs. Real resilience payoff arrives on bare-metal local disks.

---

## Concept: what Ceph is (vs Longhorn)

- **Longhorn** thinks in whole volumes and copies them. Simple.
- **Ceph** is a distributed object store (**RADOS**) underneath; block (RBD), file (CephFS), and object (RGW) are interfaces layered on top. Data is chopped into objects, scattered across OSDs by **CRUSH** (a placement algorithm, no central lookup table), self-healing and rebalancing.
- For 3 nodes it's arguably overkill (Longhorn is the pragmatic small-cluster choice) — we use Ceph because it's the **production target** (replacing a proprietary storage appliance on the bare metal direction).

**The three daemon types, and why each count differs:**
- **mon (monitor)** — cluster map + quorum. Run **3 (odd)** → majority = 2 → survive losing 1. Runs as a StatefulSet (stable identity).
- **mgr (manager)** — dashboard/metrics/orchestration. Run **2 (active/standby)** → one works, one is warm spare. Not quorum; no odd-number rule.
- **osd (object storage daemon)** — owns bytes on **one disk each**. Needs a **raw** device. 3 disks → 3 OSDs.

---

## Part 1 — Add a raw disk per worker (govc)

**Why:** Talos reserves the *entire* OS disk for itself, and Ceph refuses any device with a partition table or filesystem. So the existing 50 GB `sda` is off-limits — each worker needs a second, empty disk (`sdb`) for its OSD. (RAM was correctly sized for Ceph; the disk was the missing piece.)

```bash
# govc env (keep OUT of git — secrets)
export GOVC_URL='https://vcenter.example.local'
export GOVC_USERNAME='...'; export GOVC_PASSWORD='...'
export GOVC_INSECURE=1
export GOVC_DATACENTER='...'
govc about   # confirm auth

# add a 50GB thin disk to each worker
for vm in worker01 worker02 worker03; do
  govc vm.disk.create -vm "$vm" -ds 'YOUR-DATASTORE' \
    -name "$vm/ceph-osd-1.vmdk" -size 50G -thick=false
done
```

**Gotcha — multiple datastores:** `govc: default datastore resolves to multiple instances` → must pass `-ds 'name'` (or set `GOVC_DATASTORE`). Find it with `govc datastore.info`.

**Sizing:** 50 GB thin. Raw total = 150 GB; usable ≈ 1/3 at replica-3 (~50 GB). Thin = only consumes what Ceph writes. Hard OSD floor is a few GB (BlueStore/RocksDB overhead eats the bottom ~2 GB+); practical lab floor ~20 GB; 50 GB gives room to fail nodes and watch recovery without hitting `nearfull`.

**Verify (Omni UI or):** each worker shows `/dev/sda` (Talos) + `/dev/sdb` (raw). Hot-add registered live, no reboot needed for the *disk*.

---

## Part 2 — Talos node prep via the Omni template (git-managed)

**Why:** Disks existing isn't enough. Talos is minimal/immutable — it must be told to load the Ceph kernel modules and provide a durable mount. This goes as a patch on the **`Workers`** document of the git-managed Omni cluster template (same `validate → diff → sync` as Task 1's "1f") — NOT hand-run `talosctl apply-config`.

Patch added to `kind: Workers`:
```yaml
patches:
  - name: rook-ceph-node-prep
    inline:
      machine:
        kernel:
          modules:
            - name: rbd      # Ceph block-device client (RBD)
            - name: ceph     # Ceph kernel client (CephFS)
        kubelet:
          extraMounts:
            - destination: /var/lib/rook
              type: bind
              source: /var/lib/rook
              options: [bind, rshared, rw]
        sysctls:
          net.core.rmem_max: "8388608"   # optional network tuning
          net.core.wmem_max: "8388608"
```

- `rbd` module → lets a node map a Ceph block volume as a local device (CSI needs it). Ships in Talos kernel; just needs loading.
- `/var/lib/rook` bind-mount w/ `rshared` → durable daemon state on Talos's writable `/var`, with mount propagation so CSI mounts flow correctly.

Apply:
```bash
omnictl cluster template validate -f omni-cluster-template.yaml
omnictl cluster template diff -f omni-cluster-template.yaml   # must show ONLY this patch
omnictl cluster template sync -f omni-cluster-template.yaml
omnictl cluster template status -f omni-cluster-template.yaml
```

**Verify modules loaded** (use the Omni-provided talosconfig):
```bash
omnictl talosconfig -c omni-cluster ./omni-talosconfig
talosctl --talosconfig=./omni-talosconfig -n <worker-ip> get modules   # expect: ceph, rbd
```

**Gotchas hit here:**
- **TLS/CA error** (`x509: certificate signed by unknown authority "talos"`) → wrong cluster's talosconfig. Pull the omni-cluster one with `omnictl talosconfig -c omni-cluster`. On Omni you go *through* Omni (SideroLink), not direct-to-node PKI from the hand-built cluster.
- **`PermissionDenied: not authorized`** on `talosctl ... read /proc/modules` → Omni scopes talosconfig to least privilege; raw file reads need `os:admin`. Use `talosctl get modules` (in-scope COSI resource) instead — cleaner, no escalation.
- `talosctl` warns `server 1.7.4 older than client 1.13.5` → harmless here, but the "pin talosctl to server version" lesson.

---

## Part 3 — Install the Rook operator (Helm)

**Version logic:** k8s 1.30.1 → Rook **v1.19** (its floor is 1.30; v1.20 needs 1.31). Ceph image must match Rook version.

```bash
# namespace FIRST, labeled privileged (Talos PodSecurity blocks Ceph's privileged pods otherwise)
kubectl create namespace rook-ceph
kubectl label namespace rook-ceph pod-security.kubernetes.io/enforce=privileged --overwrite

kubectl config current-context   # confirm omni-cluster

helm repo add rook-release https://charts.rook.io/release && helm repo update
helm install rook-ceph rook-release/rook-ceph \
  --namespace rook-ceph --version v1.19.7 \
  --kube-context omni-cluster
```

- `--kube-context` names the target explicitly (belt-and-suspenders with 2 clusters). Helm uses the *current context* by default — it does NOT prompt.
- Operator chart also pulls in the **ceph-csi-operator** as a dependency (`ceph-csi-controller-manager` pod).
- The operator is a **controller** — it just *watches* for a `CephCluster` and does nothing to disks until it gets one.

Confirm: `kubectl -n rook-ceph get pods` → `rook-ceph-operator` Running, `ceph-csi-controller-manager` Running. No mons/OSDs yet (correct).

---

## Part 4 — Install the CSI drivers chart (v1.19 separate chart)

**Why separate:** Rook v1.19 decomposed CSI. The operator chart installs the ceph-csi-*operator*; the actual CSI *driver* workloads come from a chart in Ceph's own repo. Without it, volume mounts later fail.

```bash
helm repo add ceph-csi-operator https://ceph.github.io/ceph-csi-operator && helm repo update
helm install ceph-csi-drivers --namespace rook-ceph \
  ceph-csi-operator/ceph-csi-drivers \
  -f https://raw.githubusercontent.com/rook/rook/release-1.19/deploy/charts/ceph-csi-drivers/values.yaml \
  --kube-context omni-cluster
```

- **The `-f values.yaml` is required** — drivers fail with chart defaults alone. Use the `release-1.19` branch values to match the operator.
- Result: `csi-rbdplugin`/`csi-cephfsplugin` — `ctrlplugin` (provisioner, Deployment) + `nodeplugin` (mounter, DaemonSet, one per node, uses the `rbd` module).
- IaC note: for reproducibility, save that values.yaml into the repo and install from the local file instead of the URL.

---

## Part 5 — CephCluster CR (the storage comes alive)

`clusters/omni-cluster/storage/cephcluster.yaml`:
```yaml
apiVersion: ceph.rook.io/v1
kind: CephCluster
metadata: {name: rook-ceph, namespace: rook-ceph}
spec:
  cephVersion: {image: quay.io/ceph/ceph:v19.2.4, allowUnsupported: false}
  dataDirHostPath: /var/lib/rook        # the Talos bind-mount from Part 2
  mon: {count: 3, allowMultiplePerNode: false}
  mgr: {count: 2, modules: [{name: rook, enabled: true}]}
  dashboard: {enabled: true, ssl: true}
  storage:
    useAllNodes: true
    useAllDevices: false                # protects sda
    devices: [{name: "sdb"}]            # only the raw disk → one OSD per worker
  resources:                            # trimmed for tight workers
    osd:  {requests: {cpu: "500m", memory: "2Gi"}, limits: {memory: "4Gi"}}
    mon:  {requests: {cpu: "250m", memory: "1Gi"}}
    mgr:  {requests: {cpu: "250m", memory: "512Mi"}}
```

**How it finds the disk:** the operator runs an `osd-prepare` **Job** per worker that inventories devices and matches your `devices: name: sdb`. Two gates: (1) you selected it, (2) it's raw. `sda` fails gate 2 always. NOTE: `sdb` naming isn't guaranteed stable across disk changes — production selects by `/dev/disk/by-id/...`.

Apply (validate first):
```bash
kubectl apply -f clusters/omni-cluster/storage/cephcluster.yaml --dry-run=client   # syntax check
kubectl apply -f clusters/omni-cluster/storage/cephcluster.yaml
kubectl -n rook-ceph get pods   # (use a `while` loop on macOS; no `watch` by default)
```

Expected cascade: `detect-version` → mon canaries → `mon-a/b/c` (quorum) → `mgr-a/b` → `osd-prepare-*` Jobs (`Completed`) → **`osd-0/1/2` Running**.

**Verify from inside Ceph** (the real trophy):
```bash
kubectl apply -f https://raw.githubusercontent.com/rook/rook/release-1.19/deploy/examples/toolbox.yaml
kubectl -n rook-ceph exec -it deploy/rook-ceph-tools -- ceph status
# want: HEALTH_OK; mon 3 quorum; mgr a(active),b; osd 3 up,3 in; pgs active+clean
```

---

## Part 6 — CephBlockPool + StorageClass (make it usable)

`clusters/omni-cluster/storage/blockpool-storageclass.yaml`:
```yaml
apiVersion: ceph.rook.io/v1
kind: CephBlockPool
metadata: {name: replicapool, namespace: rook-ceph}
spec:
  failureDomain: host       # spread 3 replicas across 3 hosts → survive 1 host loss
  replicated: {size: 3}
---
apiVersion: storage.k8s.io/v1
kind: StorageClass
metadata:
  name: rook-ceph-block
  annotations: {storageclass.kubernetes.io/is-default-class: "true"}
provisioner: rook-ceph.rbd.csi.ceph.com
parameters:
  clusterID: rook-ceph
  pool: replicapool
  imageFormat: "2"
  imageFeatures: layering
  csi.storage.k8s.io/provisioner-secret-name: rook-csi-rbd-provisioner
  csi.storage.k8s.io/provisioner-secret-namespace: rook-ceph
  csi.storage.k8s.io/controller-expand-secret-name: rook-csi-rbd-provisioner
  csi.storage.k8s.io/controller-expand-secret-namespace: rook-ceph
  csi.storage.k8s.io/node-stage-secret-name: rook-csi-rbd-node
  csi.storage.k8s.io/node-stage-secret-namespace: rook-ceph
  csi.storage.k8s.io/fstype: ext4
allowVolumeExpansion: true
reclaimPolicy: Delete
```

Apply + verify + prove end-to-end:
```bash
kubectl apply -f clusters/omni-cluster/storage/blockpool-storageclass.yaml
kubectl get storageclass                     # rook-ceph-block (default)
kubectl -n rook-ceph exec deploy/rook-ceph-tools -- ceph osd pool ls   # replicapool

# test PVC (throwaway, /tmp not repo)
kubectl apply -f - <<'EOF'
apiVersion: v1
kind: PersistentVolumeClaim
metadata: {name: ceph-test-pvc}
spec: {accessModes: [ReadWriteOnce], resources: {requests: {storage: 1Gi}}}
EOF
kubectl get pvc ceph-test-pvc   # Pending → Bound = pipeline works
kubectl delete pvc ceph-test-pvc
```

**Pipeline:** pod → PVC (request) → StorageClass (how) → CephBlockPool → OSDs.

---

## Part 7 — Self-heal test

```bash
# terminal 1 (macOS has no `watch`; use a loop):
while true; do clear; kubectl -n rook-ceph exec deploy/rook-ceph-tools -- ceph status; sleep 3; done
# terminal 2: simulate node failure (ungraceful, like real hardware death)
govc vm.power -off worker03
#   → HEALTH_WARN, osd "2 up, 3 in", mon quorum holds on 2/3, data still available
govc vm.power -on worker03
#   → OSD rejoins, PGs back to active+clean, HEALTH_OK
```
Default `mon_osd_down_out_interval` ≈ 10 min before Ceph re-replicates (so brief reboots don't trigger a full rebalance). Bring the node back inside that window to see clean re-integration without a long recovery. Review after the fact with `ceph log last 30`.

---

## KEY LESSONS / GOTCHAS (do these right next time)

1. **Ceph needs a separate RAW disk.** Talos claims the whole OS disk; Ceph refuses anything with a filesystem. Add `sdb`. RAM sizing ≠ disk sizing — two separate decisions.
2. **Namespace must be labeled `privileged` for PodSecurity** — Talos enforces `baseline` cluster-wide by default, which forbids Ceph's `hostPath` mons. **The label alone sometimes isn't honored immediately** — re-applying it + `kubectl -n rook-ceph rollout restart deploy/rook-ceph-operator` forced it to take. (Definitive fallback: exempt `rook-ceph` in the cluster-wide PSA config via the Omni template — full block, since Talos replaces lists wholesale.) Watch the *actual denial* in events: `warn`-level `restricted` messages are noise; the hard `enforce: baseline ... hostPath` FailedCreate is the real block.
3. **CNI bridge landmine after node re-registration:** stale `cni0` IP from Task 1's hostname re-register caused `FailedCreatePodSandBox: cni0 already has an IP different from ...`. Fix = **reboot the node** (destroys/recreates `cni0`). Deleting the stuck pod only moves it; it doesn't fix the node. Reboot all workers that never cycled since re-register.
4. **CPU changes on Talos need a full POWER-CYCLE, not a reboot — and Hot Add doesn't help.** Hot Add presents extra vCPUs as hot-plugged; Talos won't online them (no shell/udev). `talosctl reboot` restarts the OS but the VM hardware never stops → still sees old CPU count. `govc vm.power -off/-on` rebuilds the virtual hardware cold → CPUs present at boot → kubelet reports the new count. Verify with `kubectl get nodes -o custom-columns=NAME:.metadata.name,CPU:.status.capacity.cpu` (trust the *node's* report, not vCenter's).
5. **2 vCPU is too small for a Ceph node.** Scheduler wall was `Insufficient cpu`, not memory. Bumped workers to 4 vCPU. Ceph recovery/rebalance is CPU-heavy; leave headroom.
6. **Rook v1.19 needs the separate `ceph-csi-drivers` chart** (from `ceph.github.io/ceph-csi-operator`), installed with the version-matched `values.yaml`, or CSI mounts fail.
7. **Pin the Ceph image to the Rook version** (`v1.19.7` → `ceph:v19.2.4`). Mismatch = #1 install failure.
8. **On Omni, verify via `talosctl get modules`** (in-scope), not `read /proc/modules` (needs os:admin). Pull the right cluster's talosconfig (`-c omni-cluster`).
9. **macOS has no `watch`** — use `while true; do clear; <cmd>; sleep 3; done` or `brew install watch`.

---

## Conceptual Q&A

**Q: Onboard storage alone — is that resilient?**
No — scattered local disks aren't a platform by themselves. Ceph is the layer that pools them into one replicated store. With a SAN the *appliance* owns redundancy; with local disks the redundancy responsibility moves *up* into Ceph (3 copies across hosts). Same guarantee, different layer.

**Q: Why 2 mgr but 3 mon?**
mon = quorum (odd number, majority vote, all active) → 3 survives 1 loss. mgr = active/standby (one works, one spare, no voting) → 2 is standard. Different reason for each count: quorum drives mons, failover drives mgrs, disk count drives OSDs.

**Q: What is quorum?**
Minimum majority that must agree for a decision to be valid. Prevents split-brain (two halves disagreeing). 3 mons → majority 2 → lose 1 and still operate; the minority side refuses to act. Same mechanism as etcd. In the self-heal test, killing a node drops 1 mon but 2/3 hold quorum → cluster keeps running throughout.

**Q: How does the manifest know which disk to use?**
`storage.devices: name: sdb` selects it; the operator's `osd-prepare` Job inventories each node and matches. Safety: Ceph only claims RAW devices, so even a wrong selector can't eat the formatted `sda`.
