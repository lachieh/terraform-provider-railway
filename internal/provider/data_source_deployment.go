package provider

import (
	"context"
	"fmt"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

type deploymentDataSource struct{ engine *engine }

type deploymentDataModel struct {
	EnvironmentID types.String `tfsdk:"environment_id"`
	ServiceID     types.String `tfsdk:"service_id"`
	ID            types.String `tfsdk:"id"`
	Status        types.String `tfsdk:"status"`
	URL           types.String `tfsdk:"url"`
	StaticURL     types.String `tfsdk:"static_url"`
	CreatedAt     types.String `tfsdk:"created_at"`
	UpdatedAt     types.String `tfsdk:"updated_at"`
}

func NewDeploymentDataSource() datasource.DataSource { return &deploymentDataSource{} }

func (d *deploymentDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_deployment"
}

func (d *deploymentDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	computedString := func(description string) schema.StringAttribute {
		return schema.StringAttribute{MarkdownDescription: description, Computed: true}
	}
	resp.Schema = schema.Schema{
		MarkdownDescription: "The latest deployment of a service in an environment. Terraform reads it when it plans, so it reports the deployment as of the plan; set `wait_for_deployment` on `railway_service` to have an apply wait for its deployment instead.",
		Attributes: map[string]schema.Attribute{
			"environment_id": schema.StringAttribute{MarkdownDescription: "Environment ID.", Required: true},
			"service_id":     schema.StringAttribute{MarkdownDescription: "Railway service ID, such as `railway_service.web.railway_id`.", Required: true},
			"id":             computedString("Deployment ID."),
			"status":         computedString("Deployment status, such as `SUCCESS`, `BUILDING`, `DEPLOYING`, `FAILED` or `CRASHED`."),
			"url":            computedString("URL of the deployment, when it has one."),
			"static_url":     computedString("Railway domain the deployment serves, when it has one."),
			"created_at":     computedString("When Railway created the deployment."),
			"updated_at":     computedString("When the deployment last changed."),
		},
	}
}

func (d *deploymentDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	d.engine = configureDataSource(req, resp)
}

func (d *deploymentDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var data deploymentDataModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}
	deployment, err := d.engine.client.LatestDeployment(ctx, data.EnvironmentID.ValueString(), data.ServiceID.ValueString())
	if err != nil {
		resp.Diagnostics.AddError("Reading Railway deployment", err.Error())
		return
	}
	if deployment == nil {
		resp.Diagnostics.AddError("No Railway deployment",
			fmt.Sprintf("Service %s has not deployed in environment %s.", data.ServiceID.ValueString(), data.EnvironmentID.ValueString()))
		return
	}
	data.ID = types.StringValue(deployment.ID)
	data.Status = types.StringValue(deployment.Status)
	data.URL = types.StringValue(deployment.URL)
	data.StaticURL = types.StringValue(deployment.StaticURL)
	data.CreatedAt = types.StringValue(deployment.CreatedAt)
	data.UpdatedAt = types.StringValue(deployment.UpdatedAt)
	resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
}
