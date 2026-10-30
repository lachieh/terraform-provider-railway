package provider

import (
	"fmt"
	"testing"
	"time"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/plancheck"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
)

func projectConfig(endpoint, body string) string {
	return fmt.Sprintf(`
provider "railway" {
  token    = "test-token"
  endpoint = %q
}
%s`, endpoint, body)
}

func projectAndEnvironment(projectName, description, defaultEnvironment, environmentName string) string {
	if description != "" {
		description = fmt.Sprintf("description = %q", description)
	}
	return fmt.Sprintf(`
resource "railway_project" "app" {
  name                     = %q
  %s
  pr_deploys               = true
  default_environment_name = %q
}

resource "railway_environment" "staging" {
  project_id            = railway_project.app.id
  name                  = %q
  source_environment_id = railway_project.app.default_environment_id
}
`, projectName, description, defaultEnvironment, environmentName)
}

// A project and an environment are created, changed in place, imported and
// destroyed.
func TestProjectAndEnvironmentLifecycle(t *testing.T) {
	requireCLI(t)
	wait := containerDeleteWait
	containerDeleteWait = 100 * time.Millisecond
	t.Cleanup(func() { containerDeleteWait = wait })
	fake, server := newFakeBackboard(t)
	fake.deleteLag = 3
	var projectID, environmentID string
	created := projectConfig(server.URL, projectAndEnvironment("app", "The app", "live", "staging"))
	changed := projectConfig(server.URL, projectAndEnvironment("app-renamed", "", "prod", "stage"))

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testProviders,
		CheckDestroy: func(*terraform.State) error {
			if project := fake.container(projectID); project != nil {
				return fmt.Errorf("project %s survived destroy", projectID)
			}
			if environment := fake.container(environmentID); environment["deletedAt"] == nil {
				return fmt.Errorf("environment %s survived destroy", environmentID)
			}
			return nil
		},
		Steps: []resource.TestStep{
			{
				Config: created,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("railway_project.app", "workspace_id", "workspace-test"),
					resource.TestCheckResourceAttr("railway_project.app", "default_environment_name", "live"),
					resource.TestCheckResourceAttrPair("railway_project.app", "default_environment_id", "railway_environment.staging", "source_environment_id"),
					resource.TestCheckResourceAttrWith("railway_project.app", "id", func(id string) error {
						projectID = id
						if fake.container(id)["prDeploys"] != true || fake.container(id)["description"] != "The app" {
							return fmt.Errorf("project %s: %v", id, fake.container(id))
						}
						return nil
					}),
					resource.TestCheckResourceAttrWith("railway_environment.staging", "id", func(id string) error {
						environmentID = id
						environment := fake.container(id)
						if environment["projectId"] != projectID || environment["source"] != projectID+"-env" {
							return fmt.Errorf("environment %s: %v", id, environment)
						}
						return nil
					}),
				),
			},
			{Config: created, PlanOnly: true},
			{
				Config: changed,
				ConfigPlanChecks: resource.ConfigPlanChecks{PreApply: []plancheck.PlanCheck{
					plancheck.ExpectResourceAction("railway_project.app", plancheck.ResourceActionUpdate),
					plancheck.ExpectResourceAction("railway_environment.staging", plancheck.ResourceActionUpdate),
				}},
				Check: func(*terraform.State) error {
					project, environment := fake.container(projectID), fake.container(environmentID)
					if project["name"] != "app-renamed" || project["description"] != "" {
						return fmt.Errorf("project not updated: %v", project)
					}
					if name := fake.container(projectID + "-env")["name"]; name != "prod" {
						return fmt.Errorf("default environment is named %v", name)
					}
					if environment["name"] != "stage" {
						return fmt.Errorf("environment not renamed: %v", environment)
					}
					return nil
				},
			},
			{Config: changed, PlanOnly: true},
			{ResourceName: "railway_project.app", ImportState: true, ImportStateVerify: true},
			{ResourceName: "railway_environment.staging", ImportState: true, ImportStateVerify: true, ImportStateVerifyIgnore: []string{"source_environment_id"}},
		},
	})
}

// A project or environment deleted outside Terraform leaves state, so the
// next plan creates it again.
func TestDeletedProjectAndEnvironmentArePlannedAgain(t *testing.T) {
	requireCLI(t)
	fake, server := newFakeBackboard(t)
	config := projectConfig(server.URL, `
resource "railway_project" "app" {
  name = "app"
}

resource "railway_environment" "staging" {
  project_id = railway_project.app.id
  name       = "staging"
}
`)
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testProviders,
		Steps: []resource.TestStep{
			{Config: config},
			{
				PreConfig: func() {
					fake.mu.Lock()
					defer fake.mu.Unlock()
					for id := range fake.projects {
						delete(fake.projects, id)
					}
					for _, environment := range fake.environments {
						environment["deletedAt"] = "2026-10-30T00:00:00Z"
					}
				},
				Config:             config,
				PlanOnly:           true,
				ExpectNonEmptyPlan: true,
			},
		},
	})
}
