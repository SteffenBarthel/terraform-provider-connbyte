// Package provider implements the ConnByte Terraform/OpenTofu provider.
package provider

import (
	"context"
	"os"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/provider"
	"github.com/hashicorp/terraform-plugin-framework/provider/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/SteffenBarthel/terraform-provider-connbyte/internal/client"
)

const (
	envAPIToken = "CONNBYTE_API_TOKEN"
	envAPIURL   = "CONNBYTE_API_URL"
)

var _ provider.Provider = &connbyteProvider{}

type connbyteProvider struct {
	version string
}

type providerModel struct {
	APIToken types.String `tfsdk:"api_token"`
	APIURL   types.String `tfsdk:"api_url"`
}

// New returns a provider factory.
func New(version string) func() provider.Provider {
	return func() provider.Provider {
		return &connbyteProvider{version: version}
	}
}

func (p *connbyteProvider) Metadata(_ context.Context, _ provider.MetadataRequest, resp *provider.MetadataResponse) {
	resp.TypeName = "connbyte"
	resp.Version = p.version
}

func (p *connbyteProvider) Schema(_ context.Context, _ provider.SchemaRequest, resp *provider.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Manage DNS zones and records at [ConnByte](https://connbyte.com/) through the ConnByte API.",
		Attributes: map[string]schema.Attribute{
			"api_token": schema.StringAttribute{
				MarkdownDescription: "ConnByte API token. Can also be set with the `" + envAPIToken + "` environment variable.",
				Optional:            true,
				Sensitive:           true,
			},
			"api_url": schema.StringAttribute{
				MarkdownDescription: "Base URL of the ConnByte API. Defaults to `" + client.DefaultBaseURL + "`. Can also be set with the `" + envAPIURL + "` environment variable.",
				Optional:            true,
			},
		},
	}
}

func (p *connbyteProvider) Configure(ctx context.Context, req provider.ConfigureRequest, resp *provider.ConfigureResponse) {
	var cfg providerModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &cfg)...)
	if resp.Diagnostics.HasError() {
		return
	}

	if cfg.APIToken.IsUnknown() {
		resp.Diagnostics.AddAttributeError(path.Root("api_token"), "Unknown ConnByte API token",
			"The provider cannot create the ConnByte API client because the API token is not known yet. Set it to a static value or use the "+envAPIToken+" environment variable.")
	}
	if cfg.APIURL.IsUnknown() {
		resp.Diagnostics.AddAttributeError(path.Root("api_url"), "Unknown ConnByte API URL",
			"The provider cannot create the ConnByte API client because the API URL is not known yet.")
	}
	if resp.Diagnostics.HasError() {
		return
	}

	token := os.Getenv(envAPIToken)
	if !cfg.APIToken.IsNull() {
		token = cfg.APIToken.ValueString()
	}
	baseURL := os.Getenv(envAPIURL)
	if !cfg.APIURL.IsNull() {
		baseURL = cfg.APIURL.ValueString()
	}

	if token == "" {
		resp.Diagnostics.AddAttributeError(path.Root("api_token"), "Missing ConnByte API token",
			"Set api_token in the provider block or the "+envAPIToken+" environment variable. You can create a token in your ConnByte account at https://connbyte.com/.")
		return
	}

	c := client.New(baseURL, token, "terraform-provider-connbyte/"+p.version)
	resp.DataSourceData = c
	resp.ResourceData = c
}

func (p *connbyteProvider) Resources(_ context.Context) []func() resource.Resource {
	return []func() resource.Resource{
		NewDNSRecordResource,
	}
}

func (p *connbyteProvider) DataSources(_ context.Context) []func() datasource.DataSource {
	return []func() datasource.DataSource{
		NewDNSZoneDataSource,
	}
}
