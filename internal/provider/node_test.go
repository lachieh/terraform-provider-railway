package provider

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/tambo-ai/terraform-provider-railway-iac-sdk/internal/iac"
)

func ptr[T any](value T) *T { return &value }

func named(name string) commonModel {
	return commonModel{Name: types.StringValue(name), EnvironmentID: types.StringValue("env")}
}

func decode(t *testing.T, text string) map[string]any {
	t.Helper()
	var out map[string]any
	if err := json.Unmarshal([]byte(text), &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func assertNode(t *testing.T, got map[string]any, want string) {
	t.Helper()
	if iac.StableStringify(got) != iac.StableStringify(decode(t, want)) {
		t.Fatalf("node mismatch\n got: %s\nwant: %s", iac.StableStringify(got), iac.StableStringify(decode(t, want)))
	}
}

func build[M any](t *testing.T, kind nodeKind[M], model *M) map[string]any {
	t.Helper()
	r := &nodeResource[M]{kind: kind}
	node, diags := r.desired(model)
	if diags.HasError() {
		t.Fatalf("desired: %v", diags)
	}
	return node
}

func serviceKind() nodeKind[serviceModel] {
	return NewServiceResource().(*nodeResource[serviceModel]).kind
}

// Expected nodes are what the Railway CLI compiles from the equivalent
// railway.go. The SDK stores typed nil maps for networking and volumeMounts,
// which encode as null; the diff ignores them (see converge_test.go).

// The staging charming service from infra/charming-railway/.railway/railway.ts.
func TestServiceNodeMatchesCharmingStaging(t *testing.T) {
	model := &serviceModel{
		commonModel: named("charming"),
		Source:      &sourceModel{GitHub: &githubSourceModel{Repo: "tambo-ai/charming", CheckSuites: ptr(false)}},
		serviceSettings: serviceSettings{
			Build: &buildModel{
				Builder:          ptr("DOCKERFILE"),
				BuildEnvironment: ptr("V3"),
				DockerfilePath:   ptr("apps/server/Dockerfile"),
			},
			Deploy: &deployModel{
				DrainingSeconds:         ptr(int64(0)),
				OverlapSeconds:          ptr(int64(0)),
				HealthcheckPath:         ptr("/healthz"),
				HealthcheckTimeout:      ptr(int64(30)),
				RestartPolicyMaxRetries: ptr(int64(3)),
				LimitOverride:           &limitOverrideModel{CPU: ptr(32.0), MemoryBytes: ptr(int64(32000000000))},
			},
			Replicas:          map[string]int64{"us-west2": 1},
			Variables:         map[string]string{"CHARMING__SERVER__OP_ENV_FILE": ".env.staging.tpl"},
			PreserveVariables: []string{"OP_SERVICE_ACCOUNT_TOKEN", "DATABASE_URL"},
		},
	}
	assertNode(t, build(t, serviceKind(), model), `{
		"address": "service.charming", "type": "service", "kind": "github", "name": "charming",
		"networking": {"customDomains": null, "tcpProxies": null},
		"source": {"type": "github", "repo": "tambo-ai/charming", "branch": "main", "checkSuites": false},
		"build": {"builder": "DOCKERFILE", "buildEnvironment": "V3", "dockerfilePath": "apps/server/Dockerfile"},
		"deploy": {
			"drainingSeconds": 0, "overlapSeconds": 0,
			"healthcheckPath": "/healthz", "healthcheckTimeout": 30, "restartPolicyMaxRetries": 3,
			"limitOverride": {"containers": {"cpu": 32, "memoryBytes": 32000000000}},
			"multiRegionConfig": {"us-west2": {"numReplicas": 1}}
		},
		"variables": {
			"CHARMING__SERVER__OP_ENV_FILE": {"type": "literal", "value": ".env.staging.tpl"},
			"DATABASE_URL": {"type": "preserve"},
			"OP_SERVICE_ACCOUNT_TOKEN": {"type": "preserve"}
		}
	}`)
}

// The production release shape: a pinned image digest with auto updates off,
// custom domains on automatic and explicit ports.
func TestServiceNodeImageSourceAndDomains(t *testing.T) {
	model := &serviceModel{
		commonModel: named("charming"),
		Source: &sourceModel{Image: &imageSourceModel{
			Image:       "ghcr.io/tambo-ai/charming-server@sha256:" + strings.Repeat("a", 64),
			AutoUpdates: &autoUpdatesModel{Type: "disabled"},
		}},
		serviceSettings: serviceSettings{
			CustomDomains: map[string]domainModel{
				"charming.test":     {Port: ptr(int64(0))},
				"mcp.charming.test": {Port: ptr(int64(8080))},
				"www.charming.test": {},
			},
		},
	}
	assertNode(t, build(t, serviceKind(), model), `{
		"address": "service.charming", "type": "service", "kind": "docker-image", "name": "charming",
		"source": {"type": "image", "image": "ghcr.io/tambo-ai/charming-server@sha256:`+strings.Repeat("a", 64)+`", "autoUpdates": {"type": "disabled"}},
		"networking": {"customDomains": {
			"charming.test": {"port": 0},
			"mcp.charming.test": {"port": 8080},
			"www.charming.test": {"port": 8080}
		}, "tcpProxies": null}
	}`)
}

func TestServiceNodeVolumeMountReferencesVolumeByAddress(t *testing.T) {
	model := &serviceModel{
		commonModel:     named("postgres"),
		Source:          &sourceModel{Image: &imageSourceModel{Image: "ghcr.io/railwayapp-templates/postgres-ssl:17"}},
		serviceSettings: serviceSettings{VolumeMounts: map[string]string{"/var/lib/postgresql/data": "postgres-volume"}},
	}
	assertNode(t, build(t, serviceKind(), model), `{
		"address": "service.postgres", "type": "service", "kind": "docker-image", "name": "postgres",
		"networking": {"customDomains": null, "tcpProxies": null}, "volumeMounts": null,
		"source": {"type": "image", "image": "ghcr.io/railwayapp-templates/postgres-ssl:17"},
		"volumeAttachments": {"postgres-volume": {"volume": "volume.postgres-volume", "mountPath": "/var/lib/postgresql/data"}}
	}`)
}

func TestServiceNodeVariableKinds(t *testing.T) {
	model := &serviceModel{
		commonModel: named("web"),
		serviceSettings: serviceSettings{
			Variables:          map[string]string{"OTEL_HEADERS": "Authorization=Bearer ${{POSTHOG_API_KEY}}"},
			SharedVariables:    map[string]string{"SECRET": "WEBHOOK_SECRET"},
			VariableReferences: map[string]referenceModel{"DATABASE_URL": {Resource: "database.postgres", Output: "DATABASE_URL"}},
		},
	}
	node := build(t, serviceKind(), model)
	assertNode(t, map[string]any{"variables": node["variables"]}, `{"variables": {
		"OTEL_HEADERS": {"type": "literal", "value": "Authorization=Bearer ${{POSTHOG_API_KEY}}"},
		"SECRET": {"type": "sharedReference", "name": "WEBHOOK_SECRET"},
		"DATABASE_URL": {"type": "reference", "resource": "database.postgres", "output": "DATABASE_URL"}
	}}`)
}

func TestServiceNodeRejectsWhatTheSDKRejects(t *testing.T) {
	for name, source := range map[string]*sourceModel{
		"auto updates outside Docker Hub and GHCR": {Image: &imageSourceModel{
			Image: "registry.example.test/app:1", AutoUpdates: &autoUpdatesModel{Type: "patch"},
		}},
		"maintenance window that ends before it starts": {Image: &imageSourceModel{
			Image: "nginx:latest", AutoUpdates: &autoUpdatesModel{Type: "patch", Schedule: []updateWindowModel{{Day: 6, StartHour: 6, EndHour: 2}}},
		}},
	} {
		t.Run(name, func(t *testing.T) {
			r := &nodeResource[serviceModel]{kind: serviceKind()}
			_, diags := r.desired(&serviceModel{commonModel: named("web"), Source: source})
			if !diags.HasError() {
				t.Fatal("expected the SDK's validation error as a diagnostic")
			}
		})
	}
}

// The production postgres database: Railway's template with the region and
// the deploy overrides railway.ts spreads over it.
func TestDatabaseNodeOverlaysDeploySettings(t *testing.T) {
	kind := NewDatabaseResource().(*nodeResource[databaseModel]).kind
	model := &databaseModel{
		commonModel: named("postgres"),
		Engine:      "postgres",
		Region:      ptr("us-west2"),
		serviceSettings: serviceSettings{Deploy: &deployModel{
			DrainingSeconds: ptr(int64(0)),
			OverlapSeconds:  ptr(int64(0)),
		}},
	}
	assertNode(t, build(t, kind, model), `{
		"address": "database.postgres", "type": "database", "kind": "database", "engine": "postgres",
		"name": "postgres", "image": "ghcr.io/railwayapp-templates/postgres-ssl:18", "output": "DATABASE_URL",
		"defaultMountPath": "/var/lib/postgresql/data",
		"networking": {"customDomains": null, "tcpProxies": null},
		"source": {"type": "image", "image": "ghcr.io/railwayapp-templates/postgres-ssl:18"},
		"deploy": {
			"multiRegionConfig": {"us-west2": {"numReplicas": 1}},
			"drainingSeconds": 0, "overlapSeconds": 0
		}
	}`)
}

func TestVolumeNodeMatchesCharmingPostgresVolume(t *testing.T) {
	kind := NewVolumeResource().(*nodeResource[volumeModel]).kind
	model := &volumeModel{
		commonModel:       named("postgres-volume"),
		SizeMB:            ptr(int64(50000)),
		Region:            ptr("us-west2"),
		AllowOnlineResize: ptr(true),
		UsageAlerts:       []int64{80, 95, 100},
	}
	assertNode(t, build(t, kind, model), `{
		"address": "volume.postgres-volume", "type": "volume", "name": "postgres-volume",
		"config": {"alerts": {"usage": {"100": {}, "80": {}, "95": {}}}, "allowOnlineResize": true, "region": "us-west2", "sizeMB": 50000}
	}`)
}

func TestBucketNode(t *testing.T) {
	kind := NewBucketResource().(*nodeResource[bucketModel]).kind
	assertNode(t, build(t, kind, &bucketModel{commonModel: named("Postgres-PITR"), Region: ptr("sjc")}), `{
		"address": "bucket.Postgres-PITR", "type": "bucket", "name": "Postgres-PITR", "config": {"region": "sjc"}
	}`)
}
