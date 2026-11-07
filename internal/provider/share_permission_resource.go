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
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

type sharePermissionResource struct{ client *dsm.Client }
type permissionModel struct {
	Share              types.String `tfsdk:"share"`
	PrincipalType      types.String `tfsdk:"principal_type"`
	Principal          types.String `tfsdk:"principal"`
	Access             types.String `tfsdk:"access"`
	DeletionProtection types.Bool   `tfsdk:"deletion_protection"`
}

func NewSharePermissionResource() resource.Resource { return &sharePermissionResource{} }
func (*sharePermissionResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_share_permission"
}
func (*sharePermissionResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{MarkdownDescription: "One principal's explicit shared-folder permission. Inherit (no explicit grant or deny) is distinct from deny. Other principals and descendant file ACLs remain outside this resource's ownership. Import using share:local_group:group or share:local_user:user.", Attributes: map[string]schema.Attribute{
		"share":               schema.StringAttribute{Required: true, MarkdownDescription: "Shared-folder name. Reference synology_shared_folder.name to establish ordering.", PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()}},
		"principal_type":      schema.StringAttribute{Required: true, MarkdownDescription: "local_user or local_group.", PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()}},
		"principal":           schema.StringAttribute{Required: true, MarkdownDescription: "Local account or group name.", PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()}},
		"access":              schema.StringAttribute{Required: true, MarkdownDescription: "Explicit permission: inherit, deny, read_only, or read_write. Inherit does not imply the principal can access the folder; effective access also depends on groups and ACLs."},
		"deletion_protection": schema.BoolAttribute{Optional: true, Computed: true, Default: booldefault.StaticBool(true), MarkdownDescription: "Protect permission removal. Disable in a separate apply before revoking access, or stop management with a removed block and destroy=false."},
	}}
}
func (r *sharePermissionResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
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
func (r *sharePermissionResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var m permissionModel
	resp.Diagnostics.Append(req.State.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	p, err := r.client.SharePermission(ctx, m.Share.ValueString(), m.PrincipalType.ValueString(), m.Principal.ValueString())
	if errors.Is(err, dsm.ErrNotFound) {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		resp.Diagnostics.AddError("Cannot read share permission", err.Error())
		return
	}
	access, err := p.Access()
	if err != nil {
		resp.Diagnostics.AddError("Cannot interpret share permission", err.Error())
		return
	}
	m.Access = types.StringValue(access)
	if m.DeletionProtection.IsNull() {
		m.DeletionProtection = types.BoolValue(true)
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
}
func (r *sharePermissionResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var m permissionModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	p, err := r.client.SharePermission(ctx, m.Share.ValueString(), m.PrincipalType.ValueString(), m.Principal.ValueString())
	if err != nil {
		resp.Diagnostics.AddError("Cannot check share permission", err.Error())
		return
	}
	access, err := p.Access()
	if err != nil {
		resp.Diagnostics.AddError("Cannot interpret share permission", err.Error())
		return
	}
	if access != "inherit" {
		resp.Diagnostics.AddError("Explicit share permission already exists", "Import this permission before managing it. A create operation will not overwrite an existing grant or deny.")
		return
	}
	if err := r.client.SetSharePermission(ctx, m.Share.ValueString(), m.PrincipalType.ValueString(), m.Principal.ValueString(), m.Access.ValueString()); err != nil {
		resp.Diagnostics.AddError("Cannot create share permission", err.Error())
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
}
func (r *sharePermissionResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state permissionModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if !plan.Access.Equal(state.Access) {
		if err := r.client.SetSharePermission(ctx, plan.Share.ValueString(), plan.PrincipalType.ValueString(), plan.Principal.ValueString(), plan.Access.ValueString()); err != nil {
			resp.Diagnostics.AddError("Cannot update share permission", err.Error())
			return
		}
	}
	p, err := r.client.SharePermission(ctx, plan.Share.ValueString(), plan.PrincipalType.ValueString(), plan.Principal.ValueString())
	if err != nil {
		resp.Diagnostics.AddError("Cannot verify share permission", err.Error())
		return
	}
	access, err := p.Access()
	if err != nil {
		resp.Diagnostics.AddError("Cannot interpret share permission", err.Error())
		return
	}
	plan.Access = types.StringValue(access)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}
func (r *sharePermissionResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var m permissionModel
	resp.Diagnostics.Append(req.State.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if m.DeletionProtection.IsNull() || m.DeletionProtection.ValueBool() {
		resp.Diagnostics.AddError("Share permission removal is protected", "Disable deletion_protection in a separate apply before intentionally revoking access, or stop managing the permission with a removed block and destroy=false.")
		return
	}
	if err := r.client.SetSharePermission(ctx, m.Share.ValueString(), m.PrincipalType.ValueString(), m.Principal.ValueString(), "inherit"); err != nil {
		resp.Diagnostics.AddError("Cannot remove share permission", err.Error())
	}
}
func (*sharePermissionResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	parts := strings.SplitN(req.ID, ":", 3)
	if len(parts) != 3 || parts[0] == "" || parts[2] == "" || (parts[1] != "local_user" && parts[1] != "local_group") {
		resp.Diagnostics.AddError("Invalid permission import identifier", "Use share:local_group:group or share:local_user:user.")
		return
	}
	for i, name := range []string{"share", "principal_type", "principal"} {
		resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root(name), parts[i])...)
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("deletion_protection"), true)...)
}
