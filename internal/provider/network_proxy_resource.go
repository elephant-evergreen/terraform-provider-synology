package provider

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"net/url"
	"strings"
	"time"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/int64default"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

type networkProxyResource struct{ serviceResource }

func NewNetworkProxyResource() resource.Resource {
	return &networkProxyResource{serviceResource{definition: serviceDefinition{name: "network_proxy", api: "SYNO.Core.Network.Proxy", version: 1, description: "Outbound DSM HTTP/HTTPS proxy policy. The password is write-only and changes only with an increased version after import. Read, import and destroy never change the proxy. Existing credentials are retained when omitted; unreadable password drift cannot be detected.", fields: map[string]serviceField{
		"enabled":                {"enable", types.BoolType, "Enable outbound proxy use."},
		"http_host":              {"http_host", types.StringType, "HTTP proxy hostname or IP address, without a URL scheme."},
		"http_port":              {"http_port", types.Int64Type, "HTTP proxy port."},
		"different_https_proxy":  {"enable_different_host", types.BoolType, "Use a different HTTPS proxy. Otherwise HTTP host and port also own the HTTPS values."},
		"https_host":             {"https_host", types.StringType, "HTTPS proxy hostname or IP address."},
		"https_port":             {"https_port", types.Int64Type, "HTTPS proxy port."},
		"authentication_enabled": {"enable_auth", types.BoolType, "Authenticate to the proxy. Enabling authentication or changing its username requires an explicit password generation."},
		"username":               {"username", types.StringType, "Proxy authentication username."},
		"bypass_local":           {"enable_bypass", types.BoolType, "Bypass the proxy for local addresses."},
	}}}}
}

func (r *networkProxyResource) Schema(ctx context.Context, req resource.SchemaRequest, resp *resource.SchemaResponse) {
	r.serviceResource.Schema(ctx, req, resp)
	resp.Schema.Attributes["password_wo"] = schema.StringAttribute{Optional: true, Sensitive: true, WriteOnly: true, MarkdownDescription: "Proxy password; supply with an increased password_version. Omit for adoption. Never stored in plan or state."}
	resp.Schema.Attributes["password_version"] = schema.Int64Attribute{Optional: true, Computed: true, Default: int64default.StaticInt64(0), MarkdownDescription: "Monotonically increasing password generation. Zero preserves the imported credential; external password changes cannot be detected."}
	resp.Schema.Attributes["reconciliation_required"] = schema.BoolAttribute{Computed: true, MarkdownDescription: "An unacknowledged secret mutation requires DSM inspection and explicit management release/import before retry."}
}

func proxyState(actual types.Object, version int64, pending bool) types.Object {
	values, kinds := actual.Attributes(), actual.AttributeTypes(context.Background())
	values["password_wo"], kinds["password_wo"] = types.StringNull(), types.StringType
	values["password_version"], kinds["password_version"] = types.Int64Value(version), types.Int64Type
	values["reconciliation_required"], kinds["reconciliation_required"] = types.BoolValue(pending), types.BoolType
	return types.ObjectValueMust(kinds, values)
}

func proxyBaseline(ctx context.Context, state tfsdk.State) (int64, bool, diag.Diagnostics) {
	var version types.Int64
	var pending types.Bool
	d := state.GetAttribute(ctx, path.Root("password_version"), &version)
	d.Append(state.GetAttribute(ctx, path.Root("reconciliation_required"), &pending)...)
	return version.ValueInt64(), pending.ValueBool(), d
}

func (r *networkProxyResource) applyProxy(ctx context.Context, config tfsdk.Config, plan tfsdk.Plan, prior int64, checkpoint func(types.Object) diag.Diagnostics) (types.Object, error) {
	desired, _, err := r.desired(ctx, config)
	if err != nil {
		return types.Object{}, err
	}
	var version types.Int64
	var password types.String
	if d := plan.GetAttribute(ctx, path.Root("password_version"), &version); d.HasError() {
		return types.Object{}, errors.New("cannot decode proxy password version")
	}
	if d := config.GetAttribute(ctx, path.Root("password_wo"), &password); d.HasError() {
		return types.Object{}, errors.New("cannot decode proxy password")
	}
	if version.IsUnknown() || version.IsNull() || version.ValueInt64() < prior || password.IsUnknown() {
		return types.Object{}, errors.New("proxy password_version must be known, nonnegative and cannot decrease")
	}
	rotate := version.ValueInt64() > prior
	if rotate && (password.IsNull() || password.ValueString() == "" || strings.ContainsAny(password.ValueString(), "\x00\r\n")) {
		return types.Object{}, errors.New("supply a nonempty password_wo without control characters when increasing password_version")
	}
	var actual, target types.Object
	changed := false
	err = r.client.MutateAfterRead(ctx, r.definition.api, 1, "set", func() (url.Values, error) {
		var err error
		actual, err = r.read(ctx)
		if err != nil {
			return nil, err
		}
		values := actual.Attributes()
		for name, value := range desired {
			values[name] = value
		}
		target = types.ObjectValueMust(actual.AttributeTypes(ctx), values)
		text := func(name string) string { return values[name].(types.String).ValueString() }
		flag := func(name string) bool { return values[name].(types.Bool).ValueBool() }
		if flag("authentication_enabled") && (text("username") == "" || ((!actual.Attributes()["authentication_enabled"].(types.Bool).ValueBool() || !values["username"].Equal(actual.Attributes()["username"])) && !rotate)) {
			return nil, errors.New("enabling proxy authentication or changing its username requires a username and increased password_version with password_wo")
		}
		if rotate && !flag("authentication_enabled") {
			return nil, errors.New("proxy password changes require authentication_enabled")
		}
		if !flag("different_https_proxy") {
			for _, pair := range [][2]string{{"https_host", "http_host"}, {"https_port", "http_port"}} {
				if configured, ok := desired[pair[0]]; ok && !configured.Equal(values[pair[1]]) {
					return nil, errors.New("HTTPS proxy must equal HTTP proxy unless different_https_proxy is enabled")
				}
				values[pair[0]] = values[pair[1]]
			}
			target = types.ObjectValueMust(actual.AttributeTypes(ctx), values)
		}
		for _, protocol := range []string{"http", "https"} {
			if flag("enabled") {
				host := text(protocol + "_host")
				if _, ipErr := netip.ParseAddr(host); ipErr != nil && (host == "" || strings.ContainsAny(host, "/:?#@ \t\r\n\x00")) {
					return nil, errors.New("enabled proxy requires hostnames or IP addresses without URL syntax")
				}
				if p := values[protocol+"_port"].(types.Int64).ValueInt64(); p < 1 || p > 65535 {
					return nil, errors.New("enabled proxy ports must be from 1 to 65535")
				}
			}
		}
		if target.Equal(actual) && !rotate {
			return nil, nil
		}
		args := url.Values{}
		fields := map[string]string{}
		for name, f := range r.definition.fields {
			fields[f.wire] = name
		}
		retainServiceFields(target, args, fields)
		if rotate {
			args.Set("password", password.ValueString())
		}
		if d := checkpoint(proxyState(actual, prior, rotate)); d.HasError() {
			return nil, errors.New("cannot retain proxy failure state")
		}
		changed = true
		return args, nil
	})
	if err != nil {
		return types.Object{}, err
	}
	if !changed {
		return proxyState(actual, version.ValueInt64(), false), nil
	}
	if d := checkpoint(proxyState(target, version.ValueInt64(), false)); d.HasError() {
		return types.Object{}, errors.New("proxy write acknowledged but password generation could not be retained")
	}
	wait, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	for {
		actual, err = r.read(wait)
		if err != nil {
			return types.Object{}, err
		}
		if actual.Equal(target) {
			return proxyState(actual, version.ValueInt64(), false), nil
		}
		select {
		case <-wait.Done():
			return types.Object{}, errors.New("proxy update is unconfirmed; inspect DSM before retry")
		case <-time.After(250 * time.Millisecond):
		}
	}
}

func (r *networkProxyResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var version types.Int64
	resp.Diagnostics.Append(req.Plan.GetAttribute(ctx, path.Root("password_version"), &version)...)
	if version.ValueInt64() > 0 {
		resp.Diagnostics.AddError("Proxy password changes require import", "Import network_proxy before changing its password generation.")
		return
	}
	if resp.Diagnostics.HasError() {
		return
	}
	value, err := r.applyProxy(ctx, req.Config, req.Plan, 0, func(v types.Object) diag.Diagnostics { return resp.State.Set(ctx, v) })
	if err != nil {
		resp.Diagnostics.AddError("Cannot configure network proxy", err.Error())
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, value)...)
}
func (r *networkProxyResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	version, pending, d := proxyBaseline(ctx, req.State)
	resp.Diagnostics.Append(d...)
	if resp.Diagnostics.HasError() {
		return
	}
	actual, err := r.read(ctx)
	if err != nil {
		resp.Diagnostics.AddError("Cannot read network proxy", err.Error())
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, proxyState(actual, version, pending))...)
}
func (r *networkProxyResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	version, pending, d := proxyBaseline(ctx, req.State)
	resp.Diagnostics.Append(d...)
	if pending {
		resp.Diagnostics.AddError("Proxy password requires reconciliation", "Inspect DSM and explicitly release management without destroy, then import with password_version zero before retrying.")
	}
	if resp.Diagnostics.HasError() {
		return
	}
	value, err := r.applyProxy(ctx, req.Config, req.Plan, version, func(v types.Object) diag.Diagnostics { return resp.State.Set(ctx, v) })
	if err != nil {
		resp.Diagnostics.AddError("Cannot update network proxy", err.Error())
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, value)...)
}
func (*networkProxyResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	version, pending, d := proxyBaseline(ctx, req.State)
	resp.Diagnostics.Append(d...)
	if pending || version > 0 {
		resp.Diagnostics.AddError("Proxy credential baseline is protected", fmt.Sprintf("The applied password generation is %d. Inspect DSM and explicitly release management with a removed block using destroy = false; re-import without write-only secrets and reconcile generations before another rotation.", version))
	}
}
