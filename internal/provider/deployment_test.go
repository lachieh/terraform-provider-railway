package provider

import (
	"fmt"
	"regexp"
	"testing"
	"time"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
)

func waitingService(endpoint, port, timeout string) string {
	return environmentConfig(endpoint, fmt.Sprintf(`
resource "railway_service" "web" {
  environment_id      = local.environment_id
  name                = "web"
  source              = { image = { image = "traefik/whoami:v1.10" } }
  variables           = { PORT = "public-%s" }
  wait_for_deployment = { timeout = %q }
}

data "railway_deployment" "web" {
  environment_id = local.environment_id
  service_id     = railway_service.web.railway_id
}
`, port, timeout))
}

func fastDeploymentPolling(t *testing.T) {
	poll, start := deploymentPoll, deploymentStart
	deploymentPoll, deploymentStart = 10*time.Millisecond, 200*time.Millisecond
	t.Cleanup(func() { deploymentPoll, deploymentStart = poll, start })
}

// An apply waits for the deployment it starts and fails when that
// deployment fails, needs approval, or outlasts the timeout, even while an
// earlier deployment succeeded.
func TestApplyWaitsForTheDeploymentItStarts(t *testing.T) {
	requireCLI(t)
	fastDeploymentPolling(t)
	fake, server := newFakeBackboard(t)
	fake.deployLag = 2

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testProviders,
		Steps: []resource.TestStep{
			{
				Config: waitingService(server.URL, "8080", "1m"),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("data.railway_deployment.web", "id", "deploy-web-1"),
					resource.TestCheckResourceAttr("data.railway_deployment.web", "status", "SUCCESS"),
					resource.TestCheckResourceAttr("data.railway_deployment.web", "static_url", "web.up.railway.test"),
				),
			},
			{
				PreConfig:   func() { fake.setDeployStatus("FAILED") },
				Config:      waitingService(server.URL, "8081", "1m"),
				ExpectError: regexp.MustCompile(`deploy-web-2 of service\.web ended FAILED`),
			},
			{
				PreConfig:   func() { fake.setDeployStatus("NEEDS_APPROVAL") },
				Config:      waitingService(server.URL, "8082", "1m"),
				ExpectError: regexp.MustCompile(`deploy-web-3 of service\.web needs approval`),
			},
			{
				PreConfig:   func() { fake.setDeployStatus("BUILDING") },
				Config:      waitingService(server.URL, "8083", "100ms"),
				ExpectError: regexp.MustCompile(`deploy-web-4 of service\.web is still BUILDING after 100ms`),
			},
			// Each failed apply kept the service in state with its change
			// applied.
			{
				Config:   waitingService(server.URL, "8083", "100ms"),
				PlanOnly: true,
			},
		},
	})
	if got := fake.deploymentCount("web"); got != 4 {
		t.Fatalf("web deployed %d times, want 4", got)
	}
}

// An apply that starts no deployment finishes once deploymentStart passes.
func TestApplyWithoutADeploymentFinishes(t *testing.T) {
	requireCLI(t)
	fastDeploymentPolling(t)
	fake, server := newFakeBackboard(t)
	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testProviders,
		Steps: []resource.TestStep{{
			PreConfig: func() { fake.setDeployStatus(noDeployment) },
			Config: environmentConfig(server.URL, `
resource "railway_service" "web" {
  environment_id      = local.environment_id
  name                = "web"
  source              = { image = { image = "traefik/whoami:v1.10" } }
  wait_for_deployment = {}
}
`),
		}},
	})
}
