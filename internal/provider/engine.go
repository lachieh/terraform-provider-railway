package provider

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/lachieh/terraform-provider-railway/internal/iac"
	"github.com/lachieh/terraform-provider-railway/internal/railwayapi"
)

const (
	commitMessage  = "Apply Terraform configuration"
	maxEtagRetries = 3
	// applyTimeout bounds one ChangeSet apply, polling included.
	applyTimeout      = 10 * time.Minute
	telemetryLanguage = "terraform"
)

// engine serves every resource of one provider instance. Terraform applies
// resources concurrently, and each apply plans against a fresh read, so
// writes to an environment hold its lock from that read through the apply.
type engine struct {
	client  *railwayapi.Client
	partial string

	mu   sync.Mutex
	envs map[string]*environmentState
}

type environmentState struct {
	write sync.Mutex

	mu       sync.Mutex
	snapshot *snapshot
}

// snapshot is one read of an environment: its live configuration converted
// to a graph, plus what the diff needs alongside it.
type snapshot struct {
	env    *railwayapi.Environment
	names  *railwayapi.NameMaps
	graph  iac.Graph
	owners iac.Partials
	follow *iac.BranchFollow
}

func newEngine(client *railwayapi.Client, partial string) *engine {
	return &engine{client: client, partial: partial, envs: map[string]*environmentState{}}
}

func (e *engine) environment(id string) *environmentState {
	e.mu.Lock()
	defer e.mu.Unlock()
	state, ok := e.envs[id]
	if !ok {
		state = &environmentState{}
		e.envs[id] = state
	}
	return state
}

// read returns the cached snapshot of an environment, loading it on first
// use. Plans read through the cache; every write invalidates it.
func (e *engine) read(ctx context.Context, environmentID string) (*snapshot, error) {
	state := e.environment(environmentID)
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.snapshot != nil {
		return state.snapshot, nil
	}
	snap, err := e.load(ctx, environmentID)
	if err != nil {
		return nil, err
	}
	state.snapshot = snap
	return snap, nil
}

func (e *engine) invalidate(environmentID string) {
	state := e.environment(environmentID)
	state.mu.Lock()
	state.snapshot = nil
	state.mu.Unlock()
}

func (e *engine) load(ctx context.Context, environmentID string) (*snapshot, error) {
	env, err := e.client.FetchEnvironment(ctx, environmentID, false)
	if err != nil {
		return nil, fmt.Errorf("reading Railway environment %s: %w", environmentID, err)
	}
	names, err := e.client.FetchNameMaps(ctx, env.ProjectID, env)
	if err != nil {
		return nil, fmt.Errorf("reading Railway project %s: %w", env.ProjectID, err)
	}
	graph := iac.EnvironmentConfigToGraph(env.Config, iac.ConfigToGraphOptions{
		ProjectName:            names.ProjectName,
		ServiceNamesByID:       names.ServiceNamesByID,
		TemplateServiceIDsByID: names.TemplateServiceIDsByID,
		VolumeNamesByID:        names.VolumeNamesByID,
		VolumeGroupIDsByID:     names.VolumeGroupIDsByID,
		BucketNamesByID:        names.BucketNamesByID,
		BucketGroupIDsByID:     names.BucketGroupIDsByID,
	})
	follow := &iac.BranchFollow{}
	if pr := env.PRContext(); pr != nil {
		follow.InPREnvironment = true
		follow.PRBranch = pr.Branch
		follow.PRRepo = env.Meta.PRRepo
	}
	return &snapshot{env: env, names: names, graph: graph, owners: iac.Partials(env.IACPartials), follow: follow}, nil
}

// plan is the part of an environment ChangeSet that belongs to one address.
type plan struct {
	address     string
	changes     []map[string]any
	diagnostics []iac.Diagnostic
	claim       bool
}

func (p plan) empty() bool { return len(p.changes) == 0 && !p.claim }

// diff plans the changes that turn the live node at address into desired, or
// delete it when desired is nil. The diff runs over the whole environment so
// references and attachments resolve, but with no partial: ownership is
// checked separately, and every other resource is identical on both sides.
func (e *engine) diff(ctx context.Context, snap *snapshot, address string, desired map[string]any) (plan, error) {
	resources := make([]any, 0, len(snap.graph.Resources)+1)
	for _, resource := range snap.graph.Resources {
		if iac.ResourceAddr(resource) != address {
			resources = append(resources, resource)
		}
	}
	if desired != nil {
		resources = append(resources, asRailwayStores(desired, snap.graph.FindResource(address)))
	}
	desiredGraph := iac.ProjectDefinitionToGraph(map[string]any{
		"name":      snap.graph.Project.Name,
		"resources": resources,
	})

	follow := *snap.follow
	if iac.NeedsDefaultBranchLookup(&snap.graph, &desiredGraph, follow.InPREnvironment, follow.PRBranch, follow.PRRepo) {
		branches, err := e.client.GitHubDefaultBranches(ctx)
		if err == nil {
			follow.DefaultBranches = branches
		}
	}
	changeSet := iac.DiffGraphsFollowing(iac.DiffOptions{Current: &snap.graph, Desired: &desiredGraph}, &follow)

	out := plan{address: address}
	for _, change := range changeSet.Changes {
		if iac.FieldStr(change, "address") == address {
			out.changes = append(out.changes, change)
		}
	}
	for _, diagnostic := range changeSet.Diagnostics {
		if belongsTo(diagnostic.Path, address) {
			out.diagnostics = append(out.diagnostics, diagnostic)
		}
	}
	if desired != nil {
		out.claim = iac.NeedsPartialClaimApply([]string{address}, snap.owners, e.partial)
	}
	return out, nil
}

// asRailwayStores rewrites desired into the form Railway stores, so the diff
// sends changes Railway applies and does not report ones it already holds:
//
//   - A volume keeps the region Railway picked when the configuration has
//     none, and the 100% usage alert Railway always adds.
//   - Railway ignores null for a build setting, so clearing one sends an
//     empty value: [] for a list, "" for a string, RAILPACK for the builder.
//     A stored "" counts as unset.
//   - Railway stores a deploy setting set to its default as no value, so
//     ON_FAILURE and sleepApplication false match a setting Railway does
//     not hold.
func asRailwayStores(desired, current map[string]any) map[string]any {
	if current == nil {
		return desired
	}
	out := iac.Clone(desired).(map[string]any)
	switch iac.ResourceType(desired) {
	case "volume":
		keepAssignedVolumeSettings(section(out, "config"), asObject(current["config"]))
	case "service", "database":
		clearBuildSettings(out, asObject(current["build"]))
		matchDeployDefaults(out, asObject(current["deploy"]))
	}
	return out
}

func keepAssignedVolumeSettings(config, live map[string]any) {
	if _, set := config["region"]; !set && live["region"] != nil {
		config["region"] = live["region"]
	}
	full, kept := iac.Field(iac.Field(live, "alerts"), "usage").(map[string]any)
	if !kept || full["100"] == nil {
		return
	}
	usage := section(section(config, "alerts"), "usage")
	if _, set := usage["100"]; !set {
		usage["100"] = iac.Clone(full["100"])
	}
}

// buildDefaults are the build settings Railway fills in itself; the engine
// already treats their defaults as unchanged.
var buildDefaults = map[string]bool{"buildEnvironment": true}

func clearBuildSettings(node, live map[string]any) {
	if len(live) == 0 {
		return
	}
	build := asObject(node["build"])
	for key, value := range live {
		if _, set := build[key]; set || buildDefaults[key] {
			continue
		}
		var empty any
		switch value.(type) {
		case []any:
			empty = []any{}
		case string:
			empty = ""
			if key == "builder" {
				empty = "RAILPACK"
			}
		default:
			continue
		}
		if build == nil {
			build = map[string]any{}
			node["build"] = build
		}
		build[key] = empty
	}
}

// deployDefaults are deploy settings Railway stores as no value when they
// are set to these values.
var deployDefaults = map[string]any{"restartPolicyType": "ON_FAILURE", "sleepApplication": false}

func matchDeployDefaults(node, live map[string]any) {
	deploy := asObject(node["deploy"])
	for key, value := range deployDefaults {
		if _, held := live[key]; !held && deploy[key] == value {
			delete(deploy, key)
		}
	}
}

func asObject(value any) map[string]any {
	object, _ := value.(map[string]any)
	return object
}

// section returns parent[key] as an object, creating it when absent.
func section(parent map[string]any, key string) map[string]any {
	child, ok := parent[key].(map[string]any)
	if !ok {
		child = map[string]any{}
		parent[key] = child
	}
	return child
}

func belongsTo(path, address string) bool {
	prefix := "resources." + address
	return path == prefix || strings.HasPrefix(path, prefix+".")
}

// checkOwnership refuses to touch a resource another partial owns, and a
// whole-project configuration in an environment that has named partials.
func (e *engine) checkOwnership(snap *snapshot, address string) error {
	if e.partial == "" && iac.HasNamedPartials(snap.owners) {
		return errors.New(iac.NamelessFileMessage() + " Set partial in the railway provider configuration.")
	}
	if owner, ok := iac.OwnerOf(snap.owners, address); ok && owner != iac.EffectivePartial(e.partial) {
		return errors.New(iac.ForeignResourceMessage(address, owner))
	}
	return nil
}

// apply converges one address on desired (nil deletes it) and returns the
// fresh snapshot and whether it applied a ChangeSet. It holds the
// environment's write lock and plans against a fresh read. It sends the
// configEtag the plan read, as the Railway CLI does, and replans if Railway
// refuses it as stale. Railway accepts a stale etag, so the replan runs only
// if Railway starts refusing one.
func (e *engine) apply(ctx context.Context, environmentID, address string, desired map[string]any) (*snapshot, []iac.Diagnostic, bool, error) {
	state := e.environment(environmentID)
	state.write.Lock()
	defer state.write.Unlock()

	for attempt := 0; ; attempt++ {
		e.invalidate(environmentID)
		snap, err := e.read(ctx, environmentID)
		if err != nil {
			return nil, nil, false, err
		}
		if err := e.checkOwnership(snap, address); err != nil {
			return nil, nil, false, err
		}
		planned, err := e.diff(ctx, snap, address, desired)
		if err != nil {
			return nil, nil, false, err
		}
		if err := errorDiagnostics(planned.diagnostics); err != nil {
			return nil, planned.diagnostics, false, err
		}
		if planned.empty() {
			return snap, planned.diagnostics, false, nil
		}

		planned, err = e.preview(ctx, environmentID, planned)
		if err != nil {
			return nil, nil, false, err
		}
		if planned.empty() {
			return snap, planned.diagnostics, false, nil
		}

		changeSet := e.changeSet(planned, desired != nil)
		applyCtx, cancel := context.WithTimeout(ctx, applyTimeout)
		result, err := e.client.ApplyChangeSet(applyCtx, environmentID, changeSet, snap.env.ConfigEtag, commitMessage)
		cancel()
		if err != nil {
			if railwayapi.IsConfigEtagConflict(err) && attempt < maxEtagRetries {
				continue
			}
			e.invalidate(environmentID)
			return nil, nil, false, fmt.Errorf("applying Railway changes for %s: %w", address, err)
		}
		e.invalidate(environmentID)
		if err := result.Err(); err != nil {
			return nil, nil, false, fmt.Errorf("applying Railway changes for %s: %w", address, err)
		}
		if desired == nil {
			fresh, err := e.read(ctx, environmentID)
			return fresh, planned.diagnostics, true, err
		}
		fresh, err := e.settle(ctx, environmentID, address, desired)
		return fresh, planned.diagnostics, true, err
	}
}

// plan diffs address against the snapshot and has Railway preview the
// result, as `railway config plan` does.
func (e *engine) plan(ctx context.Context, snap *snapshot, address string, desired map[string]any) (plan, error) {
	planned, err := e.diff(ctx, snap, address, desired)
	if err != nil {
		return planned, err
	}
	return e.preview(ctx, snap.env.ID, planned)
}

// preview replaces the locally diffed changes with the ones Railway
// previews. A read without decryptVariables masks variable values, so the
// local diff reports updates for variables that already match; Railway
// compares against the decrypted values and drops them.
func (e *engine) preview(ctx context.Context, environmentID string, planned plan) (plan, error) {
	if len(planned.changes) == 0 {
		return planned, nil
	}
	changeSet := e.changeSet(planned, false)
	preview, err := e.client.PreviewChangeSet(ctx, environmentID, changeSet)
	if err != nil {
		return planned, fmt.Errorf("previewing Railway changes for %s: %w", planned.address, err)
	}
	if previewed, ok := previewedChanges(preview); ok {
		iac.PreserveResolvedCreateBranches(planned.changes, previewed)
		planned.changes = previewed
	}
	return planned, nil
}

func (e *engine) changeSet(planned plan, declare bool) iac.ChangeSet {
	changeSet := iac.ChangeSet{
		Version:     iac.ChangeSetVersion,
		Changes:     planned.changes,
		Diagnostics: []iac.Diagnostic{},
		Telemetry:   &iac.ChangeSetTelemetry{Language: telemetryLanguage},
	}
	if changeSet.Changes == nil {
		changeSet.Changes = []map[string]any{}
	}
	if e.partial != "" {
		partial := e.partial
		changeSet.Partial = &partial
	}
	if declare {
		changeSet.Declared = []string{planned.address}
	}
	return changeSet
}

func previewedChanges(preview *railwayapi.Preview) ([]map[string]any, bool) {
	raw, ok := preview.ChangeSet["changes"].([]any)
	if !ok {
		return nil, false
	}
	changes := make([]map[string]any, 0, len(raw))
	for _, item := range raw {
		if change, ok := item.(map[string]any); ok {
			changes = append(changes, change)
		}
	}
	return changes, true
}

func errorDiagnostics(diagnostics []iac.Diagnostic) error {
	var messages []string
	for _, diagnostic := range diagnostics {
		if diagnostic.Severity == "error" {
			messages = append(messages, diagnostic.Message)
		}
	}
	if len(messages) == 0 {
		return nil
	}
	return errors.New(strings.Join(messages, "\n"))
}

// summaries renders a plan the way `railway config plan` lists it: one line
// per change, followed by its details.
func summaries(changes []map[string]any) []string {
	out := make([]string, 0, len(changes))
	for _, change := range changes {
		line := iac.FieldStr(change, "summary")
		if details, ok := change["details"].([]any); ok && len(details) > 0 {
			parts := make([]string, 0, len(details))
			for _, detail := range details {
				if text, ok := detail.(string); ok {
					parts = append(parts, text)
				}
			}
			line += ": " + strings.Join(parts, "; ")
		}
		out = append(out, line)
	}
	return out
}

// settleWait bounds how long an apply waits for Railway to list, or stop
// listing, what it applied.
var settleWait = 30 * time.Second

// settle re-reads the environment after an apply until Railway lists address,
// or stops listing it when desired is nil. Railway lists a new resource
// seconds after applying it. A resource that never appears, or a deleted one
// that stays, is an error, so Terraform does not record a state Railway does
// not have. Each read costs several API requests against Railway's hourly
// rate limit, so settle polls sparingly.
func (e *engine) settle(ctx context.Context, environmentID, address string, desired map[string]any) (*snapshot, error) {
	deadline := time.Now().Add(settleWait)
	for {
		e.invalidate(environmentID)
		snap, err := e.read(ctx, environmentID)
		if err != nil {
			return nil, err
		}
		listed := snap.graph.FindResource(address) != nil
		switch {
		case listed == (desired != nil):
			return snap, nil
		case desired != nil && strings.HasPrefix(address, "group."):
			// A group is listed only while something belongs to it.
			return snap, nil
		case time.Now().After(deadline) && listed:
			return nil, fmt.Errorf("Railway accepted the deletion of %s but still lists it after %s, so Terraform keeps it in state. "+
				"Delete it in Railway, then run terraform apply again", address, settleWait)
		case time.Now().After(deadline):
			return nil, fmt.Errorf("Railway applied %s but does not list it after %s", address, settleWait)
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(settleWait / 10):
		}
	}
}

// deploymentPoll is how often waitForDeployment reads the latest deployment,
// and deploymentStart how long it waits for Railway to start one. Each read
// is one request against Railway's hourly rate limit.
var (
	deploymentPoll  = 10 * time.Second
	deploymentStart = 2 * time.Minute
)

// latestDeploymentID returns the ID of a service's newest deployment, or "".
func (e *engine) latestDeploymentID(ctx context.Context, environmentID, serviceID string) (string, error) {
	deployment, err := e.client.LatestDeployment(ctx, environmentID, serviceID)
	if err != nil || deployment == nil {
		return "", err
	}
	return deployment.ID, nil
}

// waitForDeployment waits for a deployment newer than previous to finish.
// It returns a warning when Railway starts none within deploymentStart, and
// an error when the deployment fails, needs approval, or outlasts timeout.
func (e *engine) waitForDeployment(ctx context.Context, environmentID, serviceID, previous, address string, timeout time.Duration) (string, error) {
	started := time.Now()
	for {
		deployment, err := e.client.LatestDeployment(ctx, environmentID, serviceID)
		if err != nil {
			return "", err
		}
		waited := time.Since(started)
		if deployment != nil && deployment.ID != previous {
			switch deployment.Status {
			case "SUCCESS", "SLEEPING", "SKIPPED":
				return "", nil
			case "FAILED", "CRASHED", "REMOVED":
				return "", fmt.Errorf("Railway deployment %s of %s ended %s; its build and deploy logs are in the Railway dashboard", deployment.ID, address, deployment.Status)
			case "NEEDS_APPROVAL":
				return "", fmt.Errorf("Railway deployment %s of %s needs approval in the Railway dashboard before it runs", deployment.ID, address)
			}
			if waited >= timeout {
				return "", fmt.Errorf("Railway deployment %s of %s is still %s after %s", deployment.ID, address, deployment.Status, timeout)
			}
		} else if waited >= deploymentStart {
			return fmt.Sprintf("Railway started no deployment of %s within %s of the apply, so Terraform did not wait for one.", address, deploymentStart), nil
		}
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(deploymentPoll):
		}
	}
}
