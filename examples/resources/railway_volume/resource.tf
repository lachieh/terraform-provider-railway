variable "environment_id" {
  type = string
}

resource "railway_volume" "postgres" {
  environment_id      = var.environment_id
  name                = "postgres-volume"
  region              = "us-west2"
  size_mb             = 50000
  allow_online_resize = true
  usage_alerts        = [80, 95, 100]
}
