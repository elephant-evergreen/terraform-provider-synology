package provider

import (
	"context"
	"errors"
	"github.com/elephant-evergreen/terraform-provider-synology/internal/dsm"
	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

type networkInterfacesDataSource struct{ client *dsm.Client }

func NewNetworkInterfacesDataSource() datasource.DataSource { return &networkInterfacesDataSource{} }
func interfaceObjectType() types.ObjectType {
	kinds := map[string]attr.Type{"id": types.StringType}
	for name, field := range interfaceDefinition().fields {
		kinds[name] = field.kind
	}
	return types.ObjectType{AttrTypes: kinds}
}
func (*networkInterfacesDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_network_interfaces"
}
func (*networkInterfacesDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	fields := map[string]schema.Attribute{"id": schema.StringAttribute{Computed: true, MarkdownDescription: "Physical interface identity."}}
	for name, field := range interfaceDefinition().fields {
		switch field.kind {
		case types.BoolType:
			fields[name] = schema.BoolAttribute{Computed: true, MarkdownDescription: field.description}
		case types.Int64Type:
			fields[name] = schema.Int64Attribute{Computed: true, MarkdownDescription: field.description}
		case types.StringType:
			fields[name] = schema.StringAttribute{Computed: true, MarkdownDescription: field.description}
		default:
			fields[name] = schema.SetAttribute{Computed: true, ElementType: types.StringType, MarkdownDescription: field.description}
		}
	}
	resp.Schema = schema.Schema{MarkdownDescription: "Read-only physical ethN Ethernet inventory with MAC bindings and native IPv4/IPv6 policy. It does not create, activate, disable or probe a new route, and does not own DHCP leases, bond or virtual-interface configuration.", Attributes: map[string]schema.Attribute{
		"id":         schema.StringAttribute{Computed: true, MarkdownDescription: "Inventory identity: network_interfaces."},
		"interfaces": schema.MapNestedAttribute{Computed: true, MarkdownDescription: "Physical Ethernet observations keyed by ethN interface name.", NestedObject: schema.NestedAttributeObject{Attributes: fields}},
	}}
}
func (r *networkInterfacesDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
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
func readPhysicalInterfaces(ctx context.Context, client *dsm.Client) (map[string]attr.Value, error) {
	var listed []struct {
		Name *string `json:"ifname"`
	}
	if err := client.Call(ctx, ethernetAPI, 2, "list", nil, &listed); err != nil {
		return nil, err
	}
	result := map[string]attr.Value{}
	resource := NewNetworkInterfaceResource().(*networkInterfaceResource)
	resource.client = client
	for _, device := range listed {
		if device.Name == nil {
			return nil, errors.New("DSM omitted an Ethernet inventory identity")
		}
		if !physicalInterfacePattern.MatchString(*device.Name) {
			continue
		}
		if _, exists := result[*device.Name]; exists {
			return nil, errors.New("DSM returned duplicate physical interface identities")
		}
		actual, err := resource.readInterface(ctx, *device.Name)
		if err != nil {
			return nil, err
		}
		result[*device.Name] = actual
	}
	return result, nil
}
func (r *networkInterfacesDataSource) Read(ctx context.Context, _ datasource.ReadRequest, resp *datasource.ReadResponse) {
	interfaces, err := readPhysicalInterfaces(ctx, r.client)
	if err != nil {
		resp.Diagnostics.AddError("Cannot read physical network interfaces", err.Error())
		return
	}
	value, d := types.MapValue(interfaceObjectType(), interfaces)
	resp.Diagnostics.Append(d...)
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &struct {
		ID         types.String `tfsdk:"id"`
		Interfaces types.Map    `tfsdk:"interfaces"`
	}{types.StringValue("network_interfaces"), value})...)
}
