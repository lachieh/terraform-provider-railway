variable "environment_id" {
  type = string
}

resource "railway_bucket" "backups" {
  environment_id = var.environment_id
  name           = "backups"
  region         = "sjc"
}
