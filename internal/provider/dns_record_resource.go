package provider

import (
	"context"
	"fmt"
	"net"
	"regexp"
	"strconv"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework-validators/int64validator"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/booldefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/int64default"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-log/tflog"

	"github.com/SteffenBarthel/terraform-provider-connbyte/internal/client"
)

var (
	_ resource.Resource                   = &dnsRecordResource{}
	_ resource.ResourceWithConfigure      = &dnsRecordResource{}
	_ resource.ResourceWithImportState    = &dnsRecordResource{}
	_ resource.ResourceWithValidateConfig = &dnsRecordResource{}
)

const defaultTTL = 3600

var (
	recordNamePattern = regexp.MustCompile(`^(@|\*|(\*\.)?[a-z0-9_]([a-z0-9_-]*[a-z0-9_])?(\.[a-z0-9_]([a-z0-9_-]*[a-z0-9_])?)*)$`)
	noControlChars    = regexp.MustCompile(`^[^\x00-\x1f\x7f]*$`)
)

// NewDNSRecordResource returns the connbyte_dns_record resource.
func NewDNSRecordResource() resource.Resource {
	return &dnsRecordResource{}
}

type dnsRecordResource struct {
	client *client.Client
}

type dnsRecordModel struct {
	ID       types.String `tfsdk:"id"`
	ZoneID   types.String `tfsdk:"zone_id"`
	Name     types.String `tfsdk:"name"`
	Type     types.String `tfsdk:"type"`
	Content  types.String `tfsdk:"content"`
	TTL      types.Int64  `tfsdk:"ttl"`
	Priority types.Int64  `tfsdk:"priority"`
	Disabled types.Bool   `tfsdk:"disabled"`
	FQDN     types.String `tfsdk:"fqdn"`
}

func (r *dnsRecordResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_dns_record"
}

func (r *dnsRecordResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Manages a DNS record in a zone hosted at [ConnByte](https://connbyte.com/).",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				MarkdownDescription: "Record ID assigned by ConnByte.",
				Computed:            true,
				PlanModifiers:       []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"zone_id": schema.StringAttribute{
				MarkdownDescription: "ID of the zone, for example from the `connbyte_dns_zone` data source. Changing it creates a new record.",
				Required:            true,
				PlanModifiers:       []planmodifier.String{stringplanmodifier.RequiresReplace()},
				Validators:          []validator.String{stringvalidator.LengthAtLeast(1)},
			},
			"name": schema.StringAttribute{
				MarkdownDescription: "Record name relative to the zone, in lower case. Use `@` for the zone apex, for example `www` or `_acme-challenge`.",
				Required:            true,
				Validators: []validator.String{
					stringvalidator.RegexMatches(recordNamePattern, "must be @ or a lower-case host name relative to the zone (letters, digits, '-', '_', optional leading '*.')"),
				},
			},
			"type": schema.StringAttribute{
				MarkdownDescription: "Record type. One of `" + strings.Join(client.RecordTypes, "`, `") + "`.",
				Required:            true,
				Validators:          []validator.String{stringvalidator.OneOf(client.RecordTypes...)},
			},
			"content": schema.StringAttribute{
				MarkdownDescription: "Record content, for example an IP address or a host name. For `MX` set only the mail server here and the preference in `priority`. For `SRV` use `weight port target` and set the priority in `priority`. For `TXT` give the text without quotes; long values (over 255 characters, for example DKIM keys) are split by the name server and joined again by the provider. Control characters such as line breaks are not allowed.",
				Required:            true,
				Validators: []validator.String{
					stringvalidator.LengthAtLeast(1),
					stringvalidator.RegexMatches(noControlChars, "must not contain control characters such as line breaks or tabs"),
				},
			},
			"ttl": schema.Int64Attribute{
				MarkdownDescription: fmt.Sprintf("Time to live in seconds, between %d and %d. Defaults to `%d`.", client.MinTTL, client.MaxTTL, defaultTTL),
				Optional:            true,
				Computed:            true,
				Default:             int64default.StaticInt64(defaultTTL),
				Validators:          []validator.Int64{int64validator.Between(client.MinTTL, client.MaxTTL)},
			},
			"priority": schema.Int64Attribute{
				MarkdownDescription: fmt.Sprintf("Priority for `MX` and `SRV` records, between %d and %d. Defaults to `0`.", client.MinPriority, client.MaxPriority),
				Optional:            true,
				Computed:            true,
				Default:             int64default.StaticInt64(0),
				Validators:          []validator.Int64{int64validator.Between(client.MinPriority, client.MaxPriority)},
			},
			"disabled": schema.BoolAttribute{
				MarkdownDescription: "Whether the record is disabled. Disabled records are kept but not served. Defaults to `false`.",
				Optional:            true,
				Computed:            true,
				Default:             booldefault.StaticBool(false),
			},
			"fqdn": schema.StringAttribute{
				MarkdownDescription: "Fully qualified record name without trailing dot.",
				Computed:            true,
			},
		},
	}
}

func (r *dnsRecordResource) ValidateConfig(ctx context.Context, req resource.ValidateConfigRequest, resp *resource.ValidateConfigResponse) {
	var cfg dnsRecordModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &cfg)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if cfg.Type.IsUnknown() || cfg.Type.IsNull() {
		return
	}
	t := cfg.Type.ValueString()
	if t != "MX" && t != "SRV" && !cfg.Priority.IsUnknown() && !cfg.Priority.IsNull() && cfg.Priority.ValueInt64() != 0 {
		resp.Diagnostics.AddAttributeWarning(path.Root("priority"), "Priority is not used for this record type",
			fmt.Sprintf("ConnByte uses priority only for MX and SRV records. It has no effect on %s records.", t))
	}
	if t == "MX" && !cfg.Content.IsUnknown() && !cfg.Content.IsNull() {
		if first, _, ok := strings.Cut(strings.TrimSpace(cfg.Content.ValueString()), " "); ok {
			if _, err := strconv.Atoi(first); err == nil {
				resp.Diagnostics.AddAttributeError(path.Root("content"), "MX preference belongs in priority",
					"Set only the mail server in content and the preference in priority, for example content = \"mail.example.com\" and priority = 10.")
			}
		}
	}
}

func (r *dnsRecordResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	if req.ProviderData == nil {
		return
	}
	c, ok := req.ProviderData.(*client.Client)
	if !ok {
		resp.Diagnostics.AddError("Unexpected provider data", fmt.Sprintf("Expected *client.Client, got %T.", req.ProviderData))
		return
	}
	r.client = c
}

func (r *dnsRecordResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan dnsRecordModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}

	zone, diags := r.zone(ctx, plan.ZoneID.ValueString(), plan.Name.ValueString())
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	id, err := r.client.CreateRecord(ctx, zone.ID, toInput(plan))
	if err != nil {
		resp.Diagnostics.AddError("Cannot create ConnByte DNS record", err.Error())
		return
	}
	plan.ID = types.StringValue(strconv.FormatInt(id, 10))
	// Save ID and zone first so a failed read-back leaves a tainted resource that Terraform can
	// still delete, instead of an untracked record that the next apply creates a second time.
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), plan.ID)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("zone_id"), plan.ZoneID)...)

	rec, err := r.client.GetRecord(ctx, zone.ID, id)
	if err != nil {
		resp.Diagnostics.AddError("Cannot read ConnByte DNS record after create", err.Error())
		return
	}
	applyRecord(&plan, rec, zone.Zone)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *dnsRecordResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state dnsRecordModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	recordID, diags := stateIDs(state)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	zone, err := r.client.GetZone(ctx, state.ZoneID.ValueString())
	if err != nil {
		if client.IsNotFound(err) {
			tflog.Info(ctx, "ConnByte DNS zone no longer exists, removing record from state", map[string]any{"zone_id": state.ZoneID.ValueString(), "id": state.ID.ValueString()})
			resp.State.RemoveResource(ctx)
			return
		}
		resp.Diagnostics.AddError("Cannot read ConnByte DNS zone", err.Error())
		return
	}

	rec, err := r.client.GetRecord(ctx, zone.ID, recordID)
	if err != nil {
		if client.IsNotFound(err) {
			tflog.Info(ctx, "ConnByte DNS record no longer exists, removing it from state", map[string]any{"zone_id": zone.ID, "id": state.ID.ValueString()})
			resp.State.RemoveResource(ctx)
			return
		}
		resp.Diagnostics.AddError("Cannot read ConnByte DNS record", err.Error())
		return
	}

	applyRecord(&state, rec, zone.Zone)
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *dnsRecordResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state dnsRecordModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	recordID, diags := stateIDs(state)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	zone, diags := r.zone(ctx, plan.ZoneID.ValueString(), plan.Name.ValueString())
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	if err := r.client.UpdateRecord(ctx, zone.ID, recordID, toInput(plan)); err != nil {
		resp.Diagnostics.AddError("Cannot update ConnByte DNS record", err.Error())
		return
	}

	rec, err := r.client.GetRecord(ctx, zone.ID, recordID)
	if err != nil {
		resp.Diagnostics.AddError("Cannot read ConnByte DNS record after update", err.Error())
		return
	}
	plan.ID = state.ID
	applyRecord(&plan, rec, zone.Zone)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *dnsRecordResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state dnsRecordModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	recordID, diags := stateIDs(state)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	err := r.client.DeleteRecord(ctx, state.ZoneID.ValueString(), recordID)
	if client.IsNotFound(err) {
		// The whole zone is gone, so the record is gone too.
		return
	}
	if err != nil {
		resp.Diagnostics.AddError("Cannot delete ConnByte DNS record", err.Error())
	}
}

// stateIDs checks the IDs in the state. An empty zone_id is never sent to the API, because the
// API would answer "zone not found" and the record would look deleted although it still exists.
func stateIDs(m dnsRecordModel) (int64, diag.Diagnostics) {
	var diags diag.Diagnostics
	if m.ZoneID.IsNull() || m.ZoneID.IsUnknown() || m.ZoneID.ValueString() == "" {
		diags.AddAttributeError(path.Root("zone_id"), "Missing zone ID in state",
			fmt.Sprintf("The state of record %q has no zone_id. Remove it with \"terraform state rm\" and import it again with \"<zone_id>/<record_id>\".", m.ID.ValueString()))
		return 0, diags
	}
	recordID, err := strconv.ParseInt(m.ID.ValueString(), 10, 64)
	if err != nil {
		diags.AddAttributeError(path.Root("id"), "Invalid record ID", fmt.Sprintf("%q is not a ConnByte record ID.", m.ID.ValueString()))
	}
	return recordID, diags
}

// ImportState accepts "<zone_id>/<record_id>".
func (r *dnsRecordResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	zoneID, recordID, ok := strings.Cut(req.ID, "/")
	if !ok || zoneID == "" || recordID == "" {
		resp.Diagnostics.AddError("Invalid import ID", fmt.Sprintf("Expected \"<zone_id>/<record_id>\", got %q.", req.ID))
		return
	}
	if _, err := strconv.ParseInt(recordID, 10, 64); err != nil {
		resp.Diagnostics.AddError("Invalid import ID", fmt.Sprintf("Record ID %q is not a number.", recordID))
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("zone_id"), zoneID)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), recordID)...)
}

// zone loads the zone and rejects names that already contain the zone name.
func (r *dnsRecordResource) zone(ctx context.Context, zoneID, name string) (*client.Zone, diag.Diagnostics) {
	var diags diag.Diagnostics
	zone, err := r.client.GetZone(ctx, zoneID)
	if err != nil {
		diags.AddAttributeError(path.Root("zone_id"), "Cannot read ConnByte DNS zone", err.Error())
		return nil, diags
	}
	z := client.NormalizeName(zone.Zone)
	n := client.NormalizeName(name)
	if n == z || strings.HasSuffix(n, "."+z) {
		rel := client.RelativeName(n, z)
		diags.AddAttributeError(path.Root("name"), "Record name must be relative to the zone",
			fmt.Sprintf("%q contains the zone name %q. Use %q instead.", name, z, rel))
		return nil, diags
	}
	return zone, diags
}

func toInput(m dnsRecordModel) client.RecordInput {
	name := m.Name.ValueString()
	if name == "@" {
		name = ""
	}
	return client.RecordInput{
		Name:     name,
		Type:     m.Type.ValueString(),
		Content:  m.Content.ValueString(),
		TTL:      m.TTL.ValueInt64(),
		Priority: m.Priority.ValueInt64(),
		Disabled: m.Disabled.ValueBool(),
	}
}

// applyRecord writes the API record into the model. Values that only differ in notation keep the
// configured form so Terraform does not show a diff after every refresh.
func applyRecord(m *dnsRecordModel, rec *client.Record, zoneName string) {
	content := rec.Content
	if rec.Type == "TXT" {
		content = client.UnquoteTXT(content)
	}
	if !m.Content.IsNull() && !m.Content.IsUnknown() && m.Type.ValueString() == rec.Type && sameContent(rec.Type, m.Content.ValueString(), content) {
		content = m.Content.ValueString()
	}

	m.ZoneID = types.StringValue(m.ZoneID.ValueString())
	m.Name = types.StringValue(client.RelativeName(rec.Name, zoneName))
	m.Type = types.StringValue(rec.Type)
	m.Content = types.StringValue(content)
	m.TTL = types.Int64Value(rec.TTL)
	m.Priority = types.Int64Value(rec.Priority)
	m.Disabled = types.BoolValue(rec.Disabled != 0)
	m.FQDN = types.StringValue(client.NormalizeName(rec.Name))
}

func sameContent(recordType, a, b string) bool {
	if a == b {
		return true
	}
	switch recordType {
	case "A", "AAAA":
		ipA, ipB := net.ParseIP(a), net.ParseIP(b)
		return ipA != nil && ipB != nil && ipA.Equal(ipB)
	case "CNAME", "MX", "NS":
		return client.NormalizeName(a) == client.NormalizeName(b)
	case "SRV":
		fa, fb := strings.Fields(a), strings.Fields(b)
		if len(fa) != len(fb) || len(fa) == 0 {
			return false
		}
		for i := range fa[:len(fa)-1] {
			if fa[i] != fb[i] {
				return false
			}
		}
		return client.NormalizeName(fa[len(fa)-1]) == client.NormalizeName(fb[len(fb)-1])
	}
	return false
}
