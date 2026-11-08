package provider

import (
	"context"
	"errors"
	"net/netip"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/elephant-evergreen/terraform-provider-synology/internal/dsm"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/booldefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/int64default"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

type ddnsRecordResource struct{ serviceResource }
type ddnsModel struct {
	ID                     types.String `tfsdk:"id"`
	ProviderID             types.String `tfsdk:"provider_id"`
	Enabled                types.Bool   `tfsdk:"enabled"`
	Hostname               types.String `tfsdk:"hostname"`
	Username               types.String `tfsdk:"username"`
	NetworkMode            types.String `tfsdk:"network_mode"`
	IPv4Address            types.String `tfsdk:"ipv4_address"`
	IPv6Address            types.String `tfsdk:"ipv6_address"`
	IPv4Interface          types.String `tfsdk:"ipv4_interface"`
	IPv6Interface          types.String `tfsdk:"ipv6_interface"`
	Heartbeat              types.Bool   `tfsdk:"heartbeat"`
	Status                 types.String `tfsdk:"status"`
	Password               types.String `tfsdk:"password_wo"`
	PasswordVersion        types.Int64  `tfsdk:"password_version"`
	DeletionProtection     types.Bool   `tfsdk:"deletion_protection"`
	ReconciliationRequired types.Bool   `tfsdk:"reconciliation_required"`
}

func NewDDNSRecordResource() resource.Resource {
	return &ddnsRecordResource{serviceResource{definition: serviceDefinition{name: "ddns_record"}}}
}
func (*ddnsRecordResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	optional := func(description string) schema.StringAttribute {
		return schema.StringAttribute{Optional: true, Computed: true, MarkdownDescription: description}
	}
	resp.Schema = schema.Schema{MarkdownDescription: "A DSM DDNS provider record. Existing records require import; Create rejects collisions. Credentials are write-only and versioned. Synology/TWNIC hostname registration, certificate issuance and account enrollment remain separate DSM workflows; their existing records can be imported but not renamed here. Automatic address observations are not equivalent to published DNS success. Deletion is protected by default and can affect external DNS registration.", Attributes: map[string]schema.Attribute{
		"id":                      schema.StringAttribute{Computed: true, PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()}, MarkdownDescription: "Stable DSM record identity. Import the ID from data.synology_ddns.records."},
		"provider_id":             schema.StringAttribute{Required: true, PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()}, MarkdownDescription: "DSM DDNS provider identifier, from the discovered provider catalogue. DSM supports one configured record per provider."},
		"enabled":                 schema.BoolAttribute{Optional: true, Computed: true, MarkdownDescription: "Enable DDNS updates. New records require true; omission preserves existing policy."},
		"hostname":                optional("Registered hostname. Required for creation; changing Synology/TWNIC registration is refused."),
		"username":                optional("DDNS account username. An intentional username change requires an increased password generation."),
		"network_mode":            schema.StringAttribute{Computed: true, MarkdownDescription: "Observed DSM address-selection mode, derived from the selected interfaces when writing."},
		"ipv4_address":            optional("IPv4 address. Configure explicitly only with ipv4_interface = an empty string for a manual address. Automatic addresses are observed when omitted."),
		"ipv6_address":            optional("IPv6 address. Configure explicitly only with ipv6_interface = an empty string for a manual address. Omission preserves automatic observations."),
		"ipv4_interface":          optional("default for public-address discovery, disabled, an existing interface name, or an empty string for an explicit manual address."),
		"ipv6_interface":          optional("default, disabled, an existing interface name, or an empty string for a manual IPv6 address."),
		"heartbeat":               schema.BoolAttribute{Optional: true, Computed: true, MarkdownDescription: "DSM DDNS heartbeat policy."},
		"status":                  schema.StringAttribute{Computed: true, MarkdownDescription: "Observed DSM update status. The resource owns configuration, not DNS propagation."},
		"password_wo":             schema.StringAttribute{Optional: true, Sensitive: true, WriteOnly: true, MarkdownDescription: "DDNS password or authentication key. Required for generic-provider creation and for an increased password_version. Never stored in plan or state."},
		"password_version":        schema.Int64Attribute{Optional: true, Computed: true, Default: int64default.StaticInt64(0), MarkdownDescription: "Monotonically increasing credential generation. Zero preserves imported credentials. External credential drift is unreadable."},
		"deletion_protection":     schema.BoolAttribute{Optional: true, Computed: true, Default: booldefault.StaticBool(true), MarkdownDescription: "Refuse record deletion by default. Disabling this explicitly permits DSM record deletion and possible external unregistration."},
		"reconciliation_required": schema.BoolAttribute{Computed: true, MarkdownDescription: "An ambiguous write requires DSM inspection and explicit release/import before further mutations."},
	}}
}
func ddnsModelOf(record ddnsRecord, version int64, protection bool) ddnsModel {
	return ddnsModel{
		ID: types.StringValue(record.ID), ProviderID: types.StringValue(record.ProviderID), Enabled: types.BoolValue(record.Enabled), Hostname: types.StringValue(record.Hostname), Username: types.StringValue(record.Username), NetworkMode: types.StringValue(record.NetworkMode), IPv4Address: types.StringValue(record.IPv4Address), IPv6Address: types.StringValue(record.IPv6Address), IPv4Interface: types.StringValue(record.IPv4Interface), IPv6Interface: types.StringValue(record.IPv6Interface), Heartbeat: types.BoolValue(record.Heartbeat), Status: types.StringValue(record.Status), Password: types.StringNull(), PasswordVersion: types.Int64Value(version), DeletionProtection: types.BoolValue(protection), ReconciliationRequired: types.BoolValue(false),
	}
}

var ddnsHostnamePattern = regexp.MustCompile(`^[A-Za-z0-9](?:[A-Za-z0-9.-]*[A-Za-z0-9])?$`)

func ddnsTarget(config ddnsModel, current ddnsRecord, create bool) (ddnsRecord, error) {
	target := current
	stringsToSet := []struct {
		value  types.String
		target *string
	}{{config.ProviderID, &target.ProviderID}, {config.Hostname, &target.Hostname}, {config.Username, &target.Username}, {config.IPv4Address, &target.IPv4Address}, {config.IPv6Address, &target.IPv6Address}, {config.IPv4Interface, &target.IPv4Interface}, {config.IPv6Interface, &target.IPv6Interface}}
	for _, field := range stringsToSet {
		if field.value.IsUnknown() {
			return ddnsRecord{}, errors.New("DDNS settings must be known before apply")
		}
		if !field.value.IsNull() {
			*field.target = field.value.ValueString()
		}
	}
	if config.Enabled.IsUnknown() || config.Heartbeat.IsUnknown() {
		return ddnsRecord{}, errors.New("DDNS flags must be known before apply")
	}
	if !config.Enabled.IsNull() {
		target.Enabled = config.Enabled.ValueBool()
	}
	if !config.Heartbeat.IsNull() {
		target.Heartbeat = config.Heartbeat.ValueBool()
	}
	if target.ProviderID == "" || stringsControl(target.ProviderID) || target.Hostname == "" || !ddnsHostnamePattern.MatchString(target.Hostname) || len(target.Hostname) > 253 {
		return ddnsRecord{}, errors.New("DDNS requires a provider identifier and a valid hostname")
	}
	if create && !target.Enabled {
		return ddnsRecord{}, errors.New("new DDNS records require enabled = true")
	}
	special := target.ProviderID == "Synology" || target.ProviderID == "TWNIC"
	if special && (create || target.Hostname != current.Hostname) {
		return ddnsRecord{}, errors.New("register or rename Synology/TWNIC hostnames through DSM, then import; browser consent, certificates and account enrollment are not implicit provider side effects")
	}
	for _, field := range []struct {
		selector, address string
		configured        types.String
		v4                bool
	}{{target.IPv4Interface, target.IPv4Address, config.IPv4Address, true}, {target.IPv6Interface, target.IPv6Address, config.IPv6Address, false}} {
		if stringsControl(field.selector) {
			return ddnsRecord{}, errors.New("invalid DDNS interface selector")
		}
		if !field.configured.IsNull() && field.selector != "" {
			return ddnsRecord{}, errors.New("explicit DDNS addresses require the corresponding interface selector to be an empty string")
		}
		if field.selector == "" {
			address, err := netip.ParseAddr(field.address)
			if err != nil || address.Is4() != field.v4 {
				return ddnsRecord{}, errors.New("manual DDNS addresses must match their IPv4 or IPv6 family")
			}
		}
	}
	if target.IPv4Interface == "disabled" {
		target.IPv4Address = "0.0.0.0"
	}
	if target.IPv6Interface == "disabled" {
		target.IPv6Address = "0:0:0:0:0:0:0:0"
	}
	if create || !config.IPv4Interface.IsNull() || !config.IPv6Interface.IsNull() || !config.IPv4Address.IsNull() || !config.IPv6Address.IsNull() {
		if target.IPv4Interface == "default" {
			if target.IPv6Interface == "default" {
				target.NetworkMode = "DEFAULT"
			} else {
				target.NetworkMode = "MANUAL_V6"
			}
		} else if target.IPv6Interface == "default" {
			target.NetworkMode = "MANUAL_V4"
		} else {
			target.NetworkMode = "MANUAL"
		}
	}
	return target, nil
}
func ddnsMatches(actual, target ddnsRecord, config ddnsModel) bool {
	if actual.ProviderID != target.ProviderID || actual.Hostname != target.Hostname || actual.Enabled != target.Enabled || actual.Username != target.Username || actual.Heartbeat != target.Heartbeat || actual.IPv4Interface != target.IPv4Interface || actual.IPv6Interface != target.IPv6Interface || actual.NetworkMode != target.NetworkMode {
		return false
	}
	return (config.IPv4Address.IsNull() || actual.IPv4Address == target.IPv4Address) && (config.IPv6Address.IsNull() || actual.IPv6Address == target.IPv6Address)
}
func ddnsArgs(record ddnsRecord, create bool) url.Values {
	args := url.Values{"enable": {strconv.FormatBool(record.Enabled)}, "provider": {record.ProviderID}, "hostname": {record.Hostname}, "username": {record.Username}, "net": {record.NetworkMode}, "ip": {record.IPv4Address}, "ipv6": {record.IPv6Address}, "interface_v4": {record.IPv4Interface}, "interface_v6": {record.IPv6Interface}, "heartbeat": {strconv.FormatBool(record.Heartbeat)}}
	if !create {
		args.Set("id", record.ID)
	}
	return args
}
func (r *ddnsRecordResource) applyDDNS(ctx context.Context, config tfsdk.Config, plan tfsdk.Plan, previous *ddnsModel, checkpoint func(ddnsModel) diag.Diagnostics) (ddnsModel, error) {
	var configured, planned ddnsModel
	if d := config.Get(ctx, &configured); d.HasError() {
		return ddnsModel{}, errors.New("cannot decode DDNS configuration")
	}
	if d := plan.Get(ctx, &planned); d.HasError() {
		return ddnsModel{}, errors.New("cannot decode DDNS plan")
	}
	prior := int64(0)
	if previous != nil {
		prior = previous.PasswordVersion.ValueInt64()
	}
	version := planned.PasswordVersion
	if version.IsUnknown() || version.IsNull() || version.ValueInt64() < prior || configured.Password.IsUnknown() {
		return ddnsModel{}, errors.New("password_version must be known, nonnegative and cannot decrease")
	}
	rotate := version.ValueInt64() > prior
	if configured.ProviderID.ValueString() == "Synology" && rotate {
		return ddnsModel{}, errors.New("Synology DDNS uses the existing Synology account binding; change its authentication in DSM and import without a credential generation")
	}
	if (rotate || previous == nil) && (configured.Password.IsNull() || configured.Password.ValueString() == "" || strings.ContainsAny(configured.Password.ValueString(), "\x00\r\n")) {
		return ddnsModel{}, errors.New("supply password_wo with an increased password_version for DDNS creation or credential rotation")
	}
	if previous == nil && version.ValueInt64() < 1 {
		return ddnsModel{}, errors.New("new DDNS credentials require password_version of at least one")
	}
	if planned.DeletionProtection.IsUnknown() || planned.DeletionProtection.IsNull() {
		return ddnsModel{}, errors.New("deletion_protection must be known before apply")
	}
	protection := planned.DeletionProtection.ValueBool()
	var current, target ddnsRecord
	create := previous == nil
	changed := false
	err := r.client.WithMutation(func() error {
		records, err := readDDNSRecords(ctx, r.client)
		if err != nil {
			return err
		}
		if create {
			providers, err := readDDNSProviders(ctx, r.client)
			if err != nil {
				return err
			}
			supported := false
			for _, id := range providers {
				supported = supported || id == configured.ProviderID.ValueString()
			}
			if !supported {
				return errors.New("provider_id is not in the current DSM DDNS provider catalogue")
			}
			for _, record := range records {
				if record.ProviderID == configured.ProviderID.ValueString() {
					return errors.New("DDNS provider already has a record; import its DSM record ID instead of recreating it")
				}
			}
			current = ddnsRecord{ID: configured.ProviderID.ValueString(), ProviderID: configured.ProviderID.ValueString(), Enabled: true, IPv4Interface: "default", IPv6Interface: "default", IPv4Address: "0.0.0.0", IPv6Address: "0:0:0:0:0:0:0:0", NetworkMode: "DEFAULT"}
		} else {
			var ok bool
			current, ok = records[previous.ID.ValueString()]
			if !ok {
				return errors.New("DDNS record disappeared after planning; refresh and review a new plan")
			}
			if current.ProviderID != previous.ProviderID.ValueString() || current.Hostname != previous.Hostname.ValueString() {
				return errors.New("DDNS record identity changed after planning; refresh and review before applying")
			}
		}
		target, err = ddnsTarget(configured, current, create)
		if err != nil {
			return err
		}
		if target.Username != current.Username && !rotate {
			return errors.New("changing DDNS username requires an increased password_version and password_wo")
		}
		if !create && ddnsMatches(current, target, configured) && !rotate {
			return nil
		}
		args := ddnsArgs(target, create)
		if rotate {
			args.Set("passwd", configured.Password.ValueString())
		} else if target.ProviderID == "Synology" {
			args.Set("passwd", "Synology")
		}
		method := "set"
		if create {
			method = "create"
		} else {
			guard := ddnsModelOf(current, prior, protection)
			guard.ReconciliationRequired = types.BoolValue(true)
			if d := checkpoint(guard); d.HasError() {
				return errors.New("cannot retain DDNS failure state")
			}
		}
		changed = true
		err = r.writeDDNS(ctx, method, args)
		if err != nil {
			if create && !rejectedCreation(err) {
				guard := ddnsModelOf(target, prior, protection)
				guard.ReconciliationRequired = types.BoolValue(true)
				if d := checkpoint(guard); d.HasError() {
					return errors.New("cannot retain uncertain DDNS creation")
				}
			}
			return err
		}
		guard := ddnsModelOf(target, version.ValueInt64(), protection)
		guard.ReconciliationRequired = types.BoolValue(true)
		if d := checkpoint(guard); d.HasError() {
			return errors.New("DDNS write acknowledged but credential generation could not be retained")
		}
		if err := r.writeDDNS(ctx, "update_ip_address", url.Values{"id": {target.ProviderID}}); err != nil {
			return err
		}
		if !create {
			guard.ReconciliationRequired = types.BoolValue(false)
			if d := checkpoint(guard); d.HasError() {
				return errors.New("cannot retain acknowledged DDNS credential generation")
			}
		}
		return nil
	})
	if err != nil {
		return ddnsModel{}, err
	}
	if !changed {
		return ddnsModelOf(current, version.ValueInt64(), protection), nil
	}
	wait, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	for {
		records, err := readDDNSRecords(wait, r.client)
		if err != nil {
			return ddnsModel{}, err
		}
		for _, record := range records {
			if record.ProviderID == target.ProviderID && ddnsMatches(record, target, configured) {
				return ddnsModelOf(record, version.ValueInt64(), protection), nil
			}
		}
		select {
		case <-wait.Done():
			return ddnsModel{}, errors.New("DDNS configuration is unconfirmed; inspect DSM and reconcile explicitly before another write")
		case <-time.After(3 * time.Second):
		}
	}
}
func (r *ddnsRecordResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	value, err := r.applyDDNS(ctx, req.Config, req.Plan, nil, func(v ddnsModel) diag.Diagnostics { return resp.State.Set(ctx, &v) })
	if err != nil {
		resp.Diagnostics.AddError("Cannot create DDNS record", err.Error())
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &value)...)
}
func (r *ddnsRecordResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var previous ddnsModel
	resp.Diagnostics.Append(req.State.Get(ctx, &previous)...)
	if resp.Diagnostics.HasError() {
		return
	}
	deleting := false
	if req.Private != nil {
		var d diag.Diagnostics
		deleting, d = readDDNSDeleteIntent(ctx, req.Private)
		resp.Diagnostics.Append(d...)
		if resp.Diagnostics.HasError() {
			return
		}
	}
	if previous.ReconciliationRequired.ValueBool() && !deleting {
		resp.Diagnostics.AddError("DDNS requires reconciliation", "Inspect DSM, release management without destroy and import the confirmed record before another write.")
		return
	}
	records, err := readDDNSRecords(ctx, r.client)
	if err != nil {
		resp.Diagnostics.AddError("Cannot read DDNS records", err.Error())
		return
	}
	current, exists := records[previous.ID.ValueString()]
	if !exists {
		resp.State.RemoveResource(ctx)
		if deleting {
			resp.Diagnostics.Append(resp.Private.SetKey(ctx, "ddns_delete_pending", nil)...)
		}
		return
	}
	if deleting {
		resp.Diagnostics.AddError("DDNS deletion requires reconciliation", "DSM still reports a record after an uncertain deletion. Inspect its identity and pending work before another mutation.")
		return
	}
	if !previous.ProviderID.IsNull() && current.ProviderID != previous.ProviderID.ValueString() {
		resp.Diagnostics.AddError("DDNS record identity changed", "A reused record ID cannot silently acquire ownership of another provider.")
		return
	}
	protection := true
	if !previous.DeletionProtection.IsNull() {
		protection = previous.DeletionProtection.ValueBool()
	}
	value := ddnsModelOf(current, previous.PasswordVersion.ValueInt64(), protection)
	resp.Diagnostics.Append(resp.State.Set(ctx, &value)...)
}
func (r *ddnsRecordResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var previous ddnsModel
	resp.Diagnostics.Append(req.State.Get(ctx, &previous)...)
	if req.Private != nil {
		deleting, d := readDDNSDeleteIntent(ctx, req.Private)
		resp.Diagnostics.Append(d...)
		if deleting {
			resp.Diagnostics.AddError("Pending DDNS deletion", "Refresh to confirm absence or reconcile explicitly before updating.")
		}
	}
	if previous.ReconciliationRequired.ValueBool() {
		resp.Diagnostics.AddError("DDNS requires reconciliation", "Inspect DSM and explicitly release/import the confirmed record before retrying.")
	}
	if resp.Diagnostics.HasError() {
		return
	}
	value, err := r.applyDDNS(ctx, req.Config, req.Plan, &previous, func(v ddnsModel) diag.Diagnostics { return resp.State.Set(ctx, &v) })
	if err != nil {
		resp.Diagnostics.AddError("Cannot update DDNS record", err.Error())
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &value)...)
}
func (r *ddnsRecordResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var previous ddnsModel
	resp.Diagnostics.Append(req.State.Get(ctx, &previous)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if previous.DeletionProtection.IsNull() || previous.DeletionProtection.ValueBool() || previous.ReconciliationRequired.ValueBool() {
		resp.Diagnostics.AddError("DDNS record is protected", "Disable deletion_protection in a separate reviewed apply after resolving pending writes, or release management with destroy = false.")
		return
	}
	err := r.client.WithMutation(func() error {
		records, err := readDDNSRecords(ctx, r.client)
		if err != nil {
			return err
		}
		current, exists := records[previous.ID.ValueString()]
		if !exists {
			return nil
		}
		if current.ProviderID != previous.ProviderID.ValueString() || current.Hostname != previous.Hostname.ValueString() {
			return errors.New("DDNS identity changed after planning; deletion was refused")
		}
		previous.ReconciliationRequired = types.BoolValue(true)
		d := resp.State.Set(ctx, &previous)
		d.Append(resp.Private.SetKey(ctx, "ddns_delete_pending", []byte("true"))...)
		resp.Diagnostics.Append(d...)
		if d.HasError() {
			return errors.New("cannot retain DDNS deletion state")
		}
		return r.writeDDNS(ctx, "delete", url.Values{"id": {dsm.JSON([]string{current.ProviderID})}})
	})
	if err != nil {
		resp.Diagnostics.AddError("Cannot delete DDNS record", err.Error())
		return
	}
	records, err := readDDNSRecords(ctx, r.client)
	if err != nil {
		resp.Diagnostics.AddError("Cannot confirm DDNS deletion", err.Error())
		return
	}
	if _, exists := records[previous.ID.ValueString()]; exists {
		resp.Diagnostics.AddError("DDNS deletion unconfirmed", "DSM still reports the record; refresh before another apply.")
	}
}
func (*ddnsRecordResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	if req.ID == "" || stringsControl(req.ID) {
		resp.Diagnostics.AddError("Invalid DDNS import ID", "Use the exact record ID reported by data.synology_ddns.records.")
		return
	}
	resource.ImportStatePassthroughID(ctx, path.Root("id"), req, resp)
}

func (r *ddnsRecordResource) writeDDNS(ctx context.Context, method string, args url.Values) error {
	var result dsm.MutationReply
	if err := r.client.Call(ctx, ddnsAPI, 1, method, args, &result); err != nil {
		return err
	}
	if result.Failed() || result.TaskID != "" || (result.Finish != nil && !*result.Finish) {
		return errors.New("DSM returned failed or untracked background DDNS work; inspect and reconcile explicitly")
	}
	return nil
}

func readDDNSDeleteIntent(ctx context.Context, private privateReader) (bool, diag.Diagnostics) {
	value, d := private.GetKey(ctx, "ddns_delete_pending")
	if d.HasError() || value == nil {
		return false, d
	}
	if string(value) != "true" {
		d.AddError("Invalid DDNS deletion state", "Inspect DSM and explicitly reconcile; pending deletion metadata is malformed.")
		return false, d
	}
	return true, d
}
