package provider

import (
	"context"
	"fmt"

	"github.com/hashicorp/terraform-plugin-framework/ephemeral"
	"github.com/hashicorp/terraform-plugin-framework/ephemeral/schema"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

// bucketCredentialsResource opens a bucket's S3 credentials without writing
// them to plan or state.
type bucketCredentialsResource struct{ engine *engine }

type bucketCredentialsModel struct {
	EnvironmentID   types.String `tfsdk:"environment_id"`
	BucketID        types.String `tfsdk:"bucket_id"`
	AccessKeyID     types.String `tfsdk:"access_key_id"`
	SecretAccessKey types.String `tfsdk:"secret_access_key"`
	Endpoint        types.String `tfsdk:"endpoint"`
	BucketName      types.String `tfsdk:"bucket_name"`
	Region          types.String `tfsdk:"region"`
	URLStyle        types.String `tfsdk:"url_style"`
}

func NewBucketCredentialsResource() ephemeral.EphemeralResource { return &bucketCredentialsResource{} }

func (r *bucketCredentialsResource) Metadata(_ context.Context, req ephemeral.MetadataRequest, resp *ephemeral.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_bucket_credentials"
}

func (r *bucketCredentialsResource) Schema(_ context.Context, _ ephemeral.SchemaRequest, resp *ephemeral.SchemaResponse) {
	computedString := func(description string) schema.StringAttribute {
		return schema.StringAttribute{MarkdownDescription: description, Computed: true}
	}
	resp.Schema = schema.Schema{
		MarkdownDescription: "S3 credentials for a bucket in one environment. An ephemeral resource never writes its values to the plan or state; " +
			"pass them to a write-only attribute, such as `secret_variables`, or to another provider's configuration. Needs Terraform 1.10 or OpenTofu 1.11, or later. " +
			"A service in the same environment should reference the bucket's variables with `variable_references` instead.",
		Attributes: map[string]schema.Attribute{
			"environment_id":    schema.StringAttribute{MarkdownDescription: "Environment the bucket is in.", Required: true},
			"bucket_id":         schema.StringAttribute{MarkdownDescription: "Railway bucket ID, such as `railway_bucket.assets.railway_id`.", Required: true},
			"access_key_id":     computedString("S3 access key ID."),
			"secret_access_key": schema.StringAttribute{MarkdownDescription: "S3 secret access key.", Computed: true, Sensitive: true},
			"endpoint":          computedString("S3 endpoint URL."),
			"bucket_name":       computedString("Bucket name for the S3 API. Railway adds a suffix to the bucket's name, so read it from here."),
			"region":            computedString("Region for the S3 API."),
			"url_style":         computedString("Addressing style the endpoint expects, such as `virtual-host` or `path`."),
		},
	}
}

func (r *bucketCredentialsResource) Configure(_ context.Context, req ephemeral.ConfigureRequest, resp *ephemeral.ConfigureResponse) {
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

func (r *bucketCredentialsResource) Open(ctx context.Context, req ephemeral.OpenRequest, resp *ephemeral.OpenResponse) {
	var data bucketCredentialsModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}
	environmentID := data.EnvironmentID.ValueString()
	snap, err := r.engine.read(ctx, environmentID)
	if err != nil {
		resp.Diagnostics.AddError("Reading Railway environment", err.Error())
		return
	}
	credentials, err := r.engine.client.BucketCredentials(ctx, snap.env.ProjectID, environmentID, data.BucketID.ValueString())
	if err != nil {
		resp.Diagnostics.AddError("Reading Railway bucket credentials", err.Error())
		return
	}
	data.AccessKeyID = types.StringValue(credentials.AccessKeyID)
	data.SecretAccessKey = types.StringValue(credentials.SecretAccessKey)
	data.Endpoint = types.StringValue(credentials.Endpoint)
	data.BucketName = types.StringValue(credentials.BucketName)
	data.Region = types.StringValue(credentials.Region)
	data.URLStyle = types.StringValue(credentials.URLStyle)
	resp.Diagnostics.Append(resp.Result.Set(ctx, &data)...)
}
