package provider

import (
	"fmt"
	"maps"
	"os"
	"os/exec"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/hashicorp/terraform-plugin-framework/providerserver"
	"github.com/hashicorp/terraform-plugin-go/tfprotov6"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/terraform"

	"github.com/tambo-ai/terraform-provider-railway/internal/iac"
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
		// Check from outside the repository: a version-manager shim can
		// resolve here and fail in the test's working directory.
		version := exec.Command(name, "version")
		version.Dir = t.TempDir()
		if path, err := exec.LookPath(name); err == nil && version.Run() == nil {
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
    DATABASE_URL = { resource = railway_database.postgres.address, output = "DATABASE_URL" }
  }
  volume_mounts = {
    "/data" = railway_volume.data.name
  }
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

const extendedResources = `
data "railway_project" "this" {
  id = data.railway_environment.this.project_id
}

data "railway_environment" "this" {
  id = local.environment_id
}

resource "railway_group" "data" {
  environment_id = local.environment_id
  name           = "data"
  color          = "#0f766e"
}

resource "railway_bucket" "backups" {
  environment_id = local.environment_id
  name           = "backups"
  region         = "sjc"
  group          = railway_group.data.name
}

resource "railway_function" "hook" {
  environment_id = local.environment_id
  name           = "hook"
  code           = "export default { fetch() { return new Response(\"ok\") } }"
  variables = {
    SESSION = "public-${provider::railway::random_string(data.railway_environment.this.name, "session", 16)}"
  }
}
`

func TestFunctionsGroupsAndDataSources(t *testing.T) {
	requireCLI(t)
	fake, server := newFakeBackboard(t)
	config := environmentConfig(server.URL, extendedResources)
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testProviders,
		Steps: []resource.TestStep{
			{
				Config: config,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("data.railway_environment.this", "name", "staging"),
					resource.TestCheckResourceAttr("data.railway_environment.this", "project_id", fakeProjectID),
					resource.TestCheckResourceAttr("data.railway_project.this", "environments.production", "env-production"),
					resource.TestCheckResourceAttr("railway_function.hook", "id", fakeEnvironmentID+"/service.hook"),
					// sha256("railway-iac:staging:session"), first 16 bytes.
					resource.TestCheckResourceAttr("railway_function.hook", "variables.SESSION", "public-34dfe460291ad72046d0b67e2556e9b5"),
					func(*terraform.State) error {
						if !slices.Contains(fake.addresses(), "group.data") {
							return fmt.Errorf("group not created: %v", fake.addresses())
						}
						return nil
					},
				),
			},
			{Config: config, PlanOnly: true},
		},
	})
}

// Existing resources come under management with import blocks, as a
// railway.ts migration would: the import applies no Railway change when the
// configuration matches what is live, and the next plan is empty.
func TestImportBlocksAdoptMatchingResources(t *testing.T) {
	requireCLI(t)
	fake, server := newFakeBackboard(t)
	seedFromConfig(t, fake, server.URL, lifecycleResources+extendedFunction)
	before := len(fake.changeSets())

	imports := ""
	for name, address := range map[string]string{
		"railway_volume.data": "volume.postgres-volume", "railway_database.postgres": "database.postgres",
		"railway_service.web": "service.web", "railway_bucket.backups": "bucket.backups", "railway_function.hook": "service.hook",
	} {
		imports += fmt.Sprintf("import {\n  to = %s\n  id = %q\n}\n", name, fakeEnvironmentID+"/"+address)
	}
	config := environmentConfig(server.URL, imports+lifecycleResources+extendedFunction)
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testProviders,
		Steps: []resource.TestStep{
			{
				Config: config,
				Check: func(*terraform.State) error {
					if applied := fake.changeSets()[before:]; len(applied) != 0 {
						return fmt.Errorf("import applied Railway changes: %v", applied)
					}
					return nil
				},
			},
			{Config: config, PlanOnly: true},
			importStep("railway_database.postgres", "database.postgres"),
			importStep("railway_function.hook", "service.hook"),
		},
	})
}

// seedFromConfig creates resources in the fake with a separate Terraform
// state, then forgets that state, leaving them as resources Terraform does
// not manage.
func seedFromConfig(t *testing.T, fake *fakeBackboard, endpoint, body string) {
	t.Helper()
	removed := ""
	for _, name := range []string{"railway_volume.data", "railway_database.postgres", "railway_service.web", "railway_bucket.backups", "railway_function.hook"} {
		removed += fmt.Sprintf("removed {\n  from = %s\n  lifecycle {\n    destroy = false\n  }\n}\n", name)
	}
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testProviders,
		Steps: []resource.TestStep{
			{Config: environmentConfig(endpoint, body)},
			{Config: environmentConfig(endpoint, removed)},
		},
	})
	if len(fake.addresses()) != 5 {
		t.Fatalf("seed left %v", fake.addresses())
	}
}

const extendedFunction = `
resource "railway_function" "hook" {
  environment_id = local.environment_id
  name           = "hook"
  code           = "export default { fetch() { return new Response(\"ok\") } }"
}
`

func importStep(resourceName, address string) resource.TestStep {
	return resource.TestStep{
		ResourceName:  resourceName,
		ImportState:   true,
		ImportStateId: fakeEnvironmentID + "/" + address,
		ImportStateCheck: func(states []*terraform.InstanceState) error {
			if len(states) != 1 || states[0].Attributes["address"] != address {
				return fmt.Errorf("unexpected import state for %s: %v", address, states)
			}
			return nil
		},
	}
}

// Railway can accept a deletion and keep the resource. Destroy then fails and
// Terraform keeps the resource in state, so a later destroy deletes it.
func TestDestroyFailsWhenRailwayKeepsTheResource(t *testing.T) {
	requireCLI(t)
	wait := settleWait
	settleWait = 100 * time.Millisecond
	t.Cleanup(func() { settleWait = wait })
	fake, server := newFakeBackboard(t)
	bucket := environmentConfig(server.URL, `
resource "railway_bucket" "backups" {
  environment_id = local.environment_id
  name           = "backups"
  region         = "sjc"
}
`)
	empty := environmentConfig(server.URL, "")

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testProviders,
		Steps: []resource.TestStep{
			{Config: bucket},
			{
				PreConfig:   func() { fake.keepDeletes("bucket", true) },
				Config:      empty,
				ExpectError: regexp.MustCompile(`still lists it`),
			},
			{
				PreConfig: func() { fake.keepDeletes("bucket", false) },
				Config:    empty,
				Check: func(*terraform.State) error {
					if left := fake.addresses(); len(left) > 0 {
						return fmt.Errorf("resources left in the environment: %v", left)
					}
					deletes := 0
					for _, changeSet := range fake.changeSets() {
						for _, change := range changeSet.Changes {
							if iac.FieldStr(change, "kind") == "resource.delete" {
								deletes++
							}
						}
					}
					if deletes != 2 {
						return fmt.Errorf("want the kept bucket deleted again, got %d deletions", deletes)
					}
					return nil
				},
			},
		},
	})
}

func secretService(endpoint, version string, secrets map[string]string) string {
	entries := ""
	for _, name := range slices.Sorted(maps.Keys(secrets)) {
		entries += fmt.Sprintf("    %s = %q\n", name, secrets[name])
	}
	return environmentConfig(endpoint, fmt.Sprintf(`
resource "railway_service" "api" {
  environment_id           = local.environment_id
  name                     = "api"
  source                   = { image = { image = "nginx:1" } }
  secret_variables_version = %q
  secret_variables = {
%s  }
}
`, version, entries))
}

// secret_variables reach Railway, never Terraform state, and are resent only
// when the version changes or a name is new.
func TestSecretVariablesStayOutOfState(t *testing.T) {
	requireCLI(t)
	fake, server := newFakeBackboard(t)
	expectSecret := func(name, want string) resource.TestCheckFunc {
		return func(*terraform.State) error {
			if got := fake.variable("service.api", name); got != want {
				return fmt.Errorf("Railway has %s = %q, want %q", name, got, want)
			}
			return nil
		}
	}
	notInState := func(s *terraform.State) error {
		for address, res := range s.RootModule().Resources {
			for key, value := range res.Primary.Attributes {
				if strings.Contains(value, "s3cret") {
					return fmt.Errorf("%s.%s holds a secret value", address, key)
				}
			}
		}
		return nil
	}

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testProviders,
		Steps: []resource.TestStep{
			{
				Config: secretService(server.URL, "1", map[string]string{"API_KEY": "s3cret-1"}),
				Check: resource.ComposeAggregateTestCheckFunc(
					expectSecret("API_KEY", "s3cret-1"),
					notInState,
					resource.TestCheckResourceAttr("railway_service.api", "secret_variable_names.#", "1"),
					resource.TestCheckTypeSetElemAttr("railway_service.api", "secret_variable_names.*", "API_KEY"),
					resource.TestCheckNoResourceAttr("railway_service.api", "secret_variables.%"),
				),
			},
			{Config: secretService(server.URL, "1", map[string]string{"API_KEY": "s3cret-1"}), PlanOnly: true},
			// A changed value under the same version is not sent.
			{Config: secretService(server.URL, "1", map[string]string{"API_KEY": "s3cret-2"}), PlanOnly: true},
			{
				Config: secretService(server.URL, "2", map[string]string{"API_KEY": "s3cret-2"}),
				Check:  resource.ComposeAggregateTestCheckFunc(expectSecret("API_KEY", "s3cret-2"), notInState),
			},
			// A new name is sent without a version change; the others are kept.
			{
				Config: secretService(server.URL, "2", map[string]string{"API_KEY": "s3cret-3", "WEBHOOK_KEY": "s3cret-w"}),
				Check: resource.ComposeAggregateTestCheckFunc(
					expectSecret("API_KEY", "s3cret-2"),
					expectSecret("WEBHOOK_KEY", "s3cret-w"),
					resource.TestCheckResourceAttr("railway_service.api", "secret_variable_names.#", "2"),
				),
			},
			{Config: secretService(server.URL, "2", map[string]string{"API_KEY": "s3cret-3", "WEBHOOK_KEY": "s3cret-w"}), PlanOnly: true},
			// A removed name is deleted, like any variable the configuration drops.
			{
				Config: secretService(server.URL, "2", map[string]string{"WEBHOOK_KEY": "s3cret-w"}),
				Check:  resource.ComposeAggregateTestCheckFunc(expectSecret("API_KEY", ""), expectSecret("WEBHOOK_KEY", "s3cret-w")),
			},
		},
	})
}

// A bucket name the project already holds, from a bucket Railway could not
// delete, cannot be created in this environment.
func TestCreateRefusesBucketNameTakenInProject(t *testing.T) {
	requireCLI(t)
	fake, server := newFakeBackboard(t)
	fake.projectBuckets = []string{"assets"}
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testProviders,
		Steps: []resource.TestStep{{
			Config: environmentConfig(server.URL, `
resource "railway_bucket" "assets" {
  environment_id = local.environment_id
  name           = "assets"
}
`),
			ExpectError: regexp.MustCompile(`already has a bucket named "assets"`),
		}},
	})
	if len(fake.changeSets()) > 0 {
		t.Errorf("applied %d ChangeSets for a refused bucket", len(fake.changeSets()))
	}
}
