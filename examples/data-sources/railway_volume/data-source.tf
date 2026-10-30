data "railway_volume" "data" {
  environment_id = "00000000-0000-0000-0000-000000000000"
  name           = "data"
}

output "volume_size_mb" {
  value = jsondecode(data.railway_volume.data.config).config.sizeMB
}
