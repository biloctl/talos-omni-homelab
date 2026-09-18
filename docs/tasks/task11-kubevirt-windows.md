# Task 11: KubeVirt + Windows Server 2022 (VMs as Kubernetes workloads)

> **Goal:** Run a legacy-style Windows Server VM *on* `omni-cluster` using KubeVirt — root disk on Rook-Ceph, ISO imported cluster-side by CDI, RDP exposed through MetalLB — and understand what does and doesn't carry over from a vSphere mental model. The thesis under test: KubeVirt wraps QEMU in a pod, so all of the Kubernetes machinery applies unchanged — the scheduler places VMs, PVCs are disks, Services expose them, RBAC gates the console.
>
> **Status:** ✅ Complete. KubeVirt **v1.9.0** + CDI on k8s 1.36.2 / Talos 1.13.6. Windows Server 2022 VM (`win2k22`) running, RDP-accessible via a MetalLB `LoadBalancer` Service, qemu-guest-agent connected (`virtctl guestosinfo`, graceful stop, IP shown in `kubectl get vmi`). Platform install took about half a day; the rest was infrastructure debt the install exposed (control-plane sizing, NTP, storage capacity), collected in the incident anthology below. Branch `kubevirt` → PR → squash-merge.

---

## Environment Facts

| Thing | Value |
|---|---|
| Cluster | `omni-cluster`, k8s 1.36.2 / Talos 1.13.6, 3 control-plane + 3 workers (VMs on vSphere) |
| KubeVirt / CDI | v1.9.0 / pinned CDI release; namespaces `kubevirt`, `cdi` — both labeled `enforce=privileged` |
| Nested virtualization | enabled per worker VM (`vhv.enable`) — `/dev/kvm` exists only with this on |
| Node sizes (after) | control plane 4c / 8 GB (was 2c / 4 GB); workers 6c / 12 GB (was 4c / 8 GB); Ceph OSD disks 100 GB (was 50 GB) |
| VM | `win2k22`: 2 cores / 3 Gi guest RAM, 40 Gi virtio root disk on `rook-ceph-block`, q35 machine type |
| ISO | imported by CDI `DataVolume` (`http` source) into an 8 Gi PVC — the cluster pulls it, nothing is uploaded from the workstation |
| Drivers | `quay.io/kubevirt/virtio-container-disk:v1.9.0` as a second CD-ROM |
| Networking | `masquerade` (pod-network NAT); RDP via MetalLB `LoadBalancer` Service on 3389 |
| Console | `virtctl vnc` (TigerVNC on macOS) |
| Files | `clusters/omni-cluster/kubevirt/kubevirt-cr.yaml`, `cdi-cr.yaml`, `win2k22-iso-dv.yaml`, `win2k22-vm.yaml`, `win2k22-rdp-svc.yaml` |

---

## Mental model

```
physical host (VT-x)
└─ ESXi                        hypervisor #1; must EXPOSE nested HV to each worker VM
   └─ Talos worker VM          /dev/kvm exists only if nested HV is on
      └─ virt-launcher POD     QEMU + libvirt; THE VM IS THIS POD (lives in the VM's namespace)
         └─ Windows guest      virtio disk/NIC; root disk = a Ceph PVC
```

- **`VirtualMachine`** (CRD) = desired state. **`VirtualMachineInstance`** (VMI) = the running instance. VM : VMI :: Deployment : Pod.
- **virt-controller** = reconciler. **virt-handler** = per-node DaemonSet agent. **virt-api** = an *aggregated API server* — control-plane heavy (see anthology #2).
- **CDI** (Containerized Data Importer) = imports ISOs and images into PVCs via `DataVolume`. Required by KubeVirt v1.9's template controller — install order matters.
- **virtctl** = kubectl-for-VMs: start / stop / restart / vnc / port-forward / guestosinfo.

**vSphere translation table** (this is the part that makes the whole thing click if you come from virtualization):

| vSphere | KubeVirt |
|---|---|
| VM object | `VirtualMachine` CR |
| shut down guest | `virtctl stop` (with guest agent) |
| pull the power cord | `kubectl delete pod virt-launcher-…` |
| VMware Tools | qemu-guest-agent |
| deploy from template | CDI clone (`source: pvc:`) |
| grow VMDK | `kubectl patch pvc` (SC `allowVolumeExpansion: true`) |
| bare VMRC | `virtctl vnc` |
| NAT network | `masquerade` |
| VM port group | bridge / Multus (not built here) |
| vMotion | live migration (needs RWX — not configured) |

---

## PART 1 — Pre-flight: nested HV, disk and RAM growth (per worker, one at a time)

```bash
kubectl get node <worker> -o wide                  # STEP 0: confirm the target. name<->IP mistakes happen at hour N.

kubectl -n vault get pods -o wide                  # if vault-0 is on this node: delete the pod first (PDB blocks drain)
kubectl drain <worker> --ignore-daemonsets --delete-emptydir-data --timeout=5m
talosctl -n <worker-ip> shutdown
govc vm.change      -vm <WorkerVM> -nested-hv-enabled=true
govc vm.disk.change -vm <WorkerVM> -disk.label "Hard disk 2" -size 100G
govc vm.change      -vm <WorkerVM> -c 6 -m 12288
govc vm.power -on   <WorkerVM>
kubectl get nodes -w && kubectl uncordon <worker>
kubectl -n rook-ceph exec deploy/rook-ceph-tools -- ceph status     # GATE: HEALTH_OK before the next node
kubectl -n vault exec -it vault-0 -- vault operator unseal           # if vault bounced
talosctl -n <worker-ip> ls /dev | grep kvm                           # verify: "kvm"
```

Gotchas:
- `govc` disk selectors: `-disk.label` wants the UI label ("Hard disk 2"), `-disk.name` wants `disk-1000-1`, `-disk.key` wants `2001`. `govc device.info -vm X disk-*` shows all three plus the vmdk filename — the unambiguous one.
- BlueStore auto-expanded into the grown disks on OSD restart (no rebuild). **CRUSH weight does not auto-update**: `ceph osd crush reweight osd.N <weight>` per grown OSD.
- **The Ceph gate is `HEALTH_OK`, not "node Ready."** Kubernetes and Ceph have independent definitions of recovered.
- Keep a per-node × per-change checklist. Under incident pressure two workers' RAM and one master were missed entirely; audit at the end: `kubectl describe nodes | grep -E "^Name:|memory.*%"`.

---

## PART 2 — Platform install

```bash
# KubeVirt operator + CR
kubectl apply -f https://github.com/kubevirt/kubevirt/releases/download/v1.9.0/kubevirt-operator.yaml
kubectl label ns kubevirt pod-security.kubernetes.io/enforce=privileged --overwrite
kubectl apply -f clusters/omni-cluster/kubevirt/kubevirt-cr.yaml
kubectl -n kubevirt get kubevirt                   # GATE: PHASE=Deployed

# CDI — pin the version, don't scrape "latest"
kubectl apply -f https://github.com/kubevirt/containerized-data-importer/releases/download/<CDI_VERSION>/cdi-operator.yaml
kubectl label ns cdi pod-security.kubernetes.io/enforce=privileged --overwrite
kubectl apply -f clusters/omni-cluster/kubevirt/cdi-cr.yaml     # scratchSpaceStorageClass: rook-ceph-block
```

Gotchas:
- **PSA:** `kubevirt` and `cdi` need `privileged` labels — `baseline` blocks virt-handler and the importers. Symptom is `FailedCreate` events, not logs (Task 10 lesson, again).
- **Install order:** KubeVirt v1.9's template controller crashloops with `no matches for kind "DataVolume"` until CDI's CRDs exist. `no matches for kind X` = missing CRD, always.
- **Control plane takes the hit.** An aggregated API server (virt-api) + webhooks + CRD/OpenAPI rebuilds + etcd writes, *and* virt-operator tolerates control-plane taints and can run on masters. Check control-plane headroom *before* installing aggregated-API operators (anthology #2).
- CDI components restart KubeVirt pods once (informer refresh); `uploadproxy` shows `CreateContainerConfigError` until `cdi-apiserver` generates its cert Secret. Self-resolves in ~5 minutes.
- `kubectl get kubevirt` needs `-n kubevirt` — it's a namespaced CR.

---

## PART 3 — The Windows VM

### 3.1 — ISO via CDI http import
`virtctl image-upload` tunnels through the API server at a few MB/s and depends on the workstation's uplink. Let the cluster pull instead. Resolve the vendor's redirect first (`curl -sIL "<link>" | grep -i location`), then:

`clusters/omni-cluster/kubevirt/win2k22-iso-dv.yaml`:
```yaml
apiVersion: cdi.kubevirt.io/v1beta1
kind: DataVolume
metadata: { name: win2k22-iso, namespace: default }
spec:
  storage:
    storageClassName: rook-ceph-block
    volumeMode: Filesystem        # REQUIRED on RBD with the default StorageProfile — see gotcha
    accessModes: [ReadWriteOnce]
    resources: { requests: { storage: 8Gi } }
  source:
    http: { url: "https://<vendor-download-url>/SERVER_EVAL_x64FRE_en-us.iso" }
```

**The big gotcha:** CDI's StorageProfile defaults RBD to `volumeMode: Block`; the non-root importer then dies with `blockdev: cannot open /dev/cdi-block-volume: Permission denied` — a crashloop showing `PROGRESS N/A`. Set `volumeMode: Filesystem` explicitly on *every* DataVolume (the ISO and the VM's blank root disk both hit it). Trade-off: `disk.img` on ext4 on RBD instead of raw block — fine for a POC; production fixes the StorageProfile.

Network probe pattern on a locked-down VLAN: run `curl` from a pod and expect *any* HTTP code (a `404` from a CDN root means reachable); `000` = connection failed. Remember only `demo-app` is Istio-injected, so `REGISTRY_ONLY` (Task 10) doesn't apply in other namespaces.

### 3.2 — The VM manifest (final form, decisions annotated)
`clusters/omni-cluster/kubevirt/win2k22-vm.yaml`:
```yaml
apiVersion: kubevirt.io/v1
kind: VirtualMachine
metadata: { name: win2k22, namespace: default }
spec:
  runStrategy: Halted
  dataVolumeTemplates:
    - metadata: { name: win2k22-root }
      spec:
        storage:
          storageClassName: rook-ceph-block
          volumeMode: Filesystem            # same RBD/CDI gotcha
          accessModes: [ReadWriteOnce]
          resources: { requests: { storage: 40Gi } }
        source: { blank: {} }
  template:
    metadata:
      labels: { kubevirt.io/vm: win2k22 }   # the Service's selector target
    spec:
      domain:
        cpu: { cores: 2 }
        memory: { guest: 3Gi }              # launcher requests guest + ~300Mi as ONE contiguous request on ONE node
        machine: { type: q35 }
        devices:
          disks:
            - { name: rootdisk,       bootOrder: 2, disk:  { bus: virtio } }   # fast; needs viostor at install
            - { name: windows-iso,    bootOrder: 1, cdrom: { bus: sata } }
            - { name: virtio-drivers,               cdrom: { bus: sata } }
          interfaces: [{ name: default, model: virtio, masquerade: {} }]
          inputs: [{ type: tablet, bus: usb }]   # sane VNC mouse
      networks: [{ name: default, pod: {} }]
      volumes:
        - { name: rootdisk,       dataVolume: { name: win2k22-root } }
        - { name: windows-iso,    persistentVolumeClaim: { claimName: win2k22-iso } }
        - { name: virtio-drivers, containerDisk: { image: quay.io/kubevirt/virtio-container-disk:v1.9.0 } }
```
```bash
kubectl apply -f clusters/omni-cluster/kubevirt/win2k22-vm.yaml
virtctl start win2k22
kubectl get vmi -w                 # Scheduling -> Running  (stuck in Scheduling = capacity; anthology #3)
```

### 3.3 — Console and install
- `virtctl vnc win2k22`. On macOS install TigerVNC (`brew install --cask tigervnc-viewer`); the built-in Screen Sharing chokes on no-auth VNC. VNC has no auth *by design* — access control is RBAC on the `virtualmachineinstances/vnc` subresource.
- macOS Ctrl-Alt-Del: grant TigerVNC Accessibility + Input Monitoring or every special key silently fails. Then F8 menu → Send Ctrl-Alt-Del. Lock-screen fallback: Ease of Access → On-Screen Keyboard.
- Setup shows **no disks** — virtio is invisible without the driver. Load driver → virtio CD → `amd64\2k22` → `viostor` → the disk appears.
- Post-install: run `virtio-win-guest-tools.exe` (the .exe; the MSI alone left qemu-guest-agent uninstalled). Expected: QEMU-GA Running/Automatic. SPICE vdagent stopped is fine — KubeVirt has no SPICE path.
- Time: point `w32tm` at the internal NTP server (`/manualpeerlist:<ntp> /syncfromflags:manual`); the VLAN blocks outbound UDP and Kerberos will care later.
- Licensing via DISM edition conversion (`/Set-Edition:ServerStandard /ProductKey:… /AcceptEula`), before sysprep and before any DC promotion (DISM refuses on DCs). The key never enters git.

### 3.4 — RDP via MetalLB
`clusters/omni-cluster/kubevirt/win2k22-rdp-svc.yaml`:
```yaml
apiVersion: v1
kind: Service
metadata: { name: win2k22-rdp, namespace: default }
spec:
  type: LoadBalancer
  selector: { kubevirt.io/vm: win2k22 }
  ports: [{ name: rdp, port: 3389, targetPort: 3389, protocol: TCP }]
```
Enable RDP in the guest, connect to the EXTERNAL-IP. One Service per VM (label selector).

---

## The networking model (masquerade) — and its limits

The guest NIC is *always* `10.0.2.2/24` (gateway `10.0.2.1`), DHCP'd by the virt-launcher itself. That address exists only inside the pod, is never on any wire, and is identical for every masquerade VM everywhere. Leave the guest on DHCP.

Chain: workstation → MetalLB IP (the only external identity) → Service → pod IP (real, ephemeral) → NAT → `10.0.2.2`.

Consequences:
- Reachable from outside **only** on Service-declared ports. **Services never forward ICMP — `ping` tests nothing.** New ports = new YAML lines.
- The guest lies about its own address, so anything that self-registers its IP (AD domain controllers first among them) is structurally incompatible with masquerade — DC↔DC replication is impossible under NAT.
- **Decision rule:** workload = published service → masquerade. Workload = network citizen (DC, DNS/DHCP, dynamic ports, server-initiated connections) → bridge via Multus. Node bridging is *the* standard pattern for VM-hosting clusters (Harvester ships it by default; OpenShift Virtualization does it via nmstate) — it only looks exotic from an app-only cluster.

---

## Day-2 quick reference

- **Grow a disk:** `kubectl patch pvc win2k22-root …` → Windows Disk Management → Extend. Remember ×3 raw in Ceph; update the manifest to match.
- **CPU/RAM:** edit spec → apply → `virtctl restart` (next-boot; hotplug is behind feature gates). Mind the boulder: the launcher needs guest + overhead as one contiguous request on one node.
- **More VMs:** the ISO PVC is RWO / node-locked — serialize installs or clone it. Better: a golden image — license → `sysprep /generalize /oobe /shutdown` → new VMs use `source: pvc:` (RBD copy-on-write clone, near-instant, drivers baked in).
- **Graceful stop:** with the agent connected, `virtctl stop` = guest-initiated shutdown. `kubectl delete pod virt-launcher-…` = power cord.
- **Ceph capacity:** thin-provisioned = overcommitted on paper. `nearfull` at 85% warns; `full` at 95% is a cluster-wide write freeze. Alert at 75% in Prometheus. Grow paths: regrow OSD disks / add a disk per worker (Rook auto-adopts) / add workers.

---

## Incident anthology (symptom → root cause → fix → lesson)

**#1 — "expired certificate" cascade (talosctl unusable)**
`remote error: tls: expired certificate` from every node — but the web cert was valid. Omni logs showed the Omni→node relay leg failing. Root cause: the Omni host had no working NTP (stock NTS pools unreachable on a UDP-blocked VLAN; the internal server was configured but rejected by `maxdistance` and outvoted by `prefer` flags) → clock 24 s off → freshly-minted short-lived client certs read as expired to the nodes. Fix: disable the stock pool file, one-shot `chronyd -q 'server <ntp> iburst'`, `maxdistance 16`, restart. Also found: Omni loads *copies* of its TLS cert at startup, so renewal ≠ reload — fresh copy + container restart + a deploy hook.
*Lessons:* cert errors are frequently clock errors. `remote error` = the other side aborted. Renewal automation without reload automation is a future outage. A reachable NTP source can still be unselectable. Talos/Omni out-of-band access is the only reason any of this was diagnosable.

**#2 — Control-plane OOM death spiral (mid-install)**
`kubectl` slow → dead. Talos dashboards: 2c/4 GB masters at 94–99% RAM, OOM-killer SIGKILL loops, kernel killed `kube-apiserver`, etcd health flapping; virt-operator resident *on* the masters. Triage: find the healthy etcd anchor, fix the sickest node first, one at a time, hard power-off is fine for an already-dead node, resize to 4c/8 GB, gate on `talosctl service etcd` healthy before the next. Never two masters down. No `kubectl` churn during the storm — retry storms feed themselves; hands off for 10 minutes is a strategy.
*Lessons:* aggregated-API installs tax the control plane. The kernel OOM log names the victim cgroup and its neighbors. QoS classes decide kill order (BestEffort first) — set requests on everything.

**#3 — Scheduling failures (three rounds)**
Read `FailedScheduling` as per-node bucket accounting: subtract the known constants (master taints, affinity pins); the remainder is your problem. An OSD pod stayed Pending after a drain-then-uncordon because the scheduler doesn't eagerly retry, and deleting it re-Pended because the evictees had rebounded and genuinely eaten the node — pods have no memory of where they lived; uncordon opens a door nobody must walk through. The VM Pended on all workers because launcher requests are atomic (3.3 Gi in one hole); fixed by finishing the resize. PDB gotcha: single-replica Vault + chart PDB = undrainable by eviction; PDBs guard the eviction API, not deletion → delete the pod, then drain. Production fix: HA + auto-unseal.

**#4 — CDI block-mode permission crash** — see 3.1.

**#5 — macOS console/Ctrl-Alt-Del wars** — see 3.3. Every symptom was one missing Accessibility grant.

---

## Conceptual Q&A

**Q: Where does a VM "run"?**
In its virt-launcher pod, in the VM's namespace. Services never contain workloads; they select pods by label — which is why `kubevirt.io/vm: win2k22` on the template is what makes RDP work.

**Q: Why did the masters melt but the workers didn't?**
The install is control-plane-shaped work (aggregated API, CRDs, webhooks, etcd writes). The VM is worker-shaped work.

**Q: Paravirtual vs. emulated?**
Paravirtual (virtio) is fast but the guest needs a driver — which bit at the install disk *and* the post-install NIC. Emulated is universally recognized but slow. Linux ships virtio in-kernel; Windows needs the CD. vSphere analog: pvscsi/vmxnet3 + VMware Tools.

**Q: Why is capacity planning different for VMs?**
Boulders, not sand. A guest's memory is one atomic, unswappable request on one node. Bin-packing dominates in a way it doesn't for microservices.

**Q: Homelab tax — what vanishes on bare metal?**
The nested-HV flag, vSphere port-group security toggles for bridging, the nesting cost of VBS/Credential Guard, and "independent" OSDs that are really carved from one array.

---

## Open items
1. Certbot deploy hook on the Omni host (copy fresh certs + restart the container) — renewal without reload is a fuse.
2. Verify all node sizes post-incident (`kubectl describe nodes | grep -E "Name:|memory"`); `ceph osd df` → CRUSH reweight all three OSDs.
3. Ceph capacity alert at 75% in Prometheus.
4. Move VMs from `default` to a `vms` namespace (PV retain-policy dance — VM deletion garbage-collects `dataVolumeTemplate` disks). Decide mesh policy for that namespace.
5. Convert KubeVirt/CDI manifests to Argo Applications.
6. `govc` credentials → gitignored env file with `chmod 600`, or Vault.
7. Phase 3 (optional; the POC is complete without it): port-group toggles → `br0` Omni patch (one node at a time; SideroLink survives as the recovery path) → Multus + NetworkAttachmentDefinition → sysprep golden image → 2× DC + member on static VLAN IPs → `repadmin /replsummary`. De-risk first: toggles + bridge on one worker + a Linux VMI, one hour, reversible.
