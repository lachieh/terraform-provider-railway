package provider

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	railway "github.com/railwayapp/railway-go-sdk"

	"github.com/lachieh/terraform-provider-railway/internal/iac"
)

func pathRoot(name string) path.Path { return path.Root(name) }

// commonModel is embedded in every node resource model.
type commonModel struct {
	ID            types.String `tfsdk:"id"`
	EnvironmentID types.String `tfsdk:"environment_id"`
	Name          types.String `tfsdk:"name"`
	ProjectID     types.String `tfsdk:"project_id"`
	RailwayID     types.String `tfsdk:"railway_id"`
	LiveChanges   types.List   `tfsdk:"live_changes"`
	Address       types.String `tfsdk:"address"`
}

// nodeKind describes one graph resource type: its schema, and how its model
// becomes an SDK value.
type nodeKind[M any] struct {
	// graphType is the graph address prefix: service, database, volume,
	// bucket or group.
	graphType string
	// typeName is the resource type suffix, when it differs from graphType.
	typeName    string
	description string
	attributes  map[string]schema.Attribute
	common      func(*M) *commonModel
	build       func(*M) func() any
	// remove deletes what a ChangeSet cannot, after the ChangeSet applied.
	remove func(ctx context.Context, e *engine, model *M) error
	// deploymentTimeout reports whether an apply waits for the deployment it
	// starts, and for how long.
	deploymentTimeout func(*M) (time.Duration, bool)
}

type nodeResource[M any] struct {
	kind   nodeKind[M]
	engine *engine
}

var (
	_ resource.ResourceWithConfigure   = &nodeResource[struct{}]{}
	_ resource.ResourceWithImportState = &nodeResource[struct{}]{}
	_ resource.ResourceWithModifyPlan  = &nodeResource[struct{}]{}
)

func (r *nodeResource[M]) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	name := r.kind.typeName
	if name == "" {
		name = r.kind.graphType
	}
	resp.TypeName = req.ProviderTypeName + "_" + name
}

func (r *nodeResource[M]) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	attributes := map[string]schema.Attribute{
		"id": schema.StringAttribute{
			MarkdownDescription: "`<environment_id>/<address>`, for example `" + exampleID(r.kind.graphType) + "`.",
			Computed:            true,
			PlanModifiers:       []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
		},
		"environment_id": schema.StringAttribute{
			MarkdownDescription: "ID of the Railway environment that holds this " + r.kind.graphType + ". Changing it replaces the resource.",
			Required:            true,
			PlanModifiers:       []planmodifier.String{stringplanmodifier.RequiresReplace()},
		},
		"name": schema.StringAttribute{
			MarkdownDescription: "Name of the " + r.kind.graphType + ". Railway IaC addresses resources by name, so renaming replaces it.",
			Required:            true,
			PlanModifiers:       []planmodifier.String{stringplanmodifier.RequiresReplace()},
		},
		"project_id": schema.StringAttribute{
			MarkdownDescription: "ID of the Railway project the environment belongs to.",
			Computed:            true,
			PlanModifiers:       []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
		},
		"railway_id": schema.StringAttribute{
			MarkdownDescription: "Railway's ID for this " + r.kind.graphType + ".",
			Computed:            true,
			PlanModifiers:       []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
		},
		"address": schema.StringAttribute{
			MarkdownDescription: "Graph address, such as `" + r.kind.graphType + ".web`. Use it in another resource's `variable_references` so Terraform orders the two.",
			Computed:            true,
			PlanModifiers:       []planmodifier.String{addressFromName{graphType: r.kind.graphType}},
		},
		"live_changes": schema.ListAttribute{
			MarkdownDescription: "Changes Railway needs to match this configuration, as `railway config plan` lists them. " +
				"Empty after an apply; a refresh fills it when the live environment drifts, and the next plan applies them.",
			Computed:      true,
			ElementType:   types.StringType,
			PlanModifiers: []planmodifier.List{convergedList{}},
		},
	}
	for name, attribute := range r.kind.attributes {
		attributes[name] = attribute
	}
	resp.Schema = schema.Schema{MarkdownDescription: r.kind.description, Attributes: attributes}
}

func exampleID(graphType string) string {
	return "c2b9…/" + graphType + ".web"
}

func (r *nodeResource[M]) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	if req.ProviderData == nil {
		return
	}
	e, ok := req.ProviderData.(*engine)
	if !ok {
		resp.Diagnostics.AddError("Unexpected provider data", fmt.Sprintf("Expected *engine, got %T.", req.ProviderData))
		return
	}
	r.engine = e
}

func (r *nodeResource[M]) address(model *M) string {
	return sdkAddress(r.kind.graphType, r.kind.common(model).Name.ValueString())
}

func (r *nodeResource[M]) desired(model *M) (map[string]any, diag.Diagnostics) {
	var diags diag.Diagnostics
	address := r.address(model)
	node, err := graphNode(address, r.kind.build(model))
	if err != nil {
		diags.AddError("Invalid Railway "+r.kind.graphType, fmt.Sprintf("%s: %s", address, err))
	}
	return node, diags
}

func (r *nodeResource[M]) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var model M
	resp.Diagnostics.Append(req.Plan.Get(ctx, &model)...)
	resp.Diagnostics.Append(r.prepareSecrets(ctx, &model, req.Config, nil)...)
	if resp.Diagnostics.HasError() {
		return
	}
	node, diags := r.desired(&model)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
	common := r.kind.common(&model)
	environmentID, address := common.EnvironmentID.ValueString(), r.address(&model)

	snap, err := r.engine.read(ctx, environmentID)
	if err != nil {
		resp.Diagnostics.AddError("Reading Railway environment", err.Error())
		return
	}
	if snap.graph.FindResource(address) != nil {
		resp.Diagnostics.AddError("Railway resource already exists", alreadyExists(address, environmentID))
		return
	}
	if detail := projectBucketTaken(snap, r.kind.graphType, common.Name.ValueString()); detail != "" {
		resp.Diagnostics.AddError("Railway bucket name taken", detail)
		return
	}

	fresh, warnings, applied, err := r.engine.apply(ctx, environmentID, address, node)
	appendWarnings(&resp.Diagnostics, warnings)
	if err != nil {
		resp.Diagnostics.AddError("Creating Railway "+r.kind.graphType, err.Error())
		return
	}
	r.setComputed(common, fresh, address, nil)
	resp.Diagnostics.Append(resp.State.Set(ctx, &model)...)
	if applied {
		resp.Diagnostics.Append(r.waitForDeployment(ctx, &model, "")...)
	}
}

func (r *nodeResource[M]) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var model M
	resp.Diagnostics.Append(req.State.Get(ctx, &model)...)
	if resp.Diagnostics.HasError() {
		return
	}
	common := r.kind.common(&model)
	environmentID, address := common.EnvironmentID.ValueString(), r.address(&model)

	snap, err := r.engine.read(ctx, environmentID)
	if err != nil {
		resp.Diagnostics.AddError("Reading Railway environment", err.Error())
		return
	}
	if snap.graph.FindResource(address) == nil {
		resp.State.RemoveResource(ctx)
		return
	}
	// An imported resource has only its name in state, which may not build a
	// node; it has no pending changes until a configuration is planned, and
	// ModifyPlan reports configuration errors.
	var liveChanges []string
	if node, diags := r.desired(&model); !diags.HasError() {
		planned, err := r.engine.plan(ctx, snap, address, node)
		if err != nil {
			resp.Diagnostics.AddError("Planning Railway changes", err.Error())
			return
		}
		liveChanges = summaries(planned.changes)
	}
	r.setComputed(common, snap, address, liveChanges)
	resp.Diagnostics.Append(resp.State.Set(ctx, &model)...)
}

func (r *nodeResource[M]) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var model M
	resp.Diagnostics.Append(req.Plan.Get(ctx, &model)...)
	resp.Diagnostics.Append(r.prepareSecrets(ctx, &model, req.Config, &req.State)...)
	if resp.Diagnostics.HasError() {
		return
	}
	node, diags := r.desired(&model)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
	common := r.kind.common(&model)
	environmentID, address := common.EnvironmentID.ValueString(), r.address(&model)

	previous, diags := r.latestDeployment(ctx, &model)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
	fresh, warnings, applied, err := r.engine.apply(ctx, environmentID, address, node)
	appendWarnings(&resp.Diagnostics, warnings)
	if err != nil {
		resp.Diagnostics.AddError("Updating Railway "+r.kind.graphType, err.Error())
		return
	}
	r.setComputed(common, fresh, address, nil)
	resp.Diagnostics.Append(resp.State.Set(ctx, &model)...)
	if applied {
		resp.Diagnostics.Append(r.waitForDeployment(ctx, &model, previous)...)
	}
}

// latestDeployment returns the newest deployment of a resource that waits
// for its deployments, so the wait after an apply can tell the deployment the
// apply starts from the one before it.
func (r *nodeResource[M]) latestDeployment(ctx context.Context, model *M) (string, diag.Diagnostics) {
	var diags diag.Diagnostics
	common := r.kind.common(model)
	if r.kind.deploymentTimeout == nil || common.RailwayID.ValueString() == "" {
		return "", diags
	}
	if _, wait := r.kind.deploymentTimeout(model); !wait {
		return "", diags
	}
	id, err := r.engine.latestDeploymentID(ctx, common.EnvironmentID.ValueString(), common.RailwayID.ValueString())
	if err != nil {
		diags.AddError("Reading Railway deployments", err.Error())
	}
	return id, diags
}

// waitForDeployment waits, when the resource asks to, for the deployment an
// apply started. State is already saved: a failed deployment fails the apply
// without losing track of the resource.
func (r *nodeResource[M]) waitForDeployment(ctx context.Context, model *M, previous string) diag.Diagnostics {
	var diags diag.Diagnostics
	if r.kind.deploymentTimeout == nil {
		return diags
	}
	timeout, wait := r.kind.deploymentTimeout(model)
	if !wait {
		return diags
	}
	common := r.kind.common(model)
	warning, err := r.engine.waitForDeployment(ctx, common.EnvironmentID.ValueString(), common.RailwayID.ValueString(), previous, r.address(model), timeout)
	if warning != "" {
		diags.AddWarning("No Railway deployment", warning)
	}
	if err != nil {
		diags.AddError("Railway deployment did not succeed", err.Error())
	}
	return diags
}

func (r *nodeResource[M]) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var model M
	resp.Diagnostics.Append(req.State.Get(ctx, &model)...)
	if resp.Diagnostics.HasError() {
		return
	}
	common := r.kind.common(&model)
	environmentID, address := common.EnvironmentID.ValueString(), r.address(&model)

	_, _, _, err := r.engine.apply(ctx, environmentID, address, nil)
	if err != nil {
		resp.Diagnostics.AddError("Deleting Railway "+r.kind.graphType, err.Error())
		return
	}
	if r.kind.remove != nil {
		if err := r.kind.remove(ctx, r.engine, &model); err != nil {
			resp.Diagnostics.AddError("Deleting Railway "+r.kind.graphType, err.Error())
			return
		}
	}
	if _, err := r.engine.settle(ctx, environmentID, address, nil); err != nil {
		resp.Diagnostics.AddError("Deleting Railway "+r.kind.graphType, err.Error())
	}
}

// ImportState takes `<environment_id>/<address>` or `<environment_id>/<name>`.
func (r *nodeResource[M]) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	environmentID, rest, ok := strings.Cut(req.ID, "/")
	name := strings.TrimPrefix(rest, r.kind.graphType+".")
	if !ok || environmentID == "" || name == "" {
		resp.Diagnostics.AddError("Invalid import ID",
			fmt.Sprintf("Expected <environment_id>/%s.<name>, got %q.", r.kind.graphType, req.ID))
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("environment_id"), environmentID)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("name"), name)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), environmentID+"/"+iac.ResourceAddress(r.kind.graphType, name))...)
}

// ModifyPlan reports what the apply will send to Railway, the way
// `railway config plan` prints it, once the configuration is known.
func (r *nodeResource[M]) ModifyPlan(ctx context.Context, req resource.ModifyPlanRequest, resp *resource.ModifyPlanResponse) {
	if r.engine == nil || req.Plan.Raw.IsNull() || !req.Config.Raw.IsFullyKnown() {
		return
	}
	var model M
	resp.Diagnostics.Append(req.Config.Get(ctx, &model)...)
	var prior *tfsdk.State
	if !req.State.Raw.IsNull() {
		prior = &req.State
	}
	resp.Diagnostics.Append(r.prepareSecrets(ctx, &model, req.Config, prior)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if s := secretSettings(&model); s != nil {
		resp.Diagnostics.Append(resp.Plan.SetAttribute(ctx, path.Root("secret_variable_names"), s.SecretNames)...)
	}
	node, diags := r.desired(&model)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
	common := r.kind.common(&model)
	environmentID, address := common.EnvironmentID.ValueString(), r.address(&model)
	snap, err := r.engine.read(ctx, environmentID)
	if err != nil {
		resp.Diagnostics.AddWarning("Railway plan unavailable", err.Error())
		return
	}
	if req.State.Raw.IsNull() && snap.graph.FindResource(address) != nil {
		resp.Diagnostics.AddError("Railway resource already exists", alreadyExists(address, environmentID))
		return
	}
	if detail := projectBucketTaken(snap, r.kind.graphType, common.Name.ValueString()); req.State.Raw.IsNull() && detail != "" {
		resp.Diagnostics.AddError("Railway bucket name taken", detail)
		return
	}
	if err := r.engine.checkOwnership(snap, address); err != nil {
		resp.Diagnostics.AddError("Railway resource owned elsewhere", err.Error())
		return
	}
	planned, err := r.engine.plan(ctx, snap, address, node)
	if err != nil {
		resp.Diagnostics.AddWarning("Railway plan unavailable", err.Error())
		return
	}
	appendWarnings(&resp.Diagnostics, planned.diagnostics)
	if err := errorDiagnostics(planned.diagnostics); err != nil {
		resp.Diagnostics.AddError("Invalid Railway "+r.kind.graphType, err.Error())
		return
	}
	if len(planned.changes) > 0 {
		lines := make([]string, 0, len(planned.changes))
		for _, change := range planned.changes {
			lines = append(lines, fmt.Sprintf("[%s] %s", iac.FieldStr(change, "severity"), summaries([]map[string]any{change})[0]))
		}
		resp.Diagnostics.AddWarning("Railway changes for "+address, strings.Join(lines, "\n"))
	}
}

// secretSettings returns the service settings of a model that has
// secret_variables, or nil.
func secretSettings(model any) *serviceSettings {
	if holder, ok := model.(interface{ settings() *serviceSettings }); ok {
		return holder.settings()
	}
	return nil
}

// prepareSecrets takes secret_variables from the configuration, where the
// write-only values are, and picks the ones this plan or apply sends: all of
// them on create or when secret_variables_version changes, otherwise only
// names the prior state does not have.
func (r *nodeResource[M]) prepareSecrets(ctx context.Context, model *M, config tfsdk.Config, prior *tfsdk.State) diag.Diagnostics {
	var diags diag.Diagnostics
	s := secretSettings(model)
	if s == nil {
		return diags
	}
	var values map[string]string
	var configured types.Map
	diags.Append(config.GetAttribute(ctx, path.Root("secret_variables"), &configured)...)
	if !configured.IsNull() && !configured.IsUnknown() {
		diags.Append(configured.ElementsAs(ctx, &values, false)...)
	}
	names := make([]attr.Value, 0, len(values))
	for _, name := range iac.SortedKeys(values) {
		names = append(names, types.StringValue(name))
	}
	s.SecretNames = types.SetValueMust(types.StringType, names)
	s.SecretVariables = types.MapNull(types.StringType)

	send := map[string]string{}
	var priorNames types.Set
	var priorVersion types.String
	if prior != nil {
		diags.Append(prior.GetAttribute(ctx, path.Root("secret_variable_names"), &priorNames)...)
		diags.Append(prior.GetAttribute(ctx, path.Root("secret_variables_version"), &priorVersion)...)
	}
	resend := prior == nil || !s.SecretVersion.Equal(priorVersion)
	known := setStrings(priorNames)
	for name, value := range values {
		if resend || !slices.Contains(known, name) {
			send[name] = value
		}
	}
	s.secretValues = send
	return diags
}

func (r *nodeResource[M]) setComputed(common *commonModel, snap *snapshot, address string, liveChanges []string) {
	common.ID = types.StringValue(common.EnvironmentID.ValueString() + "/" + address)
	common.Address = types.StringValue(address)
	common.ProjectID = types.StringValue(snap.env.ProjectID)
	common.RailwayID = types.StringValue(railwayID(snap, r.kind.graphType, common.Name.ValueString()))
	values := make([]attr.Value, 0, len(liveChanges))
	for _, line := range liveChanges {
		values = append(values, types.StringValue(line))
	}
	common.LiveChanges = types.ListValueMust(types.StringType, values)
}

func railwayID(snap *snapshot, graphType, name string) string {
	byID := snap.names.ServiceNamesByID
	switch graphType {
	case "group":
		groups, _ := snap.env.Config["groups"].(map[string]any)
		for _, id := range iac.SortedKeys(groups) {
			if iac.FieldStr(groups[id], "name") == name {
				return id
			}
		}
		return ""
	case "volume":
		byID = snap.names.VolumeNamesByID
	case "bucket":
		byID = snap.names.BucketNamesByID
	}
	for _, id := range iac.SortedKeys(byID) {
		if byID[id] == name {
			return id
		}
	}
	return ""
}

func appendWarnings(diags *diag.Diagnostics, diagnostics []iac.Diagnostic) {
	for _, diagnostic := range diagnostics {
		if diagnostic.Severity != "error" {
			diags.AddWarning("Railway: "+diagnostic.Path, diagnostic.Message)
		}
	}
}

// convergedList plans live_changes as empty: after an apply Railway matches
// the configuration, so the pending changes a refresh found show as removed.
type convergedList struct{}

func (convergedList) Description(context.Context) string {
	return "Plans an empty list: an apply leaves no pending Railway changes."
}

func (m convergedList) MarkdownDescription(ctx context.Context) string { return m.Description(ctx) }

func (convergedList) PlanModifyList(_ context.Context, req planmodifier.ListRequest, resp *planmodifier.ListResponse) {
	if req.Plan.Raw.IsNull() {
		return
	}
	resp.PlanValue = types.ListValueMust(types.StringType, []attr.Value{})
}

// projectBucketTaken explains why a bucket named name cannot be created in
// this environment, or returns "". Buckets belong to the project and Railway
// cannot delete one; a ChangeSet that creates a bucket under a name the
// project already holds updates that bucket and never adds it to the
// environment.
func projectBucketTaken(snap *snapshot, graphType, name string) string {
	if graphType != "bucket" {
		return ""
	}
	for _, taken := range snap.names.BucketNamesByID {
		if taken == name {
			return fmt.Sprintf("The project already has a bucket named %q, and Railway would not add it to this environment. "+
				"Choose another name; Railway cannot delete buckets, so the name stays taken.", name)
		}
	}
	return ""
}

func alreadyExists(address, environmentID string) string {
	return fmt.Sprintf("%s already exists in environment %s. Import it (ID %s/%s) or add an import block to manage it.",
		address, environmentID, environmentID, address)
}

// sdkAddress is the graph address the SDK gives a resource of graphType
// named name.
func sdkAddress(graphType, name string) string {
	switch graphType {
	case "service":
		return railway.ServiceNamed(name, nil).Address()
	case "database":
		return railway.Postgres(name).Address()
	case "volume":
		return railway.Volume(name).Address()
	case "bucket":
		return railway.Bucket(name).Address()
	case "group":
		return railway.Group(name, nil)[0].(railway.Resource).Address()
	}
	return iac.ResourceAddress(graphType, name)
}

// addressFromName plans address from name, so a reference to it is known
// before the resource exists.
type addressFromName struct{ graphType string }

func (addressFromName) Description(context.Context) string {
	return "Derives the graph address from the resource name."
}

func (m addressFromName) MarkdownDescription(ctx context.Context) string { return m.Description(ctx) }

func (m addressFromName) PlanModifyString(ctx context.Context, req planmodifier.StringRequest, resp *planmodifier.StringResponse) {
	var name types.String
	resp.Diagnostics.Append(req.Plan.GetAttribute(ctx, path.Root("name"), &name)...)
	if name.IsUnknown() || name.IsNull() {
		return
	}
	resp.PlanValue = types.StringValue(sdkAddress(m.graphType, name.ValueString()))
}
