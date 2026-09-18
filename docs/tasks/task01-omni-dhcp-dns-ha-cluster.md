# Task 1: Self-Hosted Omni + DHCP/DNS + HA Cluster Provisioning

> **Goal:** Learn a real, production-style Talos-management workflow by self-hosting Sidero Omni, fixing the lab network's provisioning friction (DHCP/DNS), and provisioning a fresh HA Kubernetes cluster entirely through Omni — as infrastructure-as-code.
>
> **Status:** ✅ Complete (including the git-managed cluster template, "1f").

---

## Environment Facts

| Thing | Value |
|---|---|
| Lab VLAN | `192.0.2.0/24`, gateway `.1`, segregated (only my VMs) |
| DNS (upstream, network-permitted) | OpenDNS `208.67.222.222` / `208.67.222.220` |
| Internal NTP (DC) | `198.51.100.20` |
| Omni host | `omni-host` — `192.0.2.60` (Ubuntu 24.04 + Docker) |
| dnsmasq host | `dnsmasq-host` — `192.0.2.61` (Ubuntu, DHCP+DNS) |
| DHCP pool | `192.0.2.100–150` |
| Omni URL | `https://omni.example.com` (Cloudflare DNS A record → `.60`, DNS-only/grey) |
| Auth0 domain | `example.us.auth0.com` |
| Auth0 client ID | `<AUTH0_CLIENT_ID>` (public — not a secret) |
| Admin/initial user | `admin@example.com` |
| Omni version (pinned) | `ghcr.io/siderolabs/omni:v0.41.0` |
| New HA cluster | `omni-cluster` — 3 CP + 3 workers, k8s v1.30.1 / Talos v1.7.4 |
| Node names | `master01/02/03`, `worker01/02/03` |

**Network personality (root cause of most friction):** this VLAN blocks outbound UDP (NTP/123, external WireGuard), blocks external DNS resolvers (Cloudflare 1.1.1.1), but allows OpenDNS and TCP/443. Originally no DHCP (all static). This is why we self-host Omni (keeps WireGuard internal) and stood up dnsmasq (fixes DHCP+DNS).

---

## Why self-host Omni (not SaaS)

Hosted SaaS Omni requires nodes to reach Omni's WireGuard endpoint over **outbound UDP**, which this network blocks. Self-hosting Omni *inside* the network keeps the WireGuard/SideroLink tunnel internal (node ↔ Omni both on the LAN), so the UDP egress block never applies. This also mirrors the likely on-prem posture of a security-conscious organization. Self-hosting is **free** for non-production/homelab use (Business Source License).

---

## Part 1 — Omni Host (omni-host)

### 1.1 Ubuntu VM + Docker
- Ubuntu Server 24.04, static IP `192.0.2.60` (set during install — no DHCP existed yet), LVM disk, OpenSSH.
- Install Docker via the official repo (NOT `apt install docker.io`):
```bash
sudo apt update && sudo apt install -y ca-certificates curl
sudo install -m 0755 -d /etc/apt/keyrings
sudo curl -fsSL https://download.docker.com/linux/ubuntu/gpg -o /etc/apt/keyrings/docker.asc
sudo chmod a+r /etc/apt/keyrings/docker.asc
echo "deb [arch=$(dpkg --print-architecture) signed-by=/etc/apt/keyrings/docker.asc] https://download.docker.com/linux/ubuntu $(. /etc/os-release && echo "$VERSION_CODENAME") stable" | sudo tee /etc/apt/sources.list.d/docker.list > /dev/null
sudo apt update && sudo apt install -y docker-ce docker-ce-cli containerd.io docker-buildx-plugin docker-compose-plugin
sudo usermod -aG docker $USER   # then log out/in
```

### 1.2 TLS certificate (Let's Encrypt via Cloudflare DNS-01)
Self-signed certs do NOT work with Omni (gRPC is strict). Bought domain `example.com` (Cloudflare Registrar). Added A record `omni.example.com → 192.0.2.60` (DNS-only / grey cloud — private IP can't be proxied).

```bash
sudo snap install --classic certbot
sudo snap set certbot trust-plugin-with-root=ok
sudo snap install certbot-dns-cloudflare
sudo ln -sf /snap/bin/certbot /usr/bin/certbot

mkdir -p ~/omni
cat > ~/omni/cloudflare.ini << 'EOF'
dns_cloudflare_api_token = <SCOPED_EDIT_ZONE_DNS_TOKEN>
EOF
chmod 600 ~/omni/cloudflare.ini

sudo certbot certonly --dns-cloudflare \
  --dns-cloudflare-credentials ~/omni/cloudflare.ini \
  -d omni.example.com --agree-tos -m admin@example.com --no-eff-email
```
Certs land at `/etc/letsencrypt/live/omni.example.com/{fullchain.pem,privkey.pem}`. Auto-renews every 90 days.

### 1.3 Auth0 (identity provider)
- Auth0 free tier, tenant `example.us.auth0.com`.
- Create **Application → Single Page Application** named `Omni`. Grab the **Client ID**.
- Set Allowed **Callback / Logout / Web Origin** URLs to exactly `https://omni.example.com/`.
- Keep the username/password (Database) connection ON for the app (no social login).
- Create a user (`admin@example.com`) → this becomes the Omni admin.

### 1.4 etcd encryption key + account UUID
```bash
cd ~/omni
gpg --quick-generate-key "Omni (etcd encryption) omni@example.com" rsa4096 cert never   # NO passphrase
gpg --list-secret-keys                                # copy the fingerprint
gpg --quick-add-key <FINGERPRINT> rsa4096 encr never
gpg --export-secret-key --armor omni@example.com > omni.asc
uuidgen                                               # save this — the stable --account-id
```

### 1.5 Copy certs + run Omni
```bash
sudo cp /etc/letsencrypt/live/omni.example.com/fullchain.pem ~/omni/tls.crt
sudo cp /etc/letsencrypt/live/omni.example.com/privkey.pem ~/omni/tls.key
sudo chown $USER:$USER ~/omni/tls.crt ~/omni/tls.key

cd ~/omni
docker run -d --name omni --restart=unless-stopped \
  --net=host --cap-add=NET_ADMIN --device /dev/net/tun \
  -v $PWD/etcd:/_out/etcd -v $PWD/tls.crt:/tls.crt -v $PWD/tls.key:/tls.key -v $PWD/omni.asc:/omni.asc \
  ghcr.io/siderolabs/omni:v0.41.0 \
    --account-id=<YOUR_UUID> --name=omni-cluster \
    --cert=/tls.crt --key=/tls.key \
    --siderolink-api-cert=/tls.crt --siderolink-api-key=/tls.key \
    --private-key-source=file:///omni.asc \
    --event-sink-port=8091 --bind-addr=0.0.0.0:443 \
    --siderolink-api-bind-addr=0.0.0.0:8090 --k8s-proxy-bind-addr=0.0.0.0:8100 \
    --advertised-api-url=https://omni.example.com/ \
    --siderolink-api-advertised-url=https://omni.example.com:8090/ \
    --siderolink-wireguard-advertised-addr=192.0.2.60:50180 \
    --advertised-kubernetes-proxy-url=https://omni.example.com:8100/ \
    --auth-auth0-enabled=true --auth-auth0-domain=example.us.auth0.com \
    --auth-auth0-client-id=<AUTH0_CLIENT_ID> \
    --initial-users=admin@example.com
```
**Key line:** `--siderolink-wireguard-advertised-addr=192.0.2.60:50180` uses the **internal IP** — this is what keeps WireGuard on the LAN and dodges the UDP egress block.

Verify: `docker ps` (Up), `docker logs omni --tail 30`, then browse `https://omni.example.com`.

**Restore note:** Omni state lives in the mounted `~/omni` dir (certs, `omni.asc`, `etcd/`), NOT in the container. Deleting/restoring the VM is survivable as long as `~/omni` is intact — `docker start omni` brings it back.

---

## Part 2 — dnsmasq: DHCP + DNS (dnsmasq-host)

**Why:** fixes the root cause of nearly all provisioning friction — no DHCP (manual static IPs everywhere) and no internal DNS (couldn't resolve `omni.example.com`). One tiny service solves both.

**Safety checks done first** (segregated VLAN, so safe): confirmed no existing DHCP (Windows box APIPA'd to `169.254.x` = nothing answering; `nmap --script broadcast-dhcp-discover`) and inventoried the VLAN (`nmap -sn 192.0.2.0/24` = only my hosts).

### Setup
```bash
# tiny Ubuntu VM (1 vCPU / 1GB / 8GB), static IP 192.0.2.61
sudo apt update && sudo apt install -y dnsmasq

# free port 53 from systemd-resolved
sudo nano /etc/systemd/resolved.conf   # set: DNSStubListener=no  and  DNS=208.67.222.222 208.67.222.220
sudo systemctl restart systemd-resolved
sudo rm -f /etc/resolv.conf
echo "nameserver 127.0.0.1" | sudo tee /etc/resolv.conf
```

`/etc/dnsmasq.conf`:
```conf
interface=ens33            # confirm with `ip link` (was ens33, not ens192)
bind-interfaces
dhcp-range=192.0.2.100,192.0.2.150,255.255.255.0,12h
dhcp-option=3,192.0.2.1        # gateway
dhcp-option=6,192.0.2.61       # DNS = this box
server=208.67.222.222             # upstream (OpenDNS)
server=208.67.222.220
address=/omni.example.com/192.0.2.60   # <-- resolves Omni for every client on the VLAN
domain-needed
bogus-priv
```
```bash
sudo systemctl restart dnsmasq && sudo systemctl enable dnsmasq
```
Test: `nslookup omni.example.com 127.0.0.1` → `.60`; a DHCP client gets an IP in `.100–150` + DNS `.61`.

**Static vs DHCP rule:** infrastructure other things depend on by address stays STATIC (gateway `.1`, Omni host `.60`, dnsmasq `.61`, Task1 cluster, MetalLB pool). Only the fungible cluster nodes use DHCP.

**Harmless warning:** `resolvconf: Failed to set DNS configuration: Link lo is loopback device` — cosmetic, ignore.

---

## Part 3 — Node Templates (vCenter)

Two role-class templates (role is assigned at cluster-creation, so these differ only in **specs**, not config):
- **Omni-Template-Master:** 2 vCPU, 2GB RAM, 30GB disk
- **Omni-Template-Worker:** 2 vCPU, 8GB RAM, 50GB disk (sized for Rook-Ceph/Task 2)

**Process:** download the Omni **installation media as ISO** (in the download dialog, the `Options: VMWare (amd64)` dropdown → switch to **ISO**). Create a VM, attach the ISO, boot — **no kernel args needed** (DHCP gives IP, dnsmasq resolves Omni; this is the payoff of Part 2). Convert to a vCenter template **with the ISO attached** (the disk install happens when Omni allocates the node to a cluster).

**Gotchas:**
- On the bare ESXi host, OVA import failed with "Host did not have any virtual network defined." ISO + manually-created VM avoids the OVA importer's network-mapping issue. (vCenter would show the port group; direct-to-ESXi doesn't.)
- Clones get fresh hardware UUIDs → they register as distinct machines. No timing games needed.

---

## Part 4 — Provision the HA Cluster

1. Clone `Omni-Template-Master` ×3 and `Omni-Template-Worker` ×3 → name them `master01–03`, `worker01–03`.
2. Power on → each DHCP-leases into `.100–150` → auto-registers in Omni's **Machines** pool (no config, no kernel args).
3. Omni → **Clusters → Create Cluster** → name `omni-cluster` → assign CP/Worker roles → Create.
4. **Apply the internal-NTP patch AT CREATION** (see key lesson below).

Omni then does the entire HA bootstrap (config gen, etcd quorum across 3 masters, control plane, worker joins) automatically — the whole by-hand process, from a few clicks.

---

## Part 5 — CLI Access (omnictl + kubectl)

`omnictl` and `kubectl` read config from your **home dir** (`~/.config/omni/` or `~/Library/Application Support/omni/` on macOS; `~/.kube/config`), NOT your working directory — run from anywhere.

```bash
# point omnictl at your Omni (download omniconfig.yaml from the Omni UI first)
omnictl config merge ~/Downloads/omniconfig.yaml
omnictl get clusters          # first run opens browser for Auth0/OIDC login, saves a PGP key

# pull kubeconfig (adds a context, switches to it)
omnictl kubeconfig --cluster omni-cluster
kubectl get nodes
```

**Gotchas hit:**
- **Version mismatch:** Homebrew installed omnictl `v1.8.2`, but the server is `v0.41.0` → `client API version mismatch`. Fix: download the version-matched `omnictl` (M4 Mac = **darwin-arm64**) from the Omni UI or the v0.41.0 GitHub release. On Apple Silicon, `brew uninstall omnictl` so `/opt/homebrew/bin` doesn't win the PATH fight; `hash -r`; clear Gatekeeper quarantine: `xattr -d com.apple.quarantine /usr/local/bin/omnictl`. **Keep omnictl pinned to the server version.**
- **`unknown command "oidc-login"`:** kubectl needs the OIDC plugin. `brew install kubelogin` (the `int128/kubelogin`, NOT Azure's). Provides `kubectl oidc-login`.
- **`connection refused 127.0.0.1:8080`:** omnictl has no config → falls back to localhost. Merge `omniconfig.yaml`.

---

## Part 6 — Switching kubectl Contexts (multi-cluster)

A **context** = which cluster kubectl talks to (endpoint + creds + namespace). kubectl only ever shows/acts on ONE context at a time — never a combined view.

```bash
kubectl config get-contexts                          # list all; * = active. Note exact NAME.
kubectl config use-context <name-from-get-contexts>  # value MUST be the exact NAME column value
kubectl config current-context                       # confirm before anything destructive
kubectl get nodes                                    # node names confirm which cluster

**Rule:** always verify `current-context` (or `get nodes` by name) before destructive commands. Clusters are fully isolated — a command can only affect the current context's cluster.

---

## Part 7 — Infra-as-Code: git-managed cluster template ("1f")

Instead of clicking config in the UI, express the whole cluster as a template in git. Also fixes node names.

**Export the running cluster → template:**
```bash
omnictl cluster template export --cluster omni-cluster --output omni-cluster-template.yaml
```
Then edit to add per-machine **hostname** patches (`kind: Machine` docs) and the cluster-wide **NTP** patch. Apply safely:
```bash
omnictl cluster template validate -f omni-cluster-template.yaml
omnictl cluster template diff -f omni-cluster-template.yaml   # PREVIEW — must show only intended changes
omnictl cluster template sync -f omni-cluster-template.yaml
omnictl cluster template status -f omni-cluster-template.yaml
```
Template structure: one `kind: Cluster` (versions + cluster-wide patches), one `kind: ControlPlane` + `kind: Workers` (machine IDs by role), and one `kind: Machine` per node (hostname patch). **Commit to git** (kept in the private repo) = infra-as-code done.

**Node-rename cleanup:** changing hostnames makes nodes re-register under new names, leaving stale `NotReady` node objects under the old `talos-*` names. Delete them:
```bash
kubectl delete node talos-xxx talos-yyy ...   # removes stale objects only; VMs/live nodes untouched
```

---

## KEY LESSONS (do these right next time)

1. **Apply the internal-NTP patch AT CLUSTER CREATION, not after.** External NTP is blocked → clock skew → TLS/etcd cert validity fails → bootstrap **hangs and won't complete on its own**. Patch (`machine.time.servers: 198.51.100.20`) at creation = nodes come up with correct time and bootstrap cleanly. Patching reactively means rescuing a stuck bootstrap.
2. **Set hostnames at cluster creation too** (in the template) — setting them after triggers a re-register + stale-node cleanup.
3. **DNS:** external resolvers (Cloudflare 1.1.1.1) are blocked; **OpenDNS works and returns the private IP** (no rebinding protection). dnsmasq's `address=/omni.example.com/` makes every client resolve Omni with zero per-node config.
4. **WireGuard stays internal** via `--siderolink-wireguard-advertised-addr=<internal-ip>` — the whole reason self-hosting beats SaaS on this network.
5. **Keep omnictl pinned to the Omni server version.** Homebrew tracks latest and breaks it.
6. **Preview before applying** — `omnictl cluster template diff` is the `git status`-before-commit equivalent.
7. **Omni state is on disk** (`~/omni`), not in the container — VM restore is survivable.

---

## Conceptual Q&A (questions I asked along the way)

**Q: What is Omni actually doing that I did manually on the reference cluster?**
Everything in the manual loop: generate machine config, deliver it, match cluster secrets, bootstrap etcd, join nodes, verify. On the reference cluster *I* was that process (per node, by hand, with the guestinfo/base64 dance and secrets-mismatch troubleshooting). Omni does it all from "boot image → machine appears → click → cluster." Imperative (me, step by step) → declarative (declare desired cluster, Omni reconciles). Same pattern as Deployments/Argo/MetalLB, applied to building the cluster itself.

**Q: If I have 6 nodes in one cluster, can I spin up a second cluster from the same nodes?**
No — a machine belongs to exactly one cluster at a time (like a book checked out of a library). To run a second cluster you need more machines, or tear down the first to free them. Omni's value isn't node-sharing; it's that clusters become **cheap to create, destroy, and recompose** from a pool, and you manage many centrally. (Container/ship analogy: a container rides one ship at a time, but the crane system makes loading/unloading/recomposing trivial.)

**Q: How does Auth0 connect to Omni? There's no client secret — is the email the secret?**
Omni delegates auth to Auth0 (OIDC). Three flags point Omni at the Auth0 app (`--auth-auth0-enabled/domain/client-id`) + the callback URL whitelist + `--initial-users` email. Flow: hit Omni → redirect to Auth0 → log in *to Auth0* (Omni never sees the password) → Auth0 returns a signed token with your verified email → Omni matches it to `--initial-users` and grants admin. **No client secret because it's a SPA** (browser apps can't hold secrets → uses PKCE instead). The email is NOT the secret — it's public authorization ("who gets admin"); the real credential is your Auth0 login. Authentication (Auth0, password/MFA) vs authorization (the email list) are different things.

**Q: Bare metal vs VMs — does one physical server only get one Talos node? Doesn't that mean one server per cluster?**
Yes: on bare metal, Talos installs directly on the physical server (no hypervisor) → the **whole server = one node**. And yes, that server belongs to one cluster at a time. That's normal — you don't put a cluster "on a server," you build a cluster **across many servers** (e.g. 3 CP servers + N worker servers = one cluster), and scale by adding servers. Same "6 nodes = 1 cluster" model, just with physical boxes as the unit. Why do it: no hypervisor overhead, no VMware licensing (likely a big driver for a production environment), simpler/thinner stack, and Talos is built for it. The layer that changes is only *how a node is provisioned* (CIMC→PXE→Talos→Omni instead of VM clone); everything above the node (k8s, storage, GitOps) is identical.

**Q: Why does Omni need PXE + CIMC in the real world?**
With no vCenter to clone VMs, something must turn a bare physical box into a registered node: **CIMC powers the server on → PXE serves the Talos image over the network → Talos installs & registers with Omni → Omni assigns it to a cluster.** That's the bare-metal replacement for "clone a VM template." PXE requires DHCP (which is why the dnsmasq work is the foundation for it).
