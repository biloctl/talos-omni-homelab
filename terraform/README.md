# Terraform

Infrastructure-as-code for the SaaS/DNS layer that sits *around* the cluster. The Omni
cluster template owns the cluster itself — **one resource, one owner** — so Terraform stays
out of the cluster's lane and manages the things Omni never will (DNS, and later
identity/secrets/CI).

- **Target:** a Cloudflare DNS `A` record (`omni.example.com`), adopted into Terraform via
  `import` (brownfield) rather than recreated — the highest-value real-world Terraform skill.
- **Credentials:** the provider reads `CLOUDFLARE_API_TOKEN` from the environment; the zone
  ID comes from `TF_VAR_cloudflare_zone_id`. No secret is ever written to a file.
- **State:** `terraform.tfstate` is git-ignored — it holds real IDs/values in plaintext and
  is never published. `.terraform.lock.hcl` (provider pins) is the file you *do* commit.

Full walkthrough — state, `plan`/`apply`, `import`, plan-until-clean — in
[`../docs/tasks/task04-terraform.md`](../docs/tasks/task04-terraform.md).
