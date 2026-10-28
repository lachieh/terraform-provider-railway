package provider

import (
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"slices"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/providerserver"
	"github.com/hashicorp/terraform-plugin-go/tfprotov6"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/terraform"

	"github.com/tambo-ai/terraform-provider-railway-iac-sdk/internal/iac"
)

var testProviders = map[string]func() (tfprotov6.ProviderServer, error){
	"railway": providerserver.NewProtocol6WithError(New("test")()),
}

// requireCLI points terraform-plugin-testing at a local OpenTofu or
// Terraform binary, or skips when neither is installed.
func requireCLI(t *testing.T) {
	t.Helper()
	if os.Getenv("TF_ACC_TERRAFORM_PATH") != "" {
		return
	}
	names := []string{"tofu", "terraform"}
	if only := os.Getenv("RAILWAY_TEST_CLI"); only != "" {
		names = []string{only}
	}
	for _, name := range names {
		if path, err := exec.LookPath(name); err == nil && exec.Command(path, "version").Run() == nil {
			t.Setenv("TF_ACC_TERRAFORM_PATH", path)
			if name == "tofu" {
				t.Setenv("TF_ACC_PROVIDER_HOST", "registry.opentofu.org")
			}
			return
		}
	}
	if os.Getenv("RAILWAY_REQUIRE_CLI") != "" {
		t.Fatalf("none of %v is installed", names)
	}
	t.Skip("neither tofu nor terraform is installed")
}

func environmentConfig(endpoint, body string) string {
	return fmt.Sprintf(`
provider "railway" {
  token    = "test-token"
  endpoint = %q
}

locals {
  environment_id = %q
}
%s`, endpoint, fakeEnvironmentID, body)
}

const lifecycleResources = `
resource "railway_volume" "data" {
  environment_id      = local.environment_id
  name                = "postgres-volume"
  size_mb             = 50000
  region              = "us-west2"
  allow_online_resize = true
  usage_alerts        = [80, 95, 100]
}

resource "railway_database" "postgres" {
  environment_id = local.environment_id
  name           = "postgres"
  engine         = "postgres"
  region         = "us-west2"
}

resource "railway_service" "web" {
  environment_id = local.environment_id
  name           = "web"

  source = {
    github = {
      repo         = "acme/web"
      check_suites = false
    }
  }
  build = {
    builder         = "DOCKERFILE"
    dockerfile_path = "Dockerfile"
  }
  deploy = {
    healthcheck_path    = "/healthz"
    healthcheck_timeout = 30
  }
  replicas = { "us-west2" = 1 }
  variables = {
    NODE_ENV = "public-production"
  }
  variable_references = {
    DATABASE_URL = { resource = "database.postgres", output = "DATABASE_URL" }
  }
  volume_mounts = {
    "/data" = railway_volume.data.name
  }

  depends_on = [railway_database.postgres]
}

resource "railway_bucket" "backups" {
  environment_id = local.environment_id
  name           = "backups"
  region         = "sjc"
}
`

func TestLifecycle(t *testing.T) {
	requireCLI(t)
	fake, server := newFakeBackboard(t)
	config := environmentConfig(server.URL, lifecycleResources)

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testProviders,
		CheckDestroy: func(*terraform.State) error {
			if left := fake.addresses(); len(left) > 0 {
				return fmt.Errorf("resources left in the environment: %v", left)
			}
			return nil
		},
		Steps: []resource.TestStep{
			{
				Config: config,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("railway_service.web", "id", fakeEnvironmentID+"/service.web"),
					resource.TestCheckResourceAttr("railway_service.web", "railway_id", "web"),
					resource.TestCheckResourceAttr("railway_service.web", "project_id", fakeProjectID),
					resource.TestCheckResourceAttr("railway_service.web", "live_changes.#", "0"),
					resource.TestCheckResourceAttr("railway_volume.data", "railway_id", "postgres-volume"),
					func(*terraform.State) error {
						return checkChangeSetsScoped(fake.changeSets())
					},
				),
			},
			// The same configuration plans nothing: the provider's view of
			// the applied environment converges.
			{Config: config, PlanOnly: true},
			// An edit made outside Terraform is drift: refresh reports it in
			// live_changes and the apply puts the setting back.
			{
				PreConfig: func() {
					fake.drift("service.web", func(node map[string]any) {
						node["deploy"].(map[string]any)["healthcheckPath"] = "/changed"
					})
				},
				Config: config,
				Check: func(*terraform.State) error {
					changeSets := fake.changeSets()
					last := changeSets[len(changeSets)-1]
					if len(last.Changes) != 1 || iac.FieldStr(last.Changes[0], "address") != "service.web" {
						return fmt.Errorf("drift repair applied %v", last.Changes)
					}
					return nil
				},
			},
			{Config: config, PlanOnly: true},
			{
				ResourceName:  "railway_bucket.backups",
				ImportState:   true,
				ImportStateId: fakeEnvironmentID + "/bucket.backups",
				ImportStateCheck: func(states []*terraform.InstanceState) error {
					if len(states) != 1 || states[0].Attributes["railway_id"] != "backups" || states[0].Attributes["name"] != "backups" {
						return fmt.Errorf("unexpected import state: %v", states)
					}
					return nil
				},
			},
		},
	})
}

// Every ChangeSet names only the resource that sent it, declares it, and
// carries no change for any other address.
func checkChangeSetsScoped(changeSets []iac.ChangeSet) error {
	if len(changeSets) != 4 {
		return fmt.Errorf("want one ChangeSet per resource, got %d", len(changeSets))
	}
	for _, changeSet := range changeSets {
		if len(changeSet.Declared) != 1 {
			return fmt.Errorf("declared %v", changeSet.Declared)
		}
		for _, change := range changeSet.Changes {
			if address := iac.FieldStr(change, "address"); address != changeSet.Declared[0] {
				return fmt.Errorf("ChangeSet for %s carries a change for %s", changeSet.Declared[0], address)
			}
		}
	}
	return nil
}

func TestCreateRefusesExistingResource(t *testing.T) {
	requireCLI(t)
	fake, server := newFakeBackboard(t)
	fake.graph.Resources = append(fake.graph.Resources, map[string]any{
		"address": "bucket.backups", "type": "bucket", "name": "backups", "config": map[string]any{"region": "sjc"},
	})
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testProviders,
		Steps: []resource.TestStep{{
			Config: environmentConfig(server.URL, `
resource "railway_bucket" "backups" {
  environment_id = local.environment_id
  name           = "backups"
}`),
			ExpectError: regexp.MustCompile(`already exists`),
		}},
	})
}

func TestResourceOwnedByAnotherPartialIsRefused(t *testing.T) {
	requireCLI(t)
	fake, server := newFakeBackboard(t)
	fake.owners = map[string]string{"bucket.backups": "billing"}
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testProviders,
		Steps: []resource.TestStep{{
			Config: environmentConfig(server.URL, `
resource "railway_bucket" "backups" {
  environment_id = local.environment_id
  name           = "backups"
}`),
			ExpectError: regexp.MustCompile(`named IaC partials`),
		}},
	})
	if len(fake.changeSets()) != 0 {
		t.Fatalf("applied %v", fake.changeSets())
	}
}

func TestStaleBaseIsReplanned(t *testing.T) {
	requireCLI(t)
	fake, server := newFakeBackboard(t)
	fake.staleOnce = true
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testProviders,
		Steps: []resource.TestStep{{
			Config: environmentConfig(server.URL, `
resource "railway_bucket" "backups" {
  environment_id = local.environment_id
  name           = "backups"
  region         = "sjc"
}`),
			Check: func(*terraform.State) error {
				if !slices.Contains(fake.addresses(), "bucket.backups") {
					return fmt.Errorf("bucket not created after a stale-base retry")
				}
				return nil
			},
		}},
	})
}
