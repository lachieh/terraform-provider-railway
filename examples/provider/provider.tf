terraform {
  required_providers {
    railway = {
      source = "lachieh/railway"
    }
  }
}

# Reads RAILWAY_TOKEN or RAILWAY_PROJECT_TOKEN when no token is set here.
provider "railway" {}
