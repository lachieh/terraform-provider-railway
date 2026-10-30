variable "environment_id" {
  type = string
}

variable "stripe_key" {
  type      = string
  sensitive = true
  ephemeral = true
}

resource "railway_volume" "data" {
  environment_id = var.environment_id
  name           = "web-data"
  size_mb        = 5000
}

resource "railway_database" "postgres" {
  environment_id = var.environment_id
  name           = "postgres"
  engine         = "postgres"
}

resource "railway_service" "web" {
  environment_id = var.environment_id
  name           = "web"

  source = {
    github = {
      repo         = "acme/web"
      branch       = "main"
      check_suites = true
    }
  }

  build = {
    builder         = "DOCKERFILE"
    dockerfile_path = "Dockerfile"
  }

  deploy = {
    healthcheck_path    = "/healthz"
    healthcheck_timeout = 30
    draining_seconds    = 0
  }

  replicas = {
    "us-west2" = 1
  }

  custom_domains = {
    "app.example.com" = { port = 8080 }
  }

  variables = {
    NODE_ENV = "production"
  }

  # Set outside Terraform; Railway keeps the current values.
  preserve_variables = ["SESSION_SECRET"]

  # Sent to Railway, never stored in state. Change the version to resend.
  secret_variables         = { STRIPE_KEY = var.stripe_key }
  secret_variables_version = "1"

  variable_references = {
    DATABASE_URL = { resource = railway_database.postgres.address, output = "DATABASE_URL" }
  }

  volume_mounts = {
    "/data" = railway_volume.data.name
  }
}
