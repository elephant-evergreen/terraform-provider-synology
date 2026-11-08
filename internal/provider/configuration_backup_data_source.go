package provider

import (
	"context"
	"github.com/elephant-evergreen/terraform-provider-synology/internal/dsm"
	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

type configurationBackupDataSource struct{ serviceResource }

func NewConfigurationBackupDataSource() datasource.DataSource {
	return &configurationBackupDataSource{serviceResource{definition: serviceDefinition{name: "configuration_backup", api: "SYNO.Backup.Config.AutoBackup", version: 1, fields: map[string]serviceField{
		"enabled":             {"enable", types.BoolType, "Observed automatic configuration-backup policy."},
		"encryption_method":   {"enc_method", types.StringType, "Observed encryption method. Passwords and recovery keys never enter state."},
		"last_status":         {"last_status", types.StringType, "Last backup result. An enabled policy does not prove a successful backup."},
		"last_check_time":     {"last_check_time", types.StringType, "Last check time returned by DSM."},
		"last_success_time":   {"last_success_time", types.StringType, "Last successful backup time returned by DSM."},
		"failure_reason_code": {"fail_reason_code", types.Int64Type, "Last DSM failure reason code."},
	}}}}
}
func (*configurationBackupDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_configuration_backup"
}
func (r *configurationBackupDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	attributes := map[string]schema.Attribute{"id": schema.StringAttribute{Computed: true, MarkdownDescription: "Stable discovery identity: configuration_backup."}}
	for name, field := range r.definition.fields {
		switch field.kind {
		case types.BoolType:
			attributes[name] = schema.BoolAttribute{Computed: true, MarkdownDescription: field.description}
		case types.StringType:
			attributes[name] = schema.StringAttribute{Computed: true, MarkdownDescription: field.description}
		case types.Int64Type:
			attributes[name] = schema.Int64Attribute{Computed: true, MarkdownDescription: field.description}
		}
	}
	resp.Schema = schema.Schema{MarkdownDescription: "Read-only automatic DSM configuration-backup policy and status. No backup, restore, enable or encryption-key operation is performed. The DSM setter can return new recovery-key material; mutations remain outside the provider until a complete key-delivery and recovery contract is established. Configuration backup does not back up shared-folder data.", Attributes: attributes}
}
func (r *configurationBackupDataSource) Configure(_ context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	if req.ProviderData == nil {
		return
	}
	client, ok := req.ProviderData.(*dsm.Client)
	if !ok {
		resp.Diagnostics.AddError("Unexpected provider client", "Expected a DSM client.")
		return
	}
	r.client = client
}
func (r *configurationBackupDataSource) Read(ctx context.Context, _ datasource.ReadRequest, resp *datasource.ReadResponse) {
	actual, err := r.read(ctx)
	if err != nil {
		resp.Diagnostics.AddError("Cannot read configuration backup", err.Error())
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, actual)...)
}
