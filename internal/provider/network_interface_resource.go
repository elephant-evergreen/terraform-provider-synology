package provider

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"strconv"
	"time"

	"github.com/elephant-evergreen/terraform-provider-synology/internal/dsm"
	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

const interfacePendingKey = "interface_operation"
const interfaceTimeout = 3 * time.Minute

type networkInterfaceResource struct{ serviceResource }
type interfaceOperation struct {
	Name         string                     `json:"name"`
	MAC          string                     `json:"mac"`
	Expected     map[string]json.RawMessage `json:"expected"`
	Acknowledged bool                       `json:"acknowledged"`
}

func NewNetworkInterfaceResource() resource.Resource {
	return &networkInterfaceResource{serviceResource{definition: interfaceDefinition()}}
}
func (r *networkInterfaceResource) Schema(ctx context.Context, req resource.SchemaRequest, resp *resource.SchemaResponse) {
	r.serviceResource.Schema(ctx, req, resp)
	resp.Schema.Attributes["name"] = schema.StringAttribute{Required: true, PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()}, MarkdownDescription: r.definition.fields["name"].description}
	for name := range interfaceObserved {
		field := r.definition.fields[name]
		switch field.kind {
		case types.StringType:
			resp.Schema.Attributes[name] = schema.StringAttribute{Computed: true, MarkdownDescription: field.description}
		case types.BoolType:
			resp.Schema.Attributes[name] = schema.BoolAttribute{Computed: true, MarkdownDescription: field.description}
		case types.Int64Type:
			resp.Schema.Attributes[name] = schema.Int64Attribute{Computed: true, MarkdownDescription: field.description}
		default:
			resp.Schema.Attributes[name] = schema.SetAttribute{Computed: true, ElementType: types.StringType, MarkdownDescription: field.description}
		}
	}
	resp.Schema.Attributes["id"] = schema.StringAttribute{Computed: true, PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()}, MarkdownDescription: "Physical interface name and import identity, such as eth0."}
	resp.Schema.Attributes["reconciliation_required"] = schema.BoolAttribute{Computed: true, MarkdownDescription: "An interrupted or unacknowledged interface change is retained. Read resumes acknowledged convergence only; uncertain writes require explicit DSM inspection and release/import."}
}
func interfaceState(actual types.Object, pending bool) types.Object {
	values, kinds := actual.Attributes(), actual.AttributeTypes(context.Background())
	values["reconciliation_required"], kinds["reconciliation_required"] = types.BoolValue(pending), types.BoolType
	return types.ObjectValueMust(kinds, values)
}
func interfaceExpectation(actual types.Object, desired map[string]attr.Value) (interfaceOperation, error) {
	operation := interfaceOperation{Name: actual.Attributes()["name"].(types.String).ValueString(), MAC: actual.Attributes()["mac_address"].(types.String).ValueString(), Expected: map[string]json.RawMessage{}}
	for name, value := range desired {
		switch v := value.(type) {
		case types.Bool:
			operation.Expected[name] = json.RawMessage(strconv.FormatBool(v.ValueBool()))
		case types.Int64:
			operation.Expected[name] = json.RawMessage(strconv.FormatInt(v.ValueInt64(), 10))
		case types.String:
			operation.Expected[name] = json.RawMessage(strconv.Quote(v.ValueString()))
		default:
			return interfaceOperation{}, errors.New("unsupported interface expectation type")
		}
	}
	return operation, nil
}
func (r *networkInterfaceResource) interfaceExpectedValues(operation interfaceOperation) (map[string]attr.Value, error) {
	values := map[string]attr.Value{}
	if !physicalInterfacePattern.MatchString(operation.Name) || operation.MAC == "" || operation.Expected == nil {
		return nil, errors.New("invalid pending interface identity or policy")
	}
	for name, raw := range operation.Expected {
		field, known := r.definition.fields[name]
		if !known || interfaceObserved[name] || string(raw) == "null" {
			return nil, errors.New("unrecognized pending interface policy field")
		}
		switch field.kind {
		case types.BoolType:
			var value bool
			if json.Unmarshal(raw, &value) != nil {
				return nil, errors.New("malformed pending interface boolean")
			}
			values[name] = types.BoolValue(value)
		case types.Int64Type:
			var value int64
			if json.Unmarshal(raw, &value) != nil {
				return nil, errors.New("malformed pending interface integer")
			}
			values[name] = types.Int64Value(value)
		case types.StringType:
			var value string
			if json.Unmarshal(raw, &value) != nil {
				return nil, errors.New("malformed pending interface string")
			}
			values[name] = types.StringValue(value)
		default:
			return nil, errors.New("unsupported pending interface policy type")
		}
	}
	identity, ok := values["name"]
	if !ok || identity.(types.String).ValueString() != operation.Name {
		return nil, errors.New("pending interface expectation omitted or changed its identity")
	}
	return values, nil
}
func (r *networkInterfaceResource) readInterfaceOperation(ctx context.Context, private privateReader) (*interfaceOperation, diag.Diagnostics) {
	value, d := private.GetKey(ctx, interfacePendingKey)
	if d.HasError() || value == nil {
		return nil, d
	}
	var operation interfaceOperation
	if json.Unmarshal(value, &operation) != nil {
		d.AddError("Invalid pending interface operation", "Inspect DSM and reconcile explicitly; private operation metadata is malformed.")
		return nil, d
	}
	if _, err := r.interfaceExpectedValues(operation); err != nil {
		d.AddError("Invalid pending interface operation", err.Error())
		return nil, d
	}
	return &operation, d
}
func (r *networkInterfaceResource) resumeInterface(ctx context.Context, operation interfaceOperation) (types.Object, error) {
	if !operation.Acknowledged {
		return types.Object{}, errors.New("interface write acknowledgement is uncertain; restore connectivity, inspect DSM and explicitly release/import the confirmed interface before another write")
	}
	expected, err := r.interfaceExpectedValues(operation)
	if err != nil {
		return types.Object{}, err
	}
	wait, cancel := context.WithTimeout(ctx, interfaceTimeout)
	defer cancel()
	for {
		actual, err := r.readInterface(wait, operation.Name)
		if err != nil {
			return types.Object{}, err
		}
		if actual.Attributes()["mac_address"].(types.String).ValueString() != operation.MAC {
			return types.Object{}, errors.New("physical interface MAC binding changed during convergence")
		}
		if serviceMatches(actual, expected) {
			return actual, nil
		}
		select {
		case <-wait.Done():
			return types.Object{}, errors.New("interface policy convergence is unconfirmed; pending target is retained")
		case <-time.After(3 * time.Second):
		}
	}
}
func (r *networkInterfaceResource) applyInterface(ctx context.Context, config tfsdk.Config, previous types.Object, checkpoint func(types.Object, *interfaceOperation) diag.Diagnostics) (types.Object, error) {
	wait, cancel := context.WithTimeout(ctx, interfaceTimeout)
	defer cancel()
	ctx = wait
	desired, _, err := r.desired(ctx, config)
	if err != nil {
		return types.Object{}, err
	}
	name := previous.Attributes()["id"].(types.String).ValueString()
	var actual types.Object
	var pending *interfaceOperation
	err = r.client.WithMutation(func() error {
		var err error
		actual, err = r.readInterface(ctx, name)
		if err != nil {
			return err
		}
		if !actual.Attributes()["mac_address"].Equal(previous.Attributes()["mac_address"]) {
			return errors.New("physical interface MAC identity changed; explicit reconciliation is required")
		}
		for field := range r.definition.fields {
			if interfaceObserved[field] || field == "name" {
				continue
			}
			if !actual.Attributes()[field].Equal(previous.Attributes()[field]) {
				if field == "ipv4_address" || field == "ipv4_netmask" || field == "ipv4_gateway" {
					if actual.Attributes()["ipv4_dhcp"].(types.Bool).ValueBool() && previous.Attributes()["ipv4_dhcp"].(types.Bool).ValueBool() {
						continue
					}
				}
				if field == "ipv6_address" || field == "ipv6_prefix_length" || field == "ipv6_router" {
					if actual.Attributes()["ipv6_type"].(types.String).ValueString() != "static" && previous.Attributes()["ipv6_type"].(types.String).ValueString() != "static" {
						continue
					}
				}
				return errors.New("interface policy changed after planning; refresh and review before applying")
			}
		}
		target, err := interfaceTarget(actual, desired)
		if err != nil {
			return err
		}
		if serviceMatches(actual, desired) {
			return nil
		}
		operation, err := interfaceExpectation(actual, desired)
		if err != nil {
			return err
		}
		pending = &operation
		save := func() error {
			if d := checkpoint(interfaceState(actual, true), pending); d.HasError() {
				return errors.New("cannot retain interface failure state")
			}
			return nil
		}
		if err := save(); err != nil {
			return err
		}
		values := target.Attributes()
		flag := func(name string) bool { return values[name].(types.Bool).ValueBool() }
		text := func(name string) string { return values[name].(types.String).ValueString() }
		integer := func(name string) int64 { return values[name].(types.Int64).ValueInt64() }
		changed4, changed6 := false, false
		for field, value := range desired {
			if actual.Attributes()[field].Equal(value) {
				continue
			}
			switch field {
			case "ipv6_type", "ipv6_address", "ipv6_prefix_length", "ipv6_router", "ipv6_default_gateway":
				changed6 = true
			default:
				if field != "name" {
					changed4 = true
				}
			}
		}
		write := func(api string, args url.Values) error {
			var reply dsm.MutationReply
			if err := r.client.Call(ctx, api, 1, "set", args, &reply); err != nil {
				return err
			}
			if reply.Failed() || reply.TaskID != "" || (reply.Finish != nil && !*reply.Finish) {
				return errors.New("DSM returned failed or untracked asynchronous interface work; reconcile before continuing")
			}
			return save()
		}
		if changed4 {
			settings := map[string]any{"ifname": name, "use_dhcp": flag("ipv4_dhcp"), "enable_ha_ip": false, "is_default_gateway": flag("ipv4_default_gateway"), "mtu": integer("mtu"), "enable_vlan": flag("vlan_enabled")}
			if !flag("ipv4_dhcp") {
				settings["ip"], settings["mask"], settings["gateway"], settings["dns"] = text("ipv4_address"), text("ipv4_netmask"), text("ipv4_gateway"), actual.Attributes()["ipv4_dns"].(types.String).ValueString()
			}
			if flag("vlan_enabled") {
				settings["vlan_id"] = integer("vlan_id")
			}
			if err := write(ethernetAPI, url.Values{"configs": {dsm.JSON([]any{settings})}}); err != nil {
				return err
			}
		}
		if changed6 {
			if changed4 {
				fresh, err := r.readInterface(ctx, name)
				if err != nil {
					return err
				}
				if !fresh.Attributes()["mac_address"].Equal(actual.Attributes()["mac_address"]) {
					return errors.New("physical interface identity changed during the multi-step update")
				}
				actual = fresh
				target, err = interfaceTarget(actual, desired)
				if err != nil {
					return err
				}
				values = target.Attributes()
			}
			args := url.Values{"ifname": {name}, "type": {text("ipv6_type")}, "is_default_gateway": {strconv.FormatBool(flag("ipv6_default_gateway"))}, "force": {"true"}}
			if text("ipv6_type") == "static" {
				args.Set("ip", text("ipv6_address"))
				args.Set("prefix_length", strconv.FormatInt(integer("ipv6_prefix_length"), 10))
				args.Set("router", text("ipv6_router"))
				args.Set("dns", actual.Attributes()["ipv6_dns"].(types.String).ValueString())
			}
			if err := write("SYNO.Core.Network.IPv6", args); err != nil {
				return err
			}
		}
		pending.Acknowledged = true
		return save()
	})
	if err != nil {
		return types.Object{}, err
	}
	if pending == nil {
		return actual, nil
	}
	return r.resumeInterface(ctx, *pending)
}
func (*networkInterfaceResource) Create(_ context.Context, _ resource.CreateRequest, resp *resource.CreateResponse) {
	resp.Diagnostics.AddError("Interface changes require import", "Import the existing physical interface, such as eth0, before configuring it. This resource does not create hardware or discard interrupted-operation baselines through replacement.")
}
func (r *networkInterfaceResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var id, mac types.String
	var guard types.Bool
	resp.Diagnostics.Append(req.State.GetAttribute(ctx, path.Root("id"), &id)...)
	resp.Diagnostics.Append(req.State.GetAttribute(ctx, path.Root("mac_address"), &mac)...)
	resp.Diagnostics.Append(req.State.GetAttribute(ctx, path.Root("reconciliation_required"), &guard)...)
	if resp.Diagnostics.HasError() {
		return
	}
	var operation *interfaceOperation
	if req.Private != nil {
		var d diag.Diagnostics
		operation, d = r.readInterfaceOperation(ctx, req.Private)
		resp.Diagnostics.Append(d...)
		if resp.Diagnostics.HasError() {
			return
		}
	}
	if operation == nil && guard.ValueBool() {
		resp.Diagnostics.AddError("Missing interface operation baseline", "Inspect DSM and reconcile explicitly; refresh cannot discard lifecycle protection.")
		return
	}
	if operation != nil && operation.Name != id.ValueString() {
		resp.Diagnostics.AddError("Interface operation identity changed", "Explicit reconciliation is required before another write.")
		return
	}
	var actual types.Object
	var err error
	if operation != nil {
		actual, err = r.resumeInterface(ctx, *operation)
	} else {
		actual, err = r.readInterface(ctx, id.ValueString())
	}
	if err != nil {
		resp.Diagnostics.AddError("Cannot read network interface", err.Error())
		return
	}
	if !mac.IsNull() && !actual.Attributes()["mac_address"].Equal(mac) {
		resp.Diagnostics.AddError("Physical interface identity changed", "The imported MAC binding no longer matches DSM. Hardware replacement or MAC changes require explicit inspection and import; the old identity was not adopted.")
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, interfaceState(actual, false))...)
	if operation != nil {
		resp.Diagnostics.Append(resp.Private.SetKey(ctx, interfacePendingKey, nil)...)
	}
}
func (r *networkInterfaceResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var previous types.Object
	resp.Diagnostics.Append(req.State.Get(ctx, &previous)...)
	var operation *interfaceOperation
	if req.Private != nil {
		var d diag.Diagnostics
		operation, d = r.readInterfaceOperation(ctx, req.Private)
		resp.Diagnostics.Append(d...)
	}
	if resp.Diagnostics.HasError() {
		return
	}
	if previous.Attributes()["reconciliation_required"].(types.Bool).ValueBool() || operation != nil {
		resp.Diagnostics.AddError("Pending interface operation", "Refresh acknowledged work or explicitly reconcile an uncertain change before another mutation.")
		return
	}
	actual, err := r.applyInterface(ctx, req.Config, previous, func(value types.Object, operation *interfaceOperation) diag.Diagnostics {
		d := resp.State.Set(ctx, value)
		d.Append(resp.Private.SetKey(ctx, interfacePendingKey, []byte(dsm.JSON(operation)))...)
		return d
	})
	if err != nil {
		resp.Diagnostics.AddError("Cannot update network interface", err.Error())
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, interfaceState(actual, false))...)
	resp.Diagnostics.Append(resp.Private.SetKey(ctx, interfacePendingKey, nil)...)
}
func (r *networkInterfaceResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var guard types.Bool
	resp.Diagnostics.Append(req.State.GetAttribute(ctx, path.Root("reconciliation_required"), &guard)...)
	var operation *interfaceOperation
	if req.Private != nil {
		var d diag.Diagnostics
		operation, d = r.readInterfaceOperation(ctx, req.Private)
		resp.Diagnostics.Append(d...)
	}
	if guard.ValueBool() || operation != nil {
		resp.Diagnostics.AddError("Pending interface operation", "Restore connectivity and inspect DSM. Use a removed block with destroy = false and import the confirmed interface; destroy cannot discard an uncertain operation.")
	}
}
func (*networkInterfaceResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	if !physicalInterfacePattern.MatchString(req.ID) {
		resp.Diagnostics.AddError("Invalid physical interface import ID", "Use an existing physical ethN interface, such as eth0.")
		return
	}
	resource.ImportStatePassthroughID(ctx, path.Root("id"), req, resp)
}

func (r *networkInterfaceResource) ValidateConfig(ctx context.Context, req resource.ValidateConfigRequest, resp *resource.ValidateConfigResponse) {
	var config types.Object
	resp.Diagnostics.Append(req.Config.Get(ctx, &config)...)
	if resp.Diagnostics.HasError() || config.IsNull() || config.IsUnknown() {
		return
	}
	values := config.Attributes()
	known := func(name string) bool { return !values[name].IsNull() && !values[name].IsUnknown() }
	if known("name") && !physicalInterfacePattern.MatchString(values["name"].(types.String).ValueString()) {
		resp.Diagnostics.AddAttributeError(path.Root("name"), "Unsupported interface identity", "Use an existing physical ethN interface, such as eth0.")
	}
	if known("ipv4_dhcp") && values["ipv4_dhcp"].(types.Bool).ValueBool() {
		for _, name := range []string{"ipv4_address", "ipv4_netmask", "ipv4_gateway"} {
			if known(name) {
				resp.Diagnostics.AddAttributeError(path.Root(name), "DHCP observations cannot be configured", "Omit static IPv4 fields while ipv4_dhcp is true.")
			}
		}
	}
	if known("ipv6_type") && values["ipv6_type"].(types.String).ValueString() != "static" {
		for _, name := range []string{"ipv6_address", "ipv6_prefix_length", "ipv6_router"} {
			if known(name) {
				resp.Diagnostics.AddAttributeError(path.Root(name), "Automatic IPv6 observations cannot be configured", "Omit static IPv6 fields while ipv6_type is auto or off.")
			}
		}
	}
	if known("mtu") {
		mtu := values["mtu"].(types.Int64).ValueInt64()
		if mtu < 1100 || mtu > 9000 {
			resp.Diagnostics.AddAttributeError(path.Root("mtu"), "Invalid MTU", "Use 1100 to 9000 within the NAS model's supported range.")
		}
	}
	if known("vlan_enabled") && !values["vlan_enabled"].(types.Bool).ValueBool() && known("vlan_id") && values["vlan_id"].(types.Int64).ValueInt64() != 0 {
		resp.Diagnostics.AddAttributeError(path.Root("vlan_id"), "Inactive VLAN ID cannot be configured", "Omit vlan_id while tagging is disabled.")
	}
}
