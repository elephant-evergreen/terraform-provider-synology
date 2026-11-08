package provider

import (
	"context"
	"errors"
	"net/url"
	"regexp"
	"sort"
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

const quickConnectAPI = "SYNO.Core.QuickConnect"

var quickConnectIDPattern = regexp.MustCompile(`^[A-Za-z](?:[A-Za-z0-9-]*[A-Za-z0-9])?$`)

type quickConnectResource struct{ serviceResource }
type quickConnectModel struct {
	ID                     types.String `tfsdk:"id"`
	Enabled                types.Bool   `tfsdk:"enabled"`
	Identifier             types.String `tfsdk:"identifier"`
	RelayEnabled           types.Bool   `tfsdk:"relay_enabled"`
	Services               types.Map    `tfsdk:"services"`
	RegisteredAccount      types.String `tfsdk:"registered_account"`
	ReconciliationRequired types.Bool   `tfsdk:"reconciliation_required"`
}

func NewQuickConnectResource() resource.Resource {
	return &quickConnectResource{serviceResource{definition: serviceDefinition{name: "quickconnect"}}}
}
func (*quickConnectResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{MarkdownDescription: "QuickConnect enabled policy, identifier, relay policy and complete service permissions. Import is required before any mutation. Synology account enrollment and browser/real-name consent remain in DSM. A successful configuration read does not prove external connectivity. Automatic router port forwarding is owned separately by synology_quickconnect_upnp. Destroy leaves QuickConnect and its registration unchanged.", Attributes: map[string]schema.Attribute{
		"id":                      schema.StringAttribute{Computed: true, MarkdownDescription: "Singleton import identity: quickconnect.", PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()}},
		"enabled":                 schema.BoolAttribute{Optional: true, Computed: true, MarkdownDescription: "Enable QuickConnect using the existing Synology account binding."},
		"identifier":              schema.StringAttribute{Optional: true, Computed: true, MarkdownDescription: "Customized QuickConnect ID. Omission preserves it; changing it requires enabled QuickConnect and an existing account binding."},
		"relay_enabled":           schema.BoolAttribute{Optional: true, Computed: true, MarkdownDescription: "Allow use of Synology relay servers."},
		"services":                schema.MapAttribute{Optional: true, Computed: true, ElementType: types.BoolType, MarkdownDescription: "Complete permission map keyed by DSM service identifier. Include all currently returned services; missing or unknown service IDs are refused rather than silently dropped."},
		"registered_account":      schema.StringAttribute{Computed: true, Sensitive: true, MarkdownDescription: "Observed Synology account binding. This is configuration metadata and enters state; no account password or session is retained."},
		"reconciliation_required": schema.BoolAttribute{Computed: true, MarkdownDescription: "An interrupted configuration write requires DSM inspection and explicit release/import before another write."},
	}}
}
func (r *quickConnectResource) readQuickConnect(ctx context.Context) (quickConnectModel, error) {
	var main struct {
		Enabled    *bool   `json:"enabled"`
		Identifier *string `json:"server_alias"`
		Account    *string `json:"myds_account"`
	}
	if err := r.client.Call(ctx, quickConnectAPI, 2, "get", nil, &main); err != nil {
		return quickConnectModel{}, err
	}
	var misc struct {
		Relay *bool `json:"relay_enabled"`
	}
	if err := r.client.Call(ctx, quickConnectAPI, 3, "get_misc_config", nil, &misc); err != nil {
		return quickConnectModel{}, err
	}
	var permissions struct {
		Services *[]struct {
			ID      *string `json:"id"`
			Enabled *bool   `json:"enabled"`
		} `json:"services"`
	}
	if err := r.client.Call(ctx, quickConnectAPI+".Permission", 1, "get", nil, &permissions); err != nil {
		return quickConnectModel{}, err
	}
	if main.Enabled == nil || main.Identifier == nil || main.Account == nil || misc.Relay == nil || permissions.Services == nil {
		return quickConnectModel{}, errors.New("DSM omitted a required QuickConnect policy setting")
	}
	services := map[string]bool{}
	for _, service := range *permissions.Services {
		if service.ID == nil || *service.ID == "" || service.Enabled == nil {
			return quickConnectModel{}, errors.New("DSM returned incomplete QuickConnect permissions")
		}
		if _, exists := services[*service.ID]; exists {
			return quickConnectModel{}, errors.New("DSM returned duplicate QuickConnect service IDs")
		}
		services[*service.ID] = *service.Enabled
	}
	value, d := types.MapValueFrom(ctx, types.BoolType, services)
	if d.HasError() {
		return quickConnectModel{}, errors.New("cannot decode QuickConnect permission map")
	}
	return quickConnectModel{ID: types.StringValue("quickconnect"), Enabled: types.BoolValue(*main.Enabled), Identifier: types.StringValue(*main.Identifier), RelayEnabled: types.BoolValue(*misc.Relay), Services: value, RegisteredAccount: types.StringValue(*main.Account), ReconciliationRequired: types.BoolValue(false)}, nil
}
func quickConnectTarget(config, actual quickConnectModel) (quickConnectModel, error) {
	if config.Enabled.IsUnknown() || config.Identifier.IsUnknown() || config.RelayEnabled.IsUnknown() || config.Services.IsUnknown() {
		return quickConnectModel{}, errors.New("QuickConnect policy must be known before apply")
	}
	target := actual
	if !config.Enabled.IsNull() {
		target.Enabled = config.Enabled
	}
	if !config.Identifier.IsNull() {
		target.Identifier = config.Identifier
	}
	if !config.RelayEnabled.IsNull() {
		target.RelayEnabled = config.RelayEnabled
	}
	if !config.Services.IsNull() {
		target.Services = config.Services
	}
	active := false
	current := actual.Services.Elements()
	if len(current) != len(target.Services.Elements()) {
		return quickConnectModel{}, errors.New("services must include every current DSM QuickConnect service ID")
	}
	for id, value := range target.Services.Elements() {
		if _, ok := current[id]; !ok || value.IsUnknown() || value.IsNull() {
			return quickConnectModel{}, errors.New("services must contain only current DSM service IDs with known booleans")
		}
		active = active || value.(types.Bool).ValueBool()
	}
	if target.Enabled.ValueBool() && !active {
		return quickConnectModel{}, errors.New("enabled QuickConnect requires at least one permitted service")
	}
	if !target.Identifier.Equal(actual.Identifier) {
		if !target.Enabled.ValueBool() || !quickConnectIDPattern.MatchString(target.Identifier.ValueString()) {
			return quickConnectModel{}, errors.New("changing identifier requires enabled QuickConnect and an ID containing letters, digits or hyphens, starting with a letter and not ending with a hyphen")
		}
	}
	if (target.Enabled.ValueBool() && !actual.Enabled.ValueBool() || !target.Identifier.Equal(actual.Identifier)) && actual.RegisteredAccount.ValueString() == "" {
		return quickConnectModel{}, errors.New("bind and authorize a Synology account in DSM before enabling or registering QuickConnect")
	}
	return target, nil
}
func quickConnectMatches(a, b quickConnectModel) bool {
	return a.Enabled.Equal(b.Enabled) && a.Identifier.Equal(b.Identifier) && a.RelayEnabled.Equal(b.RelayEnabled) && a.Services.Equal(b.Services)
}
func (r *quickConnectResource) applyQuickConnect(ctx context.Context, config tfsdk.Config, checkpoint func(quickConnectModel) diag.Diagnostics) (quickConnectModel, error) {
	var configured quickConnectModel
	if d := config.Get(ctx, &configured); d.HasError() {
		return quickConnectModel{}, errors.New("cannot decode QuickConnect configuration")
	}
	var actual, target quickConnectModel
	err := r.client.WithMutation(func() error {
		var err error
		actual, err = r.readQuickConnect(ctx)
		if err != nil {
			return err
		}
		target, err = quickConnectTarget(configured, actual)
		if err != nil {
			return err
		}
		if quickConnectMatches(actual, target) {
			return nil
		}
		write := func(api string, version int, method string, args url.Values) error {
			actual.ReconciliationRequired = types.BoolValue(true)
			if d := checkpoint(actual); d.HasError() {
				return errors.New("cannot retain QuickConnect failure state")
			}
			var reply dsm.MutationReply
			if err := r.client.Call(ctx, api, version, method, args, &reply); err != nil {
				return err
			}
			if reply.Failed() || reply.TaskID != "" || (reply.Finish != nil && !*reply.Finish) {
				return errors.New("DSM returned a failed or untracked asynchronous QuickConnect result; inspect and reconcile before retrying")
			}
			actual, err = r.readQuickConnect(ctx)
			if err != nil {
				return err
			}
			actual.ReconciliationRequired = types.BoolValue(true)
			if d := checkpoint(actual); d.HasError() {
				return errors.New("cannot retain partially updated QuickConnect policy")
			}
			return nil
		}
		if !target.Enabled.ValueBool() && actual.Enabled.ValueBool() {
			if err := write(quickConnectAPI, 2, "set", url.Values{"enabled": {"false"}}); err != nil {
				return err
			}
		}
		if !target.Services.Equal(actual.Services) {
			type permission struct {
				ID      string `json:"id"`
				Enabled bool   `json:"enabled"`
			}
			services := []permission{}
			ids := []string{}
			for id := range target.Services.Elements() {
				ids = append(ids, id)
			}
			sort.Strings(ids)
			for _, id := range ids {
				services = append(services, permission{id, target.Services.Elements()[id].(types.Bool).ValueBool()})
			}
			if err := write(quickConnectAPI+".Permission", 1, "set", url.Values{"services": {dsm.JSON(services)}}); err != nil {
				return err
			}
		}
		if !target.RelayEnabled.Equal(actual.RelayEnabled) {
			if err := write(quickConnectAPI, 3, "set_misc_config", url.Values{"relay_enabled": {strconv.FormatBool(target.RelayEnabled.ValueBool())}}); err != nil {
				return err
			}
		}
		if !target.Identifier.Equal(actual.Identifier) {
			if err := write(quickConnectAPI, 2, "set_server_alias", url.Values{"server_alias": {target.Identifier.ValueString()}}); err != nil {
				return err
			}
		}
		if !target.Enabled.Equal(actual.Enabled) {
			if err := write(quickConnectAPI, 2, "set", url.Values{"enabled": {strconv.FormatBool(target.Enabled.ValueBool())}}); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return quickConnectModel{}, err
	}
	wait, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	for {
		actual, err = r.readQuickConnect(wait)
		if err != nil {
			return quickConnectModel{}, err
		}
		if quickConnectMatches(actual, target) {
			return actual, nil
		}
		select {
		case <-wait.Done():
			return quickConnectModel{}, errors.New("QuickConnect policy convergence is unconfirmed; inspect DSM before another apply")
		case <-time.After(3 * time.Second):
		}
	}
}
func (*quickConnectResource) Create(_ context.Context, _ resource.CreateRequest, resp *resource.CreateResponse) {
	resp.Diagnostics.AddError("QuickConnect changes require import", "Import quickconnect before configuring it. Account enrollment and registration already exist outside this always-present service; replacement cannot discard an interrupted-operation baseline.")
}
func (r *quickConnectResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var previous quickConnectModel
	resp.Diagnostics.Append(req.State.Get(ctx, &previous)...)
	if resp.Diagnostics.HasError() {
		return
	}
	actual, err := r.readQuickConnect(ctx)
	if err != nil {
		resp.Diagnostics.AddError("Cannot read QuickConnect", err.Error())
		return
	}
	actual.ReconciliationRequired = types.BoolValue(previous.ReconciliationRequired.ValueBool())
	resp.Diagnostics.Append(resp.State.Set(ctx, &actual)...)
}
func (r *quickConnectResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var previous quickConnectModel
	resp.Diagnostics.Append(req.State.Get(ctx, &previous)...)
	if previous.ReconciliationRequired.ValueBool() {
		resp.Diagnostics.AddError("QuickConnect requires reconciliation", "Inspect DSM, release management explicitly with destroy = false, then import the confirmed policy before another change.")
	}
	if resp.Diagnostics.HasError() {
		return
	}
	actual, err := r.applyQuickConnect(ctx, req.Config, func(v quickConnectModel) diag.Diagnostics { return resp.State.Set(ctx, &v) })
	if err != nil {
		resp.Diagnostics.AddError("Cannot update QuickConnect", err.Error())
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &actual)...)
}
func (*quickConnectResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var pending types.Bool
	resp.Diagnostics.Append(req.State.GetAttribute(ctx, path.Root("reconciliation_required"), &pending)...)
	if pending.ValueBool() {
		resp.Diagnostics.AddError("QuickConnect requires reconciliation", "Inspect DSM and explicitly release management with a removed block using destroy = false.")
	}
}
