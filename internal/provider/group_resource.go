package provider

import (
	"context"
	"errors"

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

type groupResource struct{ client *dsm.Client }
type groupModel struct {
	Name               types.String `tfsdk:"name"`
	Description        types.String `tfsdk:"description"`
	GID                types.Int64  `tfsdk:"gid"`
	DeletionProtection types.Bool   `tfsdk:"deletion_protection"`
}

func NewGroupResource() resource.Resource { return &groupResource{} }
func (*groupResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_group"
}
func (*groupResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{MarkdownDescription: "A local DSM group. Import by group name. Built-in groups can be imported but never deleted.", Attributes: map[string]schema.Attribute{
		"name":                schema.StringAttribute{Required: true, MarkdownDescription: "Local group name and import identifier.", PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()}},
		"description":         schema.StringAttribute{Optional: true, Computed: true, Default: stringdefault.StaticString(""), MarkdownDescription: "Group description. An empty value clears it."},
		"gid":                 schema.Int64Attribute{Computed: true, MarkdownDescription: "Numeric DSM group identifier."},
		"deletion_protection": schema.BoolAttribute{Optional: true, Computed: true, Default: booldefault.StaticBool(true), MarkdownDescription: "Reject deletion using the last applied value. Disable in a separate apply before intentionally deleting a custom group."},
	}}
}
func (r *groupResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
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
func (m *groupModel) read(g dsm.Group) {
	m.Name = types.StringValue(g.Name)
	m.Description = types.StringValue(g.Description)
	m.GID = types.Int64Value(g.GID)
	if m.DeletionProtection.IsNull() || m.DeletionProtection.IsUnknown() {
		m.DeletionProtection = types.BoolValue(true)
	}
}
func (r *groupResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var m groupModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	_, err := r.client.Group(ctx, m.Name.ValueString())
	if err == nil {
		resp.Diagnostics.AddError("Group already exists", "Import the existing group by name. It will not be adopted by a create operation.")
		return
	}
	if !errors.Is(err, dsm.ErrNotFound) {
		resp.Diagnostics.AddError("Cannot check group identity", err.Error())
		return
	}
	err = r.client.WriteGroup(ctx, "create", dsm.Group{Name: m.Name.ValueString(), Description: m.Description.ValueString()})
	if err != nil {
		if !rejectedCreation(err) {
			m.GID = types.Int64Null()
			resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
			resp.Diagnostics.Append(resp.Private.SetKey(ctx, "creation_outcome", []byte(`"uncertain"`))...)
		}
		resp.Diagnostics.AddError("Group creation was not acknowledged", err.Error())
		return
	}
	m.GID = types.Int64Null()
	resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
	g, readErr := r.client.Group(ctx, m.Name.ValueString())
	if readErr == nil {
		m.read(g)
		resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
	}
	if readErr != nil {
		m.GID = types.Int64Null()
		resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
		resp.Diagnostics.AddError("Group created but readback failed", readErr.Error())
	}
}
func (r *groupResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	resp.Diagnostics.Append(guardUncertainCreation(ctx, req.Private)...)
	if resp.Diagnostics.HasError() {
		return
	}
	var m groupModel
	resp.Diagnostics.Append(req.State.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	g, err := r.client.Group(ctx, m.Name.ValueString())
	if errors.Is(err, dsm.ErrNotFound) {
		if m.DeletionProtection.IsNull() || m.DeletionProtection.ValueBool() {
			resp.Diagnostics.AddError("Protected group is missing", "Inspect DSM and explicitly reconcile the missing group before replacing its identity.")
			return
		}
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		resp.Diagnostics.AddError("Cannot read group", err.Error())
		return
	}
	if !m.GID.IsNull() && m.GID.ValueInt64() != g.GID {
		resp.Diagnostics.AddError("Group identity changed", "The group name now belongs to a different numeric identity. Inspect DSM before reconciling state.")
		return
	}
	m.read(g)
	resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
}
func (r *groupResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	resp.Diagnostics.Append(guardUncertainCreation(ctx, req.Private)...)
	if resp.Diagnostics.HasError() {
		return
	}
	var plan, state groupModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	current, err := r.client.Group(ctx, state.Name.ValueString())
	if err != nil {
		resp.Diagnostics.AddError("Cannot verify group identity", err.Error())
		return
	}
	if state.GID.IsNull() || state.GID.ValueInt64() <= 0 || current.GID != state.GID.ValueInt64() {
		resp.Diagnostics.AddError("Group identity changed", "Refusing to update an unverified or replacement group.")
		return
	}
	if !plan.Description.Equal(state.Description) {
		if err := r.client.WriteGroup(ctx, "set", dsm.Group{Name: plan.Name.ValueString(), Description: plan.Description.ValueString()}); err != nil {
			resp.Diagnostics.AddError("Cannot update group", err.Error())
			return
		}
	}
	g, err := r.client.Group(ctx, plan.Name.ValueString())
	if err != nil {
		resp.Diagnostics.AddError("Cannot verify group update", err.Error())
		return
	}
	if g.GID != state.GID.ValueInt64() {
		resp.Diagnostics.AddError("Group identity changed during update", "The provider will not adopt a replacement group.")
		return
	}
	plan.read(g)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}
func (r *groupResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	resp.Diagnostics.Append(guardUncertainCreation(ctx, req.Private)...)
	if resp.Diagnostics.HasError() {
		return
	}
	var m groupModel
	resp.Diagnostics.Append(req.State.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	name := m.Name.ValueString()
	if name == "administrators" || name == "users" || name == "http" || m.DeletionProtection.IsNull() || m.DeletionProtection.ValueBool() {
		resp.Diagnostics.AddError("Group deletion is protected", "Built-in groups cannot be deleted. Disable deletion_protection in a separate apply for an intentional custom-group deletion, or stop management with a removed block and destroy=false.")
		return
	}
	g, err := r.client.Group(ctx, name)
	if errors.Is(err, dsm.ErrNotFound) {
		return
	}
	if err != nil {
		resp.Diagnostics.AddError("Cannot verify group identity", err.Error())
		return
	}
	if m.GID.IsNull() || m.GID.ValueInt64() <= 0 || g.GID != m.GID.ValueInt64() {
		resp.Diagnostics.AddError("Group identity changed", "Refusing to delete a different group with the same name.")
		return
	}
	if err := r.client.DeletePrincipal(ctx, "group", name); err != nil {
		resp.Diagnostics.AddError("Cannot delete group", err.Error())
		return
	}
	if _, err := r.client.Group(ctx, name); !errors.Is(err, dsm.ErrNotFound) {
		resp.Diagnostics.AddError("Group deletion is unconfirmed", "Refresh DSM before retrying the deletion.")
	}
}
func (*groupResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	resource.ImportStatePassthroughID(ctx, path.Root("name"), req, resp)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("deletion_protection"), true)...)
}
