package provider

import (
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	railway "github.com/railwayapp/railway-go-sdk"

	"github.com/tambo-ai/terraform-provider-railway-iac-sdk/internal/iac"
)

type functionModel struct {
	commonModel
	serviceSettings
	Code    string  `tfsdk:"code"`
	Runtime *string `tfsdk:"runtime"`
	Image   *string `tfsdk:"image"`
}

func NewFunctionResource() resource.Resource {
	attributes := serviceSettingsAttributes()
	attributes["code"] = schema.StringAttribute{
		MarkdownDescription: "TypeScript source of the function, such as `file(\"hook.ts\")`. Railway runs it on the Bun function image. " +
			"At most " + itoa(railway.FunctionSourceMaxBytes) + " bytes of UTF-8 (`railway.FunctionSourceMaxBytes`).",
		Required:   true,
		Validators: []validator.String{stringvalidator.LengthBetween(1, railway.FunctionSourceMaxBytes)},
	}
	attributes["runtime"] = schema.StringAttribute{
		MarkdownDescription: "Runtime image from `railway.FunctionImages`. Defaults to `bun`. Conflicts with `image`.",
		Optional:            true,
		Validators: []validator.String{
			stringvalidator.OneOf(iac.SortedKeys(railway.FunctionImages)...),
			stringvalidator.ConflictsWith(path.MatchRoot("image")),
		},
	}
	attributes["image"] = schema.StringAttribute{
		MarkdownDescription: "Function runtime image to pin instead of `runtime`, such as `ghcr.io/railwayapp/function-bun:1.2.16`.",
		Optional:            true,
	}
	return &nodeResource[functionModel]{kind: nodeKind[functionModel]{
		graphType: "service",
		typeName:  "function",
		description: "A Railway Function, as `railway.Fn`: a single TypeScript file that runs on Railway's Bun function image with no repository or build step. " +
			"The source travels in the deploy start command, so `deploy.start_command` cannot be set. Accepts the other service settings.",
		attributes: attributes,
		common:     func(m *functionModel) *commonModel { return &m.commonModel },
		build: func(m *functionModel) func() any {
			return func() any {
				config := m.config()
				if deploy, ok := config["deploy"].(map[string]any); ok && deploy["startCommand"] != nil {
					panic("Function code and deploy.start_command are mutually exclusive: the code is the start command.")
				}
				config["start"] = railway.FunctionStartCommand(m.Code)
				switch {
				case m.Image != nil:
					config["source"] = railway.Image(*m.Image)
				case m.Runtime != nil:
					config["runtime"] = *m.Runtime
				default:
					config["runtime"] = "bun"
				}
				return railway.Fn(m.Name.ValueString(), config)
			}
		},
	}}
}

type groupModel struct {
	commonModel
	Color       *string `tfsdk:"color"`
	Icon        *string `tfsdk:"icon"`
	IsCollapsed *bool   `tfsdk:"is_collapsed"`
	Group       *string `tfsdk:"group"`
}

func NewGroupResource() resource.Resource {
	return &nodeResource[groupModel]{kind: nodeKind[groupModel]{
		graphType: "group",
		description: "A canvas group, as `railway.Group`. Put services, databases, volumes and buckets in it with their `group` attribute. " +
			"Railway lists a group in the environment only while something belongs to it.",
		attributes: map[string]schema.Attribute{
			"color":        schema.StringAttribute{MarkdownDescription: "Canvas color.", Optional: true},
			"icon":         schema.StringAttribute{MarkdownDescription: "Canvas icon.", Optional: true},
			"is_collapsed": schema.BoolAttribute{MarkdownDescription: "Show the group collapsed on the canvas.", Optional: true},
			"group":        schema.StringAttribute{MarkdownDescription: "Name of the parent group.", Optional: true},
		},
		common: func(m *groupModel) *commonModel { return &m.commonModel },
		build: func(m *groupModel) func() any {
			return func() any {
				options := map[string]any{}
				putString(options, "color", m.Color)
				putString(options, "icon", m.Icon)
				putBool(options, "isCollapsed", m.IsCollapsed)
				putString(options, "groupId", m.Group)
				return railway.Group(m.Name.ValueString(), nil, options)[0]
			}
		},
	}}
}

// inGroup puts an SDK resource in a canvas group with railway.Group, which
// sets the member's groupId.
func inGroup(group *string, member any) any {
	if group == nil {
		return member
	}
	return railway.Group(*group, []any{member})[1]
}
