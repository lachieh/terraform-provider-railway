package provider

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
	railway "github.com/railwayapp/railway-go-sdk"

	"github.com/tambo-ai/terraform-provider-railway-iac-sdk/internal/railwayapi"
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
		err := a.client.Do(t.Context(), `mutation AccEnvironmentDelete($id: String!) { environmentDelete(id: $id) }`,
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

// checkEmpty fails when the environment still lists resources.
func (a *accRailway) checkEmpty(environmentID string) resource.TestCheckFunc {
	return func(*terraform.State) error {
		e := newEngine(a.client, "")
		snap, err := e.read(context.Background(), environmentID)
		if err != nil {
			return err
		}
		if len(snap.graph.Resources) > 0 {
			addresses := make([]string, 0, len(snap.graph.Resources))
			for _, node := range snap.graph.Resources {
				addresses = append(addresses, fmt.Sprint(node["type"], ".", node["name"]))
			}
			return fmt.Errorf("resources left in environment %s: %v", environmentID, addresses)
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
resource "railway_volume" "data" {
  environment_id = local.environment_id
  name           = "data"
  size_mb        = 5000
  usage_alerts   = [80, 95]
}

resource "railway_database" "postgres" {
  environment_id = local.environment_id
  name           = "postgres"
  engine         = "postgres"
}

resource "railway_service" "web" {
  environment_id = local.environment_id
  name           = "web"
  source         = { image = { image = "traefik/whoami:v1.10" } }
  build          = { %s }
  deploy         = { %s }
  replicas       = { "us-west2" = 1 }
  variables      = { WHOAMI_PORT_NUMBER = "8080" }
  variable_references = {
    DATABASE_URL = { resource = railway_database.postgres.address, output = "DATABASE_URL" }
  }
  secret_variables_version = %q
  secret_variables         = { API_KEY = "acc-secret-%s" }
  volume_mounts            = { "/data" = railway_volume.data.name }
}
`, watch, deploy, secretVersion, secretVersion)
}

// The whole life of a service, database and volume against real Railway:
// create, an empty second plan, an update that unsets a setting, drift
// repair, import and destroy.
func TestAccServiceLifecycle(t *testing.T) {
	acc := requireAcc(t)
	environmentID := acc.environment(t)
	initial := accConfig(environmentID, accServices(`healthcheck_timeout = 60`, "1", true))
	updated := accConfig(environmentID, accServices(`healthcheck_timeout = 90
    restart_policy_type = "ON_FAILURE"`, "2", false))
	var serviceID string

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: testProviders,
		CheckDestroy:             acc.checkEmpty(environmentID),
		Steps: []resource.TestStep{
			{
				Config: initial,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("railway_service.web", "live_changes.#", "0"),
					resource.TestCheckResourceAttr("railway_database.postgres", "live_changes.#", "0"),
					resource.TestCheckResourceAttr("railway_volume.data", "live_changes.#", "0"),
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
			{
				Config: updated,
				Check:  resource.TestCheckResourceAttr("railway_service.web", "live_changes.#", "0"),
			},
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

// Destroying a bucket must remove it. Railway has been seen accepting a
// bucket deletion and keeping the bucket; destroy then fails with "still
// lists it", which this test reports.
func TestAccBucketInGroup(t *testing.T) {
	acc := requireAcc(t)
	environmentID := acc.environment(t)
	config := accConfig(environmentID, `
resource "railway_group" "storage" {
  environment_id = local.environment_id
  name           = "storage"
  color          = "#4f46e5"
}

resource "railway_bucket" "assets" {
  environment_id = local.environment_id
  name           = "assets"
  region         = "sjc"
  group          = railway_group.storage.name
}
`)

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: testProviders,
		CheckDestroy:             acc.checkEmpty(environmentID),
		Steps: []resource.TestStep{
			{
				Config: config,
				Check:  resource.TestCheckResourceAttr("railway_bucket.assets", "live_changes.#", "0"),
			},
			{Config: config, PlanOnly: true},
		},
	})
}

// Railway refuses a ChangeSet built on a configuration that has since
// changed, and the client recognises the refusal as a stale base.
func TestAccStaleBaseIsRecognised(t *testing.T) {
	acc := requireAcc(t)
	environmentID := acc.environment(t)
	ctx := t.Context()
	e := newEngine(acc.client, "")

	snap, err := e.read(ctx, environmentID)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("iacPartials exposed: %t", snap.env.IACPartials != nil)
	imageService := func(name string) map[string]any {
		node, err := graphNode("service."+name, func() any {
			return railway.ServiceNamed(name, railway.ServiceConfig{"source": railway.Image("traefik/whoami:v1.10")})
		})
		if err != nil {
			t.Fatal(err)
		}
		return node
	}
	stale, err := e.diff(ctx, snap, "service.stale", imageService("stale"))
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := e.apply(ctx, environmentID, "service.first", imageService("first")); err != nil {
		t.Fatal(err)
	}
	_, err = acc.client.ApplyChangeSet(ctx, environmentID, e.changeSet(stale, true), snap.env.ConfigEtag, "acceptance: stale base")
	if !railwayapi.IsConfigEtagConflict(err) {
		t.Fatalf("applying on a stale base returned %v, want a stale-base conflict", err)
	}
	t.Logf("stale-base error: %v", err)
}
