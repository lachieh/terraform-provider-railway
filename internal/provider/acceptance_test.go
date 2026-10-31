package provider

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/terraform"

	"github.com/tambo-ai/terraform-provider-railway/internal/iac"
	"github.com/tambo-ai/terraform-provider-railway/internal/railwayapi"
)

// accProjectPrefix guards the acceptance project: every test creates and
// deletes environments in it, so it must be a project made for them.
const accProjectPrefix = "tf-acc"

// accRailway is a live Railway project the acceptance tests write to.
type accRailway struct {
	client    *railwayapi.Client
	projectID string
}

// requireAcc returns the acceptance project, or skips unless TF_ACC is set
// with RAILWAY_ACC_TOKEN (an account or workspace token) and
// RAILWAY_ACC_PROJECT_ID (a project whose name starts with tf-acc).
func requireAcc(t *testing.T) *accRailway {
	t.Helper()
	token, projectID := os.Getenv("RAILWAY_ACC_TOKEN"), os.Getenv("RAILWAY_ACC_PROJECT_ID")
	if os.Getenv("TF_ACC") == "" || token == "" || projectID == "" {
		t.Skip("set TF_ACC, RAILWAY_ACC_TOKEN and RAILWAY_ACC_PROJECT_ID to run acceptance tests against Railway")
	}
	requireCLI(t)
	client, err := railwayapi.New(railwayapi.Credential{Token: token})
	if err != nil {
		t.Fatal(err)
	}
	project, err := client.Project(t.Context(), projectID)
	if err != nil {
		t.Fatalf("reading acceptance project: %v", err)
	}
	if !strings.HasPrefix(project.Name, accProjectPrefix) {
		t.Fatalf("acceptance project %s is named %q; acceptance tests only write to a project named %s…", projectID, project.Name, accProjectPrefix)
	}
	t.Setenv("RAILWAY_TOKEN", token)
	t.Setenv("RAILWAY_PROJECT_TOKEN", "")
	return &accRailway{client: client, projectID: projectID}
}

// environment creates an empty environment for one test and deletes it,
// with everything in it, when the test ends.
func (a *accRailway) environment(t *testing.T) string {
	t.Helper()
	suffix := make([]byte, 4)
	_, _ = rand.Read(suffix)
	name := "tfacc-" + hex.EncodeToString(suffix)
	var created struct {
		EnvironmentCreate struct {
			ID string `json:"id"`
		} `json:"environmentCreate"`
	}
	err := a.client.Do(t.Context(), `mutation AccEnvironmentCreate($projectId: String!, $name: String!) {
  environmentCreate(input: {projectId: $projectId, name: $name}) { id }
}`, map[string]any{"projectId": a.projectID, "name": name}, &created)
	if err != nil {
		t.Fatalf("creating environment %s: %v", name, err)
	}
	id := created.EnvironmentCreate.ID
	t.Cleanup(func() {
		// t.Context is already cancelled when cleanups run.
		err := a.client.Do(context.Background(), `mutation AccEnvironmentDelete($id: String!) { environmentDelete(id: $id) }`,
			map[string]any{"id": id}, nil)
		if err != nil {
			t.Errorf("deleting environment %s (%s): %v", name, id, err)
		}
	})
	t.Logf("environment %s (%s)", name, id)
	return id
}

// upsertVariable writes a service variable outside Terraform.
func (a *accRailway) upsertVariable(t *testing.T, environmentID, serviceID, name, value string) {
	t.Helper()
	err := a.client.Do(t.Context(), `mutation AccVariableUpsert($input: VariableUpsertInput!) { variableUpsert(input: $input) }`,
		map[string]any{"input": map[string]any{
			"projectId": a.projectID, "environmentId": environmentID, "serviceId": serviceID,
			"name": name, "value": value, "skipDeploys": true,
		}}, nil)
	if err != nil {
		t.Fatalf("writing %s outside Terraform: %v", name, err)
	}
}

// checkEmpty fails when the environment lists a resource whose address does
// not start with one of kept.
func (a *accRailway) checkEmpty(environmentID string, kept ...string) resource.TestCheckFunc {
	return func(*terraform.State) error {
		e := newEngine(a.client, "")
		snap, err := e.read(context.Background(), environmentID)
		if err != nil {
			return err
		}
		var left []string
		for _, node := range snap.graph.Resources {
			address := fmt.Sprint(node["type"], ".", node["name"])
			if !slices.ContainsFunc(kept, func(prefix string) bool { return strings.HasPrefix(address, prefix) }) {
				left = append(left, address)
			}
		}
		if len(left) > 0 {
			return fmt.Errorf("resources left in environment %s: %v", environmentID, left)
		}
		return nil
	}
}

// variablesSet fails unless the service Terraform manages as resourceName
// has each variable, with a value.
func (a *accRailway) variablesSet(environmentID, resourceName string, names ...string) resource.TestCheckFunc {
	return func(state *terraform.State) error {
		managed, ok := state.RootModule().Resources[resourceName]
		if !ok {
			return fmt.Errorf("%s is not in state", resourceName)
		}
		env, err := a.client.FetchEnvironment(context.Background(), environmentID, true)
		if err != nil {
			return err
		}
		service := iac.Field(env.Config["services"], managed.Primary.Attributes["railway_id"])
		for _, name := range names {
			if value, _ := iac.Field(iac.Field(service, "variables"), name).(map[string]any)["value"].(string); value == "" {
				return fmt.Errorf("%s has no value for %s", resourceName, name)
			}
		}
		return nil
	}
}

func accConfig(environmentID, body string) string {
	return fmt.Sprintf(`
provider "railway" {}

locals {
  environment_id = %q
}
%s`, environmentID, body)
}

func accServices(deploy, secretVersion string, watchPatterns bool) string {
	watch := ""
	if watchPatterns {
		watch = `watch_patterns = ["src/**"]`
	}
	return fmt.Sprintf(`
resource "railway_group" "app" {
  environment_id = local.environment_id
  name           = "app"
  color          = "#4f46e5"
}

# Railway creates the template's volume, postgres-volume, with the database.
resource "railway_database" "postgres" {
  environment_id = local.environment_id
  name           = "postgres"
  engine         = "postgres"
}

# No region and no 100%% alert: Railway assigns both.
resource "railway_volume" "data" {
  environment_id = local.environment_id
  name           = "data"
  size_mb        = 5000
  usage_alerts   = [80, 95]
  group          = railway_group.app.name
}

resource "railway_service" "web" {
  environment_id = local.environment_id
  name           = "web"
  source         = { image = { image = "traefik/whoami:v1.10" } }
  build          = { %s }
  deploy         = { %s }
  replicas       = { "us-west2" = 1 }
  group          = railway_group.app.name
  variables      = { WHOAMI_PORT_NUMBER = "8080", PORT = "8080" }
  variable_references = {
    DATABASE_URL = { resource = railway_database.postgres.address, output = "DATABASE_URL" }
  }
  secret_variables_version = %q
  secret_variables         = { API_KEY = "acc-secret-%s" }
  volume_mounts            = { "/data" = railway_volume.data.name }
  wait_for_deployment      = { timeout = "10m" }
}

data "railway_deployment" "web" {
  environment_id = local.environment_id
  service_id     = railway_service.web.railway_id
}

data "railway_database" "postgres" {
  environment_id = local.environment_id
  name           = railway_database.postgres.name
  depends_on     = [railway_database.postgres]
}
`, watch, deploy, secretVersion, secretVersion)
}

// The whole life of a service, Postgres, volumes and a group against real
// Railway: create, an empty second plan, an update that unsets a setting,
// drift repair, import and destroy.
func TestAccServiceLifecycle(t *testing.T) {
	acc := requireAcc(t)
	environmentID := acc.environment(t)
	initial := accConfig(environmentID, accServices(`healthcheck_path = "/health"
    healthcheck_timeout = 60`, "1", true))
	updated := accConfig(environmentID, accServices(`healthcheck_path = "/health"
    healthcheck_timeout = 90
    restart_policy_type = "ON_FAILURE"`, "2", false))
	var serviceID string
	converged := func(names ...string) resource.TestCheckFunc {
		checks := make([]resource.TestCheckFunc, 0, len(names))
		for _, name := range names {
			checks = append(checks, resource.TestCheckResourceAttr(name, "live_changes.#", "0"))
		}
		return resource.ComposeAggregateTestCheckFunc(checks...)
	}
	all := []string{"railway_group.app", "railway_database.postgres", "railway_volume.data", "railway_service.web"}

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: testProviders,
		// Destroying a template database leaves the volume Railway made for
		// it: postgres-volume, with a suffix while earlier runs' deleted
		// volumes still hold the name in the project.
		CheckDestroy: acc.checkEmpty(environmentID, "volume.postgres-volume"),
		Steps: []resource.TestStep{
			{
				Config: initial,
				Check: resource.ComposeAggregateTestCheckFunc(
					converged(all...),
					resource.TestCheckResourceAttr("data.railway_deployment.web", "status", "SUCCESS"),
					resource.TestCheckResourceAttrPair("data.railway_database.postgres", "railway_id", "railway_database.postgres", "railway_id"),
					resource.TestCheckResourceAttrWith("railway_service.web", "railway_id", func(id string) error {
						serviceID = id
						if id == "" {
							return fmt.Errorf("no Railway ID")
						}
						return nil
					}),
				),
			},
			{Config: initial, PlanOnly: true},
			{Config: updated, Check: resource.ComposeAggregateTestCheckFunc(
				converged(all...),
				resource.TestCheckResourceAttr("data.railway_deployment.web", "status", "SUCCESS"),
			)},
			// Removing watch_patterns resets it; Railway must agree it is reset.
			{Config: updated, PlanOnly: true},
			{
				PreConfig:          func() { acc.upsertVariable(t, environmentID, serviceID, "WHOAMI_PORT_NUMBER", "9090") },
				Config:             updated,
				PlanOnly:           true,
				ExpectNonEmptyPlan: true,
			},
			{Config: updated},
			{Config: updated, PlanOnly: true},
			{
				ResourceName:  "railway_volume.data",
				ImportState:   true,
				ImportStateId: environmentID + "/volume.data",
				ImportStateCheck: func(states []*terraform.InstanceState) error {
					if len(states) != 1 || states[0].Attributes["railway_id"] == "" {
						return fmt.Errorf("unexpected import state: %v", states)
					}
					return nil
				},
			},
		},
	})
}

// Railway accepts a bucket deletion and keeps the bucket, so destroy fails
// and Terraform keeps it in state. The test then forgets it with a removed
// block. The bucket stays in the project.
func TestAccBucketDestroyIsRefused(t *testing.T) {
	acc := requireAcc(t)
	environmentID := acc.environment(t)
	// Every run needs a new name: the buckets earlier runs created stay in
	// the project.
	suffix := make([]byte, 4)
	_, _ = rand.Read(suffix)
	bucket := accConfig(environmentID, fmt.Sprintf(`
resource "railway_bucket" "assets" {
  environment_id = local.environment_id
  name           = "assets-%x"
  region         = "sjc"
}

ephemeral "railway_bucket_credentials" "assets" {
  environment_id = local.environment_id
  bucket_id      = railway_bucket.assets.railway_id
}

# No source, so nothing deploys.
resource "railway_service" "uploader" {
  environment_id = local.environment_id
  name           = "uploader"
  secret_variables = {
    S3_BUCKET            = ephemeral.railway_bucket_credentials.assets.bucket_name
    S3_SECRET_ACCESS_KEY = ephemeral.railway_bucket_credentials.assets.secret_access_key
  }
}

data "railway_bucket" "assets" {
  environment_id = local.environment_id
  name           = railway_bucket.assets.name
  depends_on     = [railway_bucket.assets]
}
`, suffix))

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: testProviders,
		Steps: []resource.TestStep{
			{
				Config: bucket,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("railway_bucket.assets", "live_changes.#", "0"),
					resource.TestCheckResourceAttrPair("data.railway_bucket.assets", "railway_id", "railway_bucket.assets", "railway_id"),
					acc.variablesSet(environmentID, "railway_service.uploader", "S3_BUCKET", "S3_SECRET_ACCESS_KEY"),
				),
			},
			{Config: bucket, PlanOnly: true},
			{Config: accConfig(environmentID, ""), ExpectError: regexp.MustCompile(`still lists it`)},
			{Config: accConfig(environmentID, `
removed {
  from = railway_bucket.assets
  lifecycle {
    destroy = false
  }
}
`)},
		},
	})
}

// A project and an environment in it are created, renamed and destroyed.
// The project is deleted directly when the test ends, in case destroy did not.
func TestAccProjectAndEnvironment(t *testing.T) {
	acc := requireAcc(t)
	suffix := make([]byte, 4)
	_, _ = rand.Read(suffix)
	name := fmt.Sprintf("%s-project-%x", accProjectPrefix, suffix)
	var projectID string
	t.Cleanup(func() {
		if projectID == "" {
			return
		}
		if err := acc.client.ProjectDelete(context.Background(), projectID); err != nil {
			t.Errorf("deleting project %s (%s): %v", name, projectID, err)
		}
	})
	config := func(environmentName string) string {
		return fmt.Sprintf(`
provider "railway" {}

resource "railway_project" "app" {
  name        = %q
  description = "Created by the provider's acceptance tests"
}

resource "railway_environment" "staging" {
  project_id = railway_project.app.id
  name       = %q
}
`, name, environmentName)
	}

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: testProviders,
		CheckDestroy: func(*terraform.State) error {
			if _, err := acc.client.ProjectDetails(context.Background(), projectID); !errors.Is(err, railwayapi.ErrNotFound) {
				return fmt.Errorf("project %s survived destroy: %v", projectID, err)
			}
			return nil
		},
		Steps: []resource.TestStep{
			{
				Config: config("staging"),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("railway_project.app", "default_environment_name", "production"),
					resource.TestCheckResourceAttrWith("railway_project.app", "id", func(id string) error {
						projectID = id
						return nil
					}),
				),
			},
			{Config: config("staging"), PlanOnly: true},
			{Config: config("stage"), Check: resource.TestCheckResourceAttr("railway_environment.staging", "name", "stage")},
			{Config: config("stage"), PlanOnly: true},
			{ResourceName: "railway_project.app", ImportState: true, ImportStateVerify: true},
		},
	})
}
