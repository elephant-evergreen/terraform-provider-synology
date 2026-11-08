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
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

type packageCenterResource struct{ serviceResource }

func NewPackageCenterResource() resource.Resource {
	return &packageCenterResource{serviceResource{definition: serviceDefinition{name: "package_center", api: packageAPI + ".Setting", version: 1, description: "Package Center update-channel, notification and automatic-update policy. Package installations and running state have separate owners. Import preserves current settings; destroy releases management without disabling updates. Trust is observed only because this DSM UI does not establish a corresponding setter. Automatic updates can change software and application data later; an enabled policy does not wait for those future operations.", fields: map[string]serviceField{
		"enabled":             {"enable_autoupdate", types.BoolType, "Enable automatic package updates."},
		"update_all":          {"autoupdateall", types.BoolType, "Automatically install all package updates. Mutually exclusive with important_only."},
		"important_only":      {"autoupdateimportant", types.BoolType, "Automatically install important updates for all eligible packages."},
		"packages":            {"packages", types.SetType{ElemType: types.StringType}, "Complete selected set for all updates in enabled custom mode. Omit outside custom mode; observed membership remains readable."},
		"important_packages":  {"packages_important", types.SetType{ElemType: types.StringType}, "Complete selected set for important updates in enabled custom mode. Must be disjoint from packages."},
		"beta_channel":        {"update_channel", types.BoolType, "Enable the beta update channel in Package Center."},
		"email_notifications": {"enable_email", types.BoolType, "Send package notifications by email. Requires an existing DSM mail configuration."},
		"dsm_notifications":   {"enable_dsm", types.BoolType, "Show package notifications in DSM."},
		"trust_level":         {"trust_level", types.Int64Type, "Observed legacy package trust-level value. No trust or signature override is performed."},
		"mail_configured":     {"mailset", types.BoolType, "DSM reports an existing mail setup. SMTP configuration is not changed here."},
	}}}}
}
func (r *packageCenterResource) Schema(ctx context.Context, req resource.SchemaRequest, resp *resource.SchemaResponse) {
	r.serviceResource.Schema(ctx, req, resp)
	resp.Schema.Attributes["trust_level"] = schema.Int64Attribute{Computed: true, MarkdownDescription: r.definition.fields["trust_level"].description}
	resp.Schema.Attributes["mail_configured"] = schema.BoolAttribute{Computed: true, MarkdownDescription: r.definition.fields["mail_configured"].description}
}
func (r *packageCenterResource) readPackageCenter(ctx context.Context) (types.Object, error) {
	var raw map[string]json.RawMessage
	if err := r.client.Call(ctx, r.definition.api, 1, "get", nil, &raw); err != nil {
		return types.Object{}, err
	}
	packages, err := readInstalledPackages(ctx, r.client)
	if err != nil {
		return types.Object{}, err
	}
	all, important := []string{}, []string{}
	for id, pkg := range packages {
		if pkg.AutomaticUpdates {
			all = append(all, id)
		}
		if pkg.ImportantUpdates {
			important = append(important, id)
		}
	}
	raw["packages"], raw["packages_important"] = json.RawMessage(dsm.JSON(all)), json.RawMessage(dsm.JSON(important))
	return r.decodeSettings(ctx, raw)
}
func packageCenterCustom(values map[string]attr.Value) bool {
	return values["enabled"].(types.Bool).ValueBool() && !values["update_all"].(types.Bool).ValueBool() && !values["important_only"].(types.Bool).ValueBool()
}
func (r *packageCenterResource) applyPackageCenter(ctx context.Context, config tfsdk.Config) (types.Object, error) {
	desired, _, err := r.desired(ctx, config)
	if err != nil {
		return types.Object{}, err
	}
	var actual types.Object
	err = r.client.WithMutation(func() error {
		var err error
		actual, err = r.readPackageCenter(ctx)
		if err != nil {
			return err
		}
		if serviceMatches(actual, desired) {
			return nil
		}
		values := actual.Attributes()
		for name, value := range desired {
			values[name] = value
		}
		custom := packageCenterCustom(values)
		if values["update_all"].(types.Bool).ValueBool() && values["important_only"].(types.Bool).ValueBool() {
			return errors.New("update_all and important_only are mutually exclusive")
		}
		if values["email_notifications"].(types.Bool).ValueBool() && !values["mail_configured"].(types.Bool).ValueBool() {
			return errors.New("configure DSM mail delivery before enabling package email notifications")
		}
		_, allConfigured := desired["packages"]
		_, importantConfigured := desired["important_packages"]
		if !custom && (allConfigured || importantConfigured) {
			return errors.New("configure selected package sets only in enabled custom mode; omit them for disabled, all-update or important-only policy")
		}
		all, important := []string{}, []string{}
		if d := values["packages"].(types.Set).ElementsAs(ctx, &all, false); d.HasError() {
			return errors.New("selected package IDs must be known")
		}
		if d := values["important_packages"].(types.Set).ElementsAs(ctx, &important, false); d.HasError() {
			return errors.New("selected important-update package IDs must be known")
		}
		if custom && len(all)+len(important) == 0 {
			return errors.New("enabled custom update policy requires at least one selected package")
		}
		selected := map[string]bool{}
		if custom {
			for _, id := range all {
				selected[id] = true
			}
			for _, id := range important {
				if selected[id] {
					return errors.New("packages and important_packages must be disjoint")
				}
			}
		}
		if custom {
			records, err := readInstalledPackages(ctx, r.client)
			if err != nil {
				return err
			}
			for _, id := range append(append([]string{}, all...), important...) {
				if _, exists := records[id]; !exists {
					return errors.New("custom update policy can select only currently installed package IDs")
				}
			}
		}
		target := types.ObjectValueMust(actual.AttributeTypes(ctx), values)
		args := url.Values{}
		for name, field := range r.definition.fields {
			if name == "trust_level" || name == "mail_configured" || name == "packages" || name == "important_packages" {
				continue
			}
			flag := values[name].(types.Bool).ValueBool()
			text := strconv.FormatBool(flag)
			if name == "beta_channel" {
				text = "stable"
				if flag {
					text = "beta"
				}
			}
			args.Set(field.wire, text)
		}
		if custom && (!packageCenterCustom(actual.Attributes()) || !values["packages"].Equal(actual.Attributes()["packages"]) || !values["important_packages"].Equal(actual.Attributes()["important_packages"])) {
			args.Set("packages", dsm.JSON(all))
			args.Set("packages_important", dsm.JSON(important))
		}
		var reply dsm.MutationReply
		if err := r.client.Call(ctx, r.definition.api, 1, "set", args, &reply); err != nil {
			return err
		}
		if reply.Failed() || reply.TaskID != "" || (reply.Finish != nil && !*reply.Finish) {
			return errors.New("DSM returned failed or untracked Package Center policy work; inspect configuration before retrying")
		}
		wait, cancel := context.WithTimeout(ctx, 90*time.Second)
		defer cancel()
		for {
			actual, err = r.readPackageCenter(wait)
			if err != nil {
				return err
			}
			if serviceMatches(actual, desired) {
				return nil
			}
			if !actual.Attributes()["mail_configured"].Equal(target.Attributes()["mail_configured"]) {
				return errors.New("DSM mail configuration changed while applying package policy")
			}
			select {
			case <-wait.Done():
				return errors.New("Package Center policy is unconfirmed; refresh before retrying")
			case <-time.After(3 * time.Second):
			}
		}
	})
	return actual, err
}
func (r *packageCenterResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	prior, err := r.readPackageCenter(ctx)
	if err != nil {
		resp.Diagnostics.AddError("Cannot inspect Package Center", err.Error())
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, prior)...)
	if resp.Diagnostics.HasError() {
		return
	}
	value, err := r.applyPackageCenter(ctx, req.Config)
	if err != nil {
		resp.Diagnostics.AddError("Cannot configure Package Center", err.Error())
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, value)...)
}
func (r *packageCenterResource) Read(ctx context.Context, _ resource.ReadRequest, resp *resource.ReadResponse) {
	value, err := r.readPackageCenter(ctx)
	if err != nil {
		resp.Diagnostics.AddError("Cannot read Package Center", err.Error())
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, value)...)
}
func (r *packageCenterResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	value, err := r.applyPackageCenter(ctx, req.Config)
	if err != nil {
		resp.Diagnostics.AddError("Cannot update Package Center", err.Error())
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, value)...)
}
