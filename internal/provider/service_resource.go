package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
	"time"

	"github.com/elephant-evergreen/terraform-provider-synology/internal/dsm"
	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

type serviceField struct {
	wire        string
	kind        attr.Type
	description string
}
type serviceDefinition struct {
	name        string
	api         string
	version     int
	description string
	fields      map[string]serviceField
}
type serviceResource struct {
	definition serviceDefinition
	client     *dsm.Client
}

func service(def serviceDefinition) func() resource.Resource {
	return func() resource.Resource { return &serviceResource{definition: def} }
}
func serviceResources() []func() resource.Resource {
	return []func() resource.Resource{
		service(serviceDefinition{name: "smb", api: "SYNO.Core.FileServ.SMB", version: 3, description: "SMB file-sharing configuration. Settings omitted from configuration retain their current DSM values. Changing a service may interrupt existing connections; import existing settings first.", fields: map[string]serviceField{
			"enabled":                        {"enable_samba", types.BoolType, "Enable SMB file sharing."},
			"workgroup":                      {"workgroup", types.StringType, "SMB workgroup name."},
			"minimum_protocol":               {"smb_min_protocol", types.Int64Type, "DSM minimum protocol: 0=SMB1, 1=SMB2, 2=SMB2 with Large MTU, 3=SMB3."},
			"maximum_protocol":               {"smb_max_protocol", types.Int64Type, "DSM maximum protocol: 0=SMB1, 1=SMB2, 2=SMB2 with Large MTU, 3=SMB3."},
			"transport_encryption":           {"smb_encrypt_transport", types.Int64Type, "DSM transport-encryption policy. Preserve the imported value unless the target DSM policy has been verified."},
			"server_signing":                 {"enable_server_signing", types.Int64Type, "DSM server-signing policy. Preserve the imported value unless the target DSM policy has been verified."},
			"oplocks":                        {"enable_op_lock", types.BoolType, "Enable opportunistic locks."},
			"smb2_leases":                    {"enable_smb2_leases", types.BoolType, "Enable SMB2 leases."},
			"durable_handles":                {"enable_durable_handles", types.BoolType, "Enable durable handles."},
			"local_master_browser":           {"enable_local_master_browser", types.BoolType, "Enable the local master browser."},
			"access_based_share_enumeration": {"enable_access_based_share_enum", types.BoolType, "Hide shares according to access permissions; distinct from hiding inaccessible files within a share."},
			"ntlmv1":                         {"enable_ntlmv1_auth", types.BoolType, "Enable legacy NTLMv1 authentication. Import preserves the current value."},
		}}),
		service(serviceDefinition{name: "afp", api: "SYNO.Core.FileServ.AFP", version: 2, description: "AFP file-sharing configuration. Destroy releases management without disabling the service.", fields: map[string]serviceField{
			"enabled": {"enable_afp", types.BoolType, "Enable AFP."}, "quick_disconnect": {"enable_disconnect_quick", types.BoolType, "Disconnect idle clients quickly."}, "apply_default_permissions": {"enable_umask", types.BoolType, "Apply DSM default UNIX permissions."},
		}}),
		service(serviceDefinition{name: "nfs", api: "SYNO.Core.FileServ.NFS", version: 3, description: "NFS service configuration. Export permissions are separate and are not implicitly changed by enabling the service.", fields: map[string]serviceField{
			"enabled": {"enable_nfs", types.BoolType, "Enable NFS."}, "nfs4_enabled": {"enable_nfs_v4", types.BoolType, "Enable NFS version 4."}, "nfs4_domain": {"nfs_v4_domain", types.StringType, "NFSv4 identity domain."}, "read_size": {"read_size", types.Int64Type, "DSM read packet size."}, "write_size": {"write_size", types.Int64Type, "DSM write packet size."},
		}}),
		service(serviceDefinition{name: "ftp", api: "SYNO.Core.FileServ.FTP", version: 3, description: "FTP and FTPS service configuration. Omitted settings retain existing values. Destroy does not disable either service.", fields: map[string]serviceField{
			"enabled": {"enable_ftp", types.BoolType, "Enable FTP."}, "tls_enabled": {"enable_ftps", types.BoolType, "Enable FTPS."}, "port": {"portnum", types.Int64Type, "FTP listening port."}, "idle_timeout": {"timeout", types.Int64Type, "Idle timeout in seconds."}, "maximum_connections_per_client": {"max_conn_per_ip", types.Int64Type, "Maximum connections per IP address; zero means DSM default/unlimited."}, "passive_port_range_enabled": {"custom_port_range", types.BoolType, "Use a custom passive port range."}, "passive_port_range": {"custom_port", types.StringType, "Passive port range in DSM start:end syntax."},
		}}),
		service(serviceDefinition{name: "sftp", api: "SYNO.Core.FileServ.FTP.SFTP", version: 1, description: "SFTP file-sharing configuration. SSH terminal access remains a separate setting.", fields: map[string]serviceField{
			"enabled": {"enable", types.BoolType, "Enable SFTP."}, "port": {"portnum", types.Int64Type, "SFTP listening port."},
		}}),
		service(serviceDefinition{name: "time", api: "SYNO.Core.Region.NTP", version: 2, description: "DSM NTP and time-zone configuration. This resource never sets the wall clock directly. Synchronization success depends on the NAS network policy.", fields: map[string]serviceField{
			"synchronization": {"enable_ntp", types.StringType, "DSM synchronization mode; ntp enables NTP."}, "ntp_server": {"server", types.StringType, "NTP server name."}, "timezone": {"timezone", types.StringType, "DSM time-zone identifier, such as Seoul."}, "date_format": {"date_format", types.StringType, "DSM date display format."}, "time_format": {"time_format", types.StringType, "DSM time display format."},
		}}),
	}
}

func (r *serviceResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_" + r.definition.name
}
func (r *serviceResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	attributes := map[string]schema.Attribute{"id": schema.StringAttribute{Computed: true, MarkdownDescription: "Singleton identity and import identifier: " + r.definition.name + ". Destroy only releases Terraform management; it never resets DSM settings."}}
	for name, field := range r.definition.fields {
		switch field.kind {
		case types.BoolType:
			attributes[name] = schema.BoolAttribute{Optional: true, Computed: true, MarkdownDescription: field.description}
		case types.Int64Type:
			attributes[name] = schema.Int64Attribute{Optional: true, Computed: true, MarkdownDescription: field.description}
		case types.StringType:
			attributes[name] = schema.StringAttribute{Optional: true, Computed: true, MarkdownDescription: field.description}
		}
	}
	resp.Schema = schema.Schema{MarkdownDescription: r.definition.description, Attributes: attributes}
}
func (r *serviceResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	if req.ProviderData == nil {
		return
	}
	c, ok := req.ProviderData.(*dsm.Client)
	if !ok {
		resp.Diagnostics.AddError("Unexpected provider client", "Expected a DSM client.")
		return
	}
	r.client = c
}
func (r *serviceResource) read(ctx context.Context) (types.Object, error) {
	var raw map[string]json.RawMessage
	if err := r.client.Call(ctx, r.definition.api, r.definition.version, "get", nil, &raw); err != nil {
		return types.Object{}, err
	}
	values := map[string]attr.Value{"id": types.StringValue(r.definition.name)}
	kinds := map[string]attr.Type{"id": types.StringType}
	for name, field := range r.definition.fields {
		value, ok := raw[field.wire]
		if !ok || string(value) == "null" {
			return types.Object{}, fmt.Errorf("DSM %s response is missing the managed %s setting", r.definition.name, name)
		}
		kinds[name] = field.kind
		switch field.kind {
		case types.BoolType:
			var v bool
			if json.Unmarshal(value, &v) != nil {
				return types.Object{}, fmt.Errorf("DSM %s setting has the wrong type", name)
			}
			values[name] = types.BoolValue(v)
		case types.Int64Type:
			var v int64
			if json.Unmarshal(value, &v) != nil {
				return types.Object{}, fmt.Errorf("DSM %s setting has the wrong type", name)
			}
			values[name] = types.Int64Value(v)
		case types.StringType:
			var v string
			if json.Unmarshal(value, &v) != nil {
				return types.Object{}, fmt.Errorf("DSM %s setting has the wrong type", name)
			}
			values[name] = types.StringValue(v)
		}
	}
	return types.ObjectValueMust(kinds, values), nil
}
func (r *serviceResource) desired(ctx context.Context, config tfsdk.Config) (map[string]attr.Value, url.Values, error) {
	values := map[string]attr.Value{}
	args := url.Values{}
	for name, field := range r.definition.fields {
		switch field.kind {
		case types.BoolType:
			var v types.Bool
			if diags := config.GetAttribute(ctx, path.Root(name), &v); diags.HasError() {
				return nil, nil, fmt.Errorf("cannot decode %s", name)
			}
			if v.IsUnknown() {
				return nil, nil, fmt.Errorf("%s must be known before applying", name)
			}
			if !v.IsNull() {
				values[name] = v
				args.Set(field.wire, strconv.FormatBool(v.ValueBool()))
			}
		case types.Int64Type:
			var v types.Int64
			if diags := config.GetAttribute(ctx, path.Root(name), &v); diags.HasError() {
				return nil, nil, fmt.Errorf("cannot decode %s", name)
			}
			if v.IsUnknown() {
				return nil, nil, fmt.Errorf("%s must be known before applying", name)
			}
			if !v.IsNull() {
				values[name] = v
				args.Set(field.wire, strconv.FormatInt(v.ValueInt64(), 10))
			}
		case types.StringType:
			var v types.String
			if diags := config.GetAttribute(ctx, path.Root(name), &v); diags.HasError() {
				return nil, nil, fmt.Errorf("cannot decode %s", name)
			}
			if v.IsUnknown() {
				return nil, nil, fmt.Errorf("%s must be known before applying", name)
			}
			if !v.IsNull() {
				values[name] = v
				args.Set(field.wire, v.ValueString())
			}
		}
	}
	return values, args, nil
}
func serviceMatches(actual types.Object, desired map[string]attr.Value) bool {
	for name, value := range desired {
		if !actual.Attributes()[name].Equal(value) {
			return false
		}
	}
	return true
}
func (r *serviceResource) apply(ctx context.Context, config tfsdk.Config) (types.Object, error) {
	desired, args, err := r.desired(ctx, config)
	if err != nil {
		return types.Object{}, err
	}
	actual, err := r.read(ctx)
	if err != nil {
		return types.Object{}, err
	}
	if serviceMatches(actual, desired) {
		return actual, nil
	}
	if err := r.client.Mutate(ctx, r.definition.api, r.definition.version, "set", args); err != nil {
		return types.Object{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	for {
		actual, err = r.read(ctx)
		if err != nil {
			return types.Object{}, err
		}
		if serviceMatches(actual, desired) {
			return actual, nil
		}
		select {
		case <-ctx.Done():
			return types.Object{}, fmt.Errorf("DSM %s update is unconfirmed; refresh before retrying", r.definition.name)
		case <-time.After(250 * time.Millisecond):
		}
	}
}
func (r *serviceResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	actual, err := r.apply(ctx, req.Config)
	if err != nil {
		resp.Diagnostics.AddError("Cannot configure DSM service", err.Error())
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, actual)...)
}
func (r *serviceResource) Read(ctx context.Context, _ resource.ReadRequest, resp *resource.ReadResponse) {
	actual, err := r.read(ctx)
	if err != nil {
		resp.Diagnostics.AddError("Cannot read DSM service", err.Error())
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, actual)...)
}
func (r *serviceResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	actual, err := r.apply(ctx, req.Config)
	if err != nil {
		resp.Diagnostics.AddError("Cannot update DSM service", err.Error())
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, actual)...)
}
func (*serviceResource) Delete(context.Context, resource.DeleteRequest, *resource.DeleteResponse) {}
func (r *serviceResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	if req.ID != r.definition.name {
		resp.Diagnostics.AddError("Invalid service import identifier", "Use "+r.definition.name+" to import this DSM singleton.")
		return
	}
	resource.ImportStatePassthroughID(ctx, path.Root("id"), req, resp)
}
