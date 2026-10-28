data "railway_environment" "this" {
  id = "00000000-0000-0000-0000-000000000000"
}

output "session_label" {
  value = provider::railway::random_string(data.railway_environment.this.name, "session", 16)
}
