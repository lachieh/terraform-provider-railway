# Contributing

## Development setup

Requirements: the Go version in `go.mod`, [OpenTofu](https://opentofu.org) (`tofu`), and [golangci-lint](https://golangci-lint.run) v2.15.0 (`go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.15.0`). Terraform is optional and only matters for the acceptance matrix.

Build the provider into a local directory for `dev_overrides`:

```sh
make install
```

This writes `terraform-provider-railway-iac-sdk` to `~/.local/share/terraform-provider-railway-iac-sdk/dev`. Override the directory with `make install DEV_DIR=/some/path`.

### dev_overrides

OpenTofu: add this to `~/.tofurc`, or to a file named by `TF_CLI_CONFIG_FILE`.

```hcl
provider_installation {
  dev_overrides {
    "tambo-ai/railway-iac-sdk" = "/home/you/.local/share/terraform-provider-railway-iac-sdk/dev"
  }
  direct {}
}
```

Terraform: use the same block in `~/.terraformrc`. Replace the path with your `DEV_DIR`. With `dev_overrides`, skip `init` for this provider and run `plan` or `apply` directly. The CLI prints a warning that the provider is overridden.

Configurations declare the provider as:

```hcl
terraform {
  required_providers {
    railway = {
      source = "tambo-ai/railway-iac-sdk"
    }
  }
}
```

## Tests

Unit tests need no credentials:

```sh
make test        # go test ./...
make test-race   # go test -race ./...
```

Acceptance tests create real resources in a Railway account and need a token:

```sh
export RAILWAY_ACC_TOKEN=...
export TF_ACC_TERRAFORM_PATH="$(command -v tofu)"   # or terraform
make testacc     # TF_ACC=1, 120 minute timeout
```

CI runs them against OpenTofu and Terraform when the `RAILWAY_ACC_TOKEN` repository secret exists. Pull requests from forks skip them.

## Checks

Run the full gate before opening a pull request:

```sh
make check
```

It runs `fmt-check`, `vet`, `lint`, `test-race`, `examples-check` and `docs-check`. `docs-check` runs `go generate ./...` and fails if anything under `docs/` or `examples/` changes, so run `make generate` and commit the result when you change a schema or an example.

## Releasing

1. Merge the change to `main` and confirm CI is green.
2. Tag the commit and push the tag:

   ```sh
   git tag v1.2.3
   git push origin v1.2.3
   ```

3. The Release workflow checks that the tag points at a commit on `main`, runs `make check`, builds with GoReleaser, signs `SHA256SUMS` with the repository GPG key, attests build provenance for every archive, the checksum file and the manifest, then verifies one attestation before it finishes.

Required repository secrets: `GPG_PRIVATE_KEY` and `GPG_PASSPHRASE`. The matching public key must be registered with the Terraform Registry. The workflow uses the built-in `GITHUB_TOKEN` for the release and the attestations.

## Verifying a release

Download the archive for your platform, `SHA256SUMS`, `SHA256SUMS.sig` and the manifest from the GitHub release.

Build provenance:

```sh
gh attestation verify terraform-provider-railway-iac-sdk_1.2.3_linux_amd64.zip \
  --repo tambo-ai/terraform-provider-railway-iac-sdk
```

GPG signature on the checksums, then the archive against the checksums:

```sh
gpg --verify terraform-provider-railway-iac-sdk_1.2.3_SHA256SUMS.sig \
  terraform-provider-railway-iac-sdk_1.2.3_SHA256SUMS
sha256sum --check --ignore-missing terraform-provider-railway-iac-sdk_1.2.3_SHA256SUMS
```

Import the public key first with `gpg --import` if you have not verified a release before.
