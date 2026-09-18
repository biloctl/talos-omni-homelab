# SRE Upskilling — Terminology Glossary

> Living reference. Add new terms at the end of every task. Review the whole thing (cover the definitions, recall from the term) at each task boundary — active recall beats re-reading.

## The one distinction to anchor everything

Almost everything running on a cluster is one of two things:

- **Workload** — software that *runs and does a job* (serves requests, processes data). A tenant of the cluster. Example: nginx.
- **Controller / operator** — software that *watches for a resource and reconciles reality to match it*. Infrastructure that extends the cluster. Example: Rook, MetalLB, Argo CD.

Ask of anything: "Does it run and do work, or does it watch-and-build?" That single question sorts most confusion.

---

## Kubernetes — objects (the nouns)

- **Resource / object** — anything the cluster stores that has a `kind:` in YAML. The API's nouns.
- **Pod** — the smallest unit; one or more containers that run together. You rarely make these directly.
- **Deployment** — [workload] runs N identical, interchangeable copies of a stateless app; handles rolling updates + self-healing. The workhorse.
- **StatefulSet** — [workload] like a Deployment but each instance has a stable identity and its own storage. For databases, and Ceph mons.
- **DaemonSet** — [workload] runs exactly one copy on every (matching) node. For per-node agents: CSI drivers, log/metrics collectors.
- **Job / CronJob** — [workload] run-to-completion task. Job runs once and finishes; CronJob runs on a schedule. (Rook's "OSD prepare" is a Job.)
- **Service** — a stable network address + load-balancing in front of a set of Pods. Types include ClusterIP, NodePort, LoadBalancer.
- **Namespace** — a logical partition of the cluster for grouping/isolating resources (e.g. `rook-ceph`).
- **PersistentVolumeClaim (PVC)** — a pod's *request* for storage ("give me 10Gi"). The claim gets fulfilled by a volume.
- **PersistentVolume (PV)** — the *actual* storage that fulfills a PVC. Usually created automatically by the provisioner.
- **StorageClass** — defines *how* a PVC gets fulfilled (which provisioner/backend). Ceph gives you one.
- **CRD (Custom Resource Definition)** — how an operator adds a *new noun* to the cluster's vocabulary. Installing Rook gives you the `CephCluster` kind, which didn't exist before.

## Kubernetes — controllers & concepts

- **Controller** — [controller] runs a loop: watch a resource → make reality match it. The core k8s pattern.
- **Operator** — a controller that manages a complex app using CRDs (Rook operator manages Ceph). "Operator" ≈ "controller with domain knowledge."
- **Reconcile / reconciliation loop** — the watch-and-correct loop a controller runs continuously.
- **Declarative vs imperative** — declarative = "declare desired state, let a controller reach it" (Argo, Terraform, CephCluster CR). Imperative = "run these steps yourself, now" (govc, kubectl commands by hand).
- **Desired state** — the target you declare; the controller's job is to make actual state equal it.
- **CSI (Container Storage Interface)** — the standard plug that lets Kubernetes talk to any storage backend. Ceph ships a CSI driver so PVCs can be backed by Ceph.
- **Ingress / Ingress controller** — routes outside HTTP traffic to services inside the cluster by hostname/path. nginx is the most common controller.
- **PodSecurity (PSA)** — admission rules controlling how privileged a pod may be (privileged / baseline / restricted). Talos enforces `baseline` cluster-wide by default; Ceph's privileged pods need the namespace labeled `privileged`.
- **Scheduler** — the built-in k8s component that decides *which node* a new pod runs on. NOT the same as a controller (which reconciles state) — the scheduler only does placement.
- **Add-on / component** — [loose term] informal "a piece of software installed on the cluster." Not precise — figure out from context if it means core k8s machinery or just "a thing we added."

## Linux / Talos

- **Kernel** — the innermost part of the OS that talks to hardware and manages memory/CPU/disks/network. Apps ask it to do low-level things.
- **Kernel module** — a piece of kernel code loaded/unloaded on demand instead of built in permanently. (Workshop analogy: a tool in a cabinet vs. welded to the bench.)
- **Driver** — code that teaches the OS to talk to a specific device/protocol. Most modules are drivers, but: a driver can be built-in (not a module), and a module can be a non-driver feature (e.g. a filesystem). "Module" = packaging; "driver" = role.
- **sysctl** — a tunable kernel parameter set at runtime (e.g. network buffer sizes).
- **Block device** — storage the OS sees as a raw sequence of blocks (a disk). E.g. `/dev/sda`, `/dev/sdb`.
- **Raw device** — a block device with no partition table and no filesystem — blank. What Ceph requires for an OSD.
- **Filesystem** — the structure (ext4, XFS) that organizes files on a block device. A device *with* one is "claimed"; Ceph won't touch it.
- **Partition** — a subdivision of a disk. Talos partitions its OS disk (EFI/META/STATE/EPHEMERAL).
- **Mount / mount point** — attaching a filesystem into the directory tree at a path.
- **Bind mount** — making an existing directory also appear at another path. Ceph uses one at `/var/lib/rook`.
- **Mount propagation (rshared)** — controls whether mount events created inside a container flow back out to the host/other pods. Ceph's CSI needs `rshared`.
- **/dev/sdX naming** — Linux names disks in discovery order (sda, sdb, ...). Usually stable, not guaranteed — production selects disks by stable ID (`/dev/disk/by-id/...`).
- **Reboot vs power-cycle** — a reboot restarts the guest OS but the VM's virtual hardware keeps running; a power-cycle (off then on) rebuilds the virtual hardware cold. CPU/RAM changes on Talos need a power-cycle, not a reboot.
- **CPU Hot Add** — vSphere feature to add vCPUs to a running VM. Talos won't bring hot-plugged CPUs online (no shell/udev), so it doesn't help — you still power-cycle.

## Storage — Ceph / Rook

- **Rook** — [operator] the Kubernetes operator that deploys and manages Ceph.
- **Ceph** — a distributed storage system providing block, file, and object storage from one platform.
- **RADOS** — the object store underneath all of Ceph. Everything else is a layer on top.
- **OSD (Object Storage Daemon)** — one per disk; owns the actual bytes. Needs a raw device.
- **mon (monitor)** — keeps the cluster map and quorum. Run an odd number (3) for fault tolerance. Runs as a StatefulSet.
- **mgr (manager)** — metrics, dashboard, orchestration. Run 2 (active/standby).
- **CRUSH** — the algorithm that decides where data objects are placed across OSDs. No central lookup table.
- **RBD (RADOS Block Device)** — Ceph's block-storage interface; needs the `rbd` kernel module to map a volume as a local disk.
- **CephFS** — Ceph's shared-filesystem interface (needs the `ceph` kernel module).
- **BlueStore** — the on-disk format an OSD writes directly to a raw device (no intermediate filesystem).
- **Placement Group (PG)** — a bucket of objects; the unit CRUSH places and Ceph balances. Tuning knob.
- **Failure domain** — the boundary across which Ceph spreads replicas (`host` = one copy per node, survives a node loss).
- **Replica (size 3)** — number of copies kept. Usable capacity ≈ raw / replica count.

## This lab's infra

- **Talos Linux** — minimal, immutable, API-driven OS for running Kubernetes. No shell, no package manager.
- **Omni** — [control plane] Sidero's management plane for Talos; declares clusters as templates and reconciles them.
- **omnictl / talosctl** — CLIs for Omni and for Talos nodes respectively. Keep versions matched to the server.
- **SideroLink** — the internal WireGuard tunnel Omni uses to reach nodes (keeps traffic on the LAN).
- **PXE / CIMC** — network-boot + Cisco out-of-band management; the bare-metal replacement for "clone a VM template."
- **MetalLB** — [controller] hands out external IPs for LoadBalancer services on bare metal.
- **govc** — CLI for VMware vSphere (part of govmomi). Same vSphere API the web UI uses; scriptable. No server-version pinning (stable API), so brew-latest is fine.

---

## Task 2 additions (Rook-Ceph)

- **CephBlockPool** — [CRD] a logical pool inside Ceph with a replication policy (`size: 3`, `failureDomain: host`). Where replica-3 actually takes effect. A StorageClass points at it.
- **Quorum** — the minimum majority of members that must agree for a decision to be valid; prevents split-brain. Run odd numbers (3 -> majority 2 -> survive 1 loss). Used by Ceph mons and by etcd.
- **Active/standby** — redundancy pattern where one instance works and another is a warm spare (Ceph mgr). Distinct from quorum (no voting), so no odd-number requirement.
- **flannel** — [controller] a CNI plugin providing pod networking; creates a per-node `cni0` bridge. A stale `cni0` IP (after node re-registration) blocks new pods with `FailedCreatePodSandBox`; fix by rebooting the node. **Does NOT enforce NetworkPolicies** (see Task 9; Task 10's Istio mesh delivers enforced traffic policy at L7 instead, and istio-cni chains after flannel rather than replacing it).
- **CNI (Container Network Interface)** — the plugin standard that gives pods their network (flannel here). Separate from CSI (storage).
- **ceph-csi-operator** — [controller] manages the lifecycle of the Ceph CSI drivers; pulled in as a dependency of the Rook operator chart in v1.19.
- **ceph-csi-drivers** — the separate Helm chart (from Ceph's repo) that deploys the actual CSI driver workloads in Rook v1.19+. Required, with a version-matched values.yaml.
- **ctrlplugin / nodeplugin** — the two halves of a CSI driver. ctrlplugin = provisioner (creates/deletes volumes, runs as a Deployment). nodeplugin = per-node mounter (attaches volumes to pods, runs as a DaemonSet, uses the `rbd` module).
- **osd-prepare** — [Job] short-lived per-node Job the Rook operator runs to inventory a disk, confirm it's raw, and format it into an OSD.
- **HEALTH_OK / HEALTH_WARN** — Ceph cluster health states. `HEALTH_WARN` with "degraded"/"undersized" means fewer copies than desired (not data loss) — expected during a node failure.
- **active+clean** — the healthy PG state: data placed and fully replicated. What you want to see after recovery.

---

## Task 3 additions (Talos/k8s upgrade via Omni)

- **Version interlock (Talos <-> k8s)** — each Talos minor caps the max Kubernetes minor it will run (1.7->1.30, 1.8->1.31, ... 1.13->1.36). The OS is the floor that decides how high k8s can climb. Visible in `omnictl get talosversion`.
- **Version skew** — how far apart two components' versions may drift before they refuse to talk. Applies to Talos↔k8s, and to `omnictl`/`talosctl` client vs Omni/Talos server (re-pin the client to clear a skew warning).
- **A/B partition scheme** — Talos keeps two OS slots; an upgrade installs to the inactive one and switches over, so a failed/cancelled upgrade reverts to the previous version if the node is still responsive. The built-in rollback.
- **`--preserve` (Talos upgrade)** — keeps ephemeral/user data across the OS image swap. Omni upgrades with preserve on, so OSD disks and `/var/lib/rook` survive node reboots.
- **cordon** — mark a node unschedulable (no new pods land) without evicting what's already there.
- **drain** — evict/reschedule a node's pods (after cordoning) so it can be safely rebooted/upgraded. Omni cordons+drains each node during a Talos roll.
- **Bootstrap manifests** — the in-cluster system manifests Talos lays down (CoreDNS, kube-proxy, flannel, kubeconfig-in-cluster, RBAC). **Talos/Omni never auto-update or delete them** — you sync them yourself after a k8s upgrade so it can't clobber manual edits.
- **`manifest-sync`** — `omnictl cluster kubernetes manifest-sync <cluster>` reconciles bootstrap manifests to the running k8s version. `--dry-run` defaults **true** (preview); pass `--dry-run=false` (flag BEFORE the cluster name) to apply. Empty diff blocks = already in sync.
- **(irreversible) migration** — a one-way transformation of stored data to a new schema (e.g. Omni v1.4 BoltDB/file-logs -> SQLite). Can't be undone; why downgrades are blocked and why a pre-migration backup is mandatory.
- **`--sqlite-storage-path`** — required Omni flag from v1.4; points at a **file** (not a directory), on a persistent mount. Stateless config — must be declared at every start.
- **EULA gate** — from Omni v1.7, self-hosted instances must accept the EULA before UI/CLI work (via `--eula-accept-name/-email` flags or the `/eula` page). **Stateful** — persisted once accepted, so the flag becomes a no-op afterward.
- **Health-check jobs (Omni v1.9+)** — cluster-template jobs that gate Talos upgrades; if a check fails Omni drops the upgrade quota to zero until it passes. A safety net (e.g. gate on Ceph health between node reboots).

## Git / GitHub terms

- **commit** — record a change in the *local* repo. A snapshot with a message (message should explain WHY, not just what).
- **push** — send local commits to the *remote* (GitHub). The remote is the durable source of truth for IaC — un-pushed commits live only on your machine.
- **branch** — a movable pointer to a line of commits; used to isolate a unit of work (e.g. `task03-talos-k8s-upgrade`) from `main`.
- **merge** — [git operation] integrate one branch into another, preserving both histories and adding a merge commit.
- **rebase** — [git operation] replay a branch's commits linearly on top of another, rewriting their hashes for a flat history. **Never rebase commits already pushed/shared.** Alternative *shape* to merge, not a replacement for review.
- **pull request (PR)** — [GitHub process, NOT a git command] a request to merge a branch, with a diff to review, a permanent record, and a hook for CI. Results in a merge/squash/rebase. In GitOps, merging to `main` is what triggers the deploy (Argo); here `sync` already deployed, so the PR is the record.
- **`sync` vs `apply`** — `omnictl cluster template sync` reconciles the WHOLE template to the cluster (adds/changes AND removes un-declared things); `kubectl apply` is per-object and never deletes un-referenced resources. No `omnictl cluster template apply` exists.
- **`diff` (before sync)** — preview of what a `sync` would change; the git-status-before-commit seatbelt. `sync` will delete things you removed from the template, so always read the diff first.
- **origin** — the default name for the remote repo (`git push origin ...`, `remotes/origin/main`).
- **upstream** — the remote branch a local branch tracks (`git push -u origin <branch>` sets it, so later just `git push`).
- **`--prune`** — `git fetch --prune` drops local remote-tracking refs for branches deleted on the remote (otherwise stale `remotes/origin/<deleted>` lingers).
- **GitHub Actions** — GitHub's CI/CD system: YAML **workflows** in `.github/workflows/` run on repo events (PR opened, merge to `main`, schedule). An **action** (lowercase) is one reusable step (e.g. `actions/checkout`). This is Roadmap Task 6.
- **`git lg` (alias)** — `log --oneline --graph --all --decorate`; the built-in visual commit/branch/merge graph in the terminal.

---

## Task 4 additions (Terraform / IaC)

- **Terraform** — [IaC tool] a client-side "read config -> call an API -> make reality match" engine. Reconciles **once, on demand** at `apply`, then exits. NOT a controller (which loops continuously). BSL-licensed; OpenTofu is the CLI-compatible OSS fork.
- **provider** — [Terraform plugin] the **adapter** that teaches Terraform to talk to one platform's API (cloudflare, vsphere, auth0, vault). All platform knowledge lives in the provider; core Terraform knows nothing on its own. The platform (Cloudflare) is the *target*; the `cloudflare` provider is the adapter. Because the workflow is identical across providers, you learn the tool, not the target.
- **resource** — a single infra object Terraform manages (e.g. `cloudflare_dns_record.omni`). The nouns of HCL.
- **data source** — [read-only] a lookup with no lifecycle; reads existing infra without managing it.
- **HCL** — HashiCorp Configuration Language; the declarative text Terraform reads as desired state.
- **state / state file** (`terraform.tfstate`) — Terraform's client-side ledger of what it manages + real-world IDs, so it can compute diffs. **Contains secrets/IPs in plaintext -> NEVER commit** (git-ignore it; treat like `talosconfig`/`*.asc`). The third thing that must agree with desired (HCL) and actual (real infra). New vs the Omni-template model, which has no client-side ledger (Omni's server holds truth).
- **backend** — where state lives. Default = **local** (file on disk). Prod pattern = remote (S3 / Azure blob / TF Cloud) with locking.
- **plan** — [read-only] preview of what an `apply` would change; refresh + compare, never writes state. The `omnictl cluster template diff` analog.
- **apply** — the ONLY command that writes state / changes real infra. The `omnictl cluster template sync` analog.
- **import** — adopt an existing (brownfield) resource into state so Terraform manages it *without recreating it*. Config-driven `import {}` block (previewed in `plan`) is the modern form; import ID for a Cloudflare record is `zone_id/record_id`. Applying with empty state against an already-existing resource instead tries to CREATE it (duplicate/error) — which is why import exists. Brownfield adoption is the highest-value real-world Terraform skill.
- **plan-until-clean** — after import, iterate HCL until `plan` reports "No changes" — desired (HCL) + actual (real infra) + state all agree. The trophy state.
- **`.terraform.lock.hcl`** — dependency lock file pinning provider version + hashes (like `go.sum` / `package-lock.json`). **COMMIT it** (common mistake to ignore it).
- **Omni Infrastructure Provider** — [Omni-native, NOT Terraform] a stateless connector (bare-metal, Proxmox, vSphere, KubeVirt...) by which Omni itself owns the substrate/machine layer. The production direction (bare-metal infra provider + PXE/CIMC). Contrast with the Terraform vSphere provider doing the same job — "no Terraform for the OS."
- **one resource, one owner** — the golden rule: never let two reconcilers (e.g. a community Omni TF provider AND the omnictl template) manage the same object, or they fight over "drift." Terraform owns substrate/SaaS layers; the Omni template owns the cluster.

## Git / GitHub terms (Task 4 additions)

- **`-u` / `--upstream` (push)** — `git push -u origin <branch>` links a local branch to its remote on first push, so later a bare `git push` works. Pushing a new commit to a branch with an open PR auto-adds it to that PR (a PR is a live view of the branch, not a snapshot).
- **squash merge** — [GitHub merge method] collapse a PR's commits into a single commit on `main`; the PR page keeps the individual-commit detail. Tidiest linear history for a small feature branch. (vs merge-commit = keeps commits + visible fork/join in `git lg`; vs rebase = linear, no merge commit.)

---

## Task 5 additions (Autoscaling — HPA / KEDA / Prometheus / Argo CD)

### Autoscaling
- **HPA (HorizontalPodAutoscaler)** — [controller] built-in k8s autoscaler; reads a metric, compares to a target, sets replica count. `desiredReplicas = ceil(currentReplicas × currentUtilization/targetUtilization)`. Can ONLY read the k8s metrics APIs.
- **metrics-server** — [workload] serves the resource metrics API (`metrics.k8s.io`) — live CPU/mem only, no history. The prerequisite for native HPA and `kubectl top`. NOT in Omni/Talos bootstrap manifests; install separately. On Talos needs `--kubelet-insecure-tls` (self-signed kubelet certs).
- **resource metrics vs custom/external metrics** — three HPA-readable APIs: `metrics.k8s.io` (CPU/mem, from metrics-server), `custom.metrics.k8s.io` + `external.metrics.k8s.io` (from an adapter you install, e.g. KEDA).
- **requests as the autoscaling denominator** — native HPA measures CPU as a % of the pod's `requests` (not limits, not node capacity). Right-sizing requests = calibrating the autoscaler.
- **scale-down stabilization window** — HPA default ~5 min wait before removing replicas (anti-flap). Scale-up is fast; scale-down is cautious.
- **KEDA** — [controller + metrics adapter] extends HPA to scale on event/app metrics (60+ scalers) and to scale-to-zero. Two parts: `keda-operator` (watches ScaledObjects, CREATES an HPA per object) + `keda-operator-metrics-apiserver` (serves `external.metrics.k8s.io`, answers the HPA's queries). **KEDA drives HPA, doesn't replace it.**
- **ScaledObject** — [CRD] KEDA's instruction sheet: scale THIS Deployment on THIS trigger between min/max. Committed to git (desired state). The HPA KEDA generates from it is NOT committed (KEDA owns it).
- **scaler / trigger** — [KEDA] the metric source in a ScaledObject (prometheus, kafka, cron, datadog…). A `prometheus` trigger queries a PromQL expression against a Prometheus server address.
- **scale-to-zero / activation** — KEDA can scale to 0 replicas (raw HPA floors at 1); the operator handles the 0↔1 "activation" phase itself, outside the HPA.

### Metrics / observability
- **Prometheus** — [workload] pull-based metrics collector + time-series DB (TSDB); scrapes `/metrics` endpoints on an interval and stores them. The flight-recorder to metrics-server's speedometer.
- **Grafana** — [workload] dashboards; queries Prometheus, collects nothing itself.
- **kube-prometheus-stack** — the Helm chart bundling Prometheus Operator + Prometheus + Grafana + Alertmanager + node-exporter + kube-state-metrics. "Scoped" here = Grafana on, Alertmanager off.
- **Prometheus Operator** — [controller] turns ServiceMonitor/PodMonitor CRDs into live scrape config.
- **ServiceMonitor / PodMonitor** — [CRD] declarative "scrape this Service/Pod." Discovered by the operator. `serviceMonitorSelectorNilUsesHelmValues: false` makes Prometheus scrape ALL of them (else only chart-owned ones).
- **node-exporter** — [workload, DaemonSet] per-node host metrics. Needs hostPath/hostNetwork → requires the `privileged` PSA label on its namespace on Talos. Pod count = NODE count.
- **kube-state-metrics** — [workload] exposes k8s object state (deployments, pods…) as metrics.
- **counter (metric type)** — a monotonically increasing value (e.g. `http_requests_total`). Only appears AFTER the first event; `rate()` over it gives per-second rate. An idle app legitimately has no counter series yet.

### Delivery / GitOps
- **Helm** — [tool] k8s package manager + templating engine. Renders a chart + values.yaml into manifests. One-shot/imperative (`helm install`), like `terraform apply`. NOT a controller.
- **chart / values.yaml** — the installable package / the knobs. Pin the chart version.
- **Argo CD** — [controller] GitOps continuous-delivery tool; watches git and reconciles the cluster to match. Uses Helm internally to render charts. Continuous reconcile loop (unlike Helm's one-shot).
- **Application** — [Argo CRD] "keep this source (git path / Helm chart) synced to this destination (cluster/namespace)." Lives in the `argocd` namespace.
- **sync / self-heal / prune** — [Argo syncPolicy] sync = apply desired state; selfHeal = revert live drift back to git; prune = delete cluster objects removed from git. `automated: {selfHeal, prune}` = full GitOps.
- **bootstrap paradox** — the GitOps tool (and its git-read credential) can't be installed FROM git — nothing's running yet to read git. So Argo + its repo Secret are introduced imperatively once; everything after is declarative.
- **repository Secret (Argo)** — a k8s Secret labeled `argocd.argoproj.io/secret-type: repository` holding a git URL + credential; the label is how Argo auto-detects it. `argocd repo add` just creates this under the hood.
- **deploy key** — a repo-scoped SSH key (GitHub). Read-only, single-repo = least privilege for Argo (it only reads). Not a personal key or broad PAT. (NOTE: from Task 6, the SSH deploy key only works from OFF-VLAN; the cluster itself must use HTTPS — see Task 6 additions.)
- **app-of-apps** — [Argo pattern] one Application that points at a git dir of other Applications, so even the first Application is git-managed (removes the last manual `kubectl apply`). Noted, not implemented in Task 5.

### PodSecurity / diagnostics (reinforced)
- **admission (rejection point)** — PSA rejects a non-compliant pod at CREATION time (API server), before scheduling/pulling. Symptom: workload object exists, 0 pods, `FailedCreate` event citing `violates PodSecurity`. Read events, not logs.
- **PSA levels in practice** — `kubectl run` defaults to `restricted` enforcement (blocks root busybox); an unprivileged app runs fine under `baseline`; node-exporter needs `privileged`. Different workloads, different privilege needs — the label is a response to a need, not a reflex.
- **ImagePullBackOff root causes** — `pull access denied / does not exist` = wrong image name/tag or missing creds (REFERENCE problem, fix the string). `i/o timeout / connection refused` = can't reach the registry (NETWORK problem). Always read the event message.

## Git / GitHub terms (Task 5 additions)
- **one commit per step, one PR per task** — the established rhythm (Tasks 3-5): readable per-step history on the branch, a single PR at task end as the record.
- **committed-vs-throwaway rule** — durable desired-state (want it reconciled) → repo + git flow. Proving/probing then tearing down → inline `kubectl apply` / `/tmp`, never committed. Never git-manage what another controller owns (e.g. the KEDA-generated HPA).

---

## Task 6 additions (GitHub Actions CI/CD)

### CI/CD
- **CI (Continuous Integration)** — source → tested, published artifact. Ends when a new immutable image exists in the registry. Never touches the cluster.
- **CD (Continuous Delivery)** — declared desired state → running reality. Reconciles the cluster to match git. Knows nothing about how the image was built.
- **GitHub Actions** — [CI platform] GitHub's event-driven automation. Runs workflows on repo events.
- **workflow** — [config] a YAML file in `.github/workflows/` (repo root ONLY — GitHub reads nowhere else) describing what runs on which events. Made of jobs. *Builds from* a subdirectory via `context:`, but doesn't *live* there.
- **job / step** — a job runs on one runner; steps are the ordered commands/actions inside it. `needs:` orders jobs (e.g. update-manifest `needs:` build-and-push).
- **runner** — [one-shot executor] the machine that runs a job. GitHub-hosted = ephemeral VM: spins up, runs, is destroyed. Same category as `helm install`/`terraform apply` — after it finishes, nothing is watching. Has Docker built in (no local Docker needed).
- **action** (lowercase) — [reusable step] a packaged step referenced via `uses:` (e.g. `actions/checkout@v4`, `docker/build-push-action@v6`), version-pinned.
- **artifact** — a produced thing (a built image), inert until something runs it. Neither workload nor controller.
- **push vs pull deploy** — push = CI runs `kubectl apply` at the cluster (CI holds long-lived cluster creds; drift possible). pull = CI updates git, a controller inside the cluster pulls (cluster holds its own creds, git = source of truth, drift self-healed). GitOps = pull. Cost: async deploy (CI "succeeds" on commit; app live only after the controller syncs — check the controller, not the CI log).
- **the handoff / manifest bump** — CI's last act: rewrite the image tag in the k8s manifest and commit it. The seam where CI writes git and CD reads git. CI and CD never talk directly.
- **`GITHUB_TOKEN`** — a per-run, auto-provisioned, auto-expiring, repo-scoped credential Actions mints each run. No stored secret to create/rotate. Scope set via `permissions:` (least privilege — bump `contents: write` only for the commit-back job).

### Images / registry
- **container image** — [artifact] a frozen, read-only, layered bundle containing a compiled program + everything it needs to run. A sealed, labeled shipping container: pack once, copy anywhere, identical contents every time. Inert until run. The output of `docker build` (here: static binary on a distroless base, a few MB).
- **container** — a running instance of an image (image *unsealed* + executing = a live process). One image → many containers. The running app is the **workload**.
- **registry** — a server that holds images and hands them out. "push/store" = upload; "pull" = download to run. The warehouse between the builder (ephemeral runner) and the runner-of-it (cluster node).
- **tag** — the label on one specific image in a registry (`…:e79b55ac…`). A registry holds many images per name, one per build; the tag says which exact bundle. Immutable SHA tags make "deploy commit X" point at one exact bundle forever.
- **GHCR (GitHub Container Registry)** — `ghcr.io/<owner>/<name>` (parallel to `github.com/<owner>/…` for code). Pull over 443 (VLAN-OK). Push from Actions via `GITHUB_TOKEN`. New packages default **private**. View at GitHub profile → Packages → package. First-push may need manual repo-linkage in the UI.
- **mutable vs immutable tag** — mutable (`latest`) = name stays, image behind it changes → git never changes → Argo sees no diff → never deploys (silent GitOps failure). immutable (git SHA) = new tag per build → git changes → Argo rolls. Use immutable tags in GitOps; bonus: honest `git revert` rollbacks.
- **multi-stage build** — a Dockerfile with multiple `FROM`s; the build toolchain lives in an early stage, only the artifact is `COPY --from` into a minimal final stage → tiny image, smaller attack surface.
- **distroless** — a base image with no shell/package manager/OS userland, just the binary + runtime deps. `CGO_ENABLED=0` static binary lets you use `distroless/static`. No `/bin/sh` (can't `exec` a shell in — the build-side of the `exec format error`). `nonroot` variant passes Talos `baseline` PSA with no privileged label.
- **imagePullSecret** — [namespaced Secret, `docker-registry`/`.dockerconfigjson` type] the credential the kubelet uses to pull a private image. Referenced by name in the pod spec; Secret and Deployment must share a namespace. Git carries the *reference*; the cluster holds the *contents* (same rule as the Argo repo Secret / `*.asc`; Vault automates it in Task 8).
- **401 vs 403 (image pull)** — 401 = no/unknown credential (anonymous pull against a private package). 403 = authenticated but not permitted (wrong token scope/type).

### Apply strategies (from the KEDA fix)
- **client-side apply** — default `kubectl apply`; stores the full applied manifest in a `kubectl.kubernetes.io/last-applied-configuration` annotation. Large CRDs overflow the API server's **256 KB (262144-byte) annotation limit**.
- **server-side apply (SSA)** — tracks field ownership in `managedFields` on the server; no last-applied annotation, no size cap. Argo `ServerSideApply=true` sync option. PREVENTS future annotation writes but can't create an object that already failed — bootstrap once with `kubectl apply --server-side --field-manager=<name>`.
- **field manager** — the named owner of a field under SSA. Matching the manager name (`--field-manager=argocd-controller`) lets Argo cleanly adopt a hand-applied object (one-owner rule at the field level).
- **Unknown (Argo sync status)** — Argo couldn't COMPARE at all (repo fetch/auth failed → no target state). Distinct from `OutOfSync` (compared, they differ). `context deadline exceeded` with no clone logs = the fetch is black-holing (e.g. SSH/22 blocked).

### Auth / shell
- **classic vs fine-grained PAT** — classic = `ghp_`, ~40 chars, scope-based (`read:packages`). fine-grained = `github_pat_…`, ~93 chars, per-repo permissions (`Contents:Read`). GHCR pull needs **classic** `read:packages`; git HTTPS clone works with a fine-grained `Contents:Read`. A prefix/length check catches a wrong type instantly.
- **`gh` (GitHub CLI)** — talks to GitHub's API from the terminal: `gh pr create` / `gh pr merge --squash --delete-branch` (real PR record), `gh run watch`/`gh run list` (stream a workflow run — handy when the web UI is flaky). Distinct from plain `git`, which can merge but knows nothing about PRs.
- **three-dot diff (`main...HEAD`)** — what a branch changed since it diverged from `main` (the PR "Files changed" view). Two-dot (`main..HEAD`) is subtly different.
- **zsh `read`** — `read -rs "VAR?prompt"` (the `?prompt` form); bash's `read -p` FAILS in zsh (`no coprocess`). Bare `#` comment lines get executed (`command not found: #`). Always GATE a captured secret (`echo "starts: ${VAR:0:4} length: ${#VAR}"`) before using it — a silent `read` once stored the prompt text itself as a secret's password.

---

## Task 7 additions (Datadog — SaaS observability)

### Datadog components
- **Datadog Agent** — [workload, DaemonSet] one pod per node (node-exporter shape). Collects that node's host/container metrics, logs, and (if enabled) APM traces, and **pushes** them to the Datadog SaaS. A tenant doing a job per node.
- **Datadog Cluster Agent** — [controller] a small Deployment (1, HA-2) between the node Agents and the kube API server. Queries cluster-level data once and fans it to the node Agents (cuts API-server load from N readers to 1), and hosts cluster-level features. The external-metrics collision lives ONLY here.
- **Datadog Operator** — [controller] Datadog's operator; watches for a `DatadogAgent` CR and reconciles both agent tiers into existence. Same install pattern as Rook/KEDA (install a controller, hand it a CR).
- **DatadogAgent** — [CRD, `datadoghq.com/v2alpha1`] the git-committed desired-state object declaring what to collect (features) and how to override the agent pods. Analog of `CephCluster` / a KEDA `ScaledObject`.
- **API key (Datadog)** — 32-hex write-intake credential; lets an Agent *submit* data. Stored as a k8s Secret, referenced by name from the CR, never committed.
- **App key (Datadog)** — user-scoped credential for *querying/managing* via the API. Required only by the external-metrics feature — NOT needed for plain collection.
- **site (Datadog)** — which regional intake the Agent ships to. US1 = `datadoghq.com` (no subdomain); US5/EU carry a prefix. Wrong site = agents auth but data silently never arrives.

### Concepts
- **push vs pull (COLLECTION axis)** — pull = the collector (Prometheus) reaches out and scrapes targets' `/metrics` (in-cluster, east-west; absence of a target = `up==0` = a signal; needs reachability TO every target). push = the agent collects locally and reaches OUT to a backend (Datadog SaaS; only needs outbound reachability, scales across firewalls/NAT; absence is ambiguous — node down vs network down vs agent crash). DISTINCT from the Task-6 *deploy* push/pull axis — always name which axis. Test: "who initiates the connection?"
- **SaaS vs self-hosted observability** — Datadog (SaaS, push, managed backend, billed per host + ingest volume) vs Prometheus/Grafana (self-hosted, pull, you operate it, cheap at rest). Datadog can REPLACE or COMPLEMENT Prometheus (it can even ingest from it via OpenMetrics check / remote_write). Common hybrid: Prometheus for bulk in-cluster metrics, Datadog for correlation/APM/logs/long-retention. Deciding factor = cost, not capability.
- **external-metrics collision (KEDA vs Datadog)** — `external.metrics.k8s.io` is a single-owner APIService cluster-wide. KEDA's metrics-apiserver owns it (Task 5). Datadog's Cluster Agent external-metrics feature would register the same APIService → conflict. Resolved by leaving `features.externalMetricsServer.enabled: false` (chart default). Prometheus never collides (registers no APIService — pull). For DD-driven autoscaling, use KEDA's Datadog *scaler* (queries the DD API directly, keeps owning the slot).

### Kubernetes internals surfaced this task
- **`x-kubernetes-list-map-keys` / list-map** — a CRD/schema annotation marking an array as a MAP keyed on the named field(s) (e.g. `volumes` keyed on `name`). Redefining an entry with an existing key MERGES/REPLACES it rather than appending a duplicate — how an Operator override replaces a default volume by name. Read it from the live CRD (`kubectl get crd ... -o jsonpath=...schema...`), which is ground truth, unlike `kubectl explain` (may read a stale base schema).
- **single-file volume mount** — mounting a volume onto a FILE path (e.g. `/etc/passwd`) requires the source to be a file, not a directory. `emptyDir` and a bare `configMap` mount as DIRECTORIES → "not a directory" error. Use a `configMap`/`secret` volume PLUS a `subPath` on the container's `volumeMount` to project a single file.
- **CreateContainerError vs RunContainerError vs CrashLoopBackOff** — lifecycle stages of container failure. CreateContainerError = runtime can't even build the container (OCI/mount setup failed — e.g. a bad mount). RunContainerError = container created but the process won't start. CrashLoopBackOff = process starts then exits repeatedly; kubelet backs off restarts. Reading which stage narrows the cause; the *message* (`.status.containerStatuses[].lastState.terminated.message`) survives even when logs are empty.
- **hostPath (as a failure mode on Talos)** — [volume type] mounts a host path into a pod. On Talos (minimal/immutable, no `/etc/passwd`, read-only host fs) a hostPath to a nonexistent host file fails at OCI mount setup — the documented Datadog-on-Talos friction.

### Method / judgement
- **read the STATE, not the symptom string (reinforced)** — the status string can stay the same (`CreateContainerError`) while the underlying cause changes layer by layer. Pull `lastState.terminated.message` / the live object each time; don't re-apply the same fix to an unchanged-looking symptom. (Same trap as the Task-6 KEDA CRD saga.)
- **read the SCHEMA, not recall** — for exact override field paths, read the live CRD's openAPIV3Schema (or the vendor's version-matched docs), not memory. Field paths are version-specific.
- **scope decision / know when to stop tuning** — deliberately shipping the working subset (Cluster Agent) plus an honest in-progress finding, rather than brute-forcing an open-ended platform-vs-tool integration (node Agent on Talos). An SRE judgement call, not a failure.
- **verify the network before blaming config** — a netshoot pod + `nc -zv <host> <port>` isolates the network layer so an ambiguous failure isn't mis-attributed. (Task-6/Task-7 reflex.)

## Task 8 additions (HashiCorp Vault + External Secrets Operator)

### Vault
- **Vault** — [secrets management service — a stateful workload you operate, not a one-shot tool] stores/issues credentials via an API. Not just a password vault: its point is *manufacturing* short-lived creds on demand, not only storing static ones.
- **secrets engine** — [Vault plugin] a mount that produces one KIND of secret. `kv` (store/return static values), `database` (issues a fresh DB user per request with a TTL), `pki` (mints certs), `transit` (encryption-as-a-service). The "how to talk to X" logic, like a Terraform provider but for issuing creds.
- **static vs dynamic secret** — static (`kv`): value in, same value out (our repo/GHCR PATs). dynamic (`database`/`pki`): Vault GENERATES the credential per request, attaches a lease/TTL, and revokes it at the source on expiry. Dynamic is where "nothing to leak or expire-by-surprise" lives.
- **lease / TTL** — the timer on a (dynamic) secret or an issued token. Vault tracks every lease and can mass-revoke. A client renews its lease while healthy, so expiry is the normal handled case.
- **kv-v2** — versioned key-value engine (soft-delete + history), the current default. Its raw API path injects a hidden `data/` segment: a secret at `secret/argocd/repo` is `secret/data/argocd/repo` at the API. **`data/` goes in Vault POLICIES, NOT in ESO config** (ESO adds it itself).
- **seal / unseal** — Vault encrypts everything with a master key it never stores on disk; it boots SEALED (has ciphertext, not the key) and is useless until unsealed. Readiness probe fails while sealed (pod `0/1`). **Re-seals on every pod restart** (manual mode).
- **Shamir's Secret Sharing** — [threshold cryptography] the master key is split into N shares at `vault operator init`; K of N are needed to reconstruct it and unseal. In prod, N shares go to N humans (no single-person unseal). The shares + initial root token print ONCE and are unrecoverable — the single most dangerous artifact in the lab.
- **auto-unseal** — supplies the unseal key automatically from a cloud KMS or a second Vault's `transit` engine, removing the manual chore at the cost of an external dependency. The prod-vs-simple trade; lab chose manual Shamir to learn it.
- **integrated storage (Raft)** — Vault's built-in Raft-based storage backend (no external Consul). Single-node in the lab; PVC on `rook-ceph-block`. Guidance: disable mlock (`VAULT_DISABLE_MLOCK=true`) with integrated storage — also sidesteps the `IPC_LOCK` capability vs Talos baseline PSA.
- **Kubernetes auth method** — [Vault auth backend] Vault trusts the cluster's identity system: a client presents its ServiceAccount JWT, Vault verifies it via the k8s TokenReview API, maps the identity to a role→policy, and issues a short-lived Vault token. The pod's k8s identity IS the credential — no static Vault token stored anywhere. Modern Vault uses its OWN mounted SA token + CA for the review (`token_reviewer_jwt_set:false` is expected/correct).
- **Vault policy** — [ACL object] a grant: `path { capabilities = [...] }`. Attached to nothing on its own (like a k8s Role before a RoleBinding). **A `path` ending in `/*` matches DESCENDANTS, not the node itself** — a leaf secret needs its exact path granted (the ghcr 403).
- **Vault (k8s auth) role** — [binding in the k8s auth backend] ties specific `bound_service_account_names`/`_namespaces` to policies + a token `ttl`. The RoleBinding half. The SecretStore's `role:` selects which role→policy→paths a client gets.
- **root token** — the god credential printed at init. Used to bootstrap (enable engines/auth/policies); should be revoked or vaulted after bootstrap in prod.

### The Vault↔Kubernetes integration patterns
- **Vault Agent Injector** — [controller / mutating admission webhook] annotate a pod → sidecar injected that logs into Vault and writes secret FILES into an in-memory volume. Good for app config; can't produce a labeled/typed native k8s Secret.
- **Vault CSI provider** — [per-node driver, DaemonSet] secrets as a mounted volume via the Secrets Store CSI driver. Same file-into-pod shape/limitation as the injector.
- **External Secrets Operator (ESO)** — [controller/operator, CNCF/community, multi-backend] watches an `ExternalSecret` and MATERIALIZES a native k8s Secret synced from a backend (Vault here). The fit when the target must be a real k8s Secret (Argo `repository` label, `dockerconfigjson` pull secret). Reconciles + self-heals on drift.
- **Vault Secrets Operator (VSO)** — [controller/operator] HashiCorp's own ESO-equivalent (newer). Same CRD→native-Secret idea.
- **SecretStore / ClusterSecretStore** — [ESO CRD, namespaced / cluster-scoped] *how/where* to reach the backend + *who I am* (connection + auth). `Valid` status = ESO reached + authenticated to the backend.
- **ExternalSecret** — [ESO CRD, namespaced] the mapping: which backend key → which k8s Secret, shaped how (via `template`). `creationPolicy: Owner` = ESO creates + fully owns the Secret (stamps `reconcile.external-secrets.io/managed: true`). `SecretSynced/Ready True` = it read the backend + wrote the Secret.

### Method / diagnostics reinforced
- **SecretSyncedError (ESO)** — a status, NOT a cause. Read the ExternalSecret's `.status.conditions[*].message` / events for the real error (e.g. `403 permission denied` on a specific Vault path). Same "read the STATE not the symptom string" trap as the Task-6 CRD saga.
- **`git check-ignore -v <path>`** — the authoritative "is this ignored, and by which pattern/line?" query. Beats eyeballing `.gitignore` (accounts for precedence, negations, nested files). Use before assuming a rename is needed.
- **gitignore by SHAPE not vocabulary** — patterns should name dangerous FILE TYPES (`*.key`, `*.asc`, `*.tfstate`, `*.secret`), not ban an English word (`*secret*`), which over-matches safe manifests and trains you to rename around it (the habit that eventually commits a real secret).
- **namespace typo → `--server`** — `kubectl -n external -secrets ...` (stray space) parsed `-secrets` as `-s`=`--server` → `dial tcp: lookup ecrets: no such host`. `dial tcp: lookup <weird-host>` almost always = an arg got parsed as `--server`.

### Chart hygiene
- **chart version vs app version** — `helm search repo --versions` shows both: CHART VERSION = the packaging (what Argo `targetRevision` / Helm install by); APP VERSION = the software the chart deploys (informational). They drift independently — record BOTH. (Vault chart `0.34.0` shipped app `v2.0.3`; the app crossed a major between chart minors.)

### Recall quiz answers (Task 8) — cover and self-test
- secrets engine → a Vault mount that produces one kind of secret (kv/database/pki/transit).
- static vs dynamic → same-bytes-back vs Vault-generates-per-request-with-a-lease.
- kv-v2 `data/` rule → `data/` in the POLICY path, never in ESO's SecretStore/ExternalSecret.
- Vault policy `/*` → matches descendants, not the node; a leaf needs its exact path.
- k8s auth method → ESO presents its SA JWT, Vault TokenReviews it, maps to role→policy, issues a short-lived token; no static Vault token stored.

---

## Task 9 additions (StrongDM + RBAC + NetworkPolicies) — the access & authorization planes

### The five control planes (settled across Tasks 8-9)
- **five control planes** — five INDEPENDENT security questions (not a dependency stack): human access/audit (StrongDM), authentication (Auth0/SA auth), authorization (RBAC), secrets (Vault), network segmentation (NetworkPolicy). Read as five locks on five doors.

### StrongDM
- **StrongDM** — [SaaS-brokered PAM platform] brokers, authorizes, and AUDITS human sessions to infrastructure (k8s, SSH, RDP, DBs, web apps) for the precise time needed. The human-side mirror of Vault: no long-lived credential on the laptop; sessions are brokered, short-lived, audited. Does NOT replace authN or authZ — it wraps around them.
- **PAM (Privileged Access Management)** — the product category: control + audit of privileged human access to infrastructure. StrongDM is a Zero-Trust PAM.
- **StrongDM control plane** — [SaaS, `app.strongdm.com`] holds policy, identity federation, and the audit log. Never dials into your network; everything authenticates OUT to it on 443.
- **gateway** — [self-hosted StrongDM node] the entry point clients connect to. LISTENS for inbound client connections (default TCP 5000, configurable) and dials OUT to resources/relays.
- **relay** — [self-hosted StrongDM node, egress-only] same binary, different role. Does NOT listen; creates a reverse tunnel OUT to a gateway, preserving an egress-only firewall. The right node type for a locked-down subnet (dials out, no inbound).
- **node (StrongDM)** — the umbrella term for gateway or relay (one binary, role set at registration). Categorize as [workload/relay] — proxies traffic, does not reconcile desired→actual (not a controller).
- **brokered session** — a short-lived, policy-checked, fully-logged connection to a resource. The local kubeconfig/SSH points at `127.0.0.1:<port>`; the real credential is injected at the gateway/relay and never touches the endpoint.
- **just-in-time (JIT) access** — access granted for the precise time it's needed, then gone. The PAM analog of Vault's short-lived leases.
- **audit log** — StrongDM's record of who did what, where, when — the "audited" half of the human-access plane. The value that a raw TCP tunnel (e.g. brokering talosconfig) would lose.
- **node listener port (TCP 5000)** — StrongDM's default node-to-node / client-to-gateway port. Configurable. On the locked-down VLAN (non-443 TCP blocked) you MUST reconfigure the gateway to listen on 443 and place it OUTSIDE the VLAN; the relay inside dials out on 443. Same "get onto 443 or stay internal" lesson as SSH/22 in Task 6.

### RBAC (authorization plane)
- **RBAC (Role-Based Access Control)** — [k8s authorization] decides what an already-authenticated identity may DO to the k8s API. Additive-only and **default-DENY**: no grant = denied, so you never write a deny rule.
- **Role / ClusterRole** — [grant] a set of permissions: `verbs` on `resources` in `apiGroups`. Role = namespaced; ClusterRole = cluster-scoped. Inert until bound — the k8s analog of a Vault policy.
- **RoleBinding / ClusterRoleBinding** — [binding] ties a subject to a Role/ClusterRole. RoleBinding = grants in one namespace; ClusterRoleBinding = cluster-wide. A ClusterRole + RoleBinding grants a cluster-wide *definition* in ONE namespace (idiomatic reuse). The k8s analog of a Vault k8s-auth role.
- **subject** — who a binding applies to: a `User`, `Group` (often OIDC/SSO-federated, e.g. via StrongDM), or `ServiceAccount`.
- **verb / resource / apiGroup** — the three axes of a rule. verbs = get/list/watch/create/update/patch/delete; resources = pods/services/deployments/secrets…; `apiGroups: [""]` = the CORE group (pods, services, secrets), `apps` = deployments, `batch` = jobs. Wrong/missing apiGroup = rule matches nothing silently.
- **`kubectl auth can-i`** — the read-only RBAC seatbelt: `kubectl auth can-i <verb> <resource> --as-group <g>` tests a grant WITHOUT switching identity. Same "preview before trusting" discipline as `terraform plan` / `omnictl diff`.
- **built-in ClusterRoles** — `view` / `edit` / `admin` / `cluster-admin` ship with k8s; prefer binding these for common cases over hand-rolling.

### NetworkPolicies (network segmentation plane)
- **NetworkPolicy** — [namespaced object] controls which pods may talk to which at L3/L4 by label selectors. **Default is ALLOW-ALL** (opposite of RBAC). The first policy selecting a pod flips that pod to default-DENY for the covered direction; then only listed rules are allowed.
- **podSelector / namespaceSelector / ipBlock** — how a policy picks sources/targets: by pod labels, by namespace labels (`kubernetes.io/metadata.name` is the reliable auto-label), or by CIDR.
- **policyTypes (Ingress / Egress)** — which direction(s) a policy governs; they're independent (a default-deny-ingress does nothing to egress).
- **default-deny (authored)** — a policy with `podSelector: {}` (all pods) and empty rules for a direction = deny that direction for the whole namespace. You must AUTHOR this to get deny-by-default (unlike RBAC's built-in default-deny).
- **policy-capable CNI** — a CNI that actually ENFORCES NetworkPolicies (Calico, Cilium). **flannel does NOT** — on flannel a NetworkPolicy is a no-op (API accepts it, nothing enforces). Verify the CNI before trusting any policy. L3/L4 enforcement on omni-cluster would need a CNI swap; the enforced alternative actually built is L7 policy in the Istio sidecar (Task 10: PeerAuthentication, REGISTRY_ONLY egress).

### Method / judgement (Task 9)
- **scope-and-stop at the TASK level** — the Task-7 node-agent judgement applied to a whole task: document the architecture when a live build costs disproportionately (StrongDM: cloud VM + 14-day trial for an ephemeral result). Shipping the documented architecture is the higher-value SRE call.
- **IaC honesty for inert config** — authoring correct-but-unenforced manifests (NetworkPolicies on flannel) is fine for portability/intent, but LABEL them clearly as inert so no one assumes protection that isn't there.
- **RBAC default-deny vs NetworkPolicy default-allow** — the two authorization planes have OPPOSITE defaults. RBAC: grant or it's denied. NetworkPolicy: allowed until a policy selects the pod. A classic trip-up worth stating explicitly.

### Recall quiz answers (Task 9) — cover and self-test
- gateway vs relay → both self-hosted StrongDM nodes; gateway LISTENS for clients (default 5000), relay is egress-only and DIALS OUT to a gateway (right type for a firewalled subnet).
- Role vs RoleBinding → Role is the inert grant (verbs on resources); RoleBinding ties a subject to it. (≈ Vault policy vs k8s-auth role.)
- RBAC default vs NetworkPolicy default → RBAC = default-DENY (no grant = denied); NetworkPolicy = default-ALLOW until a policy selects the pod.
- why NetworkPolicies are no-ops here → flannel doesn't enforce them; need a policy-capable CNI (Calico/Cilium).
- what StrongDM replaces → the long-lived kubeconfig/SSH file on the laptop; it brokers short-lived, audited sessions with the real credential held off the endpoint (human-side mirror of Vault).

---

## Task 10 additions (Istio service mesh + MetalLB)

### The mesh, structurally
- **service mesh** — a dedicated infrastructure layer that handles service-to-service communication (encryption, identity, routing, retries, telemetry) *outside* the application, in a proxy next to it. The app speaks plain HTTP; the mesh does the rest.
- **Istio** — [control plane + data plane] the mesh used here. Sidecar mode: one Envoy proxy injected into every meshed pod.
- **istiod** — [controller] Istio's control plane. Watches Istio CRDs, compiles them into Envoy config, pushes it to every proxy. Also the **mesh CA**. Never touches a packet.
- **Envoy** — [workload — a proxy] the data plane. Every sidecar and every gateway is the same Envoy binary in a different role. "Learning Istio" = learning to configure a fleet of Envoys declaratively.
- **xDS** — the gRPC protocol istiod uses to stream config to Envoys. `istioctl proxy-status` shows `SYNCED` and the subscribed types: **CDS/LDS/EDS/RDS** (Clusters, Listeners, Endpoints, Routes). CRD → istiod → xDS → Envoy reconfigures live, no restart.
- **sidecar injection** — a mutating admission webhook that rewrites the pod spec at creation, gated on the **namespace** label `istio-injection=enabled`. Existing pods never gain a sidecar; `rollout restart` is the mechanism. Opt-in by design.
- **native sidecar** — newer Kubernetes starts sidecars as a special init container that keeps running for the pod's life; `istio-proxy` appearing under `initContainers` is this, not a bug.
- **paper vs infrastructure** — the sort that decides the verb. Infrastructure (istiod, sidecars, gateway, CNI DaemonSet) = pods, installed/changed with Helm. Paper (VirtualService, Gateway CRD, DestinationRule, PeerAuthentication, ServiceEntry) = CRDs istiod compiles, applied with kubectl or Argo. The `Gateway` CRD is the naming trap: paper that configures the gateway pod.

### Injection paths / PodSecurity
- **`istio-init`** — the classic injection path: a privileged init container (`NET_ADMIN`/`NET_RAW`) inside every meshed pod sets up the iptables redirect into the sidecar. Violates Talos's `baseline` PSA → every meshed namespace must be labeled `privileged`.
- **`istio-cni`** — [DaemonSet + chained CNI plugin] the alternative: a privileged DaemonSet does the iptables work once per node at pod creation, so app pods stay unprivileged and app namespaces stay `baseline`. Privilege is confined to `istio-system`.
- **`istio-validation`** — the unprivileged init container the CNI path injects instead of `istio-init`; only checks the redirect exists. **Fail-closed**: a pod stuck `Init:0/1` means the CNI half is broken on that node.
- **chained CNI** — a conflist in `/etc/cni/net.d` runs plugins in order; flannel first (veth, IP, routes), istio-cni second (redirect). istio-cni is a passenger on flannel's bus, not a replacement.
- **PSA is a rulebook, not a grant** — `enforce=privileged` means "stop checking this namespace." Checked at pod creation only; running pods are never re-checked. Removing the label doesn't kill anything — a restart is needed.

### Security
- **mTLS (mutual TLS)** — both sides present certs and verify each other. Sidecar-to-sidecar; the app never knows.
- **PeerAuthentication** — [CRD] sets the mTLS mode for a namespace, mesh, or workload. **PERMISSIVE** (default) = accept mTLS *and* plaintext, the migration mode. **STRICT** = mTLS only; a pod without a sidecar gets `connection reset`.
- **workload identity** — a sidecar's cert is tied to the pod's ServiceAccount and minted/rotated by istiod. Identity comes from being in the mesh, not from anything the pod does.

### Ingress / traffic
- **MetalLB** — [controller + speaker DaemonSet] assigns VLAN IPs from a pool to `LoadBalancer` Services and answers ARP for them. An IP assigner, never an ingress — it doesn't read HTTP.
- **IPAddressPool / L2Advertisement** — [MetalLB CRDs] the address range / "announce these by ARP." Both required.
- **speaker** — the per-node MetalLB pod that answers "who has this IP?" No speakers = IPs assigned but nobody answering; `kubectl` looks perfect and curls hang.
- **ingress gateway** — [workload] a standalone Envoy at the mesh edge; the north-south front door. Has **zero listeners** until a `Gateway` CRD binds to it.
- **Gateway** — [CRD] opens a port + hostname on gateway Envoys (becomes an Envoy listener, LDS). "Open the door."
- **VirtualService** — [CRD] routing rules: this hostname/path → that Service, with weights, retries, timeouts (becomes Envoy routes, RDS). "The directions." Door without directions = 404; directions without a door = unreachable.
- **DestinationRule** — [CRD] names **subsets** of a Service's pods by label and holds per-destination traffic policy. Routes nothing by itself — the seating chart the VirtualService uses.
- **subset** — a named slice of one Service's pods, by label, evaluated live. Not a second Service; only Envoy sees the carving.
- **weighted routing / canary release** — a VirtualService route with several subset destinations and weights; a per-request dice roll. New version gets a small percentage, dial up on confidence, dial to zero to roll back. Weight changes are route pushes — no pods restart.
- **TLS termination** — encryption from the browser ends at the gateway (`tls.mode: SIMPLE`); it decrypts, reads, routes. Standard production pattern.
- **credentialName** — the Gateway field naming the TLS Secret to present. **The Secret must live in `istio-system`** (the gateway reads its own namespace).
- **DNS-01 challenge** — certbot proves domain ownership by planting a DNS record via the provider API. The only challenge type that allows wildcard certs; nothing needs to be internet-reachable.
- **HSTS preload (`.dev`)** — the whole `.dev` TLD is preloaded; browsers force HTTPS for every `.dev` name. `curl` on 80 works, a browser never will.
- **REFUSED vs TIMEOUT** — refused = something answered with a reset (ARP/LB worked, no listener). Timeout = nothing answered (speaker/ARP problem). Which layer failed, in one word.

### Egress
- **REGISTRY_ONLY** — [`meshConfig.outboundTrafficPolicy.mode`] sidecars refuse outbound destinations the mesh doesn't know. Deny-by-default egress. Default is `ALLOW_ANY`.
- **ServiceEntry** — [CRD] registers an external destination with the mesh; the allowlist entry.
- **egress vs ingress** — about who *dialed*, not which way bytes flow. Replies ride the original call; `REGISTRY_ONLY` never affects inbound conversations.
- **egress gateway** — [workload, optional, not built] one audited exit pod that approved outbound traffic routes through. A funnel with a fixed network location a firewall team can pin; the policy is the brains, the gateway is where it physically exits.

### Observability
- **Kiali** — [workload] the mesh map: a UI that draws services and live traffic edges from Envoy metrics in Prometheus. Visualization only — not a control plane, not enforcement, not general dashboards (that's Grafana). Separate project from Istio.
- **`istio_requests_total`** — the metric behind the map: every request, labeled with source, destination, version, response code, and mTLS status. The labels *are* the graph edges.
- **15090 / `http-envoy-prom`** — Envoy's raw metrics port, declared on every injected pod; what a PodMonitor scrapes for mesh observability. (15020 is the merged Envoy+app endpoint, undeclared, needs relabeling.)
- **Traffic Graph vs Mesh page (Kiali)** — your applications' traffic (incident triage) vs Istio's own anatomy (is the mesh healthy). Triangle = Service, square = workload, padlock = mTLS-verified hop.

### Helm hygiene (reinforced)
- **unknown values keys are silently ignored** — Helm won't warn. `helm get values <release>` (live) vs `helm show values <chart> --version <pinned>` (what the chart reads). Read the schema, not recall; field names are version-specific.
- **a values file is the complete desired state** — every `helm upgrade -f` carries all settings; omissions revert to chart defaults.
- **requests vs usage** — the scheduler reads promises, not reality. `Insufficient memory` on a busy cluster usually means *requests* exhausted; `kubectl top nodes` (actual) vs `describe nodes` → `Allocated resources` (promised). Rolling upgrades need one pod of slack.

### Recall quiz answers (Task 10) — cover and self-test
- DestinationRule vs VirtualService → seating chart (names subsets) vs the host using it (routes to subsets with weights). The DR routes nothing.
- why only demo-app got a sidecar → the webhook checks the *namespace* label `istio-injection=enabled`; only demo-app carries it.
- istio-init vs istio-cni → per-pod privileged init container (needs `privileged` namespaces) vs per-node privileged DaemonSet (app namespaces stay `baseline`). Same mesh behavior.
- where a weight change "runs" → VS → istiod → RDS over xDS → gateway Envoy → per-request choice. No pods.
- does REGISTRY_ONLY break the site → no; inbound conversations are ingress, it only governs calls pods initiate.
- where Kiali gets the picture → Envoy counts → Prometheus scrapes 15090 → Kiali queries and draws. Topology inferred, never declared.

---

## Task 11 additions (KubeVirt — VMs as Kubernetes workloads)

- **KubeVirt** — [operator + CRDs] runs virtual machines as pods. Wraps QEMU/libvirt in a `virt-launcher` pod, so the scheduler places VMs, PVCs are disks, Services expose them, RBAC gates the console.
- **VirtualMachine (VM)** — [CRD] desired state for a VM (`runStrategy`, disks, CPU/memory, networks). VM : VMI :: Deployment : Pod.
- **VirtualMachineInstance (VMI)** — [CRD] the running instance. `kubectl get vmi` shows phase, node, and (with the guest agent) IP.
- **virt-launcher** — [workload] the pod that *is* the VM — QEMU + libvirt, in the VM's namespace. `kubectl delete pod virt-launcher-…` = pulling the power cord.
- **virt-controller / virt-handler / virt-api** — reconciler / per-node DaemonSet agent / **aggregated API server**. The last is why installing KubeVirt is control-plane-shaped work (CRDs, OpenAPI rebuilds, webhooks, etcd writes) and can OOM undersized masters.
- **CDI (Containerized Data Importer)** — [operator] imports ISOs and disk images into PVCs via `DataVolume`. KubeVirt v1.9's template controller requires its CRDs — install order matters (`no matches for kind "DataVolume"` = missing CRD).
- **DataVolume** — [CDI CRD] "make a PVC and fill it from this source" (`http`, `pvc` clone, `blank`). `dataVolumeTemplates` in a VM spec creates disks that are garbage-collected with the VM.
- **volumeMode Filesystem vs Block (CDI on RBD)** — CDI's StorageProfile defaults RBD to Block; the non-root importer then fails with `Permission denied`. Set `volumeMode: Filesystem` explicitly on every DataVolume (POC trade-off; production fixes the StorageProfile).
- **virtctl** — kubectl-for-VMs: `start`/`stop`/`restart`/`vnc`/`port-forward`/`guestosinfo`. With the guest agent connected, `stop` is a guest-initiated shutdown.
- **qemu-guest-agent** — the in-guest agent (VMware Tools analog) that reports IP/OS info and enables graceful shutdown. On Windows, install via `virtio-win-guest-tools.exe`.
- **virtio / paravirtual** — fast disk/NIC devices the guest must have drivers for (`viostor` at Windows install; Linux ships them in-kernel). Emulated devices are universally recognized but slow. vSphere analog: pvscsi/vmxnet3.
- **containerDisk** — an OCI image mounted as a read-only disk (here: the virtio driver ISO as a second CD-ROM).
- **masquerade networking** — the guest always gets `10.0.2.2/24` from the launcher, NAT'd to the pod IP. Reachable only on Service-declared ports (Services never forward ICMP — `ping` tests nothing). Anything that self-registers its IP (AD domain controllers) is structurally incompatible → needs **bridge / Multus** instead.
- **nested virtualization** — the hypervisor must expose VT-x to the worker VM (`vhv.enable`) or `/dev/kvm` doesn't exist and KubeVirt falls back to unusable emulation. Homelab tax; vanishes on bare metal.
- **boulders, not sand** — a guest's memory is one atomic, unswappable request on one node. VM capacity planning is bin-packing; a Pending VMI on every worker means no single node has the contiguous headroom.
- **PodDisruptionBudget vs drain** — a PDB guards the *eviction* API, not deletion. A single-replica StatefulSet with a PDB is undrainable by eviction; `kubectl delete pod` then drain. Production fix: HA.
- **the Ceph gate** — after any node cycle, `ceph status` = `HEALTH_OK` before touching the next node. Kubernetes "Ready" and Ceph "recovered" are independent definitions.
- **CRUSH reweight after disk growth** — BlueStore auto-expands into a grown OSD disk, but its CRUSH weight does not update; `ceph osd crush reweight osd.N <weight>` or placement stays skewed.
- **cert errors are frequently clock errors** — a `tls: expired certificate` from every node was a 24-second clock skew on the Omni host (no working NTP on a UDP-blocked VLAN). `remote error` = the other side aborted.
- **renewal ≠ reload** — a process that loads cert *copies* at startup keeps serving the old cert after renewal. Renewal automation without reload automation is a scheduled outage.
- **QoS classes decide kill order** — under memory pressure the kernel OOM-kills BestEffort pods first, then Burstable, Guaranteed last. Set requests on everything so the kill order is the one you chose.

### Recall quiz answers (Task 11) — cover and self-test
- where does a VM run → in its `virt-launcher` pod, in the VM's namespace; the pod *is* the VM.
- why masters melted, workers didn't → install = aggregated API + CRDs + webhooks (control-plane work); the VM = worker work.
- volumeMode rule on RBD → `Filesystem`, explicitly, on every DataVolume.
- masquerade limit → guest lies about its IP; published services fine, network citizens (DCs) need a bridge.
- pod-failure taxonomy → `ImagePullBackOff` (can't fetch) / `Init:0/1` (init blocked) / `RunContainerError` (never started, logs empty) / `CrashLoopBackOff` (started then died — `logs --previous` + exit code).
