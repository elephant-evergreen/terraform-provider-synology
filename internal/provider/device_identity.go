package provider

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/elephant-evergreen/terraform-provider-synology/internal/dsm"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-log/tflog"
)

const deviceSerialKey = "device_serial"

type privateState interface {
	GetKey(context.Context, string) ([]byte, diag.Diagnostics)
	SetKey(context.Context, string, []byte) diag.Diagnostics
}

var (
	_ resource.Resource                     = (*deviceBoundResource)(nil)
	_ resource.ResourceWithConfigure        = (*deviceBoundResource)(nil)
	_ resource.ResourceWithImportState      = (*deviceBoundResource)(nil)
	_ resource.ResourceWithValidateConfig   = (*deviceBoundResource)(nil)
	_ resource.ResourceWithModifyPlan       = (*deviceBoundResource)(nil)
	_ resource.ResourceWithConfigValidators = (*deviceBoundResource)(nil)
	_ resource.ResourceWithUpgradeState     = (*deviceBoundResource)(nil)
)

type deviceBoundResource struct {
	inner  resource.Resource
	client *dsm.Client
}

func newDeviceBoundResource(inner resource.Resource) resource.Resource {
	return &deviceBoundResource{inner: inner}
}

func (r *deviceBoundResource) Metadata(ctx context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	r.inner.Metadata(ctx, req, resp)
}

func (r *deviceBoundResource) Schema(ctx context.Context, req resource.SchemaRequest, resp *resource.SchemaResponse) {
	r.inner.Schema(ctx, req, resp)
}

func (r *deviceBoundResource) Configure(ctx context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	if req.ProviderData == nil {
		return
	}
	client, ok := req.ProviderData.(*dsm.Client)
	if !ok {
		resp.Diagnostics.AddError("Unexpected provider client", "Expected a DSM client.")
		return
	}
	r.client = client
	if inner, ok := r.inner.(resource.ResourceWithConfigure); ok {
		inner.Configure(ctx, req, resp)
	}
}

func (r *deviceBoundResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	r.inner.Create(ctx, req, resp)
	if resp.Diagnostics.HasError() {
		return
	}
	r.bind(ctx, resp.Private, &resp.Diagnostics)
}

func (r *deviceBoundResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	if !r.verify(ctx, req.Private, &resp.Diagnostics) {
		return
	}
	r.inner.Read(ctx, req, resp)
	if resp.Diagnostics.HasError() {
		return
	}
	r.bind(ctx, resp.Private, &resp.Diagnostics)
}

func (r *deviceBoundResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	if !r.verify(ctx, req.Private, &resp.Diagnostics) {
		return
	}
	r.inner.Update(ctx, req, resp)
	if resp.Diagnostics.HasError() {
		return
	}
	r.bind(ctx, resp.Private, &resp.Diagnostics)
}

func (r *deviceBoundResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	if !r.verify(ctx, req.Private, &resp.Diagnostics) {
		return
	}
	r.inner.Delete(ctx, req, resp)
}

func (r *deviceBoundResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	inner, ok := r.inner.(resource.ResourceWithImportState)
	if !ok {
		resp.Diagnostics.AddError("Import is not supported", "This resource cannot be imported.")
		return
	}
	inner.ImportState(ctx, req, resp)
	if resp.Diagnostics.HasError() {
		return
	}
	r.bind(ctx, resp.Private, &resp.Diagnostics)
}

func (r *deviceBoundResource) ValidateConfig(ctx context.Context, req resource.ValidateConfigRequest, resp *resource.ValidateConfigResponse) {
	if inner, ok := r.inner.(resource.ResourceWithValidateConfig); ok {
		inner.ValidateConfig(ctx, req, resp)
	}
}

func (r *deviceBoundResource) ModifyPlan(ctx context.Context, req resource.ModifyPlanRequest, resp *resource.ModifyPlanResponse) {
	if inner, ok := r.inner.(resource.ResourceWithModifyPlan); ok {
		inner.ModifyPlan(ctx, req, resp)
	}
}

func (r *deviceBoundResource) ConfigValidators(ctx context.Context) []resource.ConfigValidator {
	if inner, ok := r.inner.(resource.ResourceWithConfigValidators); ok {
		return inner.ConfigValidators(ctx)
	}
	return nil
}

func (r *deviceBoundResource) UpgradeState(ctx context.Context) map[int64]resource.StateUpgrader {
	if inner, ok := r.inner.(resource.ResourceWithUpgradeState); ok {
		return inner.UpgradeState(ctx)
	}
	return nil
}

func (r *deviceBoundResource) bind(ctx context.Context, private privateState, diags *diag.Diagnostics) {
	serial, err := r.client.Serial(ctx)
	if err != nil {
		if r.identityUnavailable(ctx, err) {
			return
		}
		diags.AddError("Cannot read the NAS identity", err.Error())
		return
	}
	encoded, err := json.Marshal(serial)
	if err != nil {
		diags.AddError("Cannot encode the NAS identity", err.Error())
		return
	}
	diags.Append(private.SetKey(ctx, deviceSerialKey, encoded)...)
}

func (*deviceBoundResource) identityUnavailable(ctx context.Context, err error) bool {
	var unsupported *dsm.UnsupportedAPIError
	if !errors.As(err, &unsupported) {
		return false
	}
	tflog.Warn(ctx, "Device identity is unavailable; state binding is skipped", map[string]any{"api": unsupported.API})
	return true
}

func (r *deviceBoundResource) verify(ctx context.Context, private privateState, diags *diag.Diagnostics) bool {
	expected, d := private.GetKey(ctx, deviceSerialKey)
	diags.Append(d...)
	if diags.HasError() {
		return false
	}
	if expected == nil {
		return true
	}
	var bound string
	if err := json.Unmarshal(expected, &bound); err != nil {
		diags.AddError("Cannot decode the NAS identity", "The stored device serial is not valid JSON. Inspect the state and refresh before changing objects.")
		return false
	}
	serial, err := r.client.Serial(ctx)
	if err != nil {
		if r.identityUnavailable(ctx, err) {
			return true
		}
		diags.AddError("Cannot read the NAS identity", err.Error())
		return false
	}
	if bound != serial {
		diags.AddError("Terraform state belongs to another NAS", fmt.Sprintf("The state was bound to a NAS with serial %q, but the configured endpoint reports %q. Refusing to read or change objects through the wrong device.", bound, serial))
		return false
	}
	return true
}
