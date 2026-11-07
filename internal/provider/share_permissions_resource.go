package provider

import (
	"context"
	"fmt"

	"github.com/elephant-evergreen/terraform-provider-synology/internal/dsm"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

type sharePermissionsResource struct{ client *dsm.Client }
type permissionsModel struct {
	Share     types.String `tfsdk:"share"`
	Users     types.Map    `tfsdk:"users"`
	Groups    types.Map    `tfsdk:"groups"`
	ShareUUID types.String `tfsdk:"share_uuid"`
	UserIDs   types.Map    `tfsdk:"user_ids"`
	GroupIDs  types.Map    `tfsdk:"group_ids"`
}

func NewSharePermissionsResource() resource.Resource { return &sharePermissionsResource{} }
func (*sharePermissionsResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_share_permissions"
}
func (*sharePermissionsResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{MarkdownDescription: "Own the complete explicit local-user and local-group permission policy for one shared folder. Each map value is read_only, read_write or deny. Omission means no explicit grant or deny, rather than denial. Effective access also depends on group membership and file ACLs. Import by shared-folder name to preserve an existing policy. Creating this configuration resource applies the declared policy. Destroy only releases management and leaves permissions unchanged.", Attributes: map[string]schema.Attribute{
		"share":      schema.StringAttribute{Required: true, MarkdownDescription: "Shared-folder name and import identifier.", PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()}},
		"users":      schema.MapAttribute{Required: true, ElementType: types.StringType, MarkdownDescription: "Complete explicit local-user policy, keyed by account name. Use an empty map for no individual overrides. Unlisted local users receive no explicit grant or deny."},
		"groups":     schema.MapAttribute{Required: true, ElementType: types.StringType, MarkdownDescription: "Complete explicit local-group policy, keyed by group name. Keep administrators = read_write to preserve management access."},
		"share_uuid": schema.StringAttribute{Computed: true, MarkdownDescription: "Shared-folder identity used to reject a same-name replacement.", PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()}},
		"user_ids":   schema.MapAttribute{Computed: true, ElementType: types.Int64Type, MarkdownDescription: "Observed numeric local-user identities used to reject replacement principals."},
		"group_ids":  schema.MapAttribute{Computed: true, ElementType: types.Int64Type, MarkdownDescription: "Observed numeric local-group identities used to reject replacement principals."},
	}}
}
func (r *sharePermissionsResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
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
func (*sharePermissionsResource) ValidateConfig(ctx context.Context, req resource.ValidateConfigRequest, resp *resource.ValidateConfigResponse) {
	var m permissionsModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() || m.Users.IsUnknown() || m.Groups.IsUnknown() {
		return
	}
	users, groups, err := m.maps(ctx)
	if err == nil {
		err = dsm.ValidateAccessPolicy(users, groups)
	}
	if err != nil {
		resp.Diagnostics.AddError("Invalid shared-folder permission policy", err.Error())
	}
}
func (m *permissionsModel) maps(ctx context.Context) (map[string]string, map[string]string, error) {
	users, groups := map[string]string{}, map[string]string{}
	if m.Users.IsUnknown() || m.Groups.IsUnknown() {
		return nil, nil, fmt.Errorf("permission policy must be known before applying")
	}
	if d := m.Users.ElementsAs(ctx, &users, false); d.HasError() {
		return nil, nil, fmt.Errorf("cannot decode local-user permissions")
	}
	if d := m.Groups.ElementsAs(ctx, &groups, false); d.HasError() {
		return nil, nil, fmt.Errorf("cannot decode local-group permissions")
	}
	return users, groups, nil
}
func (m *permissionsModel) read(ctx context.Context, p dsm.AccessPolicy) error {
	for _, pair := range []struct {
		stored types.Map
		actual map[string]int64
	}{{m.UserIDs, p.UserIDs}, {m.GroupIDs, p.GroupIDs}} {
		if !pair.stored.IsNull() && !pair.stored.IsUnknown() {
			previous := map[string]int64{}
			if d := pair.stored.ElementsAs(ctx, &previous, false); d.HasError() {
				return fmt.Errorf("cannot decode bound principal identities")
			}
			for name, id := range previous {
				if _, exists := pair.actual[name]; !exists {
					pair.actual[name] = id
				}
			}
		}
	}
	users, d := types.MapValueFrom(ctx, types.StringType, p.Users)
	if d.HasError() {
		return fmt.Errorf("cannot encode local-user permissions")
	}
	groups, d := types.MapValueFrom(ctx, types.StringType, p.Groups)
	if d.HasError() {
		return fmt.Errorf("cannot encode local-group permissions")
	}
	userIDs, d := types.MapValueFrom(ctx, types.Int64Type, p.UserIDs)
	if d.HasError() {
		return fmt.Errorf("cannot encode local-user identities")
	}
	groupIDs, d := types.MapValueFrom(ctx, types.Int64Type, p.GroupIDs)
	if d.HasError() {
		return fmt.Errorf("cannot encode local-group identities")
	}
	m.Users = users
	m.Groups = groups
	m.UserIDs = userIDs
	m.GroupIDs = groupIDs
	m.ShareUUID = types.StringValue(p.ShareUUID)
	return nil
}
func (m *permissionsModel) guard(ctx context.Context, p dsm.AccessPolicy) error {
	if m.ShareUUID.IsNull() || m.ShareUUID.IsUnknown() || m.ShareUUID.ValueString() == "" || m.ShareUUID.ValueString() != p.ShareUUID {
		return fmt.Errorf("shared-folder identity is unverified or changed; explicitly reconcile the policy before continuing")
	}
	users, groups, err := m.maps(ctx)
	if err != nil {
		return err
	}
	for _, pair := range []struct {
		stored types.Map
		actual map[string]int64
		grants map[string]string
	}{{m.UserIDs, p.UserIDs, users}, {m.GroupIDs, p.GroupIDs, groups}} {
		if pair.stored.IsNull() || pair.stored.IsUnknown() {
			return fmt.Errorf("principal identities are unverified; refresh or import the policy before continuing")
		}
		ids := map[string]int64{}
		if d := pair.stored.ElementsAs(ctx, &ids, false); d.HasError() {
			return fmt.Errorf("cannot decode stored principal identities")
		}
		for name, id := range ids {
			actual, ok := pair.actual[name]
			if ok && actual != id {
				return fmt.Errorf("a policy principal has a different numeric identity; inspect DSM and explicitly reconcile the policy")
			}
			if !ok && pair.grants[name] != "" {
				return fmt.Errorf("a granted policy principal is missing; inspect DSM and explicitly reconcile the policy")
			}
		}
	}
	return nil
}
func (r *sharePermissionsResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var m permissionsModel
	resp.Diagnostics.Append(req.State.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	p, err := r.client.AccessPolicy(ctx, m.Share.ValueString())
	if err != nil {
		resp.Diagnostics.AddError("Cannot read shared-folder policy", err.Error())
		return
	}
	if !m.ShareUUID.IsNull() {
		if err := m.guard(ctx, p); err != nil {
			resp.Diagnostics.AddError("Shared-folder policy identity changed", err.Error())
			return
		}
	}
	if err := m.read(ctx, p); err != nil {
		resp.Diagnostics.AddError("Cannot record shared-folder policy", err.Error())
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
}
func (r *sharePermissionsResource) apply(ctx context.Context, plan permissionsModel, expected dsm.AccessPolicy) (permissionsModel, error) {
	users, groups, err := plan.maps(ctx)
	if err != nil {
		return plan, err
	}
	if err := r.client.WriteAccessPolicy(ctx, plan.Share.ValueString(), expected, users, groups); err != nil {
		return plan, err
	}
	actual, err := r.client.AccessPolicy(ctx, plan.Share.ValueString())
	if err != nil {
		return plan, err
	}
	if expected.ShareUUID != actual.ShareUUID {
		return plan, fmt.Errorf("shared-folder identity changed during policy update")
	}
	if err := plan.read(ctx, actual); err != nil {
		return plan, err
	}
	return plan, nil
}
func (r *sharePermissionsResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan permissionsModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	current, err := r.client.AccessPolicy(ctx, plan.Share.ValueString())
	if err != nil {
		resp.Diagnostics.AddError("Cannot inspect existing shared-folder policy", err.Error())
		return
	}
	prior := plan
	if err := prior.read(ctx, current); err != nil {
		resp.Diagnostics.AddError("Cannot record policy identity", err.Error())
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &prior)...)
	actual, err := r.apply(ctx, plan, current)
	if err != nil {
		resp.Diagnostics.AddError("Cannot apply shared-folder policy", err.Error())
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &actual)...)
}
func (r *sharePermissionsResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state permissionsModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	current, err := r.client.AccessPolicy(ctx, state.Share.ValueString())
	if err != nil {
		resp.Diagnostics.AddError("Cannot inspect shared-folder policy", err.Error())
		return
	}
	if err := state.guard(ctx, current); err != nil {
		resp.Diagnostics.AddError("Shared-folder policy identity changed", err.Error())
		return
	}
	users, groups, err := state.maps(ctx)
	if err != nil {
		resp.Diagnostics.AddError("Cannot decode prior shared-folder policy", err.Error())
		return
	}
	if !dsm.SameAccess(users, current.Users) || !dsm.SameAccess(groups, current.Groups) {
		resp.Diagnostics.AddError("Shared-folder policy changed after planning", "Re-run terraform plan before applying. The provider will not overwrite an unreviewed permission change.")
		return
	}
	plan.UserIDs = state.UserIDs
	plan.GroupIDs = state.GroupIDs
	actual, err := r.apply(ctx, plan, current)
	if err != nil {
		resp.Diagnostics.AddError("Cannot update shared-folder policy", err.Error())
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &actual)...)
}
func (*sharePermissionsResource) Delete(context.Context, resource.DeleteRequest, *resource.DeleteResponse) {
}
func (*sharePermissionsResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	resource.ImportStatePassthroughID(ctx, path.Root("share"), req, resp)
}
