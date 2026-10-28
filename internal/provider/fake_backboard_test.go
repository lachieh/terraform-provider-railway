package provider

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"sync"
	"testing"

	"github.com/tambo-ai/terraform-provider-railway-iac-sdk/internal/iac"
)

// fakeBackboard is an in-memory Railway environment. It serves the
// environment config compiled from its graph, and applies ChangeSets to the
// graph the way backboard does: create, update a top-level field, set or
// delete a variable, delete. Resource IDs are the resource names.
type fakeBackboard struct {
	t *testing.T

	mu      sync.Mutex
	graph   iac.Graph
	etag    int
	owners  map[string]string
	applied []iac.ChangeSet
	deleted []string
	// staleOnce answers the next apply with a stale-base error.
	staleOnce bool
}

const (
	fakeEnvironmentID = "env-test"
	fakeProjectID     = "project-test"
)

func newFakeBackboard(t *testing.T) (*fakeBackboard, *httptest.Server) {
	fake := &fakeBackboard{t: t, graph: iac.ProjectDefinitionToGraph(map[string]any{"name": "test", "resources": []any{}})}
	server := httptest.NewServer(http.HandlerFunc(fake.serve))
	t.Cleanup(server.Close)
	return fake, server
}

var operationName = regexp.MustCompile(`^\s*(query|mutation)\s+(\w+)`)

func (f *fakeBackboard) serve(w http.ResponseWriter, r *http.Request) {
	var request struct {
		Query     string         `json:"query"`
		Variables map[string]any `json:"variables"`
	}
	if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	match := operationName.FindStringSubmatch(request.Query)
	if match == nil {
		http.Error(w, "no operation name", http.StatusBadRequest)
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	data, gqlErr := f.handle(match[2], request.Variables)
	w.Header().Set("Content-Type", "application/json")
	if gqlErr != nil {
		_ = json.NewEncoder(w).Encode(map[string]any{"errors": []any{gqlErr}})
		return
	}
	_ = json.NewEncoder(w).Encode(map[string]any{"data": data})
}

func (f *fakeBackboard) handle(operation string, variables map[string]any) (any, map[string]any) {
	switch operation {
	case "IacEnvironmentConfig":
		var existing []string
		for _, resource := range f.graph.Resources {
			existing = append(existing, iac.ResourceName(resource))
		}
		config := iac.GraphToEnvironmentConfig(f.graph, iac.CompileOptions{ExistingServiceIDs: existing})
		environment := map[string]any{
			"id": fakeEnvironmentID, "name": "staging", "projectId": fakeProjectID, "isEphemeral": false,
			"meta": nil, "sourceEnvironment": nil, "config": f.liveConfig(config),
			"configEtag": f.etagString(), "canvasGroupRefs": map[string]any{},
		}
		if f.owners != nil {
			environment["iacPartials"] = f.owners
		}
		return map[string]any{"environment": environment}, nil
	case "IacProjectName":
		return map[string]any{"project": map[string]any{"name": "test"}}, nil
	case "IacProjectServices":
		var edges []any
		for _, resource := range f.graph.Resources {
			if kind := iac.ResourceType(resource); kind == "service" || kind == "database" {
				node := map[string]any{"id": iac.ResourceName(resource), "name": iac.ResourceName(resource), "templateServiceId": nil}
				if kind == "database" {
					node["templateServiceId"] = "template"
				}
				edges = append(edges, map[string]any{"node": node})
			}
		}
		return connection(edges), nil
	case "IacProjectVolumes", "IacProjectBuckets":
		want := "volume"
		if operation == "IacProjectBuckets" {
			want = "bucket"
		}
		var edges []any
		for _, resource := range f.graph.Resources {
			if iac.ResourceType(resource) == want {
				node := map[string]any{"id": iac.ResourceName(resource), "name": iac.ResourceName(resource), "groupId": nil}
				if group, ok := resource["groupId"].(string); ok {
					node["groupId"] = group
				}
				edges = append(edges, map[string]any{"node": node})
			}
		}
		return connection(edges), nil
	case "IacProject":
		return map[string]any{"project": map[string]any{"id": fakeProjectID, "name": "test", "workspaceId": "workspace-test"}}, nil
	case "IacProjectEnvironments":
		return connection([]any{
			map[string]any{"node": map[string]any{"id": fakeEnvironmentID, "name": "staging", "isEphemeral": false}},
			map[string]any{"node": map[string]any{"id": "env-production", "name": "production", "isEphemeral": false}},
		}), nil
	case "GitHubRepos":
		return map[string]any{"githubRepos": []any{}}, nil
	case "IacPreviewChangeSet":
		return map[string]any{"environmentPreviewChangeSet": map[string]any{
			"changeSet": f.preview(variables["input"].(map[string]any)), "diagnostics": []any{}, "effects": []any{},
		}}, nil
	case "IacApplyChangeSet":
		if base, _ := variables["baseConfigEtag"].(string); base != f.etagString() || f.staleOnce {
			f.staleOnce = false
			return nil, map[string]any{
				"message":    "The environment configuration changed since this plan was made.",
				"extensions": map[string]any{"code": "STALE_ENVIRONMENT_BASE"},
			}
		}
		raw, _ := json.Marshal(variables["input"])
		var changeSet iac.ChangeSet
		if err := json.Unmarshal(raw, &changeSet); err != nil {
			f.t.Errorf("apply input is not a ChangeSet: %v", err)
		}
		f.applied = append(f.applied, changeSet)
		f.apply(changeSet)
		return map[string]any{"environmentApplyChangeSet": map[string]any{
			"id": fmt.Sprintf("apply-%d", f.etag), "status": "applied", "deploymentId": nil, "stagedPatchId": nil,
			"diagnostics": []any{}, "changes": []any{},
		}}, nil
	case "IacVolumeDelete":
		f.deleted = append(f.deleted, variables["volumeId"].(string))
		f.remove("volume." + variables["volumeId"].(string))
		return map[string]any{"volumeDelete": true}, nil
	}
	f.t.Errorf("fake backboard: unexpected operation %s", operation)
	return nil, map[string]any{"message": "unexpected operation " + operation}
}

// preview drops variable sets that match the stored, unmasked value, as
// backboard does when it compares against decrypted state.
func (f *fakeBackboard) preview(changeSet map[string]any) map[string]any {
	changes, _ := changeSet["changes"].([]any)
	kept := []any{}
	for _, item := range changes {
		change := item.(map[string]any)
		if iac.FieldStr(change, "kind") == "variable.set" {
			node := f.graph.FindResource(iac.FieldStr(change, "address"))
			stored := iac.Field(iac.Field(node, "variables"), iac.FieldStr(change, "variable"))
			if stored != nil && iac.StableStringify(stored) == iac.StableStringify(change["after"]) {
				continue
			}
		}
		kept = append(kept, change)
	}
	out := iac.CloneMap(changeSet)
	out["changes"] = kept
	return out
}

func (f *fakeBackboard) etagString() string { return fmt.Sprintf("etag-%d", f.etag) }

func (f *fakeBackboard) apply(changeSet iac.ChangeSet) {
	for _, change := range changeSet.Changes {
		address := iac.FieldStr(change, "address")
		switch iac.FieldStr(change, "kind") {
		case "resource.create":
			f.graph.Resources = append(f.graph.Resources, change["resource"].(map[string]any))
		case "resource.delete":
			f.remove(address)
		case "resource.update":
			node := f.graph.FindResource(address)
			if after, ok := change["after"]; ok && after != nil {
				node[iac.FieldStr(change, "field")] = after
			} else {
				delete(node, iac.FieldStr(change, "field"))
			}
		case "variable.set", "variable.delete":
			node := f.graph.FindResource(address)
			vars, _ := node["variables"].(map[string]any)
			if vars == nil {
				vars = map[string]any{}
				node["variables"] = vars
			}
			if iac.FieldStr(change, "kind") == "variable.set" {
				after := change["after"].(map[string]any)
				if after["type"] != "preserve" {
					vars[iac.FieldStr(change, "variable")] = after
				}
			} else {
				delete(vars, iac.FieldStr(change, "variable"))
			}
		}
	}
	f.rebuild()
	f.etag++
}

func (f *fakeBackboard) remove(address string) {
	kept := f.graph.Resources[:0]
	for _, resource := range f.graph.Resources {
		if iac.ResourceAddr(resource) != address {
			kept = append(kept, resource)
		}
	}
	f.graph.Resources = kept
	f.rebuild()
}

// rebuild recomputes edges after the resources change.
func (f *fakeBackboard) rebuild() {
	resources := make([]any, 0, len(f.graph.Resources))
	for _, resource := range f.graph.Resources {
		resources = append(resources, resource)
	}
	f.graph = iac.ProjectDefinitionToGraph(map[string]any{"name": "test", "resources": resources})
}

// drift edits the live environment outside Terraform.
func (f *fakeBackboard) drift(address string, edit func(node map[string]any)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	edit(f.graph.FindResource(address))
	f.etag++
}

func (f *fakeBackboard) addresses() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for _, resource := range f.graph.Resources {
		out = append(out, iac.ResourceAddr(resource))
	}
	return out
}

func (f *fakeBackboard) changeSets() []iac.ChangeSet {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]iac.ChangeSet(nil), f.applied...)
}

// liveConfig drops what GraphToEnvironmentConfig writes for a patch but a
// committed environment config does not carry.
func (f *fakeBackboard) liveConfig(config map[string]any) map[string]any {
	// GraphToEnvironmentConfig prunes the empty alert thresholds a live
	// volume config keeps.
	for _, resource := range f.graph.Resources {
		if iac.ResourceType(resource) == "volume" {
			config["volumes"].(map[string]any)[iac.ResourceName(resource)] = iac.Clone(resource["config"])
		}
	}
	for _, section := range []string{"services", "volumes", "buckets"} {
		entries, _ := config[section].(map[string]any)
		for _, entry := range entries {
			if object, ok := entry.(map[string]any); ok {
				delete(object, "isCreated")
				if section == "buckets" {
					// Railway reports a bucket's group through the buckets
					// query, not its config.
					delete(object, "groupId")
				}
			}
		}
	}
	return maskVariables(config)
}

// maskVariables hides variable values the way a read without
// decryptVariables does, except for the ones Terraform set as literals in
// this test, which carry a "public-" prefix.
func maskVariables(config map[string]any) map[string]any {
	services, _ := config["services"].(map[string]any)
	for _, service := range services {
		vars, _ := service.(map[string]any)["variables"].(map[string]any)
		for key, value := range vars {
			object, _ := value.(map[string]any)
			if text, _ := object["value"].(string); !strings.HasPrefix(text, "public-") {
				vars[key] = map[string]any{"value": "*****"}
			}
		}
	}
	return config
}

func connection(edges []any) map[string]any {
	if edges == nil {
		edges = []any{}
	}
	return map[string]any{"project": map[string]any{"items": map[string]any{
		"edges": edges, "pageInfo": map[string]any{"hasNextPage": false, "endCursor": nil},
	}}}
}
