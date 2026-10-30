data "railway_database" "postgres" {
  environment_id = "00000000-0000-0000-0000-000000000000"
  name           = "postgres"
}

output "postgres_id" {
  value = data.railway_database.postgres.railway_id
}
