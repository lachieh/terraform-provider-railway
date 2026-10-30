resource "railway_bucket" "assets" {
  environment_id = "00000000-0000-0000-0000-000000000000"
  name           = "assets"
  region         = "sjc"
}

ephemeral "railway_bucket_credentials" "assets" {
  environment_id = railway_bucket.assets.environment_id
  bucket_id      = railway_bucket.assets.railway_id
}

# The credentials reach Railway as write-only secrets and never enter the
# plan or state. A service could reference the bucket's variables directly;
# this suits a service that must hold a copy, or another provider.
resource "railway_service" "uploader" {
  environment_id = railway_bucket.assets.environment_id
  name           = "uploader"
  source         = { image = { image = "ghcr.io/acme/uploader:1" } }
  secret_variables = {
    S3_ENDPOINT          = ephemeral.railway_bucket_credentials.assets.endpoint
    S3_BUCKET            = ephemeral.railway_bucket_credentials.assets.bucket_name
    S3_ACCESS_KEY_ID     = ephemeral.railway_bucket_credentials.assets.access_key_id
    S3_SECRET_ACCESS_KEY = ephemeral.railway_bucket_credentials.assets.secret_access_key
  }
}
