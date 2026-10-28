// Ported from railwayapp/cli src/iac/tests.rs (MIT, Copyright (c) Railway Corp.)

package iac

import (
	"encoding/json"
	"fmt"
	"testing"
)

// js decodes a JSON literal so fixture numbers are float64, as they are when
// the engine reads real input.
func js(text string) any {
	var value any
	if err := json.Unmarshal([]byte(text), &value); err != nil {
		panic(fmt.Sprintf("bad fixture %s: %v", text, err))
	}
	return value
}

func obj(text string) map[string]any {
	return js(text).(map[string]any)
}

// merge returns base with every key of the extras written over it.
func toValues(resources []map[string]any) []any {
	out := make([]any, len(resources))
	for index, resource := range resources {
		out[index] = resource
	}
	return out
}

func graphFrom(resources ...map[string]any) Graph {
	return ProjectDefinitionToGraph(map[string]any{"name": "app", "resources": toValues(resources)})
}

func service(name string, extra map[string]any) map[string]any {
	node := map[string]any{
		"address": "service." + name,
		"type":    "service",
		"kind":    "empty",
		"name":    name,
	}
	for key, value := range extra {
		node[key] = value
	}
	switch FieldStr(node["source"], "type") {
	case "github":
		node["kind"] = "github"
	case "image":
		node["kind"] = "docker-image"
	}
	return node
}

func github(repo string) map[string]any {
	return map[string]any{"type": "github", "repo": repo, "branch": "main"}
}

func image(name string) map[string]any {
	return map[string]any{"type": "image", "image": name}
}

func postgres(name, region string) map[string]any {
	node := map[string]any{
		"address":          "database." + name,
		"type":             "database",
		"kind":             "database",
		"engine":           "postgres",
		"name":             name,
		"image":            "ghcr.io/railwayapp-templates/postgres-ssl:18",
		"output":           "DATABASE_URL",
		"defaultMountPath": "/var/lib/postgresql/data",
		"source":           image("ghcr.io/railwayapp-templates/postgres-ssl:18"),
	}
	if region != "" {
		node["deploy"] = map[string]any{"multiRegionConfig": map[string]any{region: map[string]any{"numReplicas": float64(1)}}}
	}
	return node
}

func redis(name string) map[string]any {
	return map[string]any{
		"address":          "database." + name,
		"type":             "database",
		"kind":             "database",
		"engine":           "redis",
		"name":             name,
		"image":            "railwayapp/redis:8.2",
		"output":           "REDIS_URL",
		"defaultMountPath": "/bitnami",
		"source":           image("railwayapp/redis:8.2"),
	}
}

func volume(name string, config any) map[string]any {
	return map[string]any{
		"address": "volume." + name,
		"type":    "volume",
		"name":    name,
		"config":  config,
	}
}

func bucket(name, region string) map[string]any {
	return map[string]any{
		"address": "bucket." + name,
		"type":    "bucket",
		"name":    name,
		"config":  map[string]any{"region": region},
	}
}

func envConfig(config map[string]any) Graph {
	return EnvironmentConfigToGraph(config, ConfigToGraphOptions{ProjectName: "app"})
}

// managedDBConfig imports config with every service keyed by name and marked
// as deployed from a template, which is how Railway-managed databases show
// up.
func managedDBConfig(config map[string]any) Graph {
	templates := map[string]any{}
	services, _ := config["services"].(map[string]any)
	for id := range services {
		templates[id] = "tpl"
	}
	return EnvironmentConfigToGraph(config, ConfigToGraphOptions{ProjectName: "app", TemplateServiceIDsByID: templates})
}

func diff(current, desired Graph) ChangeSet {
	return DiffGraphs(DiffOptions{Current: &current, Desired: &desired})
}

func kinds(changeSet ChangeSet) []string {
	out := []string{}
	for _, change := range changeSet.Changes {
		out = append(out, FieldStr(change, "kind"))
	}
	return out
}

// at walks a decoded value by object keys and array indices, returning nil
// for any missing step, like serde_json's Index.
func at(value any, path ...any) any {
	for _, step := range path {
		switch key := step.(type) {
		case string:
			value = Field(value, key)
		case int:
			items, ok := value.([]any)
			if !ok || key < 0 || key >= len(items) {
				return nil
			}
			value = items[key]
		}
	}
	return value
}

func sameJSON(got, want any) bool {
	return StableStringify(got) == StableStringify(want)
}

func assertJSON(t *testing.T, got, want any) {
	t.Helper()
	if !sameJSON(got, want) {
		t.Fatalf("got %s, want %s", StableStringify(got), StableStringify(want))
	}
}

func assertEqual[T comparable](t *testing.T, got, want T) {
	t.Helper()
	if got != want {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func assertTrue(t *testing.T, condition bool, format string, args ...any) {
	t.Helper()
	if !condition {
		t.Fatalf(format, args...)
	}
}

func change(t *testing.T, changeSet ChangeSet, index int) map[string]any {
	t.Helper()
	if index >= len(changeSet.Changes) {
		t.Fatalf("want change %d, got %d changes: %s", index, len(changeSet.Changes), StableStringify(changeSet.Changes))
	}
	return changeSet.Changes[index]
}

func diagnostic(t *testing.T, changeSet ChangeSet, index int) Diagnostic {
	t.Helper()
	if index >= len(changeSet.Diagnostics) {
		t.Fatalf("want diagnostic %d, got %+v", index, changeSet.Diagnostics)
	}
	return changeSet.Diagnostics[index]
}

func assertNoChanges(t *testing.T, changeSet ChangeSet) {
	t.Helper()
	if len(changeSet.Changes) != 0 {
		t.Fatalf("want no changes, got %s", StableStringify(changeSet.Changes))
	}
}

func findChange(changeSet ChangeSet, match func(map[string]any) bool) map[string]any {
	for _, change := range changeSet.Changes {
		if match(change) {
			return change
		}
	}
	return nil
}

func anyChange(changeSet ChangeSet, match func(map[string]any) bool) bool {
	return findChange(changeSet, match) != nil
}

func anyDiagnostic(changeSet ChangeSet, match func(Diagnostic) bool) bool {
	for _, diagnostic := range changeSet.Diagnostics {
		if match(diagnostic) {
			return true
		}
	}
	return false
}

func contains(items []string, want string) bool {
	for _, item := range items {
		if item == want {
			return true
		}
	}
	return false
}
