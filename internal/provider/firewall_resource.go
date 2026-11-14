package provider

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"strconv"
	"time"

	"github.com/elephant-evergreen/terraform-provider-synology/internal/dsm"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/booldefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/boolplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/int64planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/mapplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/setdefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringdefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

const firewallPendingKey = "firewall_operation"
const firewallTimeout = 5 * time.Minute

type firewallResource struct{ serviceResource }
type firewallModel struct {
	ID                     types.String `tfsdk:"id"`
	Enabled                types.Bool   `tfsdk:"enabled"`
	ProfileName            types.String `tfsdk:"profile_name"`
	PortCheck              types.Bool   `tfsdk:"port_check_enabled"`
	Adapters               types.Map    `tfsdk:"adapters"`
	ManagementNetworks     types.Set    `tfsdk:"management_networks"`
	ManagementPort         types.Int64  `tfsdk:"management_port"`
	ReconciliationRequired types.Bool   `tfsdk:"reconciliation_required"`
	OperationID            types.String `tfsdk:"operation_id"`
}
type firewallPolicy struct {
	Enabled     bool                       `json:"enabled"`
	ProfileName string                     `json:"profile_name"`
	PortCheck   bool                       `json:"port_check"`
	Adapters    map[string]firewallAdapter `json:"adapters"`
}
type firewallOperation struct {
	Target       firewallPolicy `json:"target"`
	Acknowledged bool           `json:"acknowledged"`
	TaskID       string         `json:"task_id,omitempty"`
}

func NewFirewallResource() resource.Resource {
	return &firewallResource{serviceResource{definition: serviceDefinition{name: "firewall"}}}
}
func (*firewallResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	text := func(description, value string) schema.StringAttribute {
		return schema.StringAttribute{Optional: true, Computed: true, Default: stringdefault.StaticString(value), MarkdownDescription: description}
	}
	resp.Schema = schema.Schema{MarkdownDescription: "Manage the selected DSM firewall profile and its ordered rules. Destroy releases Terraform management and leaves the selected profile and firewall enabled state in place.", Attributes: map[string]schema.Attribute{
		"id":                      schema.StringAttribute{Computed: true, MarkdownDescription: "Singleton import identity: firewall.", PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()}},
		"enabled":                 schema.BoolAttribute{Optional: true, Computed: true, PlanModifiers: []planmodifier.Bool{boolplanmodifier.UseStateForUnknown()}, MarkdownDescription: "Enable the selected existing profile. Disabling is explicit and leaves profiles intact."},
		"profile_name":            schema.StringAttribute{Optional: true, Computed: true, PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()}, MarkdownDescription: "Existing profile to activate. Changing the selection requires enabled = true and an explicit complete adapters policy for review."},
		"port_check_enabled":      schema.BoolAttribute{Optional: true, Computed: true, PlanModifiers: []planmodifier.Bool{boolplanmodifier.UseStateForUnknown()}, MarkdownDescription: "DSM built-in port checking. Must be true when applying an enabled firewall policy."},
		"management_networks":     schema.SetAttribute{Optional: true, Computed: true, ElementType: types.StringType, Default: setdefault.StaticValue(types.SetValueMust(types.StringType, nil)), MarkdownDescription: "Actual administrator source CIDRs as seen by DSM, including any NAT or VPN translation. Required for enabled-policy mutations. These are operator assertions, not automatically discovered addresses; maintain an independent recovery path."},
		"management_port":         schema.Int64Attribute{Computed: true, PlanModifiers: []planmodifier.Int64{int64planmodifier.UseStateForUnknown()}, MarkdownDescription: "Observed native DSM HTTPS port. Firewall updates require a direct DSM endpoint whose port matches this value. Matching port numbers do not prove the absence of a proxy or NAT."},
		"reconciliation_required": schema.BoolAttribute{Computed: true, PlanModifiers: []planmodifier.Bool{boolplanmodifier.UseStateForUnknown()}, MarkdownDescription: "An unfinished or uncertain policy operation is retained. Refresh only polls acknowledged work. Unacknowledged changes require DSM inspection and explicit management release/import."},
		"operation_id":            schema.StringAttribute{Computed: true, PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()}, MarkdownDescription: "Pending acknowledged DSM firewall-apply task ID, or an empty string after convergence."},
		"adapters": schema.MapNestedAttribute{Optional: true, Computed: true, PlanModifiers: []planmodifier.Map{mapplanmodifier.UseStateForUnknown()}, MarkdownDescription: "Complete selected-profile policy keyed by DSM adapter name, including global. Rule list order is priority. Omission preserves the observed policy; unknown wire fields fail rather than being discarded.", NestedObject: schema.NestedAttributeObject{Attributes: map[string]schema.Attribute{
			"default_policy": schema.StringAttribute{Required: true, MarkdownDescription: "Fall-through policy: allow, drop or none. Preserve the imported global policy."},
			"rules": schema.ListNestedAttribute{Required: true, MarkdownDescription: "Ordered rules. The first matching rule wins.", NestedObject: schema.NestedAttributeObject{Attributes: map[string]schema.Attribute{
				"enabled":        schema.BoolAttribute{Optional: true, Computed: true, PlanModifiers: []planmodifier.Bool{boolplanmodifier.UseStateForUnknown()}, Default: booldefault.StaticBool(true), MarkdownDescription: "Enable this rule."},
				"name":           text("DSM rule description.", ""),
				"action":         text("Rule action: allow or drop.", "allow"),
				"protocol":       text("Protocol: all, tcp, udp, icmp or igmp.", "all"),
				"port_group":     text("Port selector: all, custom, service or reserved.", "all"),
				"ports":          text("all, comma-separated ports, one start-end range, or service IDs according to port_group.", "all"),
				"port_direction": text("destination or source for custom TCP/UDP ports; empty or destination for all-port rules, empty for ICMP/IGMP rules.", ""),
				"source_group":   text("Source selector: all, ip, netmask, iprange or geoip.", "all"),
				"source":         text("all, an IP, address/mask or address/prefix, start-end range, or country codes according to source_group.", "all"),
				"log":            schema.BoolAttribute{Optional: true, Computed: true, Default: booldefault.StaticBool(false), MarkdownDescription: "Log matching traffic."},
			}}},
		}}},
	}}
}
func (r *firewallResource) readFirewallProfile(ctx context.Context, name string) (types.Map, error) {
	var raw map[string]json.RawMessage
	if err := r.client.Call(ctx, firewallProfileAPI, 1, "get", url.Values{"name": {name}}, &raw); err != nil {
		return types.Map{}, err
	}
	return decodeFirewallProfile(ctx, raw, name)
}
func (r *firewallResource) readFirewall(ctx context.Context) (firewallModel, error) {
	var settings struct {
		Enabled     *bool   `json:"enable_firewall"`
		ProfileName *string `json:"profile_name"`
	}
	if err := r.client.Call(ctx, firewallAPI, 1, "get", nil, &settings); err != nil {
		return firewallModel{}, err
	}
	var conf struct {
		PortCheck *bool `json:"enable_port_check"`
	}
	if err := r.client.Call(ctx, firewallAPI+".Conf", 1, "get", nil, &conf); err != nil {
		return firewallModel{}, err
	}
	var web struct {
		HTTPSPort *int64 `json:"https_port"`
	}
	if err := r.client.Call(ctx, "SYNO.Core.Web.DSM", 2, "get", nil, &web); err != nil {
		return firewallModel{}, err
	}
	if settings.Enabled == nil || settings.ProfileName == nil || *settings.ProfileName == "" || conf.PortCheck == nil || web.HTTPSPort == nil || *web.HTTPSPort < 1 || *web.HTTPSPort > 65535 {
		return firewallModel{}, errors.New("DSM omitted a required firewall setting or HTTPS port")
	}
	adapters, err := r.readFirewallProfile(ctx, *settings.ProfileName)
	if err != nil {
		return firewallModel{}, err
	}
	return firewallModel{ID: types.StringValue("firewall"), Enabled: types.BoolValue(*settings.Enabled), ProfileName: types.StringValue(*settings.ProfileName), PortCheck: types.BoolValue(*conf.PortCheck), Adapters: adapters, ManagementPort: types.Int64Value(*web.HTTPSPort), ManagementNetworks: types.SetValueMust(types.StringType, nil), ReconciliationRequired: types.BoolValue(false), OperationID: types.StringValue("")}, nil
}
func firewallPolicyOf(ctx context.Context, model firewallModel) (firewallPolicy, error) {
	adapters, err := firewallAdapters(ctx, model.Adapters)
	if err != nil {
		return firewallPolicy{}, err
	}
	return firewallPolicy{model.Enabled.ValueBool(), model.ProfileName.ValueString(), model.PortCheck.ValueBool(), adapters}, nil
}
func firewallPolicyMatches(ctx context.Context, model firewallModel, target firewallPolicy) bool {
	adapters, d := types.MapValueFrom(ctx, firewallAdapterType, target.Adapters)
	return !d.HasError() && model.Enabled.ValueBool() == target.Enabled && model.ProfileName.ValueString() == target.ProfileName && model.PortCheck.ValueBool() == target.PortCheck && model.Adapters.Equal(adapters)
}
func readFirewallOperation(ctx context.Context, private privateReader) (*firewallOperation, diag.Diagnostics) {
	value, d := private.GetKey(ctx, firewallPendingKey)
	if d.HasError() || value == nil {
		return nil, d
	}
	var operation firewallOperation
	if json.Unmarshal(value, &operation) != nil || operation.Target.ProfileName == "" || operation.Target.Adapters == nil {
		d.AddError("Invalid pending firewall operation", "Inspect DSM and reconcile explicitly; malformed operation state cannot authorize another write.")
		return nil, d
	}
	return &operation, d
}
func (r *firewallResource) waitFirewall(ctx context.Context, id string) error {
	for {
		var result dsm.TaskReply
		if err := r.client.Call(ctx, firewallApplyAPI, 1, "status", url.Values{"task_id": {id}}, &result); err != nil {
			return err
		}
		if result.TaskID != "" && result.TaskID != id {
			return errors.New("DSM returned a different firewall task identity")
		}
		if result.Failed() {
			return errors.New("DSM reported failed firewall activation; inspect policy and reconcile explicitly")
		}
		if result.Success != nil && *result.Success {
			return nil
		}
		if result.Finish != nil && *result.Finish {
			return errors.New("DSM reported finished firewall activation without a success result")
		}
		select {
		case <-ctx.Done():
			return errors.New("firewall activation timed out; pending task is retained")
		case <-time.After(3 * time.Second):
		}
	}
}
func (r *firewallResource) resumeFirewall(ctx context.Context, operation firewallOperation) (firewallModel, error) {
	if !operation.Acknowledged {
		return firewallModel{}, errors.New("firewall write acknowledgement is uncertain; inspect DSM, release management without destroy and import before another write")
	}
	wait, cancel := context.WithTimeout(ctx, firewallTimeout)
	defer cancel()
	if operation.TaskID != "" {
		if err := r.waitFirewall(wait, operation.TaskID); err != nil {
			return firewallModel{}, err
		}
	}
	for {
		actual, err := r.readFirewall(wait)
		if err != nil {
			return firewallModel{}, err
		}
		if firewallPolicyMatches(wait, actual, operation.Target) {
			return actual, nil
		}
		select {
		case <-wait.Done():
			return firewallModel{}, errors.New("stored firewall policy and activation are not confirmed; pending state is retained")
		case <-time.After(3 * time.Second):
		}
	}
}
func (r *firewallResource) applyFirewall(ctx context.Context, config tfsdk.Config, plan tfsdk.Plan, previous firewallModel, checkpoint func(firewallModel, *firewallOperation) diag.Diagnostics) (firewallModel, error) {
	wait, cancel := context.WithTimeout(ctx, firewallTimeout)
	defer cancel()
	var configured firewallModel
	if d := config.Get(wait, &configured); d.HasError() {
		return firewallModel{}, errors.New("cannot decode firewall configuration")
	}
	if !configured.Adapters.IsNull() {
		if d := plan.GetAttribute(wait, path.Root("adapters"), &configured.Adapters); d.HasError() {
			return firewallModel{}, errors.New("cannot decode planned firewall rule defaults")
		}
	}
	if configured.Enabled.IsUnknown() || configured.ProfileName.IsUnknown() || configured.PortCheck.IsUnknown() || configured.Adapters.IsUnknown() || configured.ManagementNetworks.IsUnknown() {
		return firewallModel{}, errors.New("firewall policy and management networks must be known before apply")
	}
	var actual firewallModel
	var operation *firewallOperation
	err := r.client.WithMutation(func() error {
		var err error
		actual, err = r.readFirewall(wait)
		if err != nil {
			return err
		}
		if !actual.Enabled.Equal(previous.Enabled) || !actual.ProfileName.Equal(previous.ProfileName) || !actual.PortCheck.Equal(previous.PortCheck) || !actual.Adapters.Equal(previous.Adapters) || !actual.ManagementPort.Equal(previous.ManagementPort) {
			return errors.New("firewall policy changed after planning; refresh and review a new plan before applying")
		}
		target := actual
		if !configured.Enabled.IsNull() {
			target.Enabled = configured.Enabled
		}
		if !configured.ProfileName.IsNull() {
			target.ProfileName = configured.ProfileName
		}
		if !configured.PortCheck.IsNull() {
			target.PortCheck = configured.PortCheck
		}
		if !configured.Adapters.IsNull() {
			target.Adapters = configured.Adapters
		}
		if !configured.ManagementNetworks.IsNull() {
			actual.ManagementNetworks = configured.ManagementNetworks
		} else {
			actual.ManagementNetworks = previous.ManagementNetworks
		}
		policy, err := firewallPolicyOf(wait, target)
		if err != nil {
			return err
		}
		if firewallPolicyMatches(wait, actual, policy) {
			return nil
		}
		if policy.ProfileName == "" || stringsControl(policy.ProfileName) {
			return errors.New("firewall profile_name must identify an existing profile")
		}
		if !target.ProfileName.Equal(actual.ProfileName) && (!policy.Enabled || configured.Adapters.IsNull()) {
			return errors.New("selecting a different profile requires enabled = true and an explicit complete adapters policy")
		}
		if err := validateFirewallRules(policy.Adapters); err != nil {
			return err
		}
		if policy.Enabled {
			if !policy.PortCheck {
				return errors.New("enabled firewall changes require port_check_enabled = true")
			}
			if !r.client.UsesHTTPSPort(int(actual.ManagementPort.ValueInt64())) {
				return errors.New("use a direct DSM HTTPS endpoint with its native HTTPS port; the provider endpoint port does not match")
			}
			networks := []string{}
			if d := actual.ManagementNetworks.ElementsAs(wait, &networks, false); d.HasError() {
				return errors.New("management_networks must be known CIDRs")
			}
			if err := admitFirewall(policy.Adapters, networks, int(actual.ManagementPort.ValueInt64())); err != nil {
				return err
			}
		}
		stored, err := r.readFirewallProfile(wait, policy.ProfileName)
		if err != nil {
			return err
		}
		operation = &firewallOperation{Target: policy}
		actual.ReconciliationRequired = types.BoolValue(true)
		save := func() error {
			if d := checkpoint(actual, operation); d.HasError() {
				return errors.New("cannot retain firewall operation state")
			}
			return nil
		}
		write := func(api, method string, args url.Values) error {
			operation.Acknowledged = false
			operation.TaskID = ""
			actual.OperationID = types.StringValue("")
			if err := save(); err != nil {
				return err
			}
			var result dsm.MutationReply
			if err := r.client.Call(wait, api, 1, method, args, &result); err != nil {
				return err
			}
			if result.Failed() || result.TaskID != "" || (result.Finish != nil && !*result.Finish) {
				return errors.New("DSM returned failed or untracked background policy storage; reconcile before continuing")
			}
			return save()
		}
		if policy.PortCheck != actual.PortCheck.ValueBool() {
			if err := write(firewallAPI+".Conf", "set", url.Values{"enable_port_check": {strconv.FormatBool(policy.PortCheck)}}); err != nil {
				return err
			}
		}
		if !policy.Enabled && actual.Enabled.ValueBool() {
			if err := write(firewallAPI, "set", url.Values{"set_type": {"disable"}}); err != nil {
				return err
			}
		}
		targetMap, d := types.MapValueFrom(wait, firewallAdapterType, policy.Adapters)
		if d.HasError() {
			return errors.New("cannot encode target firewall policy")
		}
		changed := !stored.Equal(targetMap)
		if changed {
			profile := map[string]any{"name": policy.ProfileName}
			for name, adapter := range policy.Adapters {
				if adapter.Rules == nil {
					adapter.Rules = []firewallRule{}
				}
				profile[name] = adapter
			}
			applying := policy.Enabled && actual.Enabled.ValueBool() && actual.ProfileName.ValueString() == policy.ProfileName
			if err := write(firewallProfileAPI, "set", url.Values{"profile": {dsm.JSON(profile)}, "profile_applying": {strconv.FormatBool(applying)}}); err != nil {
				return err
			}
			confirmed, err := r.readFirewallProfile(wait, policy.ProfileName)
			if err != nil {
				return err
			}
			if !confirmed.Equal(targetMap) {
				return errors.New("DSM acknowledged but did not persist the complete ordered firewall profile; no activation was attempted")
			}
		}
		if policy.Enabled && (changed || !actual.Enabled.ValueBool() || actual.ProfileName.ValueString() != policy.ProfileName) {
			operation.Acknowledged = false
			if err := save(); err != nil {
				return err
			}
			var reply dsm.TaskReply
			applying := actual.Enabled.ValueBool() && actual.ProfileName.ValueString() == policy.ProfileName
			if err := r.client.Call(wait, firewallApplyAPI, 1, "start", url.Values{"name": {policy.ProfileName}, "profile_applying": {strconv.FormatBool(applying)}}, &reply); err != nil {
				return err
			}
			if reply.Failed() || reply.TaskID == "" {
				return errors.New("DSM did not acknowledge a trackable firewall-apply task; inspect policy and reconcile explicitly")
			}
			operation.Acknowledged = true
			operation.TaskID = reply.TaskID
			actual.OperationID = types.StringValue(reply.TaskID)
			return save()
		}
		operation.Acknowledged = true
		return save()
	})
	if err != nil {
		return actual, err
	}
	if operation == nil {
		return actual, nil
	}
	converged, err := r.resumeFirewall(wait, *operation)
	converged.ManagementNetworks = actual.ManagementNetworks
	return converged, err
}
func stringsControl(value string) bool {
	for _, c := range value {
		if c < 32 || c == 127 {
			return true
		}
	}
	return false
}
func (*firewallResource) Create(_ context.Context, _ resource.CreateRequest, resp *resource.CreateResponse) {
	resp.Diagnostics.AddError("Firewall changes require import", "Import firewall before configuring the existing NAS policy. Create or replacement cannot discard an earlier operation baseline.")
}
func (r *firewallResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var previous firewallModel
	resp.Diagnostics.Append(req.State.Get(ctx, &previous)...)
	if resp.Diagnostics.HasError() {
		return
	}
	var operation *firewallOperation
	if req.Private != nil {
		var d diag.Diagnostics
		operation, d = readFirewallOperation(ctx, req.Private)
		resp.Diagnostics.Append(d...)
		if resp.Diagnostics.HasError() {
			return
		}
	}
	if operation == nil && previous.ReconciliationRequired.ValueBool() {
		resp.Diagnostics.AddError("Missing pending firewall operation", "Inspect DSM and reconcile explicitly; refresh cannot discard lifecycle protection.")
		return
	}
	var actual firewallModel
	var err error
	if operation != nil {
		actual, err = r.resumeFirewall(ctx, *operation)
	} else {
		actual, err = r.readFirewall(ctx)
	}
	if err != nil {
		resp.Diagnostics.AddError("Cannot read firewall", err.Error())
		return
	}
	if !previous.ManagementNetworks.IsNull() {
		actual.ManagementNetworks = previous.ManagementNetworks
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &actual)...)
	if operation != nil {
		resp.Diagnostics.Append(resp.Private.SetKey(ctx, firewallPendingKey, nil)...)
	}
}
func (r *firewallResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var previous firewallModel
	resp.Diagnostics.Append(req.State.Get(ctx, &previous)...)
	var operation *firewallOperation
	if req.Private != nil {
		var d diag.Diagnostics
		operation, d = readFirewallOperation(ctx, req.Private)
		resp.Diagnostics.Append(d...)
	}
	if previous.ReconciliationRequired.ValueBool() || operation != nil {
		resp.Diagnostics.AddError("Pending firewall operation", "Refresh to resume acknowledged polling. An uncertain write requires DSM inspection and explicit management release/import.")
	}
	if resp.Diagnostics.HasError() {
		return
	}
	actual, err := r.applyFirewall(ctx, req.Config, req.Plan, previous, func(model firewallModel, operation *firewallOperation) diag.Diagnostics {
		d := resp.State.Set(ctx, &model)
		d.Append(resp.Private.SetKey(ctx, firewallPendingKey, []byte(dsm.JSON(operation)))...)
		return d
	})
	if err != nil {
		resp.Diagnostics.AddError("Cannot update firewall", err.Error())
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &actual)...)
	resp.Diagnostics.Append(resp.Private.SetKey(ctx, firewallPendingKey, nil)...)
}
func (*firewallResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var previous firewallModel
	resp.Diagnostics.Append(req.State.Get(ctx, &previous)...)
	var operation *firewallOperation
	if req.Private != nil {
		var d diag.Diagnostics
		operation, d = readFirewallOperation(ctx, req.Private)
		resp.Diagnostics.Append(d...)
	}
	if previous.ReconciliationRequired.ValueBool() || previous.OperationID.ValueString() != "" || operation != nil {
		resp.Diagnostics.AddError("Pending firewall operation", "Inspect DSM and explicitly release management with a removed block using destroy = false. Destroy cannot discard an uncertain policy operation.")
	}
}
