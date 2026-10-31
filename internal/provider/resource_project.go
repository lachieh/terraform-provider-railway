package provider

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/booldefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/tambo-ai/terraform-provider-railway/internal/railwayapi"
)

// projectResource manages a Railway project outside the IaC graph, which
// lives inside one environment and cannot create the project around it.
type projectResource struct{ engine *engine }

type projectModel struct {
	ID                     types.String `tfsdk:"id"`
	Name                   types.String `tfsdk:"name"`
	Description            types.String `tfsdk:"description"`
	WorkspaceID            types.String `tfsdk:"workspace_id"`
	IsPublic               types.Bool   `tfsdk:"is_public"`
	PRDeploys              types.Bool   `tfsdk:"pr_deploys"`
	DefaultEnvironmentName types.String `tfsdk:"default_environment_name"`
	DefaultEnvironmentID   types.String `tfsdk:"default_environment_id"`
}

var (
	_ resource.ResourceWithConfigure   = &projectResource{}
	_ resource.ResourceWithImportState = &projectResource{}
)

func NewProjectResource() resource.Resource { return &projectResource{} }

func (r *projectResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_project"
}

func (r *projectResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	keep := []planmodifier.String{stringplanmodifier.UseStateForUnknown()}
	resp.Schema = schema.Schema{
		MarkdownDescription: "A Railway project. Railway creates the project's first environment with it; put resources in it through `default_environment_id`, or add more with `railway_environment`. " +
			"Creating a project needs an account or workspace token. Destroying it deletes everything in it, so consider `prevent_destroy`.",
		Attributes: map[string]schema.Attribute{
			"id":          schema.StringAttribute{MarkdownDescription: "Project ID.", Computed: true, PlanModifiers: keep},
			"name":        schema.StringAttribute{MarkdownDescription: "Project name.", Required: true},
			"description": schema.StringAttribute{MarkdownDescription: "Project description.", Optional: true},
			"workspace_id": schema.StringAttribute{
				MarkdownDescription: "Workspace that owns the project. Defaults to the token's workspace. Changing it replaces the project.",
				Optional:            true,
				Computed:            true,
				PlanModifiers:       []planmodifier.String{stringplanmodifier.UseStateForUnknown(), stringplanmodifier.RequiresReplaceIfConfigured()},
			},
			"is_public":  schema.BoolAttribute{MarkdownDescription: "Whether anyone can view the project.", Optional: true, Computed: true, Default: booldefault.StaticBool(false)},
			"pr_deploys": schema.BoolAttribute{MarkdownDescription: "Whether Railway creates an environment for each pull request.", Optional: true, Computed: true, Default: booldefault.StaticBool(false)},
			"default_environment_name": schema.StringAttribute{
				MarkdownDescription: "Name of the environment Railway creates with the project. Railway names it `production` when unset. Changing it renames the environment.",
				Optional:            true,
				Computed:            true,
				PlanModifiers:       keep,
			},
			"default_environment_id": schema.StringAttribute{MarkdownDescription: "ID of the environment Railway created with the project.", Computed: true, PlanModifiers: keep},
		},
	}
}

func (r *projectResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	r.engine = configureResource(req, resp)
}

func (m *projectModel) input() railwayapi.ProjectInput {
	return railwayapi.ProjectInput{
		Name:        m.Name.ValueStringPointer(),
		Description: m.Description.ValueStringPointer(),
		IsPublic:    m.IsPublic.ValueBoolPointer(),
		PRDeploys:   m.PRDeploys.ValueBoolPointer(),
	}
}

func (m *projectModel) set(project *railwayapi.ProjectDetails) {
	m.ID = types.StringValue(project.ID)
	m.Name = types.StringValue(project.Name)
	m.WorkspaceID = types.StringValue(project.WorkspaceID)
	m.IsPublic = types.BoolValue(project.IsPublic)
	m.PRDeploys = types.BoolValue(project.PRDeploys)
	if project.Description != "" || !m.Description.IsNull() {
		m.Description = types.StringValue(project.Description)
	}
}

func (r *projectResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var model projectModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &model)...)
	if resp.Diagnostics.HasError() {
		return
	}
	input := model.input()
	if !model.WorkspaceID.IsUnknown() {
		input.WorkspaceID = model.WorkspaceID.ValueStringPointer()
	}
	if !model.DefaultEnvironmentName.IsUnknown() {
		input.DefaultEnvironmentName = model.DefaultEnvironmentName.ValueStringPointer()
	}
	project, err := r.engine.client.ProjectCreate(ctx, input)
	if err != nil {
		resp.Diagnostics.AddError("Creating Railway project", err.Error())
		return
	}
	model.set(project)
	// Save the ID first, so a failure below leaves the project in state.
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), project.ID)...)
	environments, err := r.engine.client.Environments(ctx, project.ID)
	if err != nil || len(environments) == 0 {
		resp.Diagnostics.AddError("Reading Railway project environments", fmt.Sprintf("Project %s: %v", project.ID, err))
		return
	}
	environment := defaultEnvironment(project, environments)
	model.DefaultEnvironmentID = types.StringValue(environment.ID)
	model.DefaultEnvironmentName = types.StringValue(environment.Name)
	resp.Diagnostics.Append(resp.State.Set(ctx, &model)...)
}

func (r *projectResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var model projectModel
	resp.Diagnostics.Append(req.State.Get(ctx, &model)...)
	if resp.Diagnostics.HasError() {
		return
	}
	project, err := r.engine.client.ProjectDetails(ctx, model.ID.ValueString())
	if errors.Is(err, railwayapi.ErrNotFound) {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		resp.Diagnostics.AddError("Reading Railway project", err.Error())
		return
	}
	model.set(project)
	if model.DefaultEnvironmentID.IsNull() {
		environments, err := r.engine.client.Environments(ctx, project.ID)
		if err != nil {
			resp.Diagnostics.AddError("Reading Railway project environments", err.Error())
			return
		}
		if environment := defaultEnvironment(project, environments); environment != nil {
			model.DefaultEnvironmentID = types.StringValue(environment.ID)
			model.DefaultEnvironmentName = types.StringValue(environment.Name)
		}
	} else if environment, err := r.engine.client.EnvironmentDetails(ctx, model.DefaultEnvironmentID.ValueString()); err == nil {
		model.DefaultEnvironmentName = types.StringValue(environment.Name)
	} else if !errors.Is(err, railwayapi.ErrNotFound) {
		resp.Diagnostics.AddError("Reading Railway environment", err.Error())
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &model)...)
}

func (r *projectResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var model, prior projectModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &model)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &prior)...)
	if resp.Diagnostics.HasError() {
		return
	}
	input := model.input()
	if model.Description.IsNull() && !prior.Description.IsNull() {
		input.Description = new(string)
	}
	project, err := r.engine.client.ProjectUpdate(ctx, model.ID.ValueString(), input)
	if err != nil {
		resp.Diagnostics.AddError("Updating Railway project", err.Error())
		return
	}
	model.set(project)
	if name := model.DefaultEnvironmentName; !name.IsUnknown() && !name.Equal(prior.DefaultEnvironmentName) {
		if _, err := r.engine.client.EnvironmentRename(ctx, model.DefaultEnvironmentID.ValueString(), name.ValueString()); err != nil {
			resp.Diagnostics.AddError("Renaming Railway environment", err.Error())
			return
		}
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &model)...)
}

func (r *projectResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var model projectModel
	resp.Diagnostics.Append(req.State.Get(ctx, &model)...)
	if resp.Diagnostics.HasError() {
		return
	}
	id := model.ID.ValueString()
	if err := r.engine.client.ProjectDelete(ctx, id); err != nil {
		resp.Diagnostics.AddError("Deleting Railway project", err.Error())
		return
	}
	err := confirmDeleted(ctx, "project "+id, func() error {
		_, err := r.engine.client.ProjectDetails(ctx, id)
		return err
	})
	if err != nil {
		resp.Diagnostics.AddError("Deleting Railway project", err.Error())
	}
}

// containerDeleteWait bounds how long a project or environment delete waits
// for Railway, which finishes deleting in the background, to stop listing it.
var containerDeleteWait = time.Minute

// confirmDeleted polls read until it reports railwayapi.ErrNotFound, so
// Terraform drops a project or environment from state only once Railway has.
func confirmDeleted(ctx context.Context, what string, read func() error) error {
	deadline := time.Now().Add(containerDeleteWait)
	for {
		err := read()
		switch {
		case errors.Is(err, railwayapi.ErrNotFound):
			return nil
		case err != nil:
			return err
		case time.Now().After(deadline):
			return fmt.Errorf("Railway accepted the deletion of %s but still lists it after %s, so Terraform keeps it in state. "+
				"Run terraform destroy again once Railway has removed it", what, containerDeleteWait)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(containerDeleteWait / 20):
		}
	}
}

func (r *projectResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	resource.ImportStatePassthroughID(ctx, path.Root("id"), req, resp)
}

// defaultEnvironment picks, for an imported project, the environment Railway
// created with it: the project's primary environment, else the one named
// production, else the first listed. Railway lists environments in no fixed
// order.
func defaultEnvironment(project *railwayapi.ProjectDetails, environments []railwayapi.EnvironmentSummary) *railwayapi.EnvironmentSummary {
	for _, want := range []func(railwayapi.EnvironmentSummary) bool{
		func(e railwayapi.EnvironmentSummary) bool { return e.ID == project.PrimaryEnvironmentID },
		func(e railwayapi.EnvironmentSummary) bool { return e.Name == "production" },
	} {
		for i := range environments {
			if want(environments[i]) {
				return &environments[i]
			}
		}
	}
	if len(environments) == 0 {
		return nil
	}
	return &environments[0]
}

func configureResource(req resource.ConfigureRequest, resp *resource.ConfigureResponse) *engine {
	if req.ProviderData == nil {
		return nil
	}
	e, ok := req.ProviderData.(*engine)
	if !ok {
		resp.Diagnostics.AddError("Unexpected provider data", fmt.Sprintf("Expected *engine, got %T.", req.ProviderData))
	}
	return e
}
