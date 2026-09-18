# The hand-created omni.example.com A record, adopted into Terraform via `import`
# (brownfield), then managed declaratively.
resource "cloudflare_dns_record" "omni" {
  zone_id = var.cloudflare_zone_id
  name    = "omni.example.com"
  type    = "A"
  content = "192.0.2.60"
  proxied = false          # grey cloud — private IP can't be proxied
  ttl     = 1              # 1 = automatic
  comment = "Managed by Terraform"
}
