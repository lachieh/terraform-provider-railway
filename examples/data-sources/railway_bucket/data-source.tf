data "railway_bucket" "assets" {
  environment_id = "00000000-0000-0000-0000-000000000000"
  name           = "assets"
}

output "bucket_region" {
  value = jsondecode(data.railway_bucket.assets.config).config.region
}
