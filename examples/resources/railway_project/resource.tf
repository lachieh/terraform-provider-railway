resource "railway_project" "app" {
  name                     = "app"
  description              = "Managed by Terraform"
  default_environment_name = "production"

  lifecycle {
    prevent_destroy = true
  }
}

resource "railway_environment" "staging" {
  project_id = railway_project.app.id
  name       = "staging"
}

resource "railway_service" "web" {
  for_each = {
    production = railway_project.app.default_environment_id
    staging    = railway_environment.staging.id
  }
  environment_id = each.value
  name           = "web"
  source         = { image = { image = "traefik/whoami:v1.10" } }
}
