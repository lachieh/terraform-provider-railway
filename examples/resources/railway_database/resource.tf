variable "environment_id" {
  type = string
}

resource "railway_database" "postgres" {
  environment_id = var.environment_id
  name           = "postgres"
  engine         = "postgres"
  region         = "us-west2"

  deploy = {
    draining_seconds = 0
    overlap_seconds  = 0
  }
}
