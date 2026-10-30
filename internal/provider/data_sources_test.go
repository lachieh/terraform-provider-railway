package provider

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
)

// Data sources find resources Terraform does not manage by name.
func TestNodeDataSourcesFindUnmanagedResources(t *testing.T) {
	requireCLI(t)
	fake, server := newFakeBackboard(t)
	seedFromConfig(t, fake, server.URL, lifecycleResources+extendedFunction)

	config := environmentConfig(server.URL, `
data "railway_service" "web" {
  environment_id = local.environment_id
  name           = "web"
}

data "railway_database" "postgres" {
  environment_id = local.environment_id
  name           = "postgres"
}

data "railway_volume" "data" {
  environment_id = local.environment_id
  name           = "postgres-volume"
}

data "railway_bucket" "backups" {
  environment_id = local.environment_id
  name           = "backups"
}

output "bucket_region" {
  value = jsondecode(data.railway_bucket.backups.config).config.region
}
`)
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testProviders,
		Steps: []resource.TestStep{
			{
				Config: config,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("data.railway_service.web", "railway_id", "web"),
					resource.TestCheckResourceAttr("data.railway_service.web", "address", "service.web"),
					resource.TestCheckResourceAttr("data.railway_service.web", "project_id", fakeProjectID),
					resource.TestCheckTypeSetElemAttr("data.railway_service.web", "variable_names.*", "NODE_ENV"),
					resource.TestCheckTypeSetElemAttr("data.railway_service.web", "variable_names.*", "DATABASE_URL"),
					resource.TestCheckResourceAttrWith("data.railway_service.web", "config", func(config string) error {
						var node map[string]any
						if err := json.Unmarshal([]byte(config), &node); err != nil {
							return err
						}
						if _, ok := node["variables"]; ok || strings.Contains(config, "public-production") {
							return fmt.Errorf("config carries variable values: %s", config)
						}
						return nil
					}),
					resource.TestCheckResourceAttr("data.railway_database.postgres", "address", "database.postgres"),
					resource.TestCheckResourceAttr("data.railway_volume.data", "railway_id", "postgres-volume"),
					resource.TestCheckResourceAttr("data.railway_bucket.backups", "railway_id", "backups"),
					resource.TestCheckOutput("bucket_region", "sjc"),
				),
			},
		},
	})
}

func TestNodeDataSourceReportsAMissingResource(t *testing.T) {
	requireCLI(t)
	_, server := newFakeBackboard(t)
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testProviders,
		Steps: []resource.TestStep{{
			Config: environmentConfig(server.URL, `
data "railway_service" "missing" {
  environment_id = local.environment_id
  name           = "missing"
}
`),
			ExpectError: regexp.MustCompile(`has no service\.missing`),
		}},
	})
}

// Bucket credentials reach a write-only attribute without entering the plan
// or state.
func TestBucketCredentialsStayOutOfState(t *testing.T) {
	requireCLI(t)
	fake, server := newFakeBackboard(t)
	config := environmentConfig(server.URL, `
resource "railway_bucket" "assets" {
  environment_id = local.environment_id
  name           = "assets"
  region         = "sjc"
}

ephemeral "railway_bucket_credentials" "assets" {
  environment_id = local.environment_id
  bucket_id      = railway_bucket.assets.railway_id
}

resource "railway_service" "uploader" {
  environment_id = local.environment_id
  name           = "uploader"
  source         = { image = { image = "ghcr.io/acme/uploader:1" } }
  secret_variables = {
    S3_ACCESS_KEY_ID     = ephemeral.railway_bucket_credentials.assets.access_key_id
    S3_SECRET_ACCESS_KEY = ephemeral.railway_bucket_credentials.assets.secret_access_key
    S3_BUCKET            = ephemeral.railway_bucket_credentials.assets.bucket_name
  }
}
`)
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testProviders,
		Steps: []resource.TestStep{
			{
				Config: config,
				Check: func(state *terraform.State) error {
					if got := fake.variable("service.uploader", "S3_SECRET_ACCESS_KEY"); got != "secret-assets" {
						return fmt.Errorf("S3_SECRET_ACCESS_KEY = %q", got)
					}
					if got := fake.variable("service.uploader", "S3_BUCKET"); got != "assets-x1y2" {
						return fmt.Errorf("S3_BUCKET = %q", got)
					}
					for _, module := range state.Modules {
						for name, rs := range module.Resources {
							for key, value := range rs.Primary.Attributes {
								if strings.Contains(value, "secret-assets") {
									return fmt.Errorf("%s.%s holds the secret key", name, key)
								}
							}
						}
					}
					return nil
				},
			},
			{Config: config, PlanOnly: true},
		},
	})
}
