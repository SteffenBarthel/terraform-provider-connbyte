package provider

import (
	"context"
	"fmt"

	"github.com/hashicorp/terraform-plugin-framework-validators/datasourcevalidator"
	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/SteffenBarthel/terraform-provider-connbyte/internal/client"
)

var (
	_ datasource.DataSource                     = &dnsZoneDataSource{}
	_ datasource.DataSourceWithConfigure        = &dnsZoneDataSource{}
	_ datasource.DataSourceWithConfigValidators = &dnsZoneDataSource{}
)

// NewDNSZoneDataSource returns the connbyte_dns_zone data source.
func NewDNSZoneDataSource() datasource.DataSource {
	return &dnsZoneDataSource{}
}

type dnsZoneDataSource struct {
	client *client.Client
}

type dnsZoneModel struct {
	ID        types.String `tfsdk:"id"`
	Name      types.String `tfsdk:"name"`
	ProjectID types.String `tfsdk:"project_id"`
	Title     types.String `tfsdk:"title"`
	CreatedAt types.String `tfsdk:"created_at"`
	UpdatedAt types.String `tfsdk:"updated_at"`
}

func (d *dnsZoneDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_dns_zone"
}

func (d *dnsZoneDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Looks up a DNS zone at [ConnByte](https://connbyte.com/) by name or ID. Use its `id` as `zone_id` for `connbyte_dns_record`.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				MarkdownDescription: "Zone ID. Set either `id` or `name`.",
				Optional:            true,
				Computed:            true,
			},
			"name": schema.StringAttribute{
				MarkdownDescription: "Zone name, for example `example.com`. Set either `id` or `name`.",
				Optional:            true,
				Computed:            true,
			},
			"project_id": schema.StringAttribute{
				MarkdownDescription: "ID of the ConnByte project the zone belongs to.",
				Computed:            true,
			},
			"title": schema.StringAttribute{
				MarkdownDescription: "Optional title of the zone.",
				Computed:            true,
			},
			"created_at": schema.StringAttribute{
				MarkdownDescription: "Creation time of the zone (RFC 3339).",
				Computed:            true,
			},
			"updated_at": schema.StringAttribute{
				MarkdownDescription: "Time of the last zone change (RFC 3339), empty if never changed.",
				Computed:            true,
			},
		},
	}
}

func (d *dnsZoneDataSource) ConfigValidators(_ context.Context) []datasource.ConfigValidator {
	return []datasource.ConfigValidator{
		datasourcevalidator.ExactlyOneOf(path.MatchRoot("id"), path.MatchRoot("name")),
	}
}

func (d *dnsZoneDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	if req.ProviderData == nil {
		return
	}
	c, ok := req.ProviderData.(*client.Client)
	if !ok {
		resp.Diagnostics.AddError("Unexpected provider data", fmt.Sprintf("Expected *client.Client, got %T.", req.ProviderData))
		return
	}
	d.client = c
}

func (d *dnsZoneDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var cfg dnsZoneModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &cfg)...)
	if resp.Diagnostics.HasError() {
		return
	}

	var (
		zone *client.Zone
		err  error
	)
	if !cfg.ID.IsNull() {
		zone, err = d.client.GetZone(ctx, cfg.ID.ValueString())
	} else {
		zone, err = d.client.FindZoneByName(ctx, cfg.Name.ValueString())
	}
	if err != nil {
		if client.IsNotFound(err) {
			resp.Diagnostics.AddError("ConnByte DNS zone not found", err.Error())
			return
		}
		resp.Diagnostics.AddError("Cannot read ConnByte DNS zone", err.Error())
		return
	}

	state := dnsZoneModel{
		ID:        types.StringValue(zone.ID),
		Name:      types.StringValue(client.NormalizeName(zone.Zone)),
		ProjectID: types.StringValue(zone.ProjectID),
		Title:     types.StringValue(zone.Title),
		CreatedAt: types.StringValue(zone.CreatedAt),
		UpdatedAt: types.StringValue(""),
	}
	if zone.UpdatedAt != nil {
		state.UpdatedAt = types.StringValue(*zone.UpdatedAt)
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}
