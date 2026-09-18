variable "cloudflare_zone_id" {
  description = "Zone ID for example.com"
  type        = string   # supplied via TF_VAR_cloudflare_zone_id (not a secret)
}
