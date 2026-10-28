package provider

import (
	"strings"
	"testing"

	"github.com/tambo-ai/terraform-provider-railway-iac-sdk/internal/iac"
)

// planAgainstLive diffs model against a live config for service web.
func planAgainstLive(t *testing.T, liveService string, model *serviceModel) []string {
	t.Helper()
	graph := iac.EnvironmentConfigToGraph(decode(t, `{"services": {"svc-web": `+liveService+`}}`), iac.ConfigToGraphOptions{
		ProjectName:      "test",
		ServiceNamesByID: map[string]any{"svc-web": "web"},
	})
	snap := &snapshot{graph: graph, follow: &iac.BranchFollow{}}
	planned, err := (&engine{}).diff(t.Context(), snap, "service.web", build(t, serviceKind(), model))
	if err != nil {
		t.Fatal(err)
	}
	return summaries(planned.changes)
}

func minimalWeb() *serviceModel {
	return &serviceModel{
		commonModel: named("web"),
		Source:      &sourceModel{GitHub: &githubSourceModel{Repo: "acme/web"}},
	}
}

// A resource's configuration is its whole definition, as a railway.go file
// is for railway config apply: a source, build or deploy setting the
// configuration leaves unset plans a reset to Railway's default.
func TestUnsetSettingsPlanAReset(t *testing.T) {
	for name, test := range map[string]struct{ live, want string }{
		"build": {
			live: `{"source": {"repo": "acme/web", "branch": "main"}, "build": {"watchPatterns": ["src/**"]}}`,
			want: `build.watchPatterns (["src/**"] → null)`,
		},
		"deploy": {
			live: `{"source": {"repo": "acme/web", "branch": "main"}, "deploy": {"restartPolicyType": "ALWAYS", "sleepApplication": true}}`,
			want: `deploy.restartPolicyType ("ALWAYS" → null)`,
		},
	} {
		t.Run(name, func(t *testing.T) {
			changes := planAgainstLive(t, test.live, minimalWeb())
			if len(changes) != 1 || !strings.Contains(changes[0], test.want) {
				t.Fatalf("want one change mentioning %q, got %v", test.want, changes)
			}
		})
	}
}

// A live service that holds what the configuration declares, plus values the
// engine knows are Railway's defaults or deploy bookkeeping, converges.
func TestDeclaredSettingsAndDefaultsPlanNoChanges(t *testing.T) {
	model := minimalWeb()
	model.Deploy = &deployModel{RestartPolicyType: ptr("ALWAYS")}
	live := `{"source": {"repo": "acme/web", "branch": "main", "commitSha": "abc123"},
		"build": {"builder": "RAILPACK"}, "deploy": {"restartPolicyType": "ALWAYS"}}`
	if changes := planAgainstLive(t, live, model); len(changes) != 0 {
		t.Fatalf("want no changes, got %v", changes)
	}
}

// Networking is the exception: domains and TCP proxies the configuration
// does not mention stay attached.
func TestOmittedNetworkingKeepsLiveNetworking(t *testing.T) {
	for name, live := range map[string]string{
		"tcp proxy":     `{"source": {"repo": "acme/web", "branch": "main"}, "networking": {"tcpProxies": {"5432": {}}}}`,
		"custom domain": `{"source": {"repo": "acme/web", "branch": "main"}, "networking": {"customDomains": {"app.example.test": {"port": 8080}}}}`,
	} {
		t.Run(name, func(t *testing.T) {
			if changes := planAgainstLive(t, live, minimalWeb()); len(changes) != 0 {
				t.Fatalf("want no changes, got %v", changes)
			}
		})
	}
}

// Registry credentials never reach live_changes or plan warnings.
func TestRegistryCredentialsStayOutOfPlanText(t *testing.T) {
	const secret = "hunter2-registry-password"
	model := &serviceModel{
		commonModel: named("web"),
		Source:      &sourceModel{Image: &imageSourceModel{Image: "ghcr.io/acme/private:1"}},
		serviceSettings: serviceSettings{Deploy: &deployModel{
			RegistryCredentials: &registryCredentialsModel{Username: "bot", Password: secret},
		}},
	}
	changes := planAgainstLive(t, `{"source": {"image": "ghcr.io/acme/private:1"}, "deploy": {"registryCredentials": {"username": "bot", "password": "old-password"}}}`, model)
	if len(changes) == 0 {
		t.Fatal("want a credentials change")
	}
	for _, line := range changes {
		if strings.Contains(line, secret) || strings.Contains(line, "old-password") {
			t.Fatalf("credential in plan text: %q", line)
		}
	}
}
