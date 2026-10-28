package railwayapi

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
)

func TestFetchEnvironmentFallsBackWithoutIACPartials(t *testing.T) {
	envJSON := `{"id":"env","name":"pr-7","projectId":"proj","isEphemeral":true,
		"meta":{"branch":"feat/x","prNumber":7,"prRepo":"acme/app"},"sourceEnvironment":{"name":"production"},
		"config":{"services":{}},"configEtag":"etag-1","canvasGroupRefs":{"vol-1":"group-1"}}`
	f := newFake(t, func(n int, req recordedRequest, w http.ResponseWriter) {
		if strings.Contains(req.Query, "iacPartials") {
			writeJSON(w, 400, `{"errors":[{"message":"Cannot query field \"iacPartials\" on type \"Environment\"."}]}`)
			return
		}
		writeJSON(w, 200, `{"data":{"environment":`+envJSON+`}}`)
	})
	env, err := f.client(t, Credential{Token: testToken}).FetchEnvironment(context.Background(), "env", true)
	if err != nil {
		t.Fatal(err)
	}
	reqs := f.recorded()
	if len(reqs) != 2 {
		t.Fatalf("requests = %d, want 2", len(reqs))
	}
	want := map[string]any{"environmentId": "env", "decryptVariables": true}
	if !reflect.DeepEqual(reqs[1].Variables, want) {
		t.Errorf("fallback variables = %v, want %v", reqs[1].Variables, want)
	}
	if env.ConfigEtag != "etag-1" || env.IACPartials != nil || env.CanvasGroupRefs["vol-1"] != "group-1" {
		t.Errorf("environment = %+v", env)
	}
	pr := env.PRContext()
	if pr == nil || pr.Number != 7 || pr.Branch != "feat/x" || pr.Base != "production" {
		t.Errorf("PRContext = %+v", pr)
	}
}

func TestFetchEnvironmentReadsIACPartials(t *testing.T) {
	f := newFake(t, func(_ int, _ recordedRequest, w http.ResponseWriter) {
		writeJSON(w, 200, `{"data":{"environment":{"id":"env","name":"production","projectId":"proj","isEphemeral":false,
			"config":{},"configEtag":"e","canvasGroupRefs":{},"iacPartials":{"service.api":"ops","bad":3}}}}`)
	})
	env, err := f.client(t, Credential{Token: testToken}).FetchEnvironment(context.Background(), "env", false)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(env.IACPartials, map[string]string{"service.api": "ops"}) {
		t.Errorf("IACPartials = %v", env.IACPartials)
	}
	if env.PRContext() != nil {
		t.Error("non-ephemeral environment reported a PR")
	}
}

func TestFetchEnvironmentDoesNotFallBackOnOtherErrors(t *testing.T) {
	f := newFake(t, func(_ int, _ recordedRequest, w http.ResponseWriter) {
		writeJSON(w, 200, `{"errors":[{"message":"Environment not found"}]}`)
	})
	_, err := f.client(t, Credential{Token: testToken}).FetchEnvironment(context.Background(), "env", false)
	if err == nil || len(f.recorded()) != 1 {
		t.Errorf("err = %v after %d requests, want an error after 1", err, len(f.recorded()))
	}
}

func TestPRContextRequiresNumber(t *testing.T) {
	env := &Environment{IsEphemeral: true, Meta: EnvironmentMeta{Branch: "feat/x"}}
	if env.PRContext() != nil {
		t.Error("ephemeral environment without PR number reported a PR")
	}
}

func pageResponse(edges, cursor string, hasNext bool) string {
	return fmt.Sprintf(`{"data":{"project":{"items":{"edges":[%s],"pageInfo":{"hasNextPage":%t,"endCursor":%q}}}}}`, edges, hasNext, cursor)
}

func TestFetchNameMapsPaginates(t *testing.T) {
	f := newFake(t, func(_ int, req recordedRequest, w http.ResponseWriter) {
		after, _ := req.Variables["after"].(string)
		switch {
		case strings.Contains(req.Query, "IacProjectName"):
			writeJSON(w, 200, `{"data":{"project":{"name":"Acme"}}}`)
		case strings.Contains(req.Query, "IacProjectServices") && after == "":
			writeJSON(w, 200, pageResponse(`{"node":{"id":"svc-1","name":"api","templateServiceId":"tpl-1"}}`, "c1", true))
		case strings.Contains(req.Query, "IacProjectServices") && after == "c1":
			writeJSON(w, 200, pageResponse(`{"node":{"id":"svc-2","name":"web","templateServiceId":null}}`, "c2", false))
		case strings.Contains(req.Query, "IacProjectVolumes"):
			writeJSON(w, 200, pageResponse(`{"node":{"id":"vol-1","name":"data"}},{"node":{"id":"vol-2","name":"logs"}}`, "", false))
		case strings.Contains(req.Query, "IacProjectBuckets"):
			writeJSON(w, 200, pageResponse(`{"node":{"id":"bkt-1","name":"media","groupId":"g-b"}}`, "", false))
		default:
			t.Errorf("unexpected request %s %v", req.Query, req.Variables)
			writeJSON(w, 200, `{"errors":[{"message":"unexpected"}]}`)
		}
	})
	env := &Environment{CanvasGroupRefs: map[string]any{"vol-1": "g-v"}}
	names, err := f.client(t, Credential{Token: testToken}).FetchNameMaps(context.Background(), "proj", env)
	if err != nil {
		t.Fatal(err)
	}
	want := &NameMaps{
		ProjectName:            "Acme",
		ServiceNamesByID:       map[string]any{"svc-1": "api", "svc-2": "web"},
		TemplateServiceIDsByID: map[string]any{"svc-1": "tpl-1"},
		VolumeNamesByID:        map[string]any{"vol-1": "data", "vol-2": "logs"},
		VolumeGroupIDsByID:     map[string]any{"vol-1": "g-v"},
		BucketNamesByID:        map[string]any{"bkt-1": "media"},
		BucketGroupIDsByID:     map[string]any{"bkt-1": "g-b"},
	}
	if !reflect.DeepEqual(names, want) {
		t.Errorf("name maps = %+v\nwant %+v", names, want)
	}
	var serviceVars []map[string]any
	for _, r := range f.recorded() {
		if strings.Contains(r.Query, "IacProjectServices") {
			serviceVars = append(serviceVars, r.Variables)
		}
	}
	wantVars := []map[string]any{
		{"projectId": "proj", "first": float64(pageSize)},
		{"projectId": "proj", "first": float64(pageSize), "after": "c1"},
	}
	if !reflect.DeepEqual(serviceVars, wantVars) {
		t.Errorf("service page variables = %v, want %v", serviceVars, wantVars)
	}
}

func TestPaginationRejectsStuckCursor(t *testing.T) {
	f := newFake(t, func(_ int, _ recordedRequest, w http.ResponseWriter) {
		writeJSON(w, 200, pageResponse(`{"node":{"id":"env-1","name":"production","isEphemeral":false}}`, "same", true))
	})
	_, err := f.client(t, Credential{Token: testToken}).Environments(context.Background(), "proj")
	if err == nil {
		t.Fatal("pagination with a repeating cursor did not fail")
	}
	if got := len(f.recorded()); got != 2 {
		t.Errorf("requests = %d, want 2", got)
	}
}

func TestEnvironmentsAcrossPages(t *testing.T) {
	f := newFake(t, func(_ int, req recordedRequest, w http.ResponseWriter) {
		if req.Variables["after"] == nil {
			writeJSON(w, 200, pageResponse(`{"node":{"id":"env-1","name":"production","isEphemeral":false}}`, "c1", true))
			return
		}
		writeJSON(w, 200, pageResponse(`{"node":{"id":"env-2","name":"pr-3","isEphemeral":true}}`, "c2", false))
	})
	envs, err := f.client(t, Credential{Token: testToken}).Environments(context.Background(), "proj")
	if err != nil {
		t.Fatal(err)
	}
	want := []EnvironmentSummary{{"env-1", "production", false}, {"env-2", "pr-3", true}}
	if !reflect.DeepEqual(envs, want) {
		t.Errorf("environments = %+v", envs)
	}
}

func TestGitHubDefaultBranchesLowercasesNames(t *testing.T) {
	f := newFake(t, func(_ int, _ recordedRequest, w http.ResponseWriter) {
		writeJSON(w, 200, `{"data":{"githubRepos":[{"fullName":"Acme/App","defaultBranch":"main"}]}}`)
	})
	branches, err := f.client(t, Credential{Token: testToken}).GitHubDefaultBranches(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(branches, map[string]string{"acme/app": "main"}) {
		t.Errorf("branches = %v", branches)
	}
}

func TestPreviewChangeSet(t *testing.T) {
	f := newFake(t, func(_ int, _ recordedRequest, w http.ResponseWriter) {
		writeJSON(w, 200, `{"data":{"environmentPreviewChangeSet":{"changeSet":{"ops":[1]},"diagnostics":[],"effects":[{"kind":"redeploy"}]}}}`)
	})
	changeSet := map[string]any{"ops": []any{float64(1)}}
	preview, err := f.client(t, Credential{Token: testToken}).PreviewChangeSet(context.Background(), "env", changeSet)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(preview.ChangeSet, changeSet) || preview.Effects == nil {
		t.Errorf("preview = %+v", preview)
	}
	want := map[string]any{"environmentId": "env", "input": changeSet}
	if got := f.recorded()[0].Variables; !reflect.DeepEqual(got, want) {
		t.Errorf("variables = %v, want %v", got, want)
	}
}

func TestApplyChangeSetPollsUntilApplied(t *testing.T) {
	f := newFake(t, func(n int, req recordedRequest, w http.ResponseWriter) {
		switch n {
		case 0:
			writeJSON(w, 200, `{"data":{"environmentApplyChangeSet":{"id":"apply-1","status":"applying","diagnostics":[],"changes":[]}}}`)
		case 1:
			writeJSON(w, 200, `{"data":{"environmentChangeSetApply":{"id":"apply-1","status":"applying","diagnostics":[],"changes":[]}}}`)
		default:
			writeJSON(w, 200, `{"data":{"environmentChangeSetApply":{"id":"apply-1","status":"applied","deploymentId":"dep-1","diagnostics":[],
				"changes":[{"kind":"service.update","path":"services.api","summary":"Update api","status":"applied","outputs":{"id":"svc"}}]}}}`)
		}
	})
	changeSet := map[string]any{"ops": []any{}}
	result, err := f.client(t, Credential{Token: testToken}).ApplyChangeSet(context.Background(), "env", changeSet, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != "applied" || result.DeploymentID != "dep-1" || len(result.Changes) != 1 || result.Err() != nil {
		t.Errorf("result = %+v, Err = %v", result, result.Err())
	}
	reqs := f.recorded()
	if len(reqs) != 3 {
		t.Fatalf("requests = %d, want 3", len(reqs))
	}
	wantApply := map[string]any{"environmentId": "env", "input": changeSet, "commitMessage": defaultCommitMessage, "waitForCompletion": false}
	if !reflect.DeepEqual(reqs[0].Variables, wantApply) {
		t.Errorf("apply variables = %v, want %v", reqs[0].Variables, wantApply)
	}
	wantPoll := map[string]any{"environmentId": "env", "id": "apply-1"}
	if !reflect.DeepEqual(reqs[2].Variables, wantPoll) || !strings.Contains(reqs[2].Query, "environmentChangeSetApply") {
		t.Errorf("poll request = %v %q", reqs[2].Variables, reqs[2].Query)
	}
}

func TestApplyChangeSetSendsEtagAndMessage(t *testing.T) {
	f := newFake(t, func(_ int, _ recordedRequest, w http.ResponseWriter) {
		writeJSON(w, 200, `{"data":{"environmentApplyChangeSet":{"id":"apply-1","status":"staged","stagedPatchId":"patch-1","diagnostics":[],"changes":[]}}}`)
	})
	result, err := f.client(t, Credential{Token: testToken}).ApplyChangeSet(context.Background(), "env", map[string]any{}, "etag-1", "terraform apply")
	if err != nil {
		t.Fatal(err)
	}
	vars := f.recorded()[0].Variables
	if vars["baseConfigEtag"] != "etag-1" || vars["commitMessage"] != "terraform apply" {
		t.Errorf("variables = %v", vars)
	}
	if result.StagedPatchID != "patch-1" || result.Err() != nil || len(f.recorded()) != 1 {
		t.Errorf("result = %+v", result)
	}
}

func TestApplyChangeSetStopsPollingWhenContextEnds(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var polls atomic.Int32
	f := newFake(t, func(n int, _ recordedRequest, w http.ResponseWriter) {
		if n == 0 {
			writeJSON(w, 200, `{"data":{"environmentApplyChangeSet":{"id":"apply-9","status":"applying","diagnostics":[],"changes":[]}}}`)
			return
		}
		if polls.Add(1) == 3 {
			cancel()
		}
		writeJSON(w, 200, `{"data":{"environmentChangeSetApply":{"id":"apply-9","status":"applying","diagnostics":[],"changes":[]}}}`)
	})
	result, err := f.client(t, Credential{Token: testToken}).ApplyChangeSet(ctx, "env", map[string]any{}, "", "")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if !strings.Contains(err.Error(), "apply-9") || result == nil || result.ID != "apply-9" {
		t.Errorf("err = %v, result = %+v; want the apply ID", err, result)
	}
}

func TestApplyResultErr(t *testing.T) {
	tests := []struct {
		name   string
		result ApplyResult
		want   string
	}{
		{"applied", ApplyResult{Status: "applied"}, ""},
		{"staged", ApplyResult{Status: "staged"}, ""},
		{"failed", ApplyResult{Status: "failed"}, "apply finished with status failed"},
		{"empty status", ApplyResult{}, "apply finished with status failed"},
		{"partially applied", ApplyResult{Status: "partially_applied", Changes: []ApplyChange{
			{Kind: "service.create", Summary: "Create api", Status: "failed"},
			{Kind: "volume.create", Path: "volumes.data", Status: "failed"},
			{Kind: "bucket.create", Status: "failed"},
			{Kind: "service.update", Summary: "Update web", Status: "applied"},
		}}, "apply finished with status partially_applied; failed changes: Create api, volumes.data, bucket.create"},
		{"applied with failed change", ApplyResult{Status: "applied", Changes: []ApplyChange{{Status: "failed"}}},
			"apply finished with status applied; failed changes: change"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.result.Err()
			if tt.want == "" {
				if err != nil {
					t.Errorf("Err() = %v, want nil", err)
				}
				return
			}
			if err == nil || err.Error() != tt.want {
				t.Errorf("Err() = %v, want %q", err, tt.want)
			}
		})
	}
}

func TestIsConfigEtagConflict(t *testing.T) {
	staleCode := newFake(t, func(_ int, _ recordedRequest, w http.ResponseWriter) {
		writeJSON(w, 200, `{"errors":[{"message":"Rejected","extensions":{"code":"STALE_ENVIRONMENT_BASE"}}]}`)
	})
	staleMessage := newFake(t, func(_ int, _ recordedRequest, w http.ResponseWriter) {
		writeJSON(w, 200, `{"errors":[{"message":"The environment changed since this plan was computed. Re-run plan and review the changes before applying."}]}`)
	})
	other := newFake(t, func(_ int, _ recordedRequest, w http.ResponseWriter) {
		writeJSON(w, 200, `{"errors":[{"message":"The environment did not report a config etag"}]}`)
	})
	for name, tt := range map[string]struct {
		f    *fakeBackboard
		want bool
	}{"code": {staleCode, true}, "message": {staleMessage, true}, "unrelated": {other, false}} {
		_, err := tt.f.client(t, Credential{Token: testToken}).ApplyChangeSet(context.Background(), "env", map[string]any{}, "etag-old", "")
		if err == nil {
			t.Fatalf("%s: apply succeeded", name)
		}
		if got := IsConfigEtagConflict(err); got != tt.want {
			t.Errorf("%s: IsConfigEtagConflict(%v) = %v, want %v", name, err, got, tt.want)
		}
	}
	if IsConfigEtagConflict(errors.New("changed since")) {
		t.Error("plain error counted as an etag conflict")
	}
}

func TestLookups(t *testing.T) {
	f := newFake(t, func(_ int, req recordedRequest, w http.ResponseWriter) {
		switch {
		case strings.Contains(req.Query, "IacProjectToken"):
			writeJSON(w, 200, `{"data":{"projectToken":{"projectId":"proj","environmentId":"env"}}}`)
		case strings.Contains(req.Query, "IacProject("):
			writeJSON(w, 200, `{"data":{"project":{"id":"proj","name":"Acme","workspaceId":"ws"}}}`)
		case strings.Contains(req.Query, "volumeDelete"):
			writeJSON(w, 200, `{"data":{"volumeDelete":true}}`)
		default:
			t.Errorf("unexpected query %q", req.Query)
		}
	})
	c := f.client(t, Credential{ProjectToken: "project-token"})
	ctx := context.Background()
	project, err := c.Project(ctx, "proj")
	if err != nil || *project != (Project{ID: "proj", Name: "Acme", WorkspaceID: "ws"}) {
		t.Errorf("Project = %+v, %v", project, err)
	}
	scope, err := c.ProjectToken(ctx)
	if err != nil || *scope != (ProjectTokenScope{ProjectID: "proj", EnvironmentID: "env"}) {
		t.Errorf("ProjectToken = %+v, %v", scope, err)
	}
	if err := c.VolumeDelete(ctx, "vol-1"); err != nil {
		t.Errorf("VolumeDelete = %v", err)
	}
	reqs := f.recorded()
	if !reflect.DeepEqual(reqs[0].Variables, map[string]any{"id": "proj"}) || !reflect.DeepEqual(reqs[2].Variables, map[string]any{"volumeId": "vol-1"}) {
		t.Errorf("variables = %v, %v", reqs[0].Variables, reqs[2].Variables)
	}
}
