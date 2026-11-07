package provider

import (
	"context"
	"errors"
	"strings"

	"github.com/elephant-evergreen/terraform-provider-synology/internal/dsm"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/booldefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringdefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

var _ resource.ResourceWithImportState = (*sharedFolderResource)(nil)
var _ resource.ResourceWithConfigure = (*sharedFolderResource)(nil)
var _ resource.ResourceWithValidateConfig = (*sharedFolderResource)(nil)

type sharedFolderResource struct{ client *dsm.Client }
type shareModel struct {
	Name                types.String `tfsdk:"name"`
	Volume              types.String `tfsdk:"volume"`
	Description         types.String `tfsdk:"description"`
	UUID                types.String `tfsdk:"uuid"`
	Hidden              types.Bool   `tfsdk:"hidden"`
	RecycleBin          types.Bool   `tfsdk:"recycle_bin"`
	RecycleBinAdminOnly types.Bool   `tfsdk:"recycle_bin_admin_only"`
	DeletionProtection  types.Bool   `tfsdk:"deletion_protection"`
}

func NewSharedFolderResource() resource.Resource { return &sharedFolderResource{} }
func (*sharedFolderResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_shared_folder"
}
func (*sharedFolderResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{MarkdownDescription: "Own shared-folder metadata while preserving file contents and unmanaged permissions. Import by name. Renaming or moving a shared folder is deliberately unsupported; these changes must never silently replace stored data.", Attributes: map[string]schema.Attribute{
		"name":                   schema.StringAttribute{Required: true, MarkdownDescription: "DSM shared-folder name and import identifier. Changing it requires replacement; protection rejects deletion.", PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()}},
		"volume":                 schema.StringAttribute{Required: true, MarkdownDescription: "Existing volume path, for example /volume1. The provider does not create or reformat storage.", PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()}},
		"description":            schema.StringAttribute{Optional: true, Computed: true, Default: stringdefault.StaticString(""), MarkdownDescription: "Shared-folder description. An empty string clears it."},
		"uuid":                   schema.StringAttribute{Computed: true, MarkdownDescription: "DSM shared-folder UUID, when exposed by the device.", PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()}},
		"hidden":                 schema.BoolAttribute{Optional: true, Computed: true, Default: booldefault.StaticBool(false), MarkdownDescription: "Hide the share from network browsing."},
		"recycle_bin":            schema.BoolAttribute{Optional: true, Computed: true, Default: booldefault.StaticBool(true), MarkdownDescription: "Enable the shared-folder recycle bin. Changing this does not empty the recycle bin."},
		"recycle_bin_admin_only": schema.BoolAttribute{Optional: true, Computed: true, Default: booldefault.StaticBool(true), MarkdownDescription: "Restrict recycle-bin access to administrators."},
		"deletion_protection":    schema.BoolAttribute{Optional: true, Computed: true, Default: booldefault.StaticBool(true), MarkdownDescription: "Reject deletion using the last applied protection value, even if the resource is removed from configuration. Disable in a separate apply before deletion. Prefer a removed block with destroy=false to stop managing data."},
	}}
}
func (r *sharedFolderResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
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
func (*sharedFolderResource) ValidateConfig(ctx context.Context, req resource.ValidateConfigRequest, resp *resource.ValidateConfigResponse) {
	var m shareModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if !m.Name.IsUnknown() && !m.Name.IsNull() && (m.Name.ValueString() == "" || strings.ContainsAny(m.Name.ValueString(), "/\\\x00")) {
		resp.Diagnostics.AddAttributeError(path.Root("name"), "Invalid shared-folder name", "Use a nonempty DSM name without path separators or NUL.")
	}
	if !m.Volume.IsUnknown() && !m.Volume.IsNull() && (!strings.HasPrefix(m.Volume.ValueString(), "/volume") || strings.Contains(m.Volume.ValueString()[1:], "/")) {
		resp.Diagnostics.AddAttributeError(path.Root("volume"), "Invalid volume path", "Use an existing DSM volume path such as /volume1.")
	}
}
func (m *shareModel) object() dsm.Share {
	return dsm.Share{Name: m.Name.ValueString(), Volume: m.Volume.ValueString(), Description: m.Description.ValueString(), Hidden: m.Hidden.ValueBool(), RecycleBin: m.RecycleBin.ValueBool(), RecycleBinAdminOnly: m.RecycleBinAdminOnly.ValueBool()}
}
func (m *shareModel) read(s dsm.Share) {
	m.Name = types.StringValue(s.Name)
	m.Volume = types.StringValue(s.Volume)
	m.Description = types.StringValue(s.Description)
	m.UUID = types.StringValue(s.UUID)
	m.Hidden = types.BoolValue(s.Hidden)
	m.RecycleBin = types.BoolValue(s.RecycleBin)
	m.RecycleBinAdminOnly = types.BoolValue(s.RecycleBinAdminOnly)
	if m.DeletionProtection.IsNull() || m.DeletionProtection.IsUnknown() {
		m.DeletionProtection = types.BoolValue(true)
	}
}
func (r *sharedFolderResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var m shareModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	_, err := r.client.Share(ctx, m.Name.ValueString())
	if err == nil {
		resp.Diagnostics.AddError("Shared folder already exists", "Import the existing folder by name. Creating a resource never adopts or changes an existing folder.")
		return
	}
	if !errors.Is(err, dsm.ErrNotFound) {
		resp.Diagnostics.AddError("Cannot check shared-folder identity", err.Error())
		return
	}
	err = r.client.WriteShare(ctx, "create", m.object())
	if err != nil {
		if !rejectedCreation(err) {
			m.UUID = types.StringNull()
			resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
			resp.Diagnostics.Append(resp.Private.SetKey(ctx, "creation_outcome", []byte(`"uncertain"`))...)
		}
		resp.Diagnostics.AddError("Shared-folder creation was not acknowledged", err.Error())
		return
	}
	m.UUID = types.StringNull()
	resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
	resp.Diagnostics.Append(resp.Private.SetKey(ctx, "creation_outcome", []byte(`"unbound"`))...)
	actual, readErr := r.client.Share(ctx, m.Name.ValueString())
	if readErr == nil {
		m.read(actual)
		resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
		resp.Diagnostics.Append(resp.Private.SetKey(ctx, "creation_outcome", nil)...)
	}
	if readErr != nil {
		m.UUID = types.StringNull()
		resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
		resp.Diagnostics.AddError("Shared folder created but readback failed", readErr.Error())
	}
}
func (r *sharedFolderResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	resp.Diagnostics.Append(guardUncertainCreation(ctx, req.Private)...)
	if resp.Diagnostics.HasError() {
		return
	}
	var m shareModel
	resp.Diagnostics.Append(req.State.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	s, err := r.client.Share(ctx, m.Name.ValueString())
	if errors.Is(err, dsm.ErrNotFound) {
		if m.DeletionProtection.IsNull() || m.DeletionProtection.ValueBool() {
			resp.Diagnostics.AddError("Protected shared folder is missing", "DSM confirmed that the folder is absent. Inspect storage and restore access before reconciling state; the provider will not silently replace protected data with an empty folder.")
			return
		}
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		resp.Diagnostics.AddError("Cannot read shared folder", err.Error())
		return
	}
	if !m.UUID.IsNull() && m.UUID.ValueString() != "" && s.UUID != "" && m.UUID.ValueString() != s.UUID {
		resp.Diagnostics.AddError("Shared-folder identity changed", "A different shared folder now has this name. Inspect DSM and explicitly reconcile state before making changes.")
		return
	}
	m.read(s)
	resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
}
func (r *sharedFolderResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	resp.Diagnostics.Append(guardUncertainCreation(ctx, req.Private)...)
	if resp.Diagnostics.HasError() {
		return
	}
	var plan, state shareModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	current, err := r.client.Share(ctx, state.Name.ValueString())
	if err != nil {
		resp.Diagnostics.AddError("Cannot verify shared-folder identity", err.Error())
		return
	}
	if state.UUID.IsNull() || state.UUID.ValueString() == "" || current.UUID != state.UUID.ValueString() {
		resp.Diagnostics.AddError("Shared-folder identity changed", "Refusing to update an unverified or replacement folder. Refresh and explicitly reconcile its identity.")
		return
	}
	if plan.object() != state.object() {
		if err := r.client.WriteShare(ctx, "set", plan.object()); err != nil {
			resp.Diagnostics.AddError("Cannot update shared folder", err.Error())
			return
		}
	}
	s, err := r.client.Share(ctx, plan.Name.ValueString())
	if err != nil {
		resp.Diagnostics.AddError("Cannot verify shared-folder update", err.Error())
		return
	}
	if s.UUID != state.UUID.ValueString() {
		resp.Diagnostics.AddError("Shared-folder identity changed during update", "The provider will not adopt the replacement folder. Inspect DSM before continuing.")
		return
	}
	plan.read(s)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}
func (r *sharedFolderResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	resp.Diagnostics.Append(guardUncertainCreation(ctx, req.Private)...)
	if resp.Diagnostics.HasError() {
		return
	}
	var m shareModel
	resp.Diagnostics.Append(req.State.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if m.DeletionProtection.IsNull() || m.DeletionProtection.ValueBool() {
		resp.Diagnostics.AddError("Shared-folder deletion is protected", "Deleting a shared folder destroys its files. To stop managing it, use a removed block with destroy=false. Intentional deletion requires disabling deletion_protection in a separate apply.")
		return
	}
	current, err := r.client.Share(ctx, m.Name.ValueString())
	if errors.Is(err, dsm.ErrNotFound) {
		return
	}
	if err != nil {
		resp.Diagnostics.AddError("Cannot verify shared-folder identity", err.Error())
		return
	}
	if m.UUID.IsNull() || m.UUID.ValueString() == "" || current.UUID != m.UUID.ValueString() {
		resp.Diagnostics.AddError("Shared-folder identity changed", "Refusing to delete an unverified or replacement folder.")
		return
	}
	if err := r.client.DeleteShare(ctx, m.Name.ValueString()); err != nil {
		resp.Diagnostics.AddError("Cannot delete shared folder", err.Error())
		return
	}
	if _, err := r.client.Share(ctx, m.Name.ValueString()); !errors.Is(err, dsm.ErrNotFound) {
		resp.Diagnostics.AddError("Shared-folder deletion is unconfirmed", "DSM has not confirmed that the folder is absent. Refresh before retrying.")
	}
}
func (*sharedFolderResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	resource.ImportStatePassthroughID(ctx, path.Root("name"), req, resp)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("deletion_protection"), true)...)
}
