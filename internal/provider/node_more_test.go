package provider

import (
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/types"
)

const hookSource = `export default { fetch() { return new Response("ok") } }`

func TestFunctionNodeCarriesItsCodeInTheStartCommand(t *testing.T) {
	kind := NewFunctionResource().(*nodeResource[functionModel]).kind
	node := build(t, kind, &functionModel{commonModel: named("hook"), Code: types.StringValue(hookSource)})
	assertNode(t, map[string]any{"kind": node["kind"], "source": node["source"], "start": node["deploy"].(map[string]any)["startCommand"]}, `{
		"kind": "function",
		"source": {"type": "image", "image": "ghcr.io/railwayapp/function-bun:1.4.0"},
		"start": "./run.sh ZXhwb3J0IGRlZmF1bHQgeyBmZXRjaCgpIHsgcmV0dXJuIG5ldyBSZXNwb25zZSgib2siKSB9IH0="
	}`)

	pinned := build(t, kind, &functionModel{commonModel: named("hook"), Code: types.StringValue(hookSource), Image: ptr("ghcr.io/railwayapp/function-bun:1.2.16")})
	assertNode(t, pinned["source"].(map[string]any), `{"type": "image", "image": "ghcr.io/railwayapp/function-bun:1.2.16"}`)
}

func TestFunctionRefusesAStartCommand(t *testing.T) {
	r := &nodeResource[functionModel]{kind: NewFunctionResource().(*nodeResource[functionModel]).kind}
	_, diags := r.desired(&functionModel{
		commonModel:     named("hook"),
		Code:            types.StringValue(hookSource),
		serviceSettings: serviceSettings{Deploy: &deployModel{StartCommand: ptr("bun run x")}},
	})
	if !diags.HasError() {
		t.Fatal("want an error for code plus a start command")
	}
}

func TestGroupNodeAndMembership(t *testing.T) {
	group := build(t, NewGroupResource().(*nodeResource[groupModel]).kind, &groupModel{
		commonModel: named("data"), Color: ptr("#0f766e"), IsCollapsed: ptr(true), Group: ptr("platform"),
	})
	assertNode(t, group, `{"address": "group.data", "type": "group", "name": "data", "color": "#0f766e", "isCollapsed": true, "groupId": "platform"}`)

	volume := build(t, NewVolumeResource().(*nodeResource[volumeModel]).kind, &volumeModel{commonModel: named("pgdata"), Group: ptr("data")})
	bucket := build(t, NewBucketResource().(*nodeResource[bucketModel]).kind, &bucketModel{commonModel: named("backups"), Group: ptr("data")})
	service := build(t, serviceKind(), &serviceModel{commonModel: named("web"), serviceSettings: serviceSettings{Group: ptr("data")}})
	for name, node := range map[string]map[string]any{"volume": volume, "bucket": bucket, "service": service} {
		if node["groupId"] != "data" {
			t.Errorf("%s groupId = %v", name, node["groupId"])
		}
	}
}

func TestDatabaseNodeForAnEngineYouBring(t *testing.T) {
	kind := NewDatabaseResource().(*nodeResource[databaseModel]).kind
	node := build(t, kind, &databaseModel{
		commonModel: named("clickhouse"), Engine: types.StringValue("clickhouse"), Image: ptr("clickhouse/clickhouse-server:25"),
		Output: ptr("CLICKHOUSE_URL"), DefaultMountPath: ptr("/var/lib/clickhouse"),
	})
	for key, want := range map[string]string{"engine": "clickhouse", "image": "clickhouse/clickhouse-server:25", "output": "CLICKHOUSE_URL", "defaultMountPath": "/var/lib/clickhouse"} {
		if node[key] != want {
			t.Errorf("%s = %v, want %s", key, node[key], want)
		}
	}

	r := &nodeResource[databaseModel]{kind: kind}
	if _, diags := r.desired(&databaseModel{commonModel: named("clickhouse"), Engine: types.StringValue("clickhouse")}); !diags.HasError() {
		t.Fatal("want an error for a non-template engine without an image")
	}
}

func TestServiceNodeSourcesAndClusterSettings(t *testing.T) {
	node := build(t, serviceKind(), &serviceModel{
		commonModel: named("worker"),
		Source:      &sourceModel{GitHub: &githubSourceModel{Repo: "acme/app", CommitSHA: ptr("0123abc")}},
		serviceSettings: serviceSettings{
			Replicas:        map[string]int64{"us-west2": 2, "europe-west4": 1},
			ReplicaStackers: map[string]string{"us-west2": "stacker-a"},
			ClusterRole:     ptr("replica"),
			ParentServiceID: ptr("svc-root"),
			ReplicaConfig:   &replicaConfigModel{MinReplicas: ptr(int64(1)), MaxReplicas: ptr(int64(4)), Scalable: ptr(true)},
			ClusterDisplay:  &clusterDisplayModel{Badge: ptr("R"), BadgeVariant: ptr("muted")},
		},
	})
	assertNode(t, map[string]any{
		"source": node["source"], "regions": node["deploy"].(map[string]any)["multiRegionConfig"],
		"clusterRole": node["clusterRole"], "parentServiceId": node["parentServiceId"],
		"replicaConfig": node["replicaConfig"], "clusterDisplay": node["clusterDisplay"],
	}, `{
		"source": {"type": "github", "repo": "acme/app", "branch": "main", "commitSha": "0123abc"},
		"regions": {"us-west2": {"numReplicas": 2, "stackerAssignment": "stacker-a"}, "europe-west4": {"numReplicas": 1}},
		"clusterRole": "replica", "parentServiceId": "svc-root",
		"replicaConfig": {"minReplicas": 1, "maxReplicas": 4, "scalable": true},
		"clusterDisplay": {"badge": "R", "badgeVariant": "muted"}
	}`)

	empty := build(t, serviceKind(), &serviceModel{commonModel: named("upload"), Source: &sourceModel{Empty: &emptySourceModel{RootDirectory: ptr("apps/upload")}}})
	assertNode(t, empty["source"].(map[string]any), `{"type": "empty", "rootDirectory": "apps/upload"}`)
}
