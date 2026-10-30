package provider

import (
	"context"
	"errors"

	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/tambo-ai/terraform-provider-railway-iac-sdk/internal/railwayapi"
)

// environmentResource manages a Railway environment, the container the
// other resources' IaC graph lives in.
type environmentResource struct{ engine *engine }

type environmentModel struct {
	ID                  types.String `tfsdk:"id"`
	ProjectID           types.String `tfsdk:"project_id"`
	Name                types.String `tfsdk:"name"`
	SourceEnvironmentID types.String `tfsdk:"source_environment_id"`
}

var (
	_ resource.ResourceWithConfigure   = &environmentResource{}
	_ resource.ResourceWithImportState = &environmentResource{}
)

func NewEnvironmentResource() resource.Resource { return &environmentResource{} }

func (r *environmentResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_environment"
}

func (r *environmentResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	replace := []planmodifier.String{stringplanmodifier.RequiresReplace()}
	resp.Schema = schema.Schema{
		MarkdownDescription: "A Railway environment, created empty and without deploying anything. Pass its `id` as the `environment_id` of the resources that go in it. " +
			"Destroying it deletes everything in it.",
		Attributes: map[string]schema.Attribute{
			"id":         schema.StringAttribute{MarkdownDescription: "Environment ID.", Computed: true, PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()}},
			"project_id": schema.StringAttribute{MarkdownDescription: "Project ID. Changing it replaces the environment.", Required: true, PlanModifiers: replace},
			"name":       schema.StringAttribute{MarkdownDescription: "Environment name. Changing it renames the environment.", Required: true},
			"source_environment_id": schema.StringAttribute{
				MarkdownDescription: "Environment to copy services, variables and settings from when Railway creates this one. " +
					"Terraform does not manage what Railway copies: import those resources, or leave this unset and declare them. Changing it replaces the environment.",
				Optional:      true,
				PlanModifiers: replace,
			},
		},
	}
}

func (r *environmentResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	r.engine = configureResource(req, resp)
}

func (r *environmentResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var model environmentModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &model)...)
	if resp.Diagnostics.HasError() {
		return
	}
	environment, err := r.engine.client.EnvironmentCreate(ctx, model.ProjectID.ValueString(), model.Name.ValueString(), model.SourceEnvironmentID.ValueString())
	if err != nil {
		resp.Diagnostics.AddError("Creating Railway environment", err.Error())
		return
	}
	model.ID = types.StringValue(environment.ID)
	resp.Diagnostics.Append(resp.State.Set(ctx, &model)...)
}

func (r *environmentResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var model environmentModel
	resp.Diagnostics.Append(req.State.Get(ctx, &model)...)
	if resp.Diagnostics.HasError() {
		return
	}
	environment, err := r.engine.client.EnvironmentDetails(ctx, model.ID.ValueString())
	if errors.Is(err, railwayapi.ErrNotFound) {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		resp.Diagnostics.AddError("Reading Railway environment", err.Error())
		return
	}
	model.Name = types.StringValue(environment.Name)
	model.ProjectID = types.StringValue(environment.ProjectID)
	resp.Diagnostics.Append(resp.State.Set(ctx, &model)...)
}

func (r *environmentResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var model environmentModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &model)...)
	if resp.Diagnostics.HasError() {
		return
	}
	environment, err := r.engine.client.EnvironmentRename(ctx, model.ID.ValueString(), model.Name.ValueString())
	if err != nil {
		resp.Diagnostics.AddError("Renaming Railway environment", err.Error())
		return
	}
	model.Name = types.StringValue(environment.Name)
	resp.Diagnostics.Append(resp.State.Set(ctx, &model)...)
}

func (r *environmentResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var model environmentModel
	resp.Diagnostics.Append(req.State.Get(ctx, &model)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if err := r.engine.client.EnvironmentDelete(ctx, model.ID.ValueString()); err != nil {
		resp.Diagnostics.AddError("Deleting Railway environment", err.Error())
	}
	r.engine.invalidate(model.ID.ValueString())
}

func (r *environmentResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	resource.ImportStatePassthroughID(ctx, path.Root("id"), req, resp)
}
