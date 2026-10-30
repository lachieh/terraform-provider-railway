resource "railway_service" "web" {
  environment_id = "00000000-0000-0000-0000-000000000000"
  name           = "web"
  source         = { image = { image = "traefik/whoami:v1.10" } }

  # Fail the apply if the deployment it starts does not succeed.
  wait_for_deployment = { timeout = "15m" }
}

data "railway_deployment" "web" {
  environment_id = railway_service.web.environment_id
  service_id     = railway_service.web.railway_id
}

output "web_status" {
  value = data.railway_deployment.web.status
}
