# Homelab SRE Platform — Bare-Metal Talos / Kubernetes Stack

A hands-on build of an on-prem Kubernetes platform on **Talos Linux**,
managed with **Sidero Omni**, on a deliberately locked-down network. Every layer is
built as infrastructure-as-code and documented as a runbook: how it was built, *why*
each decision was made, and the gotchas hit along the way.

---

## What this demonstrates

- **Talos + Omni** cluster lifecycle: self-hosted management plane, HA provisioning,
  and full Talos/Kubernetes upgrades — all driven from a git-managed cluster template.
- **GitOps end-to-end**: Argo CD reconciling the cluster from git; GitHub Actions
  building images and bumping manifests (pull-based CD).
- **Distributed storage**: Rook-managed Ceph (block storage) with a proven self-heal
  cycle on node failure.
- **Autoscaling**: native HPA on CPU, then KEDA scaling on a live Prometheus metric.
- **Observability**: self-hosted Prometheus/Grafana (pull) contrasted with Datadog (push);
  Kiali for mesh topology.
- **Secrets**: HashiCorp Vault + External Secrets Operator, with workloads
  authenticating by Kubernetes ServiceAccount (no static tokens).
- **Access & authorization**: RBAC, NetworkPolicies, and a StrongDM (PAM) architecture.
- **Service mesh**: Istio with STRICT mTLS, a MetalLB-fronted ingress gateway with
  Let's Encrypt TLS, a live-shifted v1/v2 canary, and deny-by-default egress — including
  both sidecar-injection paths and the Talos PodSecurity trade-off between them.
- **Virtualization on Kubernetes**: KubeVirt running a Windows Server VM with its root
  disk on Ceph, exposed through MetalLB — and the incident anthology that installing an
  aggregated API server on an undersized control plane produced.
- **IaC discipline throughout**: Terraform, Helm, a prod-style git/PR workflow, and
  a "one resource, one owner" rule for reconcilers.

---

## The one idea that shapes the whole build: inside vs. outside the network

The lab lives on a restricted VLAN. It permits only
a permitted upstream resolver and TCP/443. Almost every hard problem in these runbooks
is the same shape: *something inside the wall needs to reach something outside, and tried
a door that's bolted shut.* The fix is always "get onto 443, or stay internal."

```
OUTSIDE THE NETWORK  (cloud + workstation)
  [ workstation ]     [ GitHub: repo / Actions / GHCR ]     [ Cloudflare DNS ]   [ Auth0 ]
   kubectl, git             build + image store               example.com        Omni login
        |                          |
========|==========================|==== ONLY TCP/443 (+ permitted DNS) crosses this line ====
        |                          |
INSIDE THE LOCKED-DOWN VLAN 192.0.2.0/24   (blocks UDP, SSH/22, external DNS resolvers)

  [ Omni host .60 ]   [ dnsmasq .61 ]   [ talos-cluster (reference, hand-built) ]
   manages clusters    DHCP + int DNS

  omni-cluster  (Omni-managed, k8s 1.36 / Talos 1.13):
     Rook-Ceph          [operator]            distributed storage (raw disk per worker)
     Prometheus+Grafana [workload]            scrape /metrics, TSDB on Ceph
     metrics-server     [workload]            CPU/mem for native HPA
     Argo CD            [controller]          watches git over HTTPS/443, reconciles cluster
     KEDA               [controller+adapter]  ScaledObject -> generates an HPA
     Vault + ESO        [controllers]         secrets, materialized as native k8s Secrets
     Datadog            [agent + cluster-agent]  push telemetry to SaaS over 443
     MetalLB            [controller+speakers] hands VLAN IPs (.211-.221) to LoadBalancer Services
     Istio              [istiod + Envoys]     mTLS, ingress gateway (TLS), canary weights, egress policy
     Kiali              [workload]            mesh map drawn from Envoy metrics in Prometheus
     KubeVirt + CDI     [operators]           VMs as pods; ISO/disk import into Ceph PVCs
     demo-app v1 / v2   [workload]            Go app; image pulled from GHCR; 90/10 canary
     win2k22            [VM]                  Windows Server 2022, RDP via MetalLB
```

Everything **built** lives inside the VLAN; everything **depended on** (GitHub, GHCR,
Cloudflare, Auth0, the workstation) lives outside. There is one narrow door: TCP/443.

---

## Environment at a glance

| Thing | Value |
|---|---|
| Cluster OS / orchestrator | Talos Linux 1.13.6 / Kubernetes 1.36.2 |
| Management plane | Self-hosted Sidero Omni |
| Topology | 3 control-plane + 3 worker nodes (VMs on vSphere; design target is bare-metal) |
| Network | Segregated VLAN `192.0.2.0/24`; permitted upstream DNS + TCP/443 only |
| Storage | Rook-Ceph (block), replica-3, host failure domain |
| GitOps | Argo CD + GitHub Actions + GHCR |
| Load balancing / ingress | MetalLB (L2) + Istio ingress gateway; Let's Encrypt wildcard via DNS-01 |
| Service mesh | Istio 1.30.3, sidecar mode, `istio-cni` injection path |
| Virtualization | KubeVirt v1.9.0 + CDI (nested virtualization on the worker VMs) |
| Reference cluster | `talos-cluster` (hand-built, kept for comparison) |

---

## Runbook index

Each runbook is a self-contained build log: steps, commands, configs, decisions,
gotchas, and a short conceptual Q&A.

| # | Runbook | What it builds |
|---|---|---|
| 1 | [Omni + DHCP/DNS + HA cluster](./docs/tasks/task01-omni-dhcp-dns-ha-cluster.md) | Self-hosted Omni, dnsmasq (DHCP+DNS), first Omni-provisioned HA cluster as IaC |
| 2 | [Rook-Ceph storage](./docs/tasks/task02-rook-ceph-storage.md) | Distributed block storage; PVC bind + self-heal proof |
| 3 | [Talos / Kubernetes upgrade](./docs/tasks/task03-talos-k8s-upgrade.md) | Full version-ladder upgrade via the git-managed template; Omni self-upgrade |
| 4 | [Terraform (IaC)](./docs/tasks/task04-terraform.md) | Brownfield adoption of DNS into Terraform; state, plan/apply, import |
| 5 | [HPA → KEDA autoscaling](./docs/tasks/task05-hpa-keda-autoscaling.md) | Native HPA on CPU, then KEDA on a live Prometheus metric |
| 6 | [GitHub Actions CI/CD](./docs/tasks/task06-github-actions-cicd.md) | Commit → build → push → manifest bump → Argo sync (pull-based GitOps) |
| 7 | [Datadog](./docs/tasks/task07-datadog.md) | SaaS push-model observability vs. self-hosted pull; KEDA collision avoided |
| 8 | [Vault + ESO](./docs/tasks/task08-vault-eso.md) | Secrets management; ServiceAccount auth; migrating hand-made Secrets |
| 9 | [StrongDM + RBAC + NetworkPolicies](./docs/tasks/task09-strongdm-rbac-netpol.md) | The human-access, authorization, and segmentation planes |
| 10 | [Istio service mesh](./docs/tasks/task10-istio-service-mesh.md) | Install (both injection paths) → STRICT mTLS → MetalLB + ingress gateway + TLS → v1/v2 canary → REGISTRY_ONLY egress → Kiali |
| 11 | [KubeVirt + Windows Server](./docs/tasks/task11-kubevirt-windows.md) | VMs as Kubernetes workloads: CDI ISO import, Ceph-backed root disk, RDP via MetalLB; control-plane sizing incident |

Supporting docs:
[**Terminology glossary**](./docs/glossary.md) ·
[**Reference cluster (hand-built Talos HA + VIP)**](./docs/reference-cluster-hand-built.md)

---

## Repository layout

The runbooks narrate the build; the config tree is the build. It mirrors the real GitOps
repo, so it reads as an actual platform, not a writeup.

```
talos-omni-homelab/
├── docs/
│   ├── tasks/                        the 11 narrated runbooks
│   ├── glossary.md
│   └── reference-cluster-hand-built.md
├── clusters/omni-cluster/            what runs on the cluster
│   ├── omni-cluster-template.yaml      cluster-as-code (Omni): versions, roles, patches
│   ├── storage/                        Rook-Ceph CephCluster + BlockPool/StorageClass
│   ├── metrics-server/ · monitoring/   Helm values (HPA metrics; Prometheus/Grafana)
│   ├── argocd/apps/                    one Argo Application per component (incl. MetalLB)
│   ├── argocd/vault/                   ESO SecretStores + ExternalSecrets (creds by ref)
│   ├── autoscaling/                    KEDA ScaledObject
│   ├── apps/demo-app/                  v1 + v2 Deployments, DestinationRule, PodMonitor
│   ├── apps/datadog/                   DatadogAgent CR
│   ├── metallb/                        IPAddressPool + L2Advertisement (Argo-owned)
│   ├── istio/                          Helm values (istiod, cni, gateway, kiali) +
│   │                                   Gateway/VirtualService, PeerAuthentication,
│   │                                   ServiceEntry (hand-applied; Argo adoption is debt)
│   ├── kubevirt/                       KubeVirt + CDI CRs, DataVolume, VM, RDP Service
│   ├── rbac/ · netpol/                 authz + segmentation (NetworkPolicies inert on flannel)
├── terraform/                        Cloudflare DNS as IaC (brownfield import)
├── src/demo-app/                     Go service + multi-stage distroless Dockerfile
└── .github/workflows/                CI: build → push GHCR → bump manifest → Argo syncs
```

These manifests are a **structural reference, not a one-command deploy** — some assume live
infra or credentials materialized at runtime by Vault + External Secrets Operator, and the
`istio/` and `kubevirt/` directories mix Helm values with hand-applied manifests (ownership is
noted in each runbook). See [`clusters/README.md`](./clusters/README.md), which also explains
why there are (by design) almost no secrets in the tree.

---

## How to read this

If you're skimming: read this page and the **inside/outside** diagram above, then pick
one runbook (Task 6 is a good tour of the GitOps loop; Task 8 shows the secrets design;
Task 10 is the mesh end to end).

If you're rebuilding: start at Task 1 (the network and Omni foundation) and go in order —
each task assumes the previous one's platform.

---

## Notes

A hands-on homelab where I built and documented a production-style SRE platform from scratch. Network addresses use RFC 5737 documentation ranges and example.com placeholders.
