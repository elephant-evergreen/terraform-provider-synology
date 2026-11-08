package provider

import (
	"context"
	"github.com/elephant-evergreen/terraform-provider-synology/internal/dsm"
	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

type ddnsDataSource struct{ client *dsm.Client }

func NewDDNSDataSource() datasource.DataSource { return &ddnsDataSource{} }

var ddnsRecordType = types.ObjectType{AttrTypes: map[string]attr.Type{
	"id": types.StringType, "provider_id": types.StringType, "enabled": types.BoolType, "hostname": types.StringType, "username": types.StringType, "network_mode": types.StringType, "ipv4_address": types.StringType, "ipv6_address": types.StringType, "ipv4_interface": types.StringType, "ipv6_interface": types.StringType, "heartbeat": types.BoolType, "status": types.StringType,
}}

func (*ddnsDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_ddns"
}
func (*ddnsDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	fields := map[string]schema.Attribute{}
	for name, kind := range ddnsRecordType.AttrTypes {
		if kind == types.BoolType {
			fields[name] = schema.BoolAttribute{Computed: true, MarkdownDescription: "Observed DDNS " + name + " flag."}
		} else {
			fields[name] = schema.StringAttribute{Computed: true, Sensitive: name == "username", MarkdownDescription: "Observed DDNS " + name + " setting."}
		}
	}
	resp.Schema = schema.Schema{MarkdownDescription: "Complete read-only inventory of configured DSM DDNS records. Empty records establish no configured DDNS object; authentication, unsupported, transport and malformed responses remain errors. Passwords are discarded, not redacted values in state. This never tests registration, refreshes external DNS or creates certificates.", Attributes: map[string]schema.Attribute{
		"id":        schema.StringAttribute{Computed: true, MarkdownDescription: "Inventory identity: ddns."},
		"providers": schema.SetAttribute{Computed: true, ElementType: types.StringType, MarkdownDescription: "Available DSM DDNS provider IDs. Custom provider URLs and embedded credentials are not exposed."},
		"records":   schema.MapNestedAttribute{Computed: true, MarkdownDescription: "Configured records keyed by stable DSM record ID; use these keys for resource import.", NestedObject: schema.NestedAttributeObject{Attributes: fields}},
	}}
}
func (r *ddnsDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	if req.ProviderData == nil {
		return
	}
	client, ok := req.ProviderData.(*dsm.Client)
	if !ok {
		resp.Diagnostics.AddError("Unexpected provider client", "Expected a DSM client.")
		return
	}
	r.client = client
}
func (r *ddnsDataSource) Read(ctx context.Context, _ datasource.ReadRequest, resp *datasource.ReadResponse) {
	records, err := readDDNSRecords(ctx, r.client)
	if err != nil {
		resp.Diagnostics.AddError("Cannot read DDNS records", err.Error())
		return
	}
	providers, err := readDDNSProviders(ctx, r.client)
	if err != nil {
		resp.Diagnostics.AddError("Cannot read DDNS provider catalogue", err.Error())
		return
	}
	providerIDs, d := types.SetValueFrom(ctx, types.StringType, providers)
	resp.Diagnostics.Append(d...)
	if resp.Diagnostics.HasError() {
		return
	}
	value, d := types.MapValueFrom(ctx, ddnsRecordType, records)
	resp.Diagnostics.Append(d...)
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &struct {
		ID        types.String `tfsdk:"id"`
		Records   types.Map    `tfsdk:"records"`
		Providers types.Set    `tfsdk:"providers"`
	}{types.StringValue("ddns"), value, providerIDs})...)
}
