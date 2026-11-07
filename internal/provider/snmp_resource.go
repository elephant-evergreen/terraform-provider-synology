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

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/int64default"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

type snmpResource struct{ serviceResource }
type snmpSecret struct{ input, generation, wire string }

var snmpSecrets = []snmpSecret{
	{"community_wo", "community_version", "rocommunity"},
	{"authentication_password_wo", "authentication_password_version", "password"},
	{"privacy_password_wo", "privacy_password_version", "privacy_key"},
}

func NewSNMPResource() resource.Resource {
	return &snmpResource{serviceResource: serviceResource{definition: serviceDefinition{
		name: "snmp", api: "SYNO.Core.SNMP", version: 1,
		description: "SNMP service configuration. Import adopts current settings without writing. Secrets are write-only, require import and change only through Update when their version increases; external secret changes cannot be drift-detected. Destroy releases management without changing DSM. Protocol settings are available only while the corresponding service is active.",
		fields: map[string]serviceField{
			"enabled":          {"enable_snmp", types.BoolType, "Enable the SNMP service."},
			"v1_v2_enabled":    {"enable_snmp_v1v2", types.BoolType, "Enable SNMP v1/v2c."},
			"v3_enabled":       {"enable_snmp_v3", types.BoolType, "Enable SNMP v3."},
			"name":             {"name", types.StringType, "SNMP system name."},
			"location":         {"location", types.StringType, "SNMP system location."},
			"contact":          {"contact", types.StringType, "SNMP system contact."},
			"username":         {"rouser", types.StringType, "Read-only SNMP v3 user name."},
			"auth_protocol":    {"auth_type", types.StringType, "SNMP v3 authentication: MD5 or SHA. Configure only with enabled v3; omission preserves the observed value."},
			"privacy_enabled":  {"enable_privacy", types.BoolType, "Enable SNMP v3 privacy. An inactive service with no returned privacy flag is represented as false."},
			"privacy_protocol": {"privacy_type", types.StringType, "SNMP v3 privacy: DES or AES. Configure only with enabled privacy; omission preserves the observed value."},
		},
	}}}
}

func (r *snmpResource) Schema(ctx context.Context, req resource.SchemaRequest, resp *resource.SchemaResponse) {
	r.serviceResource.Schema(ctx, req, resp)
	resp.Schema.Attributes["reconciliation_required"] = schema.BoolAttribute{Computed: true, MarkdownDescription: "Terraform lifecycle guard for a secret write whose acknowledgement is uncertain. Inspect DSM and explicitly reconcile before retry or replacement; this is local lifecycle metadata, not a DSM setting."}
	for _, s := range snmpSecrets {
		resp.Schema.Attributes[s.input] = schema.StringAttribute{Optional: true, Sensitive: true, WriteOnly: true, MarkdownDescription: "Write-only SNMP secret. Supply it with an increased " + s.generation + ". Omit during import. Never persisted in plan or state."}
		resp.Schema.Attributes[s.generation] = schema.Int64Attribute{Optional: true, Computed: true, Default: int64default.StaticInt64(0), MarkdownDescription: "Increase to apply " + s.input + ". Zero preserves the imported secret. An acknowledged write checkpoints this counter before readback. This does not detect external secret changes."}
	}
}

func (r *snmpResource) readSNMP(ctx context.Context) (types.Object, string, error) {
	var raw map[string]json.RawMessage
	if err := r.client.Call(ctx, r.definition.api, 1, "get", nil, &raw); err != nil {
		return types.Object{}, "", err
	}
	values := map[string]attr.Value{"id": types.StringValue("snmp")}
	kinds := map[string]attr.Type{"id": types.StringType}
	for name, f := range r.definition.fields {
		kinds[name] = f.kind
		v := raw[f.wire]
		if len(v) == 0 || string(v) == "null" {
			switch name {
			case "auth_protocol", "privacy_protocol":
				values[name] = types.StringNull()
				continue
			case "privacy_enabled":
				values[name] = types.BoolNull()
				continue
			default:
				return types.Object{}, "", fmt.Errorf("DSM SNMP response is missing the managed %s setting", name)
			}
		}
		switch f.kind {
		case types.BoolType:
			var b bool
			if json.Unmarshal(v, &b) != nil {
				return types.Object{}, "", fmt.Errorf("DSM SNMP %s setting has the wrong type", name)
			}
			values[name] = types.BoolValue(b)
		case types.StringType:
			var s string
			if json.Unmarshal(v, &s) != nil {
				return types.Object{}, "", fmt.Errorf("DSM SNMP %s setting has the wrong type", name)
			}
			values[name] = types.StringValue(s)
		}
	}
	active := values["enabled"].(types.Bool).ValueBool() && values["v3_enabled"].(types.Bool).ValueBool()
	if values["privacy_enabled"].IsNull() {
		if active {
			return types.Object{}, "", errors.New("DSM SNMP response is missing the active v3 privacy setting")
		}
		values["privacy_enabled"] = types.BoolValue(false)
	}
	if active && values["auth_protocol"].IsNull() {
		return types.Object{}, "", errors.New("DSM SNMP response is missing the active authentication protocol")
	}
	if active && values["privacy_enabled"].(types.Bool).ValueBool() && values["privacy_protocol"].IsNull() {
		return types.Object{}, "", errors.New("DSM SNMP response is missing the active privacy protocol")
	}
	var community string
	if v := raw["rocommunity"]; len(v) == 0 || string(v) == "null" || json.Unmarshal(v, &community) != nil {
		return types.Object{}, "", errors.New("DSM SNMP response is missing the community needed to preserve configuration")
	}
	return types.ObjectValueMust(kinds, values), community, nil
}

func snmpState(actual types.Object, versions map[string]int64) types.Object {
	values, kinds := actual.Attributes(), actual.AttributeTypes(context.Background())
	for _, s := range snmpSecrets {
		values[s.input], kinds[s.input] = types.StringNull(), types.StringType
		values[s.generation], kinds[s.generation] = types.Int64Value(versions[s.generation]), types.Int64Type
	}
	values["reconciliation_required"], kinds["reconciliation_required"] = types.BoolValue(false), types.BoolType
	return types.ObjectValueMust(kinds, values)
}

func snmpReconciliation(actual types.Object, required bool) types.Object {
	values := actual.Attributes()
	values["reconciliation_required"] = types.BoolValue(required)
	return types.ObjectValueMust(actual.AttributeTypes(context.Background()), values)
}

func snmpVersions(ctx context.Context, state tfsdk.State) (map[string]int64, diag.Diagnostics) {
	result := map[string]int64{}
	var diags diag.Diagnostics
	for _, s := range snmpSecrets {
		var v types.Int64
		diags.Append(state.GetAttribute(ctx, path.Root(s.generation), &v)...)
		if !v.IsUnknown() && !v.IsNull() {
			result[s.generation] = v.ValueInt64()
		}
	}
	return result, diags
}

var snmpSecretCharacters = regexp.MustCompile("^[0-9a-zA-Z~`!@#$%^&*()_\\-+=|/{}\\[\\];:<>,.?]+$")

func validSNMPSecret(value string, minimum, maximum int) bool {
	return len(value) >= minimum && len(value) <= maximum && snmpSecretCharacters.MatchString(value)
}

func (r *snmpResource) applySNMP(ctx context.Context, config tfsdk.Config, plan tfsdk.Plan, previous map[string]int64, checkpoint func(types.Object) diag.Diagnostics) (types.Object, error) {
	desired, _, err := r.desired(ctx, config)
	if err != nil {
		return types.Object{}, err
	}
	versions, secrets := map[string]int64{}, map[string]string{}
	for _, s := range snmpSecrets {
		var v types.Int64
		var secret types.String
		if d := plan.GetAttribute(ctx, path.Root(s.generation), &v); d.HasError() {
			return types.Object{}, errors.New("cannot decode SNMP secret version")
		}
		if d := config.GetAttribute(ctx, path.Root(s.input), &secret); d.HasError() {
			return types.Object{}, errors.New("cannot decode write-only SNMP secret")
		}
		if v.IsNull() || v.IsUnknown() || secret.IsUnknown() || v.ValueInt64() < previous[s.generation] || v.ValueInt64() < 0 {
			return types.Object{}, fmt.Errorf("%s must be known, nonnegative and cannot decrease", s.generation)
		}
		versions[s.generation] = v.ValueInt64()
		if v.ValueInt64() != previous[s.generation] {
			minimum, maximum := 8, 127
			if s.wire == "rocommunity" {
				minimum, maximum = 1, 64
			}
			if !validSNMPSecret(secret.ValueString(), minimum, maximum) {
				return types.Object{}, fmt.Errorf("supply a valid %s when increasing %s; see the DSM ASCII character and length requirements", s.input, s.generation)
			}
			secrets[s.wire] = secret.ValueString()
		}
	}
	var actual, target types.Object
	changed := false
	err = r.client.MutateAfterRead(ctx, r.definition.api, 1, "set", func() (url.Values, error) {
		current, community, readErr := r.readSNMP(ctx)
		if readErr != nil {
			return nil, readErr
		}
		actual = current
		values := current.Attributes()
		for name, v := range desired {
			values[name] = v
		}
		target = types.ObjectValueMust(current.AttributeTypes(ctx), values)
		active := values["enabled"].(types.Bool).ValueBool()
		v3 := active && values["v3_enabled"].(types.Bool).ValueBool()
		privacy := values["privacy_enabled"].(types.Bool).ValueBool()
		if active && !values["v1_v2_enabled"].(types.Bool).ValueBool() && !values["v3_enabled"].(types.Bool).ValueBool() {
			return nil, errors.New("enabled SNMP requires v1/v2 or v3")
		}
		if _, set := desired["auth_protocol"]; set && !v3 {
			return nil, errors.New("configure auth_protocol only while SNMP v3 is enabled")
		}
		if _, set := desired["privacy_protocol"]; set && (!v3 || !privacy) {
			return nil, errors.New("configure privacy_protocol only while SNMP v3 privacy is enabled")
		}
		if privacy && !v3 {
			if requested, declared := desired["privacy_enabled"]; declared && requested.(types.Bool).ValueBool() {
				return nil, errors.New("SNMP privacy requires an enabled v3 service")
			}
		}
		if secrets["password"] != "" && !v3 {
			return nil, errors.New("rotate the authentication password only with an enabled SNMP v3 service")
		}
		if secrets["privacy_key"] != "" && (!v3 || !privacy) {
			return nil, errors.New("rotate the privacy password only with enabled SNMP v3 privacy")
		}
		if secrets["rocommunity"] != "" && (!active || !values["v1_v2_enabled"].(types.Bool).ValueBool()) {
			return nil, errors.New("rotate the community only with an enabled SNMP v1/v2 service")
		}
		if active && values["v1_v2_enabled"].(types.Bool).ValueBool() && community == "" && secrets["rocommunity"] == "" {
			return nil, errors.New("enabling SNMP v1/v2 requires an existing community or community_wo with an increased version")
		}
		if v3 {
			auth := values["auth_protocol"].(types.String).ValueString()
			if auth != "MD5" && auth != "SHA" {
				return nil, errors.New("SNMP v3 requires auth_protocol MD5 or SHA")
			}
			username := values["username"].(types.String).ValueString()
			if !validSNMPSecret(username, 1, 64) {
				return nil, errors.New("SNMP v3 requires a valid username")
			}
			prior := current.Attributes()
			if (!prior["enabled"].(types.Bool).ValueBool() || !prior["v3_enabled"].(types.Bool).ValueBool() || !values["auth_protocol"].Equal(prior["auth_protocol"]) || !values["username"].Equal(prior["username"])) && secrets["password"] == "" {
				return nil, errors.New("enabling or changing SNMP v3 authentication requires authentication_password_wo and an increased version")
			}
			if privacy {
				protocol := values["privacy_protocol"].(types.String).ValueString()
				if protocol != "DES" && protocol != "AES" {
					return nil, errors.New("SNMP privacy requires privacy_protocol DES or AES")
				}
				if (!prior["privacy_enabled"].(types.Bool).ValueBool() || !values["privacy_protocol"].Equal(prior["privacy_protocol"]) || !values["username"].Equal(prior["username"])) && secrets["privacy_key"] == "" {
					return nil, errors.New("enabling or changing SNMP privacy requires privacy_password_wo and an increased version")
				}
			}
		}
		if d := checkpoint(snmpReconciliation(snmpState(current, previous), len(secrets) > 0)); d.HasError() {
			return nil, errors.New("cannot checkpoint the observed SNMP configuration")
		}
		if serviceMatches(current, desired) && len(secrets) == 0 {
			return nil, nil
		}
		args := url.Values{"rocommunity": {community}}
		for name, field := range r.definition.fields {
			value := values[name]
			if value.IsNull() || (name == "auth_protocol" && !v3) || (name == "privacy_protocol" && (!v3 || !privacy)) {
				continue
			}
			if name == "privacy_enabled" && !v3 {
				if _, declared := desired[name]; !declared {
					continue
				}
			}
			switch v := value.(type) {
			case types.Bool:
				args.Set(field.wire, strconv.FormatBool(v.ValueBool()))
			case types.String:
				args.Set(field.wire, v.ValueString())
			}
		}
		for wire, secret := range secrets {
			args.Set(wire, secret)
		}
		changed = true
		return args, nil
	})
	if err != nil {
		return types.Object{}, err
	}
	if !changed {
		return snmpState(actual, versions), nil
	}
	if d := checkpoint(snmpState(target, versions)); d.HasError() {
		return types.Object{}, errors.New("SNMP write was acknowledged but its secret versions could not be checkpointed")
	}
	wait, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	for {
		actual, _, err = r.readSNMP(wait)
		if err != nil {
			return types.Object{}, err
		}
		if serviceMatches(actual, desired) {
			return snmpState(actual, versions), nil
		}
		select {
		case <-wait.Done():
			return types.Object{}, errors.New("SNMP update is unconfirmed; inspect DSM and refresh before retrying")
		case <-time.After(250 * time.Millisecond):
		}
	}
}

func (r *snmpResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	for _, s := range snmpSecrets {
		var version types.Int64
		resp.Diagnostics.Append(req.Plan.GetAttribute(ctx, path.Root(s.generation), &version)...)
		if !version.IsNull() && !version.IsUnknown() && version.ValueInt64() > 0 {
			resp.Diagnostics.AddError("SNMP secret changes require import", "SNMP settings already exist on DSM. Add an import block with ID snmp before changing secrets. Terraform can import and apply an intentional update in one reviewed plan. Create cannot establish an existing secret-generation baseline and never rotates secrets, including during replacement.")
		}
	}
	if resp.Diagnostics.HasError() {
		return
	}
	actual, err := r.applySNMP(ctx, req.Config, req.Plan, map[string]int64{}, func(v types.Object) diag.Diagnostics { return resp.State.Set(ctx, v) })
	if err != nil {
		resp.Diagnostics.AddError("Cannot configure SNMP", err.Error())
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, actual)...)
}

func (r *snmpResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	versions, d := snmpVersions(ctx, req.State)
	resp.Diagnostics.Append(d...)
	if resp.Diagnostics.HasError() {
		return
	}
	actual, _, err := r.readSNMP(ctx)
	if err != nil {
		resp.Diagnostics.AddError("Cannot read SNMP", err.Error())
		return
	}
	var pending types.Bool
	resp.Diagnostics.Append(req.State.GetAttribute(ctx, path.Root("reconciliation_required"), &pending)...)
	if !resp.Diagnostics.HasError() {
		resp.Diagnostics.Append(resp.State.Set(ctx, snmpReconciliation(snmpState(actual, versions), pending.ValueBool()))...)
	}
}

func (r *snmpResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	versions, d := snmpVersions(ctx, req.State)
	resp.Diagnostics.Append(d...)
	var pending types.Bool
	resp.Diagnostics.Append(req.State.GetAttribute(ctx, path.Root("reconciliation_required"), &pending)...)
	if pending.ValueBool() {
		resp.Diagnostics.AddError("SNMP secret write requires reconciliation", snmpReconciliationMessage)
	}
	if resp.Diagnostics.HasError() {
		return
	}
	actual, err := r.applySNMP(ctx, req.Config, req.Plan, versions, func(v types.Object) diag.Diagnostics { return resp.State.Set(ctx, v) })
	if err != nil {
		resp.Diagnostics.AddError("Cannot update SNMP", err.Error())
		var uncertain types.Bool
		resp.Diagnostics.Append(resp.State.GetAttribute(ctx, path.Root("reconciliation_required"), &uncertain)...)
		if uncertain.ValueBool() {
			resp.Diagnostics.AddError("SNMP secret write requires reconciliation", snmpReconciliationMessage)
		}
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, actual)...)
}

func (r *snmpResource) Configure(ctx context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	r.serviceResource.Configure(ctx, req, resp)
}

func (*snmpResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var pending types.Bool
	resp.Diagnostics.Append(req.State.GetAttribute(ctx, path.Root("reconciliation_required"), &pending)...)
	if pending.ValueBool() {
		resp.Diagnostics.AddError("SNMP secret write requires reconciliation", snmpReconciliationMessage)
	}
}

const snmpReconciliationMessage = "A secret write was not acknowledged reliably. Automatic retries and replacement are blocked. Back up state and inspect DSM. Release management using a removed block with destroy = false, reconcile configuration secret versions to zero, and import the confirmed configuration without write-only secrets. Do not delete NAS data or accept replacement."

var _ resource.ResourceWithImportState = (*snmpResource)(nil)
var _ resource.ResourceWithConfigure = (*snmpResource)(nil)
