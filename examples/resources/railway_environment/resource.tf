resource "railway_environment" "preview" {
  project_id = "00000000-0000-0000-0000-000000000000"
  name       = "preview"
}

resource "railway_service" "web" {
  environment_id = railway_environment.preview.id
  name           = "web"
  source         = { image = { image = "traefik/whoami:v1.10" } }
}
