package provider

import (
	"context"
	"os"

	"github.com/elephant-evergreen/terraform-provider-synology/internal/dsm"
	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/provider"
	"github.com/hashicorp/terraform-plugin-framework/provider/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

type synologyProvider struct{ version string }
type providerModel struct {
	Endpoint types.String `tfsdk:"endpoint"`
	Username types.String `tfsdk:"username"`
	Password types.String `tfsdk:"password"`
	OTP      types.String `tfsdk:"otp"`
	CAFile   types.String `tfsdk:"ca_file"`
}

func New(version string) func() provider.Provider {
	return func() provider.Provider { return &synologyProvider{version: version} }
}

func (p *synologyProvider) Metadata(_ context.Context, _ provider.MetadataRequest, resp *provider.MetadataResponse) {
	resp.TypeName = "synology"
	resp.Version = p.version
}

func (*synologyProvider) Schema(_ context.Context, _ provider.SchemaRequest, resp *provider.SchemaResponse) {
	resp.Schema = schema.Schema{MarkdownDescription: "Manage Synology DSM configuration directly over verified HTTPS. Import existing objects before managing them.", Attributes: map[string]schema.Attribute{
		"endpoint": schema.StringAttribute{Optional: true, MarkdownDescription: "DSM HTTPS origin, such as https://nas.example.test:5001. Falls back to SYNOLOGY_ENDPOINT. Certificate verification is mandatory."},
		"username": schema.StringAttribute{Optional: true, Sensitive: true, MarkdownDescription: "Existing DSM account. Configure username and password together, or supply both SYNOLOGY_USERNAME and SYNOLOGY_PASSWORD."},
		"password": schema.StringAttribute{Optional: true, Sensitive: true, MarkdownDescription: "Existing DSM password. Supports ephemeral values. Falls back with username to SYNOLOGY_PASSWORD; never mixed across credential sources."},
		"otp":      schema.StringAttribute{Optional: true, Sensitive: true, MarkdownDescription: "Current one-time password, if required. Falls back to SYNOLOGY_OTP. Not a TOTP seed; never persisted by this provider."},
		"ca_file":  schema.StringAttribute{Optional: true, MarkdownDescription: "Optional PEM certificate-authority bundle added to the system trust store. Falls back to SYNOLOGY_CA_FILE. Verification cannot be disabled."},
	}}
}

func (*synologyProvider) Configure(ctx context.Context, req provider.ConfigureRequest, resp *provider.ConfigureResponse) {
	var config providerModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &config)...)
	if resp.Diagnostics.HasError() {
		return
	}
	for name, value := range map[string]types.String{"endpoint": config.Endpoint, "username": config.Username, "password": config.Password, "otp": config.OTP, "ca_file": config.CAFile} {
		if value.IsUnknown() {
			resp.Diagnostics.AddAttributeError(path.Root(name), "Unknown provider configuration", "The provider needs "+name+" before planning. Supply a known configuration value or its documented environment variable.")
		}
	}
	if resp.Diagnostics.HasError() {
		return
	}
	username, password := config.Username.ValueString(), config.Password.ValueString()
	if username != "" || password != "" {
		if username == "" || password == "" {
			resp.Diagnostics.AddError("Incomplete configured credentials", "Set both username and password in the provider configuration. Credentials are never combined with environment variables.")
			return
		}
	} else {
		username, password = os.Getenv("SYNOLOGY_USERNAME"), os.Getenv("SYNOLOGY_PASSWORD")
	}
	if username == "" || password == "" {
		resp.Diagnostics.AddError("No complete DSM credentials", "Sources tried: provider username/password, then SYNOLOGY_USERNAME/SYNOLOGY_PASSWORD. Supply a complete pair. See https://registry.terraform.io/providers/elephant-evergreen/synology/latest/docs.")
		return
	}
	endpoint := fallback(config.Endpoint, "SYNOLOGY_ENDPOINT")
	client, err := dsm.New(endpoint, username, password, fallback(config.OTP, "SYNOLOGY_OTP"), fallback(config.CAFile, "SYNOLOGY_CA_FILE"))
	if err != nil {
		resp.Diagnostics.AddError("Invalid DSM connection settings", err.Error())
		return
	}
	if err := client.Connect(ctx); err != nil {
		resp.Diagnostics.AddError("Cannot authenticate to DSM", err.Error())
		return
	}
	resp.ResourceData = client
	resp.DataSourceData = client
}

func fallback(v types.String, env string) string {
	if !v.IsNull() {
		return v.ValueString()
	}
	return os.Getenv(env)
}

func (*synologyProvider) Resources(context.Context) []func() resource.Resource {
	return append([]func() resource.Resource{NewSharedFolderResource, NewUserResource, NewGroupResource, NewSharePermissionResource}, serviceResources()...)
}

func (*synologyProvider) DataSources(context.Context) []func() datasource.DataSource { return nil }
