package provider

import (
	"context"
	"errors"
	"net/url"
	"slices"

	"github.com/elephant-evergreen/terraform-provider-synology/internal/dsm"
	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/booldefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/int64default"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringdefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

type userResource struct{ client *dsm.Client }
type userModel struct {
	Name               types.String `tfsdk:"name"`
	Description        types.String `tfsdk:"description"`
	Email              types.String `tfsdk:"email"`
	Expiration         types.String `tfsdk:"expiration"`
	UID                types.Int64  `tfsdk:"uid"`
	Groups             types.Set    `tfsdk:"groups"`
	Password           types.String `tfsdk:"password_wo"`
	PasswordVersion    types.Int64  `tfsdk:"password_version"`
	DeletionProtection types.Bool   `tfsdk:"deletion_protection"`
}

func NewUserResource() resource.Resource { return &userResource{} }
func (*userResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_user"
}
func (*userResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{MarkdownDescription: "A local DSM account and its group memberships. Import requires no password. Passwords are write-only and cannot be drift-detected. Built-in and currently authenticated accounts cannot be deleted.", Attributes: map[string]schema.Attribute{
		"name":                schema.StringAttribute{Required: true, MarkdownDescription: "Local DSM account name and import identifier.", PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()}},
		"description":         schema.StringAttribute{Optional: true, Computed: true, Default: stringdefault.StaticString(""), MarkdownDescription: "Account description. An empty value clears it."},
		"email":               schema.StringAttribute{Optional: true, Computed: true, Default: stringdefault.StaticString(""), MarkdownDescription: "Account email address. An empty value clears it."},
		"expiration":          schema.StringAttribute{Optional: true, Computed: true, Default: stringdefault.StaticString("normal"), MarkdownDescription: "DSM account state: normal, now (disabled), or a DSM expiration date. Imported dates are preserved exactly."},
		"uid":                 schema.Int64Attribute{Computed: true, MarkdownDescription: "Numeric DSM user identifier."},
		"groups":              schema.SetAttribute{Optional: true, Computed: true, ElementType: types.StringType, MarkdownDescription: "Authoritative local group membership, including the implicit users group. Omit to preserve existing membership. References to synology_group names establish creation order."},
		"password_wo":         schema.StringAttribute{Optional: true, Sensitive: true, WriteOnly: true, MarkdownDescription: "Password used only for creation or an explicit password_version change. Omit on import. Never persisted in plan or state."},
		"password_version":    schema.Int64Attribute{Optional: true, Computed: true, Default: int64default.StaticInt64(0), MarkdownDescription: "Increase to rotate password_wo. Zero leaves the imported password unchanged. This counter does not detect external password changes."},
		"deletion_protection": schema.BoolAttribute{Optional: true, Computed: true, Default: booldefault.StaticBool(true), MarkdownDescription: "Reject deletion using the last applied value. Disable in a separate apply before intentionally deleting a custom account."},
	}}
}
func (r *userResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
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
func (m *userModel) read(u dsm.User) {
	m.Name = types.StringValue(u.Name)
	m.Description = types.StringValue(u.Description)
	m.Email = types.StringValue(u.Email)
	m.Expiration = types.StringValue(u.Expired)
	m.UID = types.Int64Value(u.UID)
	items := make([]attr.Value, 0, len(u.Groups))
	for _, g := range u.Groups {
		items = append(items, types.StringValue(g))
	}
	m.Groups = types.SetValueMust(types.StringType, items)
	m.Password = types.StringNull()
	if m.PasswordVersion.IsNull() || m.PasswordVersion.IsUnknown() {
		m.PasswordVersion = types.Int64Value(0)
	}
	if m.DeletionProtection.IsNull() || m.DeletionProtection.IsUnknown() {
		m.DeletionProtection = types.BoolValue(true)
	}
}
func (r *userResource) write(ctx context.Context, method string, m userModel, password string, writeGroups bool) error {
	var groups []string
	if !m.Groups.IsUnknown() && !m.Groups.IsNull() {
		if diags := m.Groups.ElementsAs(ctx, &groups, false); diags.HasError() {
			return errors.New("group membership is invalid")
		}
	}
	if r.client.IsSessionUser(m.Name.ValueString()) && (m.Expiration.ValueString() != "normal" || (writeGroups && !slices.Contains(groups, "administrators"))) {
		return errors.New("refusing to disable, expire, or remove administrator access from the authenticated account")
	}
	if writeGroups && !slices.Contains(groups, "users") {
		return errors.New("DSM local accounts must retain membership in users")
	}
	args := url.Values{"name": {m.Name.ValueString()}, "description": {m.Description.ValueString()}, "email": {m.Email.ValueString()}, "expired": {m.Expiration.ValueString()}}
	if password != "" {
		args.Set("password", password)
	}
	if err := r.client.Mutate(ctx, "SYNO.Core.User", 1, method, args); err != nil {
		return err
	}
	return nil
}
func (r *userResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan, config userModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.Config.Get(ctx, &config)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if config.Password.ValueString() == "" || plan.PasswordVersion.ValueInt64() < 1 {
		resp.Diagnostics.AddError("New user needs a password", "Supply password_wo and password_version >= 1 when creating a new account. Existing users must be imported without resetting their password.")
		return
	}
	if config.Groups.IsUnknown() {
		resp.Diagnostics.AddError("Unknown group membership", "Resolve groups before creating the account.")
		return
	}
	wantedGroups := []string{}
	hasGroups := !config.Groups.IsNull()
	if hasGroups {
		resp.Diagnostics.Append(config.Groups.ElementsAs(ctx, &wantedGroups, false)...)
		if resp.Diagnostics.HasError() {
			return
		}
		if !slices.Contains(wantedGroups, "users") {
			resp.Diagnostics.AddError("Invalid group membership", "Local accounts must retain membership in users.")
			return
		}
	}
	_, err := r.client.User(ctx, plan.Name.ValueString())
	if err == nil {
		resp.Diagnostics.AddError("User already exists", "Import the existing account by name. Creation will not adopt or reset an existing account.")
		return
	}
	if !errors.Is(err, dsm.ErrNotFound) {
		resp.Diagnostics.AddError("Cannot check user identity", err.Error())
		return
	}
	err = r.write(ctx, "create", plan, config.Password.ValueString(), false)
	if err != nil {
		if !rejectedCreation(err) {
			plan.UID = types.Int64Null()
			plan.Groups = types.SetNull(types.StringType)
			plan.Password = types.StringNull()
			resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
			resp.Diagnostics.Append(resp.Private.SetKey(ctx, "creation_outcome", []byte(`"uncertain"`))...)
		}
		resp.Diagnostics.AddError("User creation was not acknowledged", err.Error())
		return
	}
	plan.UID = types.Int64Null()
	plan.Groups = types.SetNull(types.StringType)
	plan.Password = types.StringNull()
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
	resp.Diagnostics.Append(resp.Private.SetKey(ctx, "creation_outcome", []byte(`"unbound"`))...)
	u, readErr := r.client.User(ctx, plan.Name.ValueString())
	if readErr != nil {
		resp.Diagnostics.AddError("User created but identity readback failed", readErr.Error())
		return
	}
	plan.read(u)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
	resp.Diagnostics.Append(resp.Private.SetKey(ctx, "creation_outcome", nil)...)
	if hasGroups {
		if err := r.client.SetUserGroups(ctx, plan.Name.ValueString(), wantedGroups); err != nil {
			resp.Diagnostics.AddError("User created but membership update failed", err.Error()+" The confirmed account identity is retained in state. Refresh before retrying.")
			return
		}
		actual, err := r.client.User(ctx, plan.Name.ValueString())
		if err != nil {
			resp.Diagnostics.AddError("Cannot verify created user membership", err.Error())
			return
		}
		if actual.UID != plan.UID.ValueInt64() {
			resp.Diagnostics.AddError("User identity changed during creation", "The provider will not adopt a replacement account.")
			return
		}
		plan.read(actual)
		resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
	}

}
func (r *userResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	resp.Diagnostics.Append(guardUncertainCreation(ctx, req.Private)...)
	if resp.Diagnostics.HasError() {
		return
	}
	var m userModel
	resp.Diagnostics.Append(req.State.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	u, err := r.client.User(ctx, m.Name.ValueString())
	if errors.Is(err, dsm.ErrNotFound) {
		if m.DeletionProtection.IsNull() || m.DeletionProtection.ValueBool() {
			resp.Diagnostics.AddError("Protected account is missing", "Inspect DSM and explicitly reconcile the account before replacing its identity.")
			return
		}
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		resp.Diagnostics.AddError("Cannot read user", err.Error())
		return
	}
	if !m.UID.IsNull() && m.UID.ValueInt64() != u.UID {
		resp.Diagnostics.AddError("User identity changed", "The account name now belongs to a different numeric identity. Inspect DSM before reconciling state.")
		return
	}
	m.read(u)
	resp.Diagnostics.Append(resp.State.Set(ctx, &m)...)
}
func (r *userResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	resp.Diagnostics.Append(guardUncertainCreation(ctx, req.Private)...)
	if resp.Diagnostics.HasError() {
		return
	}
	var plan, state, config userModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	resp.Diagnostics.Append(req.Config.Get(ctx, &config)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if config.Groups.IsUnknown() {
		resp.Diagnostics.AddError("Unknown group membership", "Resolve groups before updating the account.")
		return
	}
	current, err := r.client.User(ctx, state.Name.ValueString())
	if err != nil {
		resp.Diagnostics.AddError("Cannot verify user identity", err.Error())
		return
	}
	if state.UID.IsNull() || state.UID.ValueInt64() <= 0 || current.UID != state.UID.ValueInt64() {
		resp.Diagnostics.AddError("User identity changed", "Refusing to update an unverified or replacement account.")
		return
	}
	password := ""
	if !plan.PasswordVersion.Equal(state.PasswordVersion) {
		password = config.Password.ValueString()
		if password == "" || plan.PasswordVersion.ValueInt64() <= state.PasswordVersion.ValueInt64() {
			resp.Diagnostics.AddError("Invalid password rotation", "Supply password_wo and increase password_version to rotate the password.")
			return
		}
	}
	groupsChanged := !config.Groups.IsNull() && !config.Groups.IsUnknown() && !plan.Groups.Equal(state.Groups)
	if password != "" || !plan.Description.Equal(state.Description) || !plan.Email.Equal(state.Email) || !plan.Expiration.Equal(state.Expiration) {
		if err := r.write(ctx, "set", plan, password, groupsChanged); err != nil {
			resp.Diagnostics.AddError("Cannot update user", err.Error())
			return
		}
		state.Description = plan.Description
		state.Email = plan.Email
		state.Expiration = plan.Expiration
		state.PasswordVersion = plan.PasswordVersion
		state.DeletionProtection = plan.DeletionProtection
		resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
		if resp.Diagnostics.HasError() {
			return
		}
	}
	if groupsChanged {
		var groups []string
		resp.Diagnostics.Append(plan.Groups.ElementsAs(ctx, &groups, false)...)
		if resp.Diagnostics.HasError() {
			return
		}
		if err := r.client.SetUserGroups(ctx, plan.Name.ValueString(), groups); err != nil {
			resp.Diagnostics.AddError("Cannot update user membership", err.Error())
			return
		}
	}

	u, err := r.client.User(ctx, plan.Name.ValueString())
	if err != nil {
		resp.Diagnostics.AddError("Cannot verify user update", err.Error())
		return
	}
	if u.UID != state.UID.ValueInt64() {
		resp.Diagnostics.AddError("User identity changed during update", "The provider will not adopt the replacement account.")
		return
	}
	plan.read(u)
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}
func (r *userResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	resp.Diagnostics.Append(guardUncertainCreation(ctx, req.Private)...)
	if resp.Diagnostics.HasError() {
		return
	}
	var m userModel
	resp.Diagnostics.Append(req.State.Get(ctx, &m)...)
	if resp.Diagnostics.HasError() {
		return
	}
	name := m.Name.ValueString()
	if name == "admin" || name == "guest" || r.client.IsSessionUser(name) || m.DeletionProtection.IsNull() || m.DeletionProtection.ValueBool() {
		resp.Diagnostics.AddError("User deletion is protected", "Built-in and authenticated accounts cannot be deleted. Custom-account deletion requires a separate apply disabling deletion_protection. To keep the account, use a removed block with destroy=false.")
		return
	}
	u, err := r.client.User(ctx, name)
	if errors.Is(err, dsm.ErrNotFound) {
		return
	}
	if err != nil {
		resp.Diagnostics.AddError("Cannot verify user identity", err.Error())
		return
	}
	if m.UID.IsNull() || m.UID.ValueInt64() <= 0 || u.UID != m.UID.ValueInt64() {
		resp.Diagnostics.AddError("User identity changed", "Refusing to delete a different account with the same name.")
		return
	}
	if err := r.client.DeletePrincipal(ctx, "user", name); err != nil {
		resp.Diagnostics.AddError("Cannot delete user", err.Error())
		return
	}
	if _, err := r.client.User(ctx, name); !errors.Is(err, dsm.ErrNotFound) {
		resp.Diagnostics.AddError("User deletion is unconfirmed", "Refresh DSM before retrying the deletion.")
	}
}
func (*userResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	resource.ImportStatePassthroughID(ctx, path.Root("name"), req, resp)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("deletion_protection"), true)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("password_version"), int64(0))...)
}
