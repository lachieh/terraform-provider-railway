package provider

import (
	"context"
	"fmt"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/function"
	"github.com/hashicorp/terraform-plugin-framework/types"
	railway "github.com/railwayapp/railway-go-sdk"
)

func configureDataSource(req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) *engine {
	if req.ProviderData == nil {
		return nil
	}
	e, ok := req.ProviderData.(*engine)
	if !ok {
		resp.Diagnostics.AddError("Unexpected provider data", fmt.Sprintf("Expected *engine, got %T.", req.ProviderData))
	}
	return e
}

type environmentDataSource struct{ engine *engine }

type environmentDataModel struct {
	ID              types.String `tfsdk:"id"`
	Name            types.String `tfsdk:"name"`
	ProjectID       types.String `tfsdk:"project_id"`
	ProjectName     types.String `tfsdk:"project_name"`
	IsEphemeral     types.Bool   `tfsdk:"is_ephemeral"`
	PRNumber        types.Int64  `tfsdk:"pr_number"`
	PRBranch        types.String `tfsdk:"pr_branch"`
	PRRepo          types.String `tfsdk:"pr_repo"`
	BaseEnvironment types.String `tfsdk:"base_environment"`
	ConfigEtag      types.String `tfsdk:"config_etag"`
}

func NewEnvironmentDataSource() datasource.DataSource { return &environmentDataSource{} }

func (d *environmentDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_environment"
}

func (d *environmentDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	computedString := func(description string) schema.StringAttribute {
		return schema.StringAttribute{MarkdownDescription: description, Computed: true}
	}
	resp.Schema = schema.Schema{
		MarkdownDescription: "A Railway environment: the context a `railway.go` file receives as `railway.Context`, plus its pull request details.",
		Attributes: map[string]schema.Attribute{
			"id":               schema.StringAttribute{MarkdownDescription: "Environment ID.", Required: true},
			"name":             computedString("Environment name, as `railway.Context.Environment`."),
			"project_id":       computedString("Project ID."),
			"project_name":     computedString("Project name."),
			"is_ephemeral":     schema.BoolAttribute{MarkdownDescription: "Whether Railway deletes the environment with its pull request.", Computed: true},
			"pr_number":        schema.Int64Attribute{MarkdownDescription: "Pull request number of a pull request environment.", Computed: true},
			"pr_branch":        computedString("Branch of a pull request environment."),
			"pr_repo":          computedString("Repository of a pull request environment."),
			"base_environment": computedString("Environment a pull request environment was forked from."),
			"config_etag":      computedString("Version of the environment's configuration."),
		},
	}
}

func (d *environmentDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	d.engine = configureDataSource(req, resp)
}

func (d *environmentDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var data environmentDataModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}
	snap, err := d.engine.read(ctx, data.ID.ValueString())
	if err != nil {
		resp.Diagnostics.AddError("Reading Railway environment", err.Error())
		return
	}
	env := snap.env
	data.Name = types.StringValue(env.Name)
	data.ProjectID = types.StringValue(env.ProjectID)
	data.ProjectName = types.StringValue(snap.names.ProjectName)
	data.IsEphemeral = types.BoolValue(env.IsEphemeral)
	data.ConfigEtag = types.StringValue(env.ConfigEtag)
	data.PRNumber, data.PRBranch, data.PRRepo, data.BaseEnvironment = types.Int64Null(), types.StringNull(), types.StringNull(), types.StringNull()
	if pr := env.PRContext(); pr != nil {
		data.PRNumber = types.Int64Value(pr.Number)
		data.PRBranch = types.StringValue(pr.Branch)
		data.PRRepo = types.StringValue(env.Meta.PRRepo)
		data.BaseEnvironment = types.StringValue(pr.Base)
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
}

type projectDataSource struct{ engine *engine }

type projectDataModel struct {
	ID           types.String `tfsdk:"id"`
	Name         types.String `tfsdk:"name"`
	WorkspaceID  types.String `tfsdk:"workspace_id"`
	Environments types.Map    `tfsdk:"environments"`
}

func NewProjectDataSource() datasource.DataSource { return &projectDataSource{} }

func (d *projectDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_project"
}

func (d *projectDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "A Railway project and the IDs of its environments, for wiring `environment_id`.",
		Attributes: map[string]schema.Attribute{
			"id":           schema.StringAttribute{MarkdownDescription: "Project ID.", Required: true},
			"name":         schema.StringAttribute{MarkdownDescription: "Project name.", Computed: true},
			"workspace_id": schema.StringAttribute{MarkdownDescription: "Workspace that owns the project.", Computed: true},
			"environments": schema.MapAttribute{
				MarkdownDescription: "Environment IDs keyed by environment name.",
				Computed:            true,
				ElementType:         types.StringType,
			},
		},
	}
}

func (d *projectDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	d.engine = configureDataSource(req, resp)
}

func (d *projectDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var data projectDataModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}
	project, err := d.engine.client.Project(ctx, data.ID.ValueString())
	if err != nil {
		resp.Diagnostics.AddError("Reading Railway project", err.Error())
		return
	}
	environments, err := d.engine.client.Environments(ctx, data.ID.ValueString())
	if err != nil {
		resp.Diagnostics.AddError("Reading Railway environments", err.Error())
		return
	}
	ids := map[string]string{}
	for _, environment := range environments {
		ids[environment.Name] = environment.ID
	}
	data.Name = types.StringValue(project.Name)
	data.WorkspaceID = types.StringValue(project.WorkspaceID)
	environmentIDs, diags := types.MapValueFrom(ctx, types.StringType, ids)
	resp.Diagnostics.Append(diags...)
	data.Environments = environmentIDs
	resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
}

// randomStringFunction is railway.Context.RandomString: a value derived from
// the environment name and a label, stable across plans, the same in every
// configuration that asks for the same label in the same environment.
type randomStringFunction struct{}

func NewRandomStringFunction() function.Function { return randomStringFunction{} }

func (randomStringFunction) Metadata(_ context.Context, _ function.MetadataRequest, resp *function.MetadataResponse) {
	resp.Name = "random_string"
}

func (randomStringFunction) Definition(_ context.Context, _ function.DefinitionRequest, resp *function.DefinitionResponse) {
	resp.Definition = function.Definition{
		Summary: "Deterministic per-environment random string, as railway.Context.RandomString.",
		MarkdownDescription: "Returns `bytes` bytes, hex encoded, of SHA-256 over the environment name and `label`, the same value a `railway.go` file gets from `ctx.RandomString(label, bytes)`. " +
			"It is derived, not secret: anyone who knows the environment name and label can compute it.",
		Parameters: []function.Parameter{
			function.StringParameter{Name: "environment", MarkdownDescription: "Environment name, such as `data.railway_environment.this.name`."},
			function.StringParameter{Name: "label", MarkdownDescription: "What the value is for. An empty label means `random`."},
			function.Int64Parameter{Name: "bytes", MarkdownDescription: "Length in bytes, 1 to 32. Zero or less means 12."},
		},
		Return: function.StringReturn{},
	}
}

func (randomStringFunction) Run(ctx context.Context, req function.RunRequest, resp *function.RunResponse) {
	var environment, label string
	var bytes int64
	resp.Error = function.ConcatFuncErrors(req.Arguments.Get(ctx, &environment, &label, &bytes))
	if resp.Error != nil {
		return
	}
	if bytes > 32 {
		resp.Error = function.NewArgumentFuncError(2, "bytes must be at most 32, the length of a SHA-256 digest")
		return
	}
	value := railway.NewContext(railway.Context{Environment: environment}).RandomString(label, int(bytes))
	resp.Error = function.ConcatFuncErrors(resp.Result.Set(ctx, value))
}
