package provider

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"strings"
	"time"

	"github.com/elephant-evergreen/terraform-provider-synology/internal/dsm"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/booldefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

const packagePendingKey = "package_operation"
const packageTimeout = 3 * time.Minute

type packageResource struct{ serviceResource }
type packageModel struct {
	ID                     types.String `tfsdk:"id"`
	PackageID              types.String `tfsdk:"package_id"`
	Name                   types.String `tfsdk:"name"`
	Version                types.String `tfsdk:"version"`
	Status                 types.String `tfsdk:"status"`
	Volume                 types.String `tfsdk:"volume"`
	Running                types.Bool   `tfsdk:"running"`
	Startable              types.Bool   `tfsdk:"startable"`
	CanUninstall           types.Bool   `tfsdk:"can_uninstall"`
	CustomUninstall        types.Bool   `tfsdk:"custom_uninstall"`
	AutomaticUpdates       types.Bool   `tfsdk:"automatic_updates"`
	ImportantUpdates       types.Bool   `tfsdk:"important_updates"`
	SilentUpgrade          types.Bool   `tfsdk:"silent_upgrade"`
	Beta                   types.Bool   `tfsdk:"beta"`
	DeletionProtection     types.Bool   `tfsdk:"deletion_protection"`
	ReconciliationRequired types.Bool   `tfsdk:"reconciliation_required"`
}
type packageTarget struct {
	ID      string `json:"id"`
	Version string `json:"version"`
	Volume  string `json:"volume"`
	Running *bool  `json:"running,omitempty"`
	Absent  bool   `json:"absent"`
}
type packageOperation struct {
	Target       packageTarget `json:"target"`
	Acknowledged bool          `json:"acknowledged"`
}

func NewPackageResource() resource.Resource {
	return &packageResource{serviceResource{definition: serviceDefinition{name: "package"}}}
}
func (*packageResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	attributes := map[string]schema.Attribute{
		"id":                      schema.StringAttribute{Computed: true, PlanModifiers: []planmodifier.String{stringplanmodifier.UseStateForUnknown()}, MarkdownDescription: "Stable installed-package ID and import identifier."},
		"package_id":              schema.StringAttribute{Required: true, PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()}, MarkdownDescription: "Installed DSM package identifier. Import is required; this resource never installs or reinstalls software."},
		"version":                 schema.StringAttribute{Computed: true, MarkdownDescription: "Observed installed version. Installation and exact-version upgrades are not exposed because the verified quick API does not bind its mutation to a selected version. Package Center separately owns automatic-update policy."},
		"volume":                  schema.StringAttribute{Computed: true, MarkdownDescription: "Observed installation volume: system or /volumeN. Relocation is not exposed."},
		"running":                 schema.BoolAttribute{Optional: true, Computed: true, MarkdownDescription: "Requested running policy. Omission preserves the stable existing policy. Unsupported or transient states are not reported as stopped."},
		"deletion_protection":     schema.BoolAttribute{Optional: true, Computed: true, Default: booldefault.StaticBool(true), MarkdownDescription: "Protect package and application data from uninstall by default. Disabling this in a separate reviewed apply permits deletion of an eligible package; package data may be removed by DSM."},
		"reconciliation_required": schema.BoolAttribute{Computed: true, MarkdownDescription: "An unfinished or uncertain package mutation is retained. Acknowledged work resumes with read-only polling; unacknowledged updates require explicit reconciliation."},
	}
	for name, description := range map[string]string{"name": "Observed display name.", "status": "Observed DSM status. Configuration readback is not evidence that application data has been restored."} {
		attributes[name] = schema.StringAttribute{Computed: true, MarkdownDescription: description}
	}
	for name, description := range map[string]string{"startable": "DSM allows start/stop operations.", "can_uninstall": "DSM permits package uninstallation.", "custom_uninstall": "Uninstallation requires a custom wizard and is refused by this resource.", "automatic_updates": "Observed automatic-update membership; package update policy has a separate owner.", "important_updates": "Observed important-update membership.", "silent_upgrade": "Observed silent-upgrade behavior.", "beta": "Observed beta-channel installation."} {
		attributes[name] = schema.BoolAttribute{Computed: true, MarkdownDescription: description}
	}
	resp.Schema = schema.Schema{MarkdownDescription: "Existing installed Synology package and running policy. Import is required and read-only. Version and placement are observed; installation, upgrade, downgrade and relocation are not exposed. Start/stop and protected uninstall require an eligible queue containing only the requested package. Package operations are serialized through readback. Built-ins and custom uninstall wizards remain protected. No custom SPK download or license/signature bypass is performed.", Attributes: attributes}
}
func packageModelOf(record installedPackage, protection bool) packageModel {
	running := types.BoolNull()
	if value, known := packageRunning(record.Status); known {
		running = types.BoolValue(value)
	}
	return packageModel{ID: types.StringValue(record.ID), PackageID: types.StringValue(record.ID), Name: types.StringValue(record.Name), Version: types.StringValue(record.Version), Status: types.StringValue(record.Status), Volume: types.StringValue(record.Volume), Running: running, Startable: types.BoolValue(record.Startable), CanUninstall: types.BoolValue(record.CanUninstall), CustomUninstall: types.BoolValue(record.CustomUninstall), AutomaticUpdates: types.BoolValue(record.AutomaticUpdates), ImportantUpdates: types.BoolValue(record.ImportantUpdates), SilentUpgrade: types.BoolValue(record.SilentUpgrade), Beta: types.BoolValue(record.Beta), DeletionProtection: types.BoolValue(protection), ReconciliationRequired: types.BoolValue(false)}
}
func readPackageOperation(ctx context.Context, private privateReader) (*packageOperation, diag.Diagnostics) {
	value, d := private.GetKey(ctx, packagePendingKey)
	if d.HasError() || value == nil {
		return nil, d
	}
	var operation packageOperation
	if json.Unmarshal(value, &operation) != nil || operation.Target.ID == "" || stringsControl(operation.Target.ID) || (!operation.Target.Absent && (operation.Target.Version == "" || operation.Target.Volume == "" || operation.Target.Running == nil)) {
		d.AddError("Invalid pending package operation", "Inspect DSM and reconcile explicitly; malformed operation metadata cannot authorize mutation.")
		return nil, d
	}
	return &operation, d
}
func packageTargetMatches(record installedPackage, target packageTarget) bool {
	if target.Absent || record.ID != target.ID || record.Version != target.Version || record.Volume != target.Volume || packageTransient(record.Status) {
		return false
	}
	if target.Running != nil {
		running, known := packageRunning(record.Status)
		return known && running == *target.Running
	}
	return true
}
func (r *packageResource) resumePackage(ctx context.Context, operation packageOperation) (*installedPackage, error) {
	wait, cancel := context.WithTimeout(ctx, packageTimeout)
	defer cancel()
	if !operation.Acknowledged && !operation.Target.Absent {
		return nil, errors.New("package write acknowledgement is uncertain; inspect DSM and explicitly release/import the confirmed installation")
	}
	for {
		records, err := readInstalledPackages(wait, r.client)
		if err != nil {
			return nil, err
		}
		record, exists := records[operation.Target.ID]
		if operation.Target.Absent {
			if !exists {
				return nil, nil
			}
			if !operation.Acknowledged {
				return nil, errors.New("package uninstall acknowledgement is uncertain and the package is still present; inspect DSM before another write")
			}
		} else if exists && packageTargetMatches(record, operation.Target) {
			return &record, nil
		}
		select {
		case <-wait.Done():
			return nil, errors.New("package target is not confirmed; pending operation is retained without retrying the mutation")
		case <-time.After(3 * time.Second):
		}
	}
}
func packageTargetOf(config, planned packageModel, current installedPackage) (packageTarget, error) {
	if config.PackageID.IsUnknown() || config.Running.IsUnknown() || planned.DeletionProtection.IsUnknown() || planned.DeletionProtection.IsNull() {
		return packageTarget{}, errors.New("package ID, running policy and deletion protection must be known before apply")
	}
	if config.PackageID.ValueString() != current.ID {
		return packageTarget{}, errors.New("package ID must match the imported installation")
	}
	target := packageTarget{ID: current.ID, Version: current.Version, Volume: current.Volume}
	if value, known := packageRunning(current.Status); known {
		target.Running = &value
	}
	if !config.Running.IsNull() {
		value := config.Running.ValueBool()
		target.Running = &value
	}
	return target, nil
}
func (r *packageResource) applyPackage(ctx context.Context, config tfsdk.Config, plan tfsdk.Plan, previous packageModel, checkpoint func(packageModel, *packageOperation) diag.Diagnostics) (packageModel, error) {
	wait, cancel := context.WithTimeout(ctx, packageTimeout)
	defer cancel()
	var configured, planned packageModel
	if d := config.Get(wait, &configured); d.HasError() {
		return packageModel{}, errors.New("cannot decode package configuration")
	}
	if d := plan.Get(wait, &planned); d.HasError() {
		return packageModel{}, errors.New("cannot decode package plan")
	}
	var result packageModel
	err := r.client.WithMutation(func() error {
		records, err := readInstalledPackages(wait, r.client)
		if err != nil {
			return err
		}
		record, exists := records[configured.PackageID.ValueString()]
		if !exists {
			return errors.New("package disappeared after planning; refresh and review before importing a separately installed package")
		}
		if record.ID != previous.ID.ValueString() || record.Version != previous.Version.ValueString() || record.Volume != previous.Volume.ValueString() || record.Status != previous.Status.ValueString() {
			return errors.New("package installation changed after planning; refresh and review before applying")
		}
		target, err := packageTargetOf(configured, planned, record)
		if err != nil {
			return err
		}
		protection := planned.DeletionProtection.ValueBool()
		if packageTargetMatches(record, target) {
			result = packageModelOf(record, protection)
			return nil
		}
		for _, installed := range records {
			if packageTransient(installed.Status) {
				return errors.New("DSM Package Center has an operation in progress; wait and refresh before applying")
			}
		}
		_, stable := packageRunning(record.Status)
		if !stable || !record.Startable || target.Running == nil {
			return errors.New("DSM does not expose an eligible stable package start/stop policy")
		}
		method := "start"
		args := url.Values{"id": {target.ID}}
		if !*target.Running {
			method = "stop"
		}
		if err := packageControlQueue(wait, r.client, target.ID, record.Version, method); err != nil {
			return err
		}
		if err := packageFeasibility(wait, r.client, target.ID, method); err != nil {
			return err
		}
		if method == "start" {
			args.Set("dsm_apps", dsm.JSON(strings.Fields(record.Apps)))
		}
		operation := &packageOperation{Target: target}
		baseline := packageModelOf(record, protection)
		baseline.ReconciliationRequired = types.BoolValue(true)
		save := func() error {
			if d := checkpoint(baseline, operation); d.HasError() {
				return errors.New("cannot retain package operation state")
			}
			return nil
		}
		if err := save(); err != nil {
			return err
		}
		var reply dsm.MutationReply
		if err := r.client.Call(wait, packageAPI+".Control", 1, method, args, &reply); err != nil {
			return err
		}
		if reply.Failed() || reply.TaskID != "" || (reply.Finish != nil && !*reply.Finish) {
			return errors.New("DSM returned failed or untracked package control; inspect and reconcile explicitly")
		}
		operation.Acknowledged = true
		if err := save(); err != nil {
			return err
		}
		confirmed, err := r.resumePackage(wait, *operation)
		if err != nil {
			return err
		}
		if confirmed == nil {
			return errors.New("package installation disappeared before confirmation")
		}
		result = packageModelOf(*confirmed, protection)
		return nil
	})
	return result, err
}
func (*packageResource) Create(_ context.Context, _ resource.CreateRequest, resp *resource.CreateResponse) {
	resp.Diagnostics.AddError("Package import required", "Import its package ID before managing running policy. Installation, reinstallation and exact-version upgrades are not exposed: the verified quick API does not bind its mutation to the selected version. Install through Package Center and import without changing software.")
}
func (r *packageResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var previous packageModel
	resp.Diagnostics.Append(req.State.Get(ctx, &previous)...)
	if resp.Diagnostics.HasError() {
		return
	}
	var operation *packageOperation
	if req.Private != nil {
		var d diag.Diagnostics
		operation, d = readPackageOperation(ctx, req.Private)
		resp.Diagnostics.Append(d...)
		if resp.Diagnostics.HasError() {
			return
		}
	}
	if operation != nil && operation.Target.ID != previous.ID.ValueString() {
		resp.Diagnostics.AddError("Package operation identity changed", "Inspect DSM and reconcile explicitly; another package cannot be adopted through pending state.")
		return
	}
	if operation == nil && previous.ReconciliationRequired.ValueBool() {
		resp.Diagnostics.AddError("Missing package operation baseline", "Inspect DSM and reconcile explicitly; refresh cannot discard lifecycle protection.")
		return
	}
	var current *installedPackage
	var err error
	if operation != nil {
		current, err = r.resumePackage(ctx, *operation)
	} else {
		var records map[string]installedPackage
		records, err = readInstalledPackages(ctx, r.client)
		if value, exists := records[previous.ID.ValueString()]; exists {
			current = &value
		}
	}
	if err != nil {
		resp.Diagnostics.AddError("Cannot read package", err.Error())
		return
	}
	if current == nil {
		resp.State.RemoveResource(ctx)
		if operation != nil {
			resp.Diagnostics.Append(resp.Private.SetKey(ctx, packagePendingKey, nil)...)
		}
		return
	}
	protection := true
	if !previous.DeletionProtection.IsNull() {
		protection = previous.DeletionProtection.ValueBool()
	}
	value := packageModelOf(*current, protection)
	resp.Diagnostics.Append(resp.State.Set(ctx, &value)...)
	if operation != nil {
		resp.Diagnostics.Append(resp.Private.SetKey(ctx, packagePendingKey, nil)...)
	}
}
func (r *packageResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var previous packageModel
	resp.Diagnostics.Append(req.State.Get(ctx, &previous)...)
	if req.Private != nil {
		operation, d := readPackageOperation(ctx, req.Private)
		resp.Diagnostics.Append(d...)
		if operation != nil {
			resp.Diagnostics.AddError("Pending package operation", "Refresh acknowledged work before updating, or reconcile an unacknowledged operation explicitly.")
		}
	}
	if previous.ReconciliationRequired.ValueBool() {
		resp.Diagnostics.AddError("Pending package operation", "Inspect DSM and reconcile before another mutation.")
	}
	if resp.Diagnostics.HasError() {
		return
	}
	value, err := r.applyPackage(ctx, req.Config, req.Plan, previous, func(model packageModel, operation *packageOperation) diag.Diagnostics {
		d := resp.State.Set(ctx, &model)
		d.Append(resp.Private.SetKey(ctx, packagePendingKey, []byte(dsm.JSON(operation)))...)
		return d
	})
	if err != nil {
		resp.Diagnostics.AddError("Cannot update package", err.Error())
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &value)...)
	resp.Diagnostics.Append(resp.Private.SetKey(ctx, packagePendingKey, nil)...)
}
func (r *packageResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var previous packageModel
	resp.Diagnostics.Append(req.State.Get(ctx, &previous)...)
	if req.Private != nil {
		operation, d := readPackageOperation(ctx, req.Private)
		resp.Diagnostics.Append(d...)
		if operation != nil {
			resp.Diagnostics.AddError("Pending package operation", "Refresh acknowledged work or reconcile explicitly before uninstalling.")
		}
	}
	if previous.DeletionProtection.IsNull() || previous.DeletionProtection.ValueBool() || previous.ReconciliationRequired.ValueBool() {
		resp.Diagnostics.AddError("Package is protected", "Disable deletion_protection in a separate reviewed apply after resolving pending work. DSM uninstallation can delete application data; use destroy = false to release management without uninstalling.")
	}
	if resp.Diagnostics.HasError() {
		return
	}
	wait, cancel := context.WithTimeout(ctx, packageTimeout)
	defer cancel()
	err := r.client.WithMutation(func() error {
		records, err := readInstalledPackages(wait, r.client)
		if err != nil {
			return err
		}
		record, exists := records[previous.ID.ValueString()]
		if !exists {
			return nil
		}
		if record.Version != previous.Version.ValueString() || record.Volume != previous.Volume.ValueString() {
			return errors.New("package installation changed after planning; uninstallation was refused")
		}
		if !record.CanUninstall || record.CustomUninstall {
			return errors.New("DSM marks this package as built-in, uninstall-protected or requiring a custom wizard")
		}
		if err := packageControlQueue(wait, r.client, record.ID, record.Version, "uninstall"); err != nil {
			return err
		}
		if err := packageFeasibility(wait, r.client, record.ID, "uninstall"); err != nil {
			return err
		}
		operation := &packageOperation{Target: packageTarget{ID: record.ID, Absent: true}}
		previous.ReconciliationRequired = types.BoolValue(true)
		save := func() error {
			d := resp.State.Set(ctx, &previous)
			d.Append(resp.Private.SetKey(ctx, packagePendingKey, []byte(dsm.JSON(operation)))...)
			resp.Diagnostics.Append(d...)
			if d.HasError() {
				return errors.New("cannot retain package uninstall state")
			}
			return nil
		}
		if err := save(); err != nil {
			return err
		}
		var reply dsm.MutationReply
		if err := r.client.Call(wait, packageAPI+".Uninstallation", 1, "uninstall", url.Values{"id": {record.ID}, "dsm_apps": {record.Apps}}, &reply); err != nil {
			return err
		}
		if reply.Failed() || reply.TaskID != "" || (reply.Finish != nil && !*reply.Finish) {
			return errors.New("DSM returned failed or untracked package uninstallation; reconcile explicitly")
		}
		operation.Acknowledged = true
		if err := save(); err != nil {
			return err
		}
		_, err = r.resumePackage(wait, *operation)
		return err
	})
	if err != nil {
		resp.Diagnostics.AddError("Cannot uninstall package", err.Error())
	}
}
func (*packageResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	if req.ID == "" || stringsControl(req.ID) || strings.ContainsAny(req.ID, "/\\") {
		resp.Diagnostics.AddError("Invalid package import ID", "Use the exact installed-package ID reported by data.synology_packages.")
		return
	}
	resource.ImportStatePassthroughID(ctx, path.Root("id"), req, resp)
}
