package provider

import (
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/tambo-ai/terraform-provider-railway/internal/iac"
)

// stagingEnvironmentConfig is the shape backboard returns for Charming's
// staging environment, with variable values masked as a non-decrypting read
// returns them.
const stagingEnvironmentConfig = `{
	"services": {
		"svc-charming": {
			"source": {"repo": "tambo-ai/charming", "branch": "main", "checkSuites": false},
			"build": {"builder": "DOCKERFILE", "buildEnvironment": "V3", "dockerfilePath": "apps/server/Dockerfile"},
			"deploy": {
				"drainingSeconds": 0, "overlapSeconds": 0,
				"healthcheckPath": "/healthz", "healthcheckTimeout": 30, "restartPolicyMaxRetries": 3,
				"limitOverride": {"containers": {"cpu": 32, "memoryBytes": 32000000000}},
				"multiRegionConfig": {"us-west2": {"numReplicas": 1}}
			},
			"variables": {
				"CHARMING__SERVER__OP_ENV_FILE": {"value": ".env.staging.tpl"},
				"DATABASE_URL": {"value": "*****"},
				"OP_SERVICE_ACCOUNT_TOKEN": {"value": "*****"}
			}
		},
		"svc-postgres": {
			"source": {"image": "ghcr.io/railwayapp-templates/postgres-ssl:18"},
			"deploy": {"drainingSeconds": 0, "overlapSeconds": 0, "multiRegionConfig": {"us-west2": {"numReplicas": 1}}, "requiredMountPath": "/var/lib/postgresql/data"},
			"volumeMounts": {"vol-postgres": {"mountPath": "/var/lib/postgresql/data"}}
		}
	},
	"volumes": {
		"vol-postgres": {"sizeMB": 50000, "region": "us-west2", "allowOnlineResize": true, "alerts": {"usage": {"80": {}, "95": {}, "100": {}}}}
	}
}`

func stagingGraph(t *testing.T) iac.Graph {
	t.Helper()
	return iac.EnvironmentConfigToGraph(decode(t, stagingEnvironmentConfig), iac.ConfigToGraphOptions{
		ProjectName:            "buildy",
		ServiceNamesByID:       map[string]any{"svc-charming": "charming", "svc-postgres": "postgres"},
		TemplateServiceIDsByID: map[string]any{"svc-postgres": "tmpl-postgres"},
		VolumeNamesByID:        map[string]any{"vol-postgres": "postgres-volume"},
	})
}

// A configuration that matches the live environment plans no changes, so a
// translated railway.ts converges to an empty plan.
func TestMatchingConfigurationPlansNoChanges(t *testing.T) {
	snap := &snapshot{graph: stagingGraph(t), follow: &iac.BranchFollow{}}
	e := &engine{}
	cases := map[string]map[string]any{}

	cases["service.charming"] = build(t, serviceKind(), stagingCharming())
	cases["database.postgres"] = build(t, NewDatabaseResource().(*nodeResource[databaseModel]).kind, &databaseModel{
		commonModel: named("postgres"), Engine: types.StringValue("postgres"), Region: ptr("us-west2"),
		serviceSettings: serviceSettings{Deploy: &deployModel{DrainingSeconds: ptr(int64(0)), OverlapSeconds: ptr(int64(0))}},
	})
	cases["volume.postgres-volume"] = build(t, NewVolumeResource().(*nodeResource[volumeModel]).kind, &volumeModel{
		commonModel: named("postgres-volume"), SizeMB: ptr(int64(50000)), Region: ptr("us-west2"),
		AllowOnlineResize: ptr(true), UsageAlerts: []int64{80, 95, 100},
	})

	for address, node := range cases {
		t.Run(address, func(t *testing.T) {
			planned, err := e.diff(t.Context(), snap, address, node)
			if err != nil {
				t.Fatal(err)
			}
			if len(planned.changes) != 0 || len(planned.diagnostics) != 0 {
				t.Fatalf("want no changes, got %v %v", summaries(planned.changes), planned.diagnostics)
			}
		})
	}
}

// Changing one setting plans exactly that change, only for the resource the
// configuration owns.
func TestChangedSettingPlansOnlyThatResource(t *testing.T) {
	snap := &snapshot{graph: stagingGraph(t), follow: &iac.BranchFollow{}}
	model := stagingCharming()
	model.Deploy.HealthcheckTimeout = ptr(int64(60))
	model.PreserveVariables = []string{"DATABASE_URL"}

	planned, err := (&engine{}).diff(t.Context(), snap, "service.charming", build(t, serviceKind(), model))
	if err != nil {
		t.Fatal(err)
	}
	kinds := map[string]bool{}
	for _, change := range planned.changes {
		if iac.FieldStr(change, "address") != "service.charming" {
			t.Fatalf("change for another resource: %v", change)
		}
		kinds[iac.FieldStr(change, "kind")+" "+iac.FieldStr(change, "path")] = true
	}
	for _, want := range []string{
		"resource.update resources.service.charming.deploy",
		"variable.delete resources.service.charming.variables.OP_SERVICE_ACCOUNT_TOKEN",
	} {
		if !kinds[want] {
			t.Errorf("missing %q in %v", want, summaries(planned.changes))
		}
	}
	if len(planned.changes) != 2 {
		t.Errorf("want 2 changes, got %v", summaries(planned.changes))
	}
}

// Deleting one resource never deletes the others in the environment.
func TestDeletePlansOnlyThatResource(t *testing.T) {
	snap := &snapshot{graph: stagingGraph(t), follow: &iac.BranchFollow{}}
	planned, err := (&engine{}).diff(t.Context(), snap, "service.charming", nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(planned.changes) != 1 || iac.FieldStr(planned.changes[0], "kind") != "resource.delete" {
		t.Fatalf("want one resource.delete, got %v", summaries(planned.changes))
	}
}

func stagingCharming() *serviceModel {
	return &serviceModel{
		commonModel: named("charming"),
		Source:      &sourceModel{GitHub: &githubSourceModel{Repo: "tambo-ai/charming", CheckSuites: ptr(false)}},
		serviceSettings: serviceSettings{
			Build: &buildModel{Builder: ptr("DOCKERFILE"), BuildEnvironment: ptr("V3"), DockerfilePath: ptr("apps/server/Dockerfile")},
			Deploy: &deployModel{
				DrainingSeconds: ptr(int64(0)), OverlapSeconds: ptr(int64(0)),
				HealthcheckPath: ptr("/healthz"), HealthcheckTimeout: ptr(int64(30)), RestartPolicyMaxRetries: ptr(int64(3)),
				LimitOverride: &limitOverrideModel{CPU: ptr(32.0), MemoryBytes: ptr(int64(32000000000))},
			},
			Replicas:          map[string]int64{"us-west2": 1},
			Variables:         map[string]string{"CHARMING__SERVER__OP_ENV_FILE": ".env.staging.tpl"},
			PreserveVariables: []string{"OP_SERVICE_ACCOUNT_TOKEN", "DATABASE_URL"},
		},
	}
}
