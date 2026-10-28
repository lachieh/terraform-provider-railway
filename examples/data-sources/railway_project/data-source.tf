data "railway_project" "app" {
  id = "00000000-0000-0000-0000-000000000000"
}

output "production_environment_id" {
  value = data.railway_project.app.environments["production"]
}
