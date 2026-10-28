variable "environment_id" {
  type = string
}

resource "railway_group" "data" {
  environment_id = var.environment_id
  name           = "data"
  color          = "#0f766e"
}

resource "railway_bucket" "backups" {
  environment_id = var.environment_id
  name           = "backups"
  group          = railway_group.data.name
}
