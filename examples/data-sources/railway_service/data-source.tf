# A service a railway.go file or the dashboard owns.
data "railway_service" "api" {
  environment_id = "00000000-0000-0000-0000-000000000000"
  name           = "api"
}

resource "railway_service" "worker" {
  environment_id = data.railway_service.api.environment_id
  name           = "worker"
  source         = { image = { image = "ghcr.io/acme/worker:1" } }
  variable_references = {
    API_URL = { resource = data.railway_service.api.address, output = "RAILWAY_PRIVATE_DOMAIN" }
  }
}

output "api_start_command" {
  value = try(jsondecode(data.railway_service.api.config).deploy.startCommand, null)
}
