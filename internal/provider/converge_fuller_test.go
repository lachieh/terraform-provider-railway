package provider

import (
	"strings"
	"testing"

	"github.com/tambo-ai/terraform-provider-railway/internal/iac"
)

// planAgainstLive diffs model against a live config for service web.
func planAgainstLive(t *testing.T, liveService string, model *serviceModel) []string {
	t.Helper()
	return summaries(changesAgainstLive(t, liveService, model))
}

func changesAgainstLive(t *testing.T, liveService string, model *serviceModel) []map[string]any {
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
	return planned.changes
}

func minimalWeb() *serviceModel {
	return &serviceModel{
		commonModel: named("web"),
		Source:      &sourceModel{GitHub: &githubSourceModel{Repo: "acme/web"}},
	}
}

// A resource's configuration is its whole definition, as a railway.go file
// is for railway config apply: a source, build or deploy setting the
// configuration leaves unset plans a reset to Railway's default. Railway
// ignores null for a build setting, so a build reset sends an empty value.
func TestUnsetSettingsPlanAReset(t *testing.T) {
	for name, test := range map[string]struct{ live, field, after string }{
		"build": {
			live:  `{"source": {"repo": "acme/web", "branch": "main"}, "build": {"watchPatterns": ["src/**"], "buildCommand": "make", "builder": "DOCKERFILE"}}`,
			field: "build",
			after: `{"buildCommand":"","builder":"RAILPACK","watchPatterns":[]}`,
		},
		"deploy": {
			live:  `{"source": {"repo": "acme/web", "branch": "main"}, "deploy": {"restartPolicyType": "ALWAYS", "sleepApplication": true}}`,
			field: "deploy",
			after: `null`,
		},
	} {
		t.Run(name, func(t *testing.T) {
			changes := changesAgainstLive(t, test.live, minimalWeb())
			if len(changes) != 1 || iac.FieldStr(changes[0], "field") != test.field || iac.StableStringify(changes[0]["after"]) != test.after {
				t.Fatalf("want %s set to %s, got %v", test.field, test.after, changes)
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

// Railway stores a cleared build string as "", and a deploy setting set to
// its default as no value; neither is a change.
func TestSettingsAsRailwayStoresThemPlanNoChanges(t *testing.T) {
	model := minimalWeb()
	model.Deploy = &deployModel{RestartPolicyType: ptr("ON_FAILURE"), SleepApplication: ptr(false)}
	live := `{"source": {"repo": "acme/web", "branch": "main"}, "build": {"buildCommand": ""}, "deploy": {}}`
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

// Railway picks a region for a volume created without one and always keeps
// a 100% usage alert; neither is a change the configuration asks for.
func TestVolumeKeepsWhatRailwayAssigns(t *testing.T) {
	graph := iac.EnvironmentConfigToGraph(decode(t, `{"volumes": {"vol-1": {
		"sizeMB": 5000, "region": "sfo", "alerts": {"usage": {"80": {}, "95": {}, "100": {}}}
	}}}`), iac.ConfigToGraphOptions{ProjectName: "test", VolumeNamesByID: map[string]any{"vol-1": "data"}})
	snap := &snapshot{graph: graph, follow: &iac.BranchFollow{}}
	kind := NewVolumeResource().(*nodeResource[volumeModel]).kind
	plan := func(model *volumeModel) []string {
		planned, err := (&engine{}).diff(t.Context(), snap, "volume.data", build(t, kind, model))
		if err != nil {
			t.Fatal(err)
		}
		return summaries(planned.changes)
	}

	if changes := plan(&volumeModel{commonModel: named("data"), SizeMB: ptr(int64(5000)), UsageAlerts: []int64{80, 95}}); len(changes) > 0 {
		t.Errorf("unset region and 100%% alert planned %v", changes)
	}
	changes := plan(&volumeModel{commonModel: named("data"), SizeMB: ptr(int64(5000)), Region: ptr("us-west2"), UsageAlerts: []int64{80}})
	if len(changes) != 1 || !strings.Contains(changes[0], `config.region ("sfo" → "us-west2")`) || !strings.Contains(changes[0], "config.alerts.usage.95") {
		t.Errorf("explicit region and a removed alert planned %v", changes)
	}
}
