package provider

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strconv"
	"time"

	"github.com/elephant-evergreen/terraform-provider-synology/internal/dsm"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

const homeAPI = "SYNO.Core.User.Home"
const homeOperationTimeout = 5 * time.Minute
const homePollInterval = 3 * time.Second
const homePendingKey = "user_home_operation"

var homeLocationPattern = regexp.MustCompile(`^/volume[1-9][0-9]*$`)

var _ resource.ResourceWithValidateConfig = (*userHomeResource)(nil)

type userHomeResource struct{ serviceResource }

type userHomeModel struct {
	ID                     types.String `tfsdk:"id"`
	Enabled                types.Bool   `tfsdk:"enabled"`
	Location               types.String `tfsdk:"location"`
	DomainEnabled          types.Bool   `tfsdk:"domain_enabled"`
	LDAPEnabled            types.Bool   `tfsdk:"ldap_enabled"`
	RecycleBinEnabled      types.Bool   `tfsdk:"recycle_bin_enabled"`
	Encryption             types.Int64  `tfsdk:"encryption"`
	RemoteLocation         types.String `tfsdk:"remote_location"`
	PersonalPhotoEnabled   types.Bool   `tfsdk:"personal_photo_enabled"`
	ReconciliationRequired types.Bool   `tfsdk:"reconciliation_required"`
	OperationID            types.String `tfsdk:"operation_id"`
}

type homePolicy struct {
	Enabled       bool   `json:"enabled"`
	Location      string `json:"location"`
	DomainEnabled bool   `json:"domain_enabled"`
	LDAPEnabled   bool   `json:"ldap_enabled"`
}

type homeOperation struct {
	Target       homePolicy `json:"target"`
	Acknowledged bool       `json:"acknowledged"`
	TaskID       string     `json:"task_id,omitempty"`
}

func NewUserHomeResource() resource.Resource {
	return &userHomeResource{serviceResource{definition: serviceDefinition{name: "user_home", api: homeAPI, version: 1}}}
}

func (*userHomeResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{MarkdownDescription: "Local, domain and LDAP user-home service policy. This always-present DSM service requires import before any write; begin with an import-only plan using the running DSM policy. Create refuses to discard a previous asynchronous operation through replacement. Existing home-folder relocation is rejected because it moves user data. Recycle-bin and encryption information are observed only; manage an existing homes share through its own protected share resource. Destroy releases management without disabling homes or deleting files.", Attributes: map[string]schema.Attribute{
		"id":                      schema.StringAttribute{Computed: true, MarkdownDescription: "Singleton identity and import identifier: user_home.", PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()}},
		"enabled":                 schema.BoolAttribute{Optional: true, Computed: true, MarkdownDescription: "Enable local user homes. Omission preserves the observed policy. DSM feasibility checks must pass before any change."},
		"location":                schema.StringAttribute{Optional: true, Computed: true, MarkdownDescription: "Volume path for initial home-folder creation, such as /volume1. Omission retains the existing location. Changing an established location is refused; perform data migration separately and import the resulting configuration."},
		"domain_enabled":          schema.BoolAttribute{Optional: true, Computed: true, MarkdownDescription: "Enable domain user homes. Local user homes must remain enabled. Disabling local homes requires explicitly disabling any enabled dependent home service."},
		"ldap_enabled":            schema.BoolAttribute{Optional: true, Computed: true, MarkdownDescription: "Enable LDAP user homes. Local user homes must remain enabled."},
		"recycle_bin_enabled":     schema.BoolAttribute{Computed: true, MarkdownDescription: "Observed homes-share recycle-bin policy. This resource never changes share settings."},
		"encryption":              schema.Int64Attribute{Computed: true, MarkdownDescription: "Observed DSM home-folder encryption state. This resource never creates or rotates encryption keys."},
		"remote_location":         schema.StringAttribute{Computed: true, Sensitive: true, MarkdownDescription: "Observed remote home location. Mutations are refused while remote homes are configured."},
		"personal_photo_enabled":  schema.BoolAttribute{Computed: true, MarkdownDescription: "Observed Personal Photo dependency. Local homes cannot be disabled while it remains enabled."},
		"reconciliation_required": schema.BoolAttribute{Computed: true, MarkdownDescription: "An unfinished or uncertain operation is retained in state. Acknowledged tasks resume through read-only polling. An unacknowledged write requires DSM inspection and explicit management release/import before retry."},
		"operation_id":            schema.StringAttribute{Computed: true, MarkdownDescription: "Acknowledged background-task identifier while an operation is pending; empty after confirmed convergence."},
	}}
}

func (*userHomeResource) ValidateConfig(ctx context.Context, req resource.ValidateConfigRequest, resp *resource.ValidateConfigResponse) {
	var model userHomeModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &model)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if !model.Location.IsNull() && !model.Location.IsUnknown() && model.Location.ValueString() != "" && !homeLocationPattern.MatchString(model.Location.ValueString()) {
		resp.Diagnostics.AddAttributeError(path.Root("location"), "Invalid home volume", "Use a local volume path such as /volume1, or preserve the imported empty location while homes are disabled.")
	}
	if !model.Enabled.IsNull() && !model.Enabled.IsUnknown() && !model.Enabled.ValueBool() && (model.DomainEnabled.ValueBool() || model.LDAPEnabled.ValueBool()) {
		resp.Diagnostics.AddError("Dependent homes require local homes", "Enable local user homes before enabling domain or LDAP homes.")
	}
}

func homePolicyOf(model userHomeModel) homePolicy {
	return homePolicy{model.Enabled.ValueBool(), model.Location.ValueString(), model.DomainEnabled.ValueBool(), model.LDAPEnabled.ValueBool()}
}

func (r *userHomeResource) readHome(ctx context.Context) (userHomeModel, error) {
	var raw struct {
		Enabled       *bool   `json:"enable"`
		Location      *string `json:"location"`
		DomainEnabled *bool   `json:"enable_domain"`
		LDAPEnabled   *bool   `json:"enable_ldap"`
		RecycleBin    *bool   `json:"enable_recycle_bin"`
		Encryption    *int64  `json:"encryption"`
		Remote        *string `json:"remote_location"`
		Additional    *struct {
			PersonalPhoto *bool `json:"personal_photo_enable"`
		} `json:"additional"`
	}
	if err := r.client.Call(ctx, homeAPI, 1, "get", url.Values{"additional": {`["personal_photo_enable"]`}}, &raw); err != nil {
		return userHomeModel{}, err
	}
	if raw.Enabled == nil || raw.Location == nil || raw.DomainEnabled == nil || raw.LDAPEnabled == nil || raw.RecycleBin == nil || raw.Encryption == nil || raw.Remote == nil || raw.Additional == nil || raw.Additional.PersonalPhoto == nil {
		return userHomeModel{}, errors.New("DSM user-home response is missing a managed setting or safety dependency")
	}
	return userHomeModel{
		ID: types.StringValue("user_home"), Enabled: types.BoolValue(*raw.Enabled), Location: types.StringValue(*raw.Location),
		DomainEnabled: types.BoolValue(*raw.DomainEnabled), LDAPEnabled: types.BoolValue(*raw.LDAPEnabled), RecycleBinEnabled: types.BoolValue(*raw.RecycleBin),
		Encryption: types.Int64Value(*raw.Encryption), RemoteLocation: types.StringValue(*raw.Remote), PersonalPhotoEnabled: types.BoolValue(*raw.Additional.PersonalPhoto),
		ReconciliationRequired: types.BoolValue(false), OperationID: types.StringValue(""),
	}, nil
}

func readHomeOperation(ctx context.Context, data privateReader) (*homeOperation, diag.Diagnostics) {
	value, diags := data.GetKey(ctx, homePendingKey)
	if diags.HasError() || value == nil {
		return nil, diags
	}
	var operation homeOperation
	if err := json.Unmarshal(value, &operation); err != nil {
		diags.AddError("Invalid pending home operation", "Private operation state is malformed. Inspect DSM and reconcile explicitly; no new write is allowed.")
		return nil, diags
	}
	return &operation, diags
}

func (r *userHomeResource) resumeHome(ctx context.Context, operation homeOperation) (userHomeModel, error) {
	if !operation.Acknowledged {
		return userHomeModel{}, errors.New("user-home write requires reconciliation: its acknowledgement is uncertain; inspect DSM, explicitly release management without destroy, and import before retrying")
	}
	ctx, cancel := context.WithTimeout(ctx, homeOperationTimeout)
	defer cancel()
	if operation.TaskID != "" {
		if err := r.client.WaitTask(ctx, homeAPI, 1, operation.TaskID, homePollInterval); err != nil {
			return userHomeModel{}, err
		}
	}
	for {
		actual, err := r.readHome(ctx)
		if err != nil {
			return userHomeModel{}, err
		}
		if homePolicyOf(actual) == operation.Target {
			return actual, nil
		}
		timer := time.NewTimer(homePollInterval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return userHomeModel{}, errors.New("DSM user-home convergence is unconfirmed; pending state is retained and the write must not be replayed")
		case <-timer.C:
		}
	}
}

func (r *userHomeResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var previous userHomeModel
	resp.Diagnostics.Append(req.State.Get(ctx, &previous)...)
	if resp.Diagnostics.HasError() {
		return
	}
	var operation *homeOperation
	if req.Private != nil {
		var diags diag.Diagnostics
		operation, diags = readHomeOperation(ctx, req.Private)
		resp.Diagnostics.Append(diags...)
		if resp.Diagnostics.HasError() {
			return
		}
	}
	if operation == nil && previous.ReconciliationRequired.ValueBool() {
		resp.Diagnostics.AddError("Missing pending home operation", "Inspect DSM and reconcile explicitly before a new write. Lifecycle protection cannot be discarded by refresh.")
		return
	}
	var actual userHomeModel
	var err error
	if operation != nil {
		actual, err = r.resumeHome(ctx, *operation)
	} else {
		actual, err = r.readHome(ctx)
	}
	if err != nil {
		resp.Diagnostics.AddError("Cannot read user-home service", err.Error())
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &actual)...)
	if operation != nil {
		resp.Diagnostics.Append(resp.Private.SetKey(ctx, homePendingKey, nil)...)
	}
}

func homeTarget(config userHomeModel, actual userHomeModel) (homePolicy, error) {
	for name, value := range map[string]bool{"enabled": config.Enabled.IsUnknown(), "location": config.Location.IsUnknown(), "domain_enabled": config.DomainEnabled.IsUnknown(), "ldap_enabled": config.LDAPEnabled.IsUnknown()} {
		if value {
			return homePolicy{}, fmt.Errorf("user-home %s must be known before applying", name)
		}
	}
	target := homePolicyOf(actual)
	if !config.Enabled.IsNull() {
		target.Enabled = config.Enabled.ValueBool()
	}
	if !config.Location.IsNull() {
		target.Location = config.Location.ValueString()
	}
	if !config.DomainEnabled.IsNull() {
		target.DomainEnabled = config.DomainEnabled.ValueBool()
	}
	if !config.LDAPEnabled.IsNull() {
		target.LDAPEnabled = config.LDAPEnabled.ValueBool()
	}
	if target == homePolicyOf(actual) {
		return target, nil
	}
	if actual.RemoteLocation.ValueString() != "" {
		return homePolicy{}, errors.New("remote user homes cannot be changed by this local home-service resource")
	}
	if actual.Location.ValueString() != "" && target.Location != actual.Location.ValueString() {
		return homePolicy{}, errors.New("changing an established home location moves user data and is refused; migrate outside Terraform and import the resulting configuration")
	}
	if !target.Enabled && target.Location != actual.Location.ValueString() {
		return homePolicy{}, errors.New("an initial home location can only be configured when enabling local homes")
	}
	if target.Enabled && !homeLocationPattern.MatchString(target.Location) {
		return homePolicy{}, errors.New("enabling homes requires a known local volume path such as /volume1")
	}
	if !target.Enabled && (target.DomainEnabled || target.LDAPEnabled || actual.PersonalPhotoEnabled.ValueBool()) {
		return homePolicy{}, errors.New("disable dependent domain, LDAP and Personal Photo services explicitly before disabling local homes")
	}
	return target, nil
}

func (r *userHomeResource) validateHomeChange(ctx context.Context, target homePolicy) error {
	var reasons struct {
		Hard *[]json.RawMessage `json:"hard_reasons"`
		Soft *[]json.RawMessage `json:"soft_reasons"`
	}
	if err := r.client.Call(ctx, homeAPI, 1, "validate_set", url.Values{"enable": {strconv.FormatBool(target.Enabled)}, "location": {target.Location}}, &reasons); err != nil {
		return err
	}
	if reasons.Hard == nil || reasons.Soft == nil {
		return errors.New("DSM omitted required user-home feasibility results; no settings were changed")
	}
	if len(*reasons.Hard) != 0 || len(*reasons.Soft) != 0 {
		return errors.New("DSM reported user-home feasibility restrictions or warnings; resolve them in DSM before applying")
	}
	return nil
}

func (r *userHomeResource) checkHomeTasks(ctx context.Context) error {
	var groups map[string]json.RawMessage
	if err := r.client.Call(ctx, "SYNO.Entry.Request.Polling", 1, "list", url.Values{"extra_group_tasks": {`["admin","user"]`}, "task_id_prefix": {"userhome"}}, &groups); err != nil {
		return err
	}
	for _, group := range []string{"admin", "user"} {
		value, ok := groups[group]
		if !ok {
			return errors.New("DSM task discovery omitted a required user-home task group")
		}
		var ids []string
		if json.Unmarshal(value, &ids) != nil {
			return errors.New("DSM returned malformed user-home task discovery")
		}
		for _, id := range ids {
			status, err := r.client.TaskStatus(ctx, homeAPI, 1, id)
			if err != nil {
				return err
			}
			if !*status.Finish {
				return errors.New("a DSM user-home task is still running; wait for it and refresh before applying another change")
			}
		}
	}
	return nil
}

func (r *userHomeResource) applyHome(ctx context.Context, config tfsdk.Config, checkpoint func(userHomeModel, *homeOperation) diag.Diagnostics) (userHomeModel, error) {
	ctx, cancel := context.WithTimeout(ctx, homeOperationTimeout)
	defer cancel()
	var configured userHomeModel
	if diags := config.Get(ctx, &configured); diags.HasError() {
		return userHomeModel{}, errors.New("cannot decode user-home configuration")
	}
	var actual userHomeModel
	var pending *homeOperation
	var reply dsm.TaskReply
	err := r.client.MutateAfterReadResult(ctx, homeAPI, 1, "set", func() (url.Values, error) {
		var err error
		actual, err = r.readHome(ctx)
		if err != nil {
			return nil, err
		}
		target, err := homeTarget(configured, actual)
		if err != nil || target == homePolicyOf(actual) {
			return nil, err
		}
		if err := r.checkHomeTasks(ctx); err != nil {
			return nil, err
		}
		actual, err = r.readHome(ctx)
		if err != nil {
			return nil, err
		}
		target, err = homeTarget(configured, actual)
		if err != nil || target == homePolicyOf(actual) {
			return nil, err
		}
		if err := r.validateHomeChange(ctx, target); err != nil {
			return nil, err
		}
		pending = &homeOperation{Target: target}
		actual.ReconciliationRequired = types.BoolValue(true)
		if diags := checkpoint(actual, pending); diags.HasError() {
			return nil, errors.New("cannot prepare user-home failure state before write")
		}
		args := url.Values{"enable": {strconv.FormatBool(target.Enabled)}}
		if target.DomainEnabled != actual.DomainEnabled.ValueBool() {
			args.Set("enable_domain", strconv.FormatBool(target.DomainEnabled))
		}
		if target.LDAPEnabled != actual.LDAPEnabled.ValueBool() {
			args.Set("enable_ldap", strconv.FormatBool(target.LDAPEnabled))
		}
		if target.Enabled {
			args.Set("location", target.Location)
		}
		return args, nil
	}, &reply)
	if err != nil {
		return actual, err
	}
	if pending == nil {
		return actual, nil
	}
	if reply.Finish != nil && !*reply.Finish && reply.TaskID == "" {
		return actual, errors.New("DSM reported an unfinished user-home operation without a task identity; inspect and reconcile before another write")
	}
	if reply.Failed() {
		return actual, errors.New("DSM reported a failed user-home operation; inspect and reconcile before another write")
	}
	pending.Acknowledged, pending.TaskID = true, reply.TaskID
	actual.OperationID = types.StringValue(reply.TaskID)
	if diags := checkpoint(actual, pending); diags.HasError() {
		return actual, errors.New("cannot checkpoint acknowledged user-home operation")
	}
	return r.resumeHome(ctx, *pending)
}

func (*userHomeResource) Create(_ context.Context, _ resource.CreateRequest, resp *resource.CreateResponse) {
	resp.Diagnostics.AddError("User-home changes require import", "This DSM service already exists. Import user_home before configuring it; an import block supports adoption and an intentional update in the same plan. Replacement cannot discard an earlier asynchronous-operation baseline.")
}

func (r *userHomeResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var previous userHomeModel
	resp.Diagnostics.Append(req.State.Get(ctx, &previous)...)
	if resp.Diagnostics.HasError() {
		return
	}
	var pending *homeOperation
	if req.Private != nil {
		var diags diag.Diagnostics
		pending, diags = readHomeOperation(ctx, req.Private)
		resp.Diagnostics.Append(diags...)
		if resp.Diagnostics.HasError() {
			return
		}
	}
	if previous.ReconciliationRequired.ValueBool() || pending != nil {
		resp.Diagnostics.AddError("Pending user-home operation", "Refresh to resume acknowledged polling before updating. An uncertain write requires explicit reconciliation.")
		return
	}
	actual, err := r.applyHome(ctx, req.Config, func(model userHomeModel, operation *homeOperation) diag.Diagnostics {
		diags := resp.State.Set(ctx, &model)
		diags.Append(resp.Private.SetKey(ctx, homePendingKey, []byte(dsm.JSON(operation)))...)
		return diags
	})
	if err != nil {
		resp.Diagnostics.AddError("Cannot update user-home service", err.Error())
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &actual)...)
	resp.Diagnostics.Append(resp.Private.SetKey(ctx, homePendingKey, nil)...)
}

func (*userHomeResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var model userHomeModel
	resp.Diagnostics.Append(req.State.Get(ctx, &model)...)
	if resp.Diagnostics.HasError() {
		return
	}
	var pending *homeOperation
	if req.Private != nil {
		var diags diag.Diagnostics
		pending, diags = readHomeOperation(ctx, req.Private)
		resp.Diagnostics.Append(diags...)
		if resp.Diagnostics.HasError() {
			return
		}
	}
	if model.ReconciliationRequired.ValueBool() || model.OperationID.ValueString() != "" || pending != nil {
		resp.Diagnostics.AddError("Pending user-home operation", "Destroy cannot discard an unfinished or uncertain operation. Inspect DSM and explicitly release management with a removed block using destroy = false before importing again.")
	}
}

func (*userHomeResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	if req.ID != "user_home" {
		resp.Diagnostics.AddError("Invalid user-home import identifier", "Use user_home to import this DSM singleton without changing it.")
		return
	}
	resource.ImportStatePassthroughID(ctx, path.Root("id"), req, resp)
}
