package provider

import (
	"context"
	"github.com/elephant-evergreen/terraform-provider-synology/internal/dsm"
	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

type packagesDataSource struct{ client *dsm.Client }

func NewPackagesDataSource() datasource.DataSource { return &packagesDataSource{} }

var installedPackageType = types.ObjectType{AttrTypes: map[string]attr.Type{
	"id": types.StringType, "name": types.StringType, "version": types.StringType, "status": types.StringType, "volume": types.StringType, "apps": types.StringType,
	"startable": types.BoolType, "can_uninstall": types.BoolType, "custom_uninstall": types.BoolType, "automatic_updates": types.BoolType, "important_updates": types.BoolType, "silent_upgrade": types.BoolType, "beta": types.BoolType,
}}

func (*packagesDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_packages"
}
func (*packagesDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	fields := map[string]schema.Attribute{}
	for name, kind := range installedPackageType.AttrTypes {
		if kind == types.BoolType {
			fields[name] = schema.BoolAttribute{Computed: true, MarkdownDescription: "Observed package " + name + " flag."}
		} else {
			fields[name] = schema.StringAttribute{Computed: true, MarkdownDescription: "Observed package " + name + " metadata."}
		}
	}
	resp.Schema = schema.Schema{MarkdownDescription: "Complete read-only installed-package inventory with exact versions, status, placement and update/lifecycle capability flags. It never refreshes the online catalogue, downloads SPKs, performs feasibility checks, resolves operation queues or starts/stops software. Failed and incomplete reads remain errors rather than empty inventories.", Attributes: map[string]schema.Attribute{
		"id":       schema.StringAttribute{Computed: true, MarkdownDescription: "Inventory identity: packages."},
		"packages": schema.MapNestedAttribute{Computed: true, MarkdownDescription: "Installed packages keyed by stable DSM package ID. Use these keys for import.", NestedObject: schema.NestedAttributeObject{Attributes: fields}},
	}}
}
func (r *packagesDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
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
func (r *packagesDataSource) Read(ctx context.Context, _ datasource.ReadRequest, resp *datasource.ReadResponse) {
	records, err := readInstalledPackages(ctx, r.client)
	if err != nil {
		resp.Diagnostics.AddError("Cannot read installed packages", err.Error())
		return
	}
	value, d := types.MapValueFrom(ctx, installedPackageType, records)
	resp.Diagnostics.Append(d...)
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &struct {
		ID       types.String `tfsdk:"id"`
		Packages types.Map    `tfsdk:"packages"`
	}{types.StringValue("packages"), value})...)
}
