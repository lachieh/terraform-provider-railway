package provider

import (
	"context"
	"fmt"
	"strconv"

	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
	railway "github.com/railwayapp/railway-go-sdk"

	"github.com/tambo-ai/terraform-provider-railway-iac-sdk/internal/iac"
)

type databaseModel struct {
	commonModel
	serviceSettings
	Engine           string  `tfsdk:"engine"`
	Image            *string `tfsdk:"image"`
	Region           *string `tfsdk:"region"`
	Output           *string `tfsdk:"output"`
	DefaultMountPath *string `tfsdk:"default_mount_path"`
}

// databaseConstructors are the SDK's managed database templates.
var databaseConstructors = map[string]func(string, ...map[string]any) railway.Resource{
	"postgres": railway.Postgres,
	"mysql":    railway.Mysql,
	"redis":    railway.Redis,
	"mongo":    railway.Mongo,
}

func NewDatabaseResource() resource.Resource {
	attributes := serviceSettingsAttributes()
	attributes["engine"] = schema.StringAttribute{
		MarkdownDescription: "`postgres`, `mysql`, `redis` or `mongo` deploy Railway's template, as `railway.Postgres`, `railway.Mysql`, `railway.Redis` and `railway.Mongo`. " +
			"Any other engine is a database you bring, as `railway.Database`, and needs `image`.",
		Required: true,
	}
	attributes["image"] = schema.StringAttribute{
		MarkdownDescription: "Image to run. Defaults to the template's image for a template engine.",
		Optional:            true,
	}
	attributes["output"] = schema.StringAttribute{
		MarkdownDescription: "Variable other services reference for the connection string. Defaults to the engine's, such as `DATABASE_URL`.",
		Optional:            true,
	}
	attributes["default_mount_path"] = schema.StringAttribute{
		MarkdownDescription: "Where the database's volume mounts. Defaults to the template's path for a template engine.",
		Optional:            true,
	}
	attributes["region"] = schema.StringAttribute{
		MarkdownDescription: "Region of the database's single replica.",
		Optional:            true,
	}
	return &nodeResource[databaseModel]{kind: nodeKind[databaseModel]{
		graphType: "database",
		description: "A Railway-managed database deployed from Railway's database template. " +
			"Accepts the service settings, which override the template's.",
		attributes: attributes,
		common:     func(m *databaseModel) *commonModel { return &m.commonModel },
		build: func(m *databaseModel) func() any {
			return func() any {
				options := map[string]any{}
				putString(options, "image", m.Image)
				putString(options, "region", m.Region)
				putString(options, "output", m.Output)
				putString(options, "defaultMountPath", m.DefaultMountPath)
				var database railway.Resource
				if template, ok := databaseConstructors[m.Engine]; ok {
					database = template(m.Name.ValueString(), options)
				} else {
					if m.Image == nil {
						panic("A " + m.Engine + " database is not a Railway template; set image.")
					}
					database = railway.Database(m.Name.ValueString(), m.Engine, options)
				}
				return overlayService(database.Graph(), m.Name.ValueString(), m.config())
			}
		},
	}}
}

// overlayService applies service settings to an SDK database node. The SDK's
// database constructors take only an image and a region; a railway.ts file
// spreads further settings over the node, and this does the same with the
// keys ServiceNamed would produce for them.
func overlayService(node map[string]any, name string, config railway.ServiceConfig) map[string]any {
	settings := railway.ServiceNamed(name, config).Graph()
	for _, key := range iac.SortedKeys(settings) {
		switch key {
		case "address", "type", "kind", "name", "source":
			continue
		case "deploy":
			node["deploy"] = iac.MergeObjects(node["deploy"], settings["deploy"])
		default:
			node[key] = settings[key]
		}
	}
	return node
}

type volumeModel struct {
	commonModel
	SizeMB                  *int64  `tfsdk:"size_mb"`
	Region                  *string `tfsdk:"region"`
	AllowOnlineResize       *bool   `tfsdk:"allow_online_resize"`
	UsageAlerts             []int64 `tfsdk:"usage_alerts"`
	ForkFromBaseEnvironment *bool   `tfsdk:"fork_from_base_environment"`
	Group                   *string `tfsdk:"group"`
}

func NewVolumeResource() resource.Resource {
	return &nodeResource[volumeModel]{kind: nodeKind[volumeModel]{
		graphType: "volume",
		description: "A Railway volume, as `railway.Volume`. Mount it with a service's `volume_mounts`. " +
			"Destroying it deletes the volume and its data.",
		attributes: map[string]schema.Attribute{
			"size_mb":             schema.Int64Attribute{MarkdownDescription: "Size in megabytes.", Optional: true},
			"region":              schema.StringAttribute{MarkdownDescription: "Region the volume lives in.", Optional: true},
			"allow_online_resize": schema.BoolAttribute{MarkdownDescription: "Resize without stopping the service.", Optional: true},
			"usage_alerts": schema.ListAttribute{
				MarkdownDescription: "Usage percentages that send an alert, such as `[80, 95, 100]`.",
				Optional:            true,
				ElementType:         types.Int64Type,
			},
			"fork_from_base_environment": schema.BoolAttribute{
				MarkdownDescription: "Create the volume in a forked environment from the base environment's data.",
				Optional:            true,
			},
			"group": schema.StringAttribute{MarkdownDescription: "Name of the canvas group the volume belongs to.", Optional: true},
		},
		common: func(m *volumeModel) *commonModel { return &m.commonModel },
		build: func(m *volumeModel) func() any {
			return func() any {
				config := map[string]any{}
				putInt(config, "sizeMB", m.SizeMB)
				putString(config, "region", m.Region)
				putBool(config, "allowOnlineResize", m.AllowOnlineResize)
				putBool(config, "forkFromBaseEnvironment", m.ForkFromBaseEnvironment)
				if m.UsageAlerts != nil {
					usage := map[string]any{}
					for _, percent := range m.UsageAlerts {
						usage[strconv.FormatInt(percent, 10)] = map[string]any{}
					}
					config["alerts"] = map[string]any{"usage": usage}
				}
				return inGroup(m.Group, railway.Volume(m.Name.ValueString(), config))
			}
		},
		// Railway's config apply never deletes a volume, so destroy deletes
		// it directly once the ChangeSet has detached it.
		remove: func(ctx context.Context, e *engine, m *volumeModel) error {
			environmentID, address := m.EnvironmentID.ValueString(), iac.ResourceAddress("volume", m.Name.ValueString())
			snap, err := e.read(ctx, environmentID)
			if err != nil {
				return err
			}
			for _, edge := range snap.graph.Edges {
				if edge.Kind == "mount" && edge.To == address {
					return fmt.Errorf("%s is still mounted by %s; remove the mount before destroying the volume", address, edge.From)
				}
			}
			id := m.RailwayID.ValueString()
			if id == "" {
				return fmt.Errorf("%s has no Railway ID in state; refresh, or delete it from the Railway dashboard", address)
			}
			defer e.invalidate(environmentID)
			return e.client.VolumeDelete(ctx, id)
		},
	}}
}

type bucketModel struct {
	commonModel
	Region *string `tfsdk:"region"`
	Group  *string `tfsdk:"group"`
}

func NewBucketResource() resource.Resource {
	return &nodeResource[bucketModel]{kind: nodeKind[bucketModel]{
		graphType:   "bucket",
		description: "A Railway object storage bucket, as `railway.Bucket`.",
		attributes: map[string]schema.Attribute{
			"region": schema.StringAttribute{
				MarkdownDescription: "`sjc`, `iad`, `ams` or `sin`.",
				Optional:            true,
				Validators:          []validator.String{stringvalidator.OneOf("sjc", "iad", "ams", "sin")},
			},
			"group": schema.StringAttribute{MarkdownDescription: "Name of the canvas group the bucket belongs to.", Optional: true},
		},
		common: func(m *bucketModel) *commonModel { return &m.commonModel },
		build: func(m *bucketModel) func() any {
			return func() any {
				config := map[string]any{}
				putString(config, "region", m.Region)
				return inGroup(m.Group, railway.Bucket(m.Name.ValueString(), config))
			}
		},
	}}
}
