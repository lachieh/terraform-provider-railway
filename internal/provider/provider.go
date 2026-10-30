// Package provider implements the railway Terraform provider. Each resource
// owns one node of a Railway environment's IaC graph: it builds the node with
// github.com/railwayapp/railway-go-sdk, diffs it against the live environment
// with the ported CLI engine in internal/iac, and applies the resulting
// ChangeSet through backboard.
package provider

import (
	"context"
	"os"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/ephemeral"
	"github.com/hashicorp/terraform-plugin-framework/function"
	"github.com/hashicorp/terraform-plugin-framework/provider"
	"github.com/hashicorp/terraform-plugin-framework/provider/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/tambo-ai/terraform-provider-railway-iac-sdk/internal/iac"
	"github.com/tambo-ai/terraform-provider-railway-iac-sdk/internal/railwayapi"
)

const (
	tokenEnvVar        = "RAILWAY_TOKEN"
	projectTokenEnvVar = "RAILWAY_PROJECT_TOKEN"
	endpointEnvVar     = "RAILWAY_GRAPHQL_ENDPOINT"
)

var (
	_ provider.ProviderWithFunctions          = &railwayProvider{}
	_ provider.ProviderWithEphemeralResources = &railwayProvider{}
)

type railwayProvider struct {
	version string
}

type providerModel struct {
	Token        types.String `tfsdk:"token"`
	ProjectToken types.String `tfsdk:"project_token"`
	Partial      types.String `tfsdk:"partial"`
	Endpoint     types.String `tfsdk:"endpoint"`
}

func New(version string) func() provider.Provider {
	return func() provider.Provider { return &railwayProvider{version: version} }
}

func (p *railwayProvider) Metadata(_ context.Context, _ provider.MetadataRequest, resp *provider.MetadataResponse) {
	resp.TypeName = "railway"
	resp.Version = p.version
}

func (p *railwayProvider) Schema(_ context.Context, _ provider.SchemaRequest, resp *provider.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Manages Railway environments through Railway's Infrastructure as Code engine. " +
			"Each resource is one node of the environment graph, built with `railway-go-sdk` and applied as a Railway ChangeSet.",
		Attributes: map[string]schema.Attribute{
			"token": schema.StringAttribute{
				MarkdownDescription: "Railway account or workspace token, sent as a bearer token. Defaults to `RAILWAY_TOKEN`. Conflicts with `project_token`.",
				Optional:            true,
				Sensitive:           true,
			},
			"project_token": schema.StringAttribute{
				MarkdownDescription: "Railway project token, scoped to one project environment. Defaults to `RAILWAY_PROJECT_TOKEN`. Conflicts with `token`.",
				Optional:            true,
				Sensitive:           true,
			},
			"partial": schema.StringAttribute{
				MarkdownDescription: "Named IaC partial that owns the resources this configuration manages, the same as `const Partial` in a `railway.go` file. " +
					"Leave unset to manage resources as the whole project. A resource owned by another partial is refused.",
				Optional: true,
			},
			"endpoint": schema.StringAttribute{
				MarkdownDescription: "Railway GraphQL endpoint. Defaults to `RAILWAY_GRAPHQL_ENDPOINT`, then `https://backboard.railway.com/graphql/v2`.",
				Optional:            true,
			},
		},
	}
}

func (p *railwayProvider) Configure(ctx context.Context, req provider.ConfigureRequest, resp *provider.ConfigureResponse) {
	var data providerModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if data.Token.IsUnknown() || data.ProjectToken.IsUnknown() || data.Partial.IsUnknown() || data.Endpoint.IsUnknown() {
		resp.Diagnostics.AddError("Unknown provider configuration",
			"The railway provider needs token, project_token, partial and endpoint to be known when it is configured.")
		return
	}

	cred := railwayapi.Credential{Token: data.Token.ValueString(), ProjectToken: data.ProjectToken.ValueString()}
	if cred.Token == "" && cred.ProjectToken == "" {
		cred = railwayapi.Credential{Token: os.Getenv(tokenEnvVar), ProjectToken: os.Getenv(projectTokenEnvVar)}
	}
	switch {
	case cred.Token == "" && cred.ProjectToken == "":
		resp.Diagnostics.AddError("Missing Railway credential",
			"Set token or project_token in the provider configuration, or set RAILWAY_TOKEN or RAILWAY_PROJECT_TOKEN.")
		return
	case cred.Token != "" && cred.ProjectToken != "":
		resp.Diagnostics.AddError("Conflicting Railway credentials",
			"Set exactly one of token (RAILWAY_TOKEN) or project_token (RAILWAY_PROJECT_TOKEN).")
		return
	}

	partial, err := iac.ParsePartialName(data.Partial.ValueString())
	if err != nil {
		resp.Diagnostics.AddAttributeError(pathRoot("partial"), "Invalid partial", err.Error())
		return
	}

	opts := []railwayapi.Option{railwayapi.WithUserAgent("terraform-provider-railway-iac-sdk/" + p.version)}
	endpoint := data.Endpoint.ValueString()
	if endpoint == "" {
		endpoint = os.Getenv(endpointEnvVar)
	}
	if endpoint != "" {
		opts = append(opts, railwayapi.WithEndpoint(endpoint))
	}
	client, err := railwayapi.New(cred, opts...)
	if err != nil {
		resp.Diagnostics.AddError("Invalid Railway client configuration", err.Error())
		return
	}

	engine := newEngine(client, partial)
	resp.ResourceData = engine
	resp.DataSourceData = engine
	resp.EphemeralResourceData = engine
}

func (p *railwayProvider) Resources(context.Context) []func() resource.Resource {
	return []func() resource.Resource{
		NewServiceResource,
		NewDatabaseResource,
		NewVolumeResource,
		NewBucketResource,
		NewFunctionResource,
		NewGroupResource,
		NewProjectResource,
		NewEnvironmentResource,
	}
}

func (p *railwayProvider) EphemeralResources(context.Context) []func() ephemeral.EphemeralResource {
	return []func() ephemeral.EphemeralResource{NewBucketCredentialsResource}
}

func (p *railwayProvider) DataSources(context.Context) []func() datasource.DataSource {
	return []func() datasource.DataSource{
		NewEnvironmentDataSource,
		NewProjectDataSource,
		NewServiceDataSource,
		NewDatabaseDataSource,
		NewVolumeDataSource,
		NewBucketDataSource,
		NewDeploymentDataSource,
	}
}

func (p *railwayProvider) Functions(context.Context) []func() function.Function {
	return []func() function.Function{NewRandomStringFunction}
}
