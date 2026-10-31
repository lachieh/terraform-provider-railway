package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/lachieh/terraform-provider-railway/internal/iac"
)

// nodeDataSource looks up one graph resource of graphType by name, for
// resources this configuration does not manage.
type nodeDataSource struct {
	graphType string
	engine    *engine
}

type nodeDataModel struct {
	EnvironmentID types.String `tfsdk:"environment_id"`
	Name          types.String `tfsdk:"name"`
	ID            types.String `tfsdk:"id"`
	ProjectID     types.String `tfsdk:"project_id"`
	RailwayID     types.String `tfsdk:"railway_id"`
	Address       types.String `tfsdk:"address"`
	Config        types.String `tfsdk:"config"`
	VariableNames types.Set    `tfsdk:"variable_names"`
}

func NewServiceDataSource() datasource.DataSource  { return &nodeDataSource{graphType: "service"} }
func NewDatabaseDataSource() datasource.DataSource { return &nodeDataSource{graphType: "database"} }
func NewVolumeDataSource() datasource.DataSource   { return &nodeDataSource{graphType: "volume"} }
func NewBucketDataSource() datasource.DataSource   { return &nodeDataSource{graphType: "bucket"} }

func (d *nodeDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_" + d.graphType
}

func (d *nodeDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: fmt.Sprintf("A %s in a Railway environment, found by name. Use it to wire a %s this configuration does not manage, such as one a `railway.go` file or the dashboard owns.", d.graphType, d.graphType),
		Attributes: map[string]schema.Attribute{
			"environment_id": schema.StringAttribute{MarkdownDescription: "ID of the environment that holds the " + d.graphType + ".", Required: true},
			"name":           schema.StringAttribute{MarkdownDescription: "Name of the " + d.graphType + ".", Required: true},
			"id":             schema.StringAttribute{MarkdownDescription: "`<environment_id>/<address>`.", Computed: true},
			"project_id":     schema.StringAttribute{MarkdownDescription: "ID of the project the environment belongs to.", Computed: true},
			"railway_id":     schema.StringAttribute{MarkdownDescription: "Railway's ID for the " + d.graphType + ".", Computed: true},
			"address": schema.StringAttribute{
				MarkdownDescription: "Graph address, such as `" + d.graphType + ".web`, for a resource's `variable_references`.",
				Computed:            true,
			},
			"config": schema.StringAttribute{
				MarkdownDescription: "The " + d.graphType + "'s live graph node as JSON, in the shape `railway config plan` diffs, without variable values. Read fields with `jsondecode`.",
				Computed:            true,
			},
			"variable_names": schema.SetAttribute{MarkdownDescription: "Names of the variables set on the " + d.graphType + ".", Computed: true, ElementType: types.StringType},
		},
	}
}

func (d *nodeDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	d.engine = configureDataSource(req, resp)
}

func (d *nodeDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var data nodeDataModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}
	environmentID, name := data.EnvironmentID.ValueString(), data.Name.ValueString()
	snap, err := d.engine.read(ctx, environmentID)
	if err != nil {
		resp.Diagnostics.AddError("Reading Railway environment", err.Error())
		return
	}
	address := sdkAddress(d.graphType, name)
	node := snap.graph.FindResource(address)
	if node == nil {
		resp.Diagnostics.AddError("Railway "+d.graphType+" not found", fmt.Sprintf("Environment %s has no %s.", environmentID, address))
		return
	}
	live := maps.Clone(node)
	variables, _ := live["variables"].(map[string]any)
	delete(live, "variables")
	raw, err := json.Marshal(live)
	if err != nil {
		resp.Diagnostics.AddError("Encoding Railway "+d.graphType, err.Error())
		return
	}
	names, diags := types.SetValueFrom(ctx, types.StringType, iac.SortedKeys(variables))
	resp.Diagnostics.Append(diags...)

	data.ID = types.StringValue(environmentID + "/" + address)
	data.ProjectID = types.StringValue(snap.env.ProjectID)
	data.RailwayID = types.StringValue(railwayID(snap, d.graphType, name))
	data.Address = types.StringValue(address)
	data.Config = types.StringValue(string(raw))
	data.VariableNames = names
	resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
}
