# Task 4: Terraform (IaC) — Cloudflare DNS as the first real target

> **Goal:** Learn Terraform against the real stack (not toy examples) by adopting an existing, hand-created piece of infrastructure under declarative management. Bank the transferable core — providers, state, `plan`/`apply`, and `import` (brownfield adoption) — as the foundation for later Terraform-managed tasks (GitHub Actions, Datadog, Vault).
>
> **Status:** ✅ Complete. Terraform installed; `cloudflare` provider v5 wired; existing `omni.example.com` A record imported into state (0 changes); modify loop proven (added a record comment, verified via API). All git-managed, merged to `main` via PR #2 (squash), branch deleted.

---

## The key decision this task settled: where Terraform sits vs. the Omni cluster template

The open question going in was "do Terraform and my git-managed Omni cluster template overlap or conflict?" Answer: **they own different layers, and the golden rule is one resource, one owner.**

Layer cake (top = closest to workloads):

```
Workloads (Rook-Ceph, Argo, nginx)        <- Helm / kubectl / Argo
Kubernetes + Talos OS config              <- Omni cluster template (omnictl sync)   [already git-managed]
Machines registered in Omni               <- Omni server / SideroLink
Compute substrate (VMs, disks, CPU, NICs) <- govc (Task 2, imperative) / candidate for IaC
Hypervisor (bare metal)        <- physical
```

**Important course-correction:** the "Omni has a Terraform provider" idea was imprecise.
- There is **no production-ready official Omni Terraform provider** (community ones are pre-alpha).
- The **official Talos Terraform provider** exists, but it's for the *non-Omni* DIY path — using it means bypassing the Omni cluster template (direct conflict).
- **Omni Infrastructure Providers** (bare-metal, Proxmox, KubeVirt, vSphere, etc.) are **not Terraform** — they're Omni's *native* way to own the substrate layer. Sidero's own line: "No Terraform for the OS."

**Consequence for learning:** don't fight Omni over the vSphere VM layer. Instead, point Terraform at things Omni will never own — DNS, identity (Auth0), later Vault/GitHub/Datadog. Cloudflare DNS was chosen as the smallest possible **real** first target.

**In an infra-provider shop, Terraform doesn't disappear — it relocates up/around the substrate** to own the surrounding SaaS/identity/cloud plane. Same tool, the factory (Omni) just took the slab-pouring (machine provisioning) job away from it.

---

## Core concepts (the WHY)

**Terraform is a generic "read a text file → call an API → make reality match" engine.** It has zero built-in knowledge of any platform. All the "how to talk to X" logic lives in a **provider** [plugin]. Cloudflare/vSphere/Auth0 are just APIs behind pluggable provider adapters — you learn the tool, not the target.

**The one genuinely new concept vs. the Omni template: state.** Three things must agree:
- **Desired state** = your HCL (`dns.tf`), in git.
- **Actual state** = the real thing (the live Cloudflare record).
- **State file** (`terraform.tfstate`) = Terraform's ledger of what it manages + real-world IDs. **NEW piece of mental furniture** — the Omni template model doesn't have a client-side ledger (Omni's server holds truth).

**`plan` / `apply` ≈ `omnictl cluster template diff` / `sync`.** Same seatbelt discipline: read the diff, then commit. `plan` is **read-only** (refresh + compare, never writes state). `apply` is the **only** command that writes to the state file / changes real infra.

**Terraform is NOT a controller.** It's a client-side IaC tool that reconciles **once, on demand** when you run `apply`, then exits. A [controller] like Argo CD or the Omni server runs the reconcile loop **continuously**. (Same as `omnictl sync` being on-demand, not a daemon.)

**Terraform vs OpenTofu:** Terraform is now BSL-licensed; OpenTofu is the CLI-compatible OSS fork (Linux Foundation. (Practical side effect: Homebrew's core formula is frozen — install from the HashiCorp tap.)

---

## Environment Facts

| Thing | Value |
|---|---|
| Terraform install | `brew install hashicorp/tap/terraform` (core formula frozen post-BSL) |
| Provider | `cloudflare/cloudflare` `~> 5` (installed v5.22.0) |
| Managed resource | `cloudflare_dns_record.omni` |
| Record adopted | `omni.example.com` A → `192.0.2.60`, `proxied = false` (grey cloud) |
| Cloudflare zone | `example.com` |
| Credential | scoped API token (Zone:DNS:Edit, single zone) via `CLOUDFLARE_API_TOKEN` env |
| Zone ID input | `TF_VAR_cloudflare_zone_id` env |
| Repo / dir | `example-user/homelab-k8s`, new top-level `terraform/` |
| Branch | `task04-terraform` → PR #2 → squash-merged to `main`, branch deleted |
| State backend | default **local** (`terraform.tfstate` on disk, git-ignored) |

**Network note:** friendly to the locked-down VLAN. `terraform init` pulls the provider from the Terraform Registry over **TCP/443**; `apply` hits the Cloudflare API over **443**. No outbound UDP / external DNS / NTP dependency. (Run from the Mac, which isn't subject to the lab VLAN lockdown anyway.)

---

## Part 1 — Branch + install

```bash
cd ~/homelab-k8s
git checkout main && git pull
git checkout -b task04-terraform
mkdir -p terraform && cd terraform

brew tap hashicorp/tap
brew install hashicorp/tap/terraform
terraform version
```

## Part 2 — Scaffold (four files)

`versions.tf`
```hcl
terraform {
  required_version = ">= 1.5.0"
  required_providers {
    cloudflare = {
      source  = "cloudflare/cloudflare"
      version = "~> 5"
    }
  }
}
```

`providers.tf` — **no token here**; the provider auto-reads `CLOUDFLARE_API_TOKEN` from env, so no secret ever touches a file.
```hcl
provider "cloudflare" {}
```

`variables.tf`
```hcl
variable "cloudflare_zone_id" {
  description = "Zone ID for example.com"
  type        = string
}
```

`dns.tf` — the Task 1 hand-made record, now as a [resource].
```hcl
resource "cloudflare_dns_record" "omni" {
  zone_id = var.cloudflare_zone_id
  name    = "omni.example.com"
  type    = "A"
  content = "192.0.2.60"
  proxied = false
  ttl     = 1   # 1 = automatic
}
```

## Part 3 — .gitignore (BEFORE init)

```gitignore
# Terraform
**/.terraform/*
*.tfstate
*.tfstate.*
*.tfvars
crash.log
# NOT ignored — COMMIT this (pins provider versions + hashes, like go.sum):
!.terraform.lock.hcl
```

## Part 4 — Credential + init

Create the token in Cloudflare dashboard: profile → API Tokens → **Edit zone DNS** template → scope to **Specific zone → example.com**. Token is shown **once**.

```bash
export CLOUDFLARE_API_TOKEN='<token>'          # keep out of screenshots/repos
export TF_VAR_cloudflare_zone_id='<zone id>'   # not a secret
terraform init
```

`init` = set up the **working directory**: initialize backend (local), download+install the provider, write `.terraform.lock.hcl`. **It does NOT call Cloudflare** — the only network call is to the Registry to fetch the provider. (init installs the adapter; it doesn't use it yet.)

## Part 5 — Import (adopt the existing record) — the core skill

The record already exists and state is empty, so **import** (don't `apply`, which would try to create a duplicate).

Get the record ID (token's DNS-edit scope includes read/list):
```bash
curl -s "https://api.cloudflare.com/client/v4/zones/$TF_VAR_cloudflare_zone_id/dns_records" \
  -H "Authorization: Bearer $CLOUDFLARE_API_TOKEN" | jq '.result[] | {id, name, type, content}'
```

Config-driven import block (modern; previewed in `plan`) — temporary `import.tf`:
```hcl
import {
  to = cloudflare_dns_record.omni
  id = "${var.cloudflare_zone_id}/<RECORD_ID>"   # format: zone_id/record_id
}
```

```bash
terraform plan     # want: "1 to import, 0 to add, 0 to change, 0 to destroy"
terraform apply    # type yes; writes state only — does NOT change DNS
terraform plan     # want: "No changes. Your infrastructure matches the configuration."
rm import.tf       # one-time instruction, delete after it lands
terraform plan     # still "No changes"
```

`No changes` = desired + actual + state all agree = record fully under management. **This "plan-until-clean" is the trophy.**

## Part 6 — Modify loop (prove ownership, not just adoption)

Add to `dns.tf` inside the resource:
```hcl
  comment = "Managed by Terraform"
```
```bash
terraform plan     # "1 to change"
terraform apply    # writes to Cloudflare via API
```

**Verify at the API, not the dashboard** (the dashboard hides comments in the list view — only a hover icon / Edit panel shows them, and it caches):
```bash
curl -s "https://api.cloudflare.com/client/v4/zones/$TF_VAR_cloudflare_zone_id/dns_records" \
  -H "Authorization: Bearer $CLOUDFLARE_API_TOKEN" | jq '.result[] | {name, type, content, comment}'
```
Confirmed `"comment": "Managed by Terraform"` from the API. Arc complete: **import = adopt, edit = own.**

---

## Git / GitHub workflow used (prod-style)

```
branch -> scaffold -> init -> import -> apply (state only) -> plan (No changes)
       -> commit (why-body) -> push -u -> PR -> review own diff
       -> second change (comment) -> commit -> push (auto-added to same PR)
       -> merge (squash) -> delete branch -> prune
```

```bash
git status                              # READ before staging (git's equivalent of `plan`)
# confirm NO terraform.tfstate / .terraform/ appear (ignore rules working)
git add terraform/
git commit -m "Task 4: adopt existing Cloudflare omni A record into Terraform

Import brought the hand-created omni.example.com A record under Terraform
management (0 changes) rather than recreating it, so DNS edits are now
previewable and version-controlled."
git push -u origin task04-terraform   # -u sets upstream (first push of a new branch)

# ... second commit (comment edit) ...
git push                                # bare push; GitHub auto-adds commit to open PR

# On GitHub: review own diff, then Merge (SQUASH chosen), Delete branch
git checkout main && git pull
git branch -d task04-terraform
git push origin --delete task04-terraform
git fetch --prune
git lg
```

**Merge methods:** squash (collapse to one clean commit on `main`; PR keeps detail — chosen here), merge-commit (keeps commits + visible fork/join in `git lg`), rebase (linear, no merge commit). PR = review gate + durable record + CI hook; the self-review is the point, the merge is just the button after it.

---

## KEY LESSONS / GOTCHAS (do these right next time)

1. **One resource, one owner.** Never let two reconcilers (e.g. a community Omni TF provider AND the omnictl template) manage the same object — they fight over "drift." Terraform owns the substrate/SaaS layers; the Omni template owns the cluster.
2. **In an infra-provider shop, Terraform moves up, not away.** Don't codify the vSphere VMs (Omni's job); codify DNS/identity/secrets/CI around them.
3. **`init` is offline-to-Cloudflare** — it only fetches the provider from the Registry. Providers get installed at `init`; used at `apply`.
4. **`import` for anything that already exists.** Applying fresh with empty state tries to *create* and duplicates/errors. Import → `plan`-until-clean → then manage.
5. **Only `apply` writes state.** `plan` is a read-only seatbelt — run it freely.
6. **Commit `.terraform.lock.hcl`; ignore `terraform.tfstate` + `.terraform/`.** The lock file pins provider version+hash (like go.sum) — people wrongly ignore it. The **state file contains secrets/IPs in plaintext** → never commit, never screenshot; it's in the "never publish" bucket with `talosconfig`/`*.asc`.
7. **No secret in HCL** — provider reads `CLOUDFLARE_API_TOKEN` from env; zone ID via `TF_VAR_`. (Task 8 endgame: token comes from Vault instead of `export`.)
8. **Rotate a credential the moment it leaves your control** (e.g. visible in an uploaded screenshot). Small blast radius ≠ skip it; it's the reflex Vault is built around. (An `export` in your own shell is fine — local.)
9. **Verify at the API, not the UI.** Dashboard hides the DNS comment in list view + caches. `apply` success + API confirm = believe it. (Same as "trust the node's report, not vCenter's" from Task 2.)
10. **`git lg` is only as good as your commit messages** — subject (imperative, ~50 chars) + why-body.

---

## Conceptual Q&A

**Q: Do Terraform and the Omni cluster template overlap/conflict?**
No, if you keep them on separate layers. The template owns the cluster (k8s + Talos config); Terraform owns the substrate/SaaS around it. Conflict only happens if both manage the *same* resource — then each sees the other's changes as drift. One resource, one owner.

**Q: Why import instead of apply?**
The record already existed (made by hand in Task 1). `apply` with empty state means "create," which duplicates or errors. `import` tells Terraform "adopt this existing thing," bringing it under management with zero change to the live record. Brownfield adoption is the single most valuable real-world Terraform skill because almost all adoption is brownfield.

**Q: What's the state file for, and why is it new?**
It's Terraform's client-side ledger of what it manages and the real-world IDs, so it can compute diffs. The Omni-template model didn't need one (Omni's server holds truth). State is the third thing that must agree with desired (HCL) and actual (real infra).

**Q: Is Terraform a controller?**
No — it's a one-shot, on-demand reconciler that runs at `apply` and exits. Controllers (Argo, Omni server) run the reconcile loop continuously.

**Q: The comment didn't show in the dashboard — did the apply fail?**
No. `apply` reported success = the API accepted the write; a direct API `curl` confirmed the comment. The dashboard just doesn't surface comments in the list view (hover icon / Edit panel only) and caches. Trust the API over the UI.
