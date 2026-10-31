# Railway IaC Provider

A Terraform and OpenTofu provider that manages Railway environments through Railway's Infrastructure as Code engine. It replaces `railway config plan` and `railway config apply` with `terraform plan` and `terraform apply`.

Each resource is one node of an environment's IaC graph. The provider builds the node with [`github.com/railwayapp/railway-go-sdk`](https://github.com/railwayapp/railway-go-sdk), diffs it against the live environment with a Go port of the Railway CLI's engine (`internal/iac`), has Railway preview the ChangeSet, and applies it with `environmentApplyChangeSet`.

```hcl
terraform {
  required_providers {
    railway = {
      source = "lachieh/railway"
    }
  }
}

provider "railway" {}

resource "railway_service" "web" {
  environment_id = var.environment_id
  name           = "web"

  source = {
    github = { repo = "acme/web", branch = "main" }
  }
  deploy = {
    healthcheck_path = "/healthz"
  }
  variables          = { NODE_ENV = "production" }
  preserve_variables = ["SESSION_SECRET"]
}
```

Resources: `railway_service`, `railway_database`, `railway_volume`, `railway_bucket`, `railway_function`, `railway_group`, and, outside the graph, `railway_project` and `railway_environment`. Data sources look up environments, projects, services, databases, volumes, buckets and deployments, and `ephemeral.railway_bucket_credentials` opens a bucket's S3 credentials without storing them. Full reference: [`docs/`](docs/index.md).

## Authentication

Set exactly one credential:

- `token` or `RAILWAY_TOKEN`: an account or workspace token, sent as `Authorization: Bearer`.
- `project_token` or `RAILWAY_PROJECT_TOKEN`: a project token, sent as `Project-Access-Token`.

## Development

```sh
make check      # gofmt, vet, golangci-lint, race tests, examples, docs drift
make testacc    # acceptance tests against Railway; see CONTRIBUTING.md
```

The unit tests drive the real `tofu` or `terraform` binary against an in-memory Railway environment (`internal/provider/fake_backboard_test.go`). See [CONTRIBUTING.md](CONTRIBUTING.md) for local overrides and the release process.

## Verifying a release

Every release archive and checksum file carries a GitHub build provenance attestation, and the checksum file is GPG-signed for the Terraform Registry:

```sh
gh attestation verify terraform-provider-railway_0.2.0_linux_amd64.zip \
  --repo lachieh/terraform-provider-railway
```

## License

MPL-2.0. `internal/iac` is ported from [railwayapp/cli](https://github.com/railwayapp/cli) under MIT; see [NOTICE](NOTICE).
