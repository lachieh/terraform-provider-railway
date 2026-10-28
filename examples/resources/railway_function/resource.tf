variable "environment_id" {
  type = string
}

resource "railway_function" "hook" {
  environment_id = var.environment_id
  name           = "hook"
  code           = "export default { fetch() { return new Response(\"ok\") } }"

  shared_variables = {
    WEBHOOK_SECRET = "WEBHOOK_SECRET"
  }
}
