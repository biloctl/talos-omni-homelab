# Task 3: Cluster Upgrade via Omni (Talos + Kubernetes) — and the Omni self-upgrade that came first

> **Goal:** Upgrade `omni-cluster` from Talos 1.7.4 / k8s 1.30.1 up to current (Talos 1.13.6 / k8s 1.36.2), entirely through the git-managed Omni cluster template, to learn the Omni-driven upgrade workflow as infrastructure-as-code. Doing it on a cluster that already has real stateful storage (Rook-Ceph) so the upgrade also proves Ceph survives rolling reboots.
>
> **Status:** ✅ Complete. Omni self-upgraded v0.41.0 → v1.9.1; cluster on Talos 1.13.6 / k8s 1.36.2; bootstrap manifests synced; Rook-Ceph `HEALTH_OK` after ~18 node reboots; all changes git-managed and merged to `main` via PR.

---

## Environment Facts

| Thing | Value |
|---|---|
| Cluster | `omni-cluster` (Omni-managed), 3 CP + 3 workers |
| Omni host | `omni-host` — `192.0.2.60`, state dir `~/omni` (`/home/user/omni`) |
| Omni version | `v0.41.0` → **`v1.9.1`** (21 sequential hops) |
| Talos version | `1.7.4` → **`1.13.6`** (6 minor rungs) |
| Kubernetes version | `1.30.1` → **`1.36.2`** (6 minor rungs) |
| Git template | `clusters/omni-cluster/omni-cluster-template.yaml` (repo `example-user/homelab-k8s`) |
| Git branch | `task03-talos-k8s-upgrade` → PR → merged to `main`, branch deleted |
| kubeconfig context | `omni-cluster` |
| Ceph disk / OS disk | `/dev/sdb` (Ceph, untouched by upgrade) / `/dev/sda` (Talos) |

**Network note:** This task is friendly to the locked-down VLAN — upgrades pull installer/component images from `ghcr.io` and `registry.k8s.io` over **TCP/443** (allowed). No outbound UDP / external DNS / NTP dependency. The only network-adjacent break was an Auth0 callback-URL mismatch after the Omni jump (see gotchas).

---

## Core concept: the Talos ↔ Kubernetes interlock (why the whole route is shaped the way it is)

Talos-the-OS and Kubernetes-the-orchestrator are **two separate version tracks** that run on the same machines, and they are **locked together**: each Talos minor release caps the maximum Kubernetes minor it will run.

- Talos 1.7 → k8s ≤ 1.30
- Talos 1.8 → k8s ≤ 1.31
- Talos 1.9 → k8s ≤ 1.32 … (pattern: +1 k8s minor per Talos minor)
- Talos 1.13 → k8s ≤ 1.36

Read it straight off `omnictl get talosversion` — each Talos row lists its supported k8s versions.

**Three rules that follow:**
1. **Talos: one minor at a time** (1.7 → 1.8 → 1.9 …). Skipping is unsupported/untested. Land on the latest patch of each minor.
2. **Kubernetes: one minor at a time** (1.30 → 1.31 → 1.32 …). Never skip.
3. **Talos first, then k8s, every rung.** The Talos bump lifts the ceiling; only then is the k8s bump legal. Omni also validates this and will refuse an out-of-order move.

**The trap I started in:** on Talos 1.7 running k8s 1.30.1, I was already at 1.7's ceiling — so k8s could not move to 1.31 until Talos went to 1.8 first.

---

## PART A — Rung 0: upgrade Omni itself (v0.41.0 → v1.9.1)

**Why this was necessary (and the biggest surprise of the task):** the pinned `omni:v0.41.0` from Task 1 was ~a whole major series behind. Its "Update Talos" list stopped at Talos **1.8.4** — it literally didn't know Talos 1.9+ existed. To reach 1.13 the *management plane* had to be upgraded first. Omni ships minors roughly monthly; a pinned tag that never got bumped drifts fast.

**Omni upgrade rules (from Sidero docs):**
- **Sequential minors only, no skipping.** v1.3 → v1.4 is fine; v1.3 → v1.5 is not.
- **No downgrades.** DB migrations are irreversible; Omni refuses to start on an older binary than its schema.
- **Read every release's "Urgent Upgrade Notes"** — required flags can appear mid-path.

**The exact ladder (latest patch per minor, read from the ghcr registry):**
```
v0.42.3 → v0.43.3 → v0.44.1 → v0.45.1 → v0.46.3 → v0.47.1 → v0.48.4 →
v0.49.1 → v0.50.1 → v0.51.0 → v0.52.0 → v1.0.2 → v1.1.5 → v1.2.1 →
v1.3.4 → v1.4.11 ★ → v1.5.11 → v1.6.6 → v1.7.3 ★ → v1.8.2 ★ → v1.9.1 ★ (target)
```
Get the list yourself with:
```bash
crane ls ghcr.io/siderolabs/omni | grep -E '^v[0-9]+\.[0-9]+\.[0-9]+$' | sort -V \
  | awk -F. '{last[$1"."$2]=$0} END{for (k in last) print last[k]}' | sort -V
```

**The gates (★ rungs that need a config change):**
- **v1.4.11** — SQLite migration runs on first startup (BoltDB/file logs → SQLite). Introduces the **required** `--sqlite-storage-path` flag. **CRITICAL: it points at a FILE, not a directory** (`/_out/sqlite/omni.db`), on a persistent mount. **Back up `~/omni` before this hop — the migration is irreversible.** Avoid the buggy `v1.4.0`–`v1.4.5` patches; land on `v1.4.11`.
- **v1.7.3** — EULA acceptance required. Accept via `--eula-accept-name` / `--eula-accept-email` flags OR the `/eula` page in the UI. **Acceptance is stateful** — once accepted it's persisted in `~/omni`, so the flag becomes a no-op and can be dropped on later hops.
- **v1.8.2** — `omnictl cluster template` now restricts `include:` to the template's own directory (`--allowed-dir` to override). N/A here — our template inlines all patches, no includes. Also `--join-tokens-mode=legacyAllowed` default won't start with Talos < 1.6 nodes (we're 1.7.4, fine).

**Per-hop loop (run on `omni-host`):**
```bash
OMNI_DIR=/home/user/omni
OMNI_VER=v0.42.3    # bump this each hop

docker stop omni 2>/dev/null; docker rm -f omni 2>/dev/null
sudo tar czf ~/omni-state-$OMNI_VER-$(date +%F-%H%M).tgz -C /home/user omni   # backup

docker run -d --name omni --restart=unless-stopped \
  --net=host --cap-add=NET_ADMIN --device /dev/net/tun \
  -v $OMNI_DIR/etcd:/_out/etcd \
  -v $OMNI_DIR/sqlite:/_out/sqlite \          # add at/after v1.4
  -v $OMNI_DIR/tls.crt:/tls.crt \
  -v $OMNI_DIR/tls.key:/tls.key \
  -v $OMNI_DIR/omni.asc:/omni.asc \
  ghcr.io/siderolabs/omni:$OMNI_VER \
    --account-id=<YOUR_ACCOUNT_UUID> --name=omni-cluster \
    --sqlite-storage-path=/_out/sqlite/omni.db \   # required at/after v1.4 (FILE, not dir)
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

docker logs -f omni          # watch it come up / run migration cleanly
# re-pin omnictl to match server when it complains of skew (not needed every hop):
omnictl --version
omnictl cluster status omni-cluster    # RUNNING Ready (6/6)
```

**Rung 0 gotchas (all hit for real):**
- **`$PWD` mount trap → key.asc became a directory.** Running `docker run` with `-v $PWD/omni.asc:...` from the wrong directory made Docker *fabricate an empty directory* at the missing source path and mount it, so Omni died with `failed to read private key file '/omni.asc': is a directory`. **Fix: always use absolute `-v /home/user/omni/...` mounts, never `$PWD`.** Find and delete fabricated dirs: `find /home/user -maxdepth 3 -name omni.asc -type d`.
- **The migration never ran on the failed key-load attempts** — Omni dies at key-read *before* touching storage, so those crashes were harmless (no half-migration).
- **Auth0 "Oops, something went wrong" after the jump.** Newer Omni changed its OAuth callback path; the Auth0 SPA app's allowlist still had only the Task 1 value. **Fix:** Auth0 dashboard → app → set **Allowed Callback URLs** to `https://omni.example.com/, https://omni.example.com/callback` (+ Logout/Web Origins as needed). Cluster/CLI were unaffected the whole time — login-only.
- **Stale browser UI after the jump** (buttons unclickable) = cached old front-end assets vs new backend. Hard reload / incognito / clear site data.
- **State survives on disk.** Because `~/omni` holds etcd + sqlite + certs + `omni.asc`, the container is disposable — `docker rm` then re-run on the new tag keeps everything.

---

## PART B — Rungs 1–6: the Talos + k8s ladder (git-managed template)

**The template is the source of truth.** The `kind: Cluster` doc holds the two version lines:
```yaml
kind: Cluster
name: omni-cluster
kubernetes:
  version: v1.30.1
talos:
  version: v1.7.4
```

**The rung loop (repeat 6×). Talos half first, then k8s half:**
```bash
cd ~/homelab-k8s

# --- Half 1: Talos ---
# edit ONLY talos.version to the next minor's latest patch
git diff                      # sanity: only the talos line changed
omnictl cluster template validate -f clusters/omni-cluster/omni-cluster-template.yaml
omnictl cluster template diff     -f clusters/omni-cluster/omni-cluster-template.yaml   # READ IT (git-status-before-commit moment)
omnictl cluster template sync     -f clusters/omni-cluster/omni-cluster-template.yaml
omnictl cluster status omni-cluster   # wait for roll to finish
git commit -am "rung N: Talos <ver>" && git push

# --- Half 2: Kubernetes ---
# edit ONLY kubernetes.version to the next minor's latest patch
git diff
omnictl cluster template validate/diff/sync ...   # same three steps
kubectl get nodes                                 # VERSION shows new k8s
# BOOTSTRAP MANIFEST SYNC (k8s-only extra step — see Part C)
omnictl cluster kubernetes manifest-sync --dry-run=false omni-cluster
git commit -am "rung N: k8s <ver>" && git push
```

**Version pairs used (Talos → then k8s):**
| Rung | Talos | Kubernetes |
|---|---|---|
| 1 | v1.8.4 | v1.31.14 |
| 2 | v1.9.6 | v1.32.13 |
| 3 | v1.10.9 | v1.33.13 |
| 4 | v1.11.6 | v1.34.9 |
| 5 | v1.12.9 | v1.35.6 |
| 6 | v1.13.6 | v1.36.2 |

**What each half does:**
- **Talos bump** = swaps the OS image via A/B partition → **reboots each node**. Omni cordons/drains a node, upgrades, un-cordons, moving to the next only when safe. Control-plane nodes go **one at a time** so etcd quorum (2 of 3) always holds; then workers. Each *worker* reboot drops its OSD → Ceph `HEALTH_WARN` (undersized, **not** data loss) → OSD rejoins → `HEALTH_OK`. Always `--preserve=true`, so `/dev/sdb` (OSD) and `/var/lib/rook` survive.
- **k8s bump** = swaps control-plane component + kubelet images. **No OS reboot** — faster, gentler on Ceph.

---

## PART C — Bootstrap manifest sync (the k8s-only extra step that's easy to miss)

Omni upgrades the k8s control plane + kubelets automatically, but **deliberately does NOT auto-apply the in-cluster bootstrap manifests** (CoreDNS, kube-proxy, flannel, kubeconfig-in-cluster, etc.) — so it never clobbers manual edits. You apply them yourself after each k8s bump. Talos upgrades have no equivalent (no user-editable in-cluster manifest layer to protect).

**Command (the syntax that tripped me up 3×):**
```bash
# preview (dry-run defaults to TRUE — always shows "< dry run, change skipped")
omnictl cluster kubernetes manifest-sync omni-cluster

# APPLY for real — flag BEFORE the positional cluster name, and =false
omnictl cluster kubernetes manifest-sync --dry-run=false omni-cluster
```
- Flag is `--dry-run` (bool, **default true**) → you must pass `--dry-run=false` to write.
- **Put the flag before the cluster name** (Cobra positional-order strictness) or it errors / silently stays dry.
- Wrong spellings that fail: `--no-dry-run`, `kubernetes-manifest-sync` (it's `kubernetes manifest-sync`, a subcommand under `kubernetes`).
- The plain preview **always lists every managed manifest with "skipped"** — that's normal. The signal is the **diff blocks**: diffs present = pending; **no diff blocks = already in sync**.

**What the 1.30 → 1.36 jump actually changed in the manifests** (why this step matters):
- `kube-proxy` image `v1.30.1` → `v1.36.2`, **`--proxy-mode` iptables → nftables**, `lib-modules` hostPath `/lib/modules` → `/usr/lib/modules`
- `coredns` image `v1.11.1` → `v1.14.2`, rewritten Corefile
- `flannel` image `v0.25.1` → `v0.28.5`

Running a 1.36 control plane with 1.30-era kube-proxy/CoreDNS is the "works until it doesn't" state — hence not optional.

---

## PART D — Ceph through the churn

After ~18 node reboots Ceph showed `HEALTH_WARN: 5 mgr modules have recently crashed`. **Benign:** data plane was healthy the whole time (`mon 3 quorum`, `osd 3 up, 3 in`, `pgs active+clean`). It's a **stale crash latch** from mgr restarts during the churn, not a live fault. Clear it:
```bash
kubectl -n rook-ceph exec deploy/rook-ceph-tools -- ceph crash ls           # see what crashed
kubectl -n rook-ceph exec deploy/rook-ceph-tools -- ceph crash archive-all  # acknowledge/clear
kubectl -n rook-ceph exec deploy/rook-ceph-tools -- ceph status             # HEALTH_OK
```
`archive-all` only acknowledges old reports — a still-crashing module would re-report, so it doesn't mask a live problem. Keep a watcher running during the whole task:
```bash
while true; do clear; kubectl -n rook-ceph exec deploy/rook-ceph-tools -- ceph status; sleep 5; done
```

---

## PART E — Git / GitHub workflow used (prod-style)

Worked the whole task as a real change would be landed:
```
branch → edit → validate/diff → sync (apply) → verify healthy → commit (msg = WHY) → push → PR → review own diff → merge → delete branch
```
```bash
git checkout -b task03-talos-k8s-upgrade        # branch per unit of work
# ... rungs, committing + pushing per rung ...
git push -u origin task03-talos-k8s-upgrade     # first push sets upstream
# GitHub: open PR task03-talos-k8s-upgrade -> main, review diff, Merge, Delete branch
git checkout main && git pull               # bring merge commit local
git branch -d task03-talos-k8s-upgrade          # delete local
git push origin --delete task03-talos-k8s-upgrade   # delete remote (or via GitHub UI / branches page)
git fetch --prune                           # drop stale remote-tracking ref
```
Useful visual alias (see the branch/merge graph):
```bash
git config --global alias.lg "log --oneline --graph --all --decorate"
```

**Key distinctions learned:**
- **commit** = record locally; **push** = send to GitHub (remote = durable truth for IaC).
- **PR** = a GitHub *process* (review gate + permanent record + CI hook), not a git command. It *results in* a merge. In GitOps, merging to `main` is what triggers Argo to deploy — here `sync` already deployed, so the PR/merge is the **record**.
- **merge vs rebase** = two ways to integrate: merge preserves true history (adds a merge commit); rebase replays commits linearly (rewrites hashes — never rebase already-pushed/shared commits). GitHub's merge button offers merge-commit / squash / rebase.
- **`omnictl ... sync` vs `kubectl apply`**: `sync` reconciles the **whole** template to the cluster (incl. removing things you deleted from the file); `apply` is **per-object** (won't delete un-referenced things). There is **no** `omnictl cluster template apply`.

---

## KEY LESSONS (do these right next time)

1. **The Talos↔k8s interlock rules the whole route.** Talos version caps max k8s. Bump **Talos first**, then k8s, one minor per step, never skip.
2. **A far-behind self-hosted Omni is its own project.** Sequential minors, no skip, **irreversible** migrations. Back up `~/omni` before the **v1.4** migration hop specifically.
3. **`--sqlite-storage-path` is a FILE path** (`/_out/sqlite/omni.db`) on a persistent mount. And **always use absolute `-v` mounts, never `$PWD`** — Docker fabricates empty dirs for missing sources and mounts them.
4. **Two flag behaviors differ:** `--sqlite-storage-path` is stateless (declare every start, forever); **EULA acceptance is stateful** (persisted in `~/omni`, drop the flag after first accept).
5. **Update Auth0 callback URLs after a big Omni jump** (add `/callback`). It's login-only — cluster/CLI keep working.
6. **Bootstrap manifests aren't auto-applied.** After each k8s bump: `omnictl cluster kubernetes manifest-sync --dry-run=false omni-cluster` — **flag before the cluster name**, dry-run defaults true.
7. **Commit after each rung so the file == the live cluster.** Un-committed edits drift; a lying template makes `diff` confusing and risks pushing a skip.
8. **`diff` before every `sync`** — it's the git-status-before-commit seatbelt; `sync` will delete anything you removed from the template.
9. **Ceph mgr-crash `HEALTH_WARN` after churn is benign** — `ceph crash archive-all` clears the latch.
10. **Keep `omnictl` matched to the Omni server**, re-pin opportunistically (when it warns of skew); mandatory before using `omnictl cluster template` for the Talos rungs.

---

## Conceptual Q&A

**Q: Are the Talos and Kubernetes upgrades independent?**
Deliberately yes — two separate operations on the same machines. A Talos bump swaps the OS (A/B, reboots nodes) and doesn't touch k8s; a k8s bump swaps component images (no reboot). We alternate Talos-then-k8s per rung because the interlock means the Talos bump must lift the version ceiling before the matching k8s bump becomes legal. Never both in one sync — easier to reason about a failure, and Omni validates them against each other anyway.

**Q: Why `sync` and not `apply`?**
Different tools, different scope. `omnictl cluster template sync` reconciles the *whole* cluster to the entire template at once (adds, changes, **and removes** things no longer declared) — declarative, git-as-truth. `kubectl apply` is per-object and never deletes an un-referenced resource. `sync` is for the cluster's own shape; `apply` is for workloads *inside* it. There's no `omnictl cluster template apply`.

**Q: Why do a PR instead of just merging?**
A PR is the review gate + durable record + CI hook around a merge. Solo it feels redundant, but reading your own complete diff catches "why did that change?" moments, and the PR page is a permanent, portfolio-worthy record of the change. Merging locally is fine for throwaway work; the PR earns its keep for documented changes.

**Q: Why was the Omni v1.4 hop the one where backup was non-negotiable?**
It runs an **irreversible** storage-format migration (BoltDB/files → SQLite) on first startup. The other 20 hops are reversible-ish (you can re-run an older tag if the schema hasn't advanced), but once v1.4 migrates, you cannot downgrade — a pre-migration `~/omni` tarball is the only way back.

**Q: Why 3 control-plane nodes, and why does Omni roll them one at a time?**
etcd (the control-plane datastore) needs a **quorum** — a strict majority — to accept writes. 3 members → majority is 2 → the cluster survives losing 1. If Omni rebooted 2 at once, only 1 member would remain, quorum (2) would be lost, and the control plane would go read-only/unavailable until a second member returned. So Omni takes exactly one etcd member down at a time and waits for it to rejoin healthy before the next.
