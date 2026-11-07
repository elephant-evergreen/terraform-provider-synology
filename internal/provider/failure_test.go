package provider

import (
	"context"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	tfprovider "github.com/hashicorp/terraform-plugin-framework/provider"
	"github.com/hashicorp/terraform-plugin-framework/providerserver"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-go/tfprotov6"
	"github.com/hashicorp/terraform-plugin-go/tftypes"
)

func protocolModel(t *testing.T, state tfsdk.State, model any) *tfprotov6.DynamicValue {
	t.Helper()
	if d := state.Set(context.Background(), model); d.HasError() {
		t.Fatalf("cannot construct protocol model: %v", d)
	}
	v, err := tfprotov6.NewDynamicValue(state.Schema.Type().TerraformType(context.Background()), state.Raw)
	if err != nil {
		t.Fatal(err)
	}
	return &v
}
func protocolErrors(diags []*tfprotov6.Diagnostic) bool {
	for _, d := range diags {
		if d.Severity == tfprotov6.DiagnosticSeverityError {
			return true
		}
	}
	return false
}

func TestCreationFailuresPreserveOwnershipBoundary(t *testing.T) {
	for _, kind := range []string{"shared_folder", "group", "user"} {
		for _, mode := range []string{"reject", "uncertain", "read_failure"} {
			t.Run(kind+"/"+mode, func(t *testing.T) {
				ctx := context.Background()
				api := map[string]string{"shared_folder": "SYNO.Core.Share", "group": "SYNO.Core.Group", "user": "SYNO.Core.User"}[kind]
				f := &fixture{shares: map[string]map[string]any{}, users: map[string]map[string]any{}, groups: map[string]map[string]any{"users": {"name": "users", "gid": 100, "description": "System default group"}}, permissions: map[string]map[string]any{}, createMode: mode, createAPI: api}
				httpServer := httptest.NewTLSServer(http.HandlerFunc(f.serve))
				defer httpServer.Close()
				ca := filepath.Join(t.TempDir(), "ca.pem")
				if err := os.WriteFile(ca, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: httpServer.Certificate().Raw}), 0o600); err != nil {
					t.Fatal(err)
				}
				p := New("test")()
				server := providerserver.NewProtocol6(p)()
				schemaResult, err := server.GetProviderSchema(ctx, &tfprotov6.GetProviderSchemaRequest{})
				if err != nil {
					t.Fatal(err)
				}
				var ps tfprovider.SchemaResponse
				p.Schema(ctx, tfprovider.SchemaRequest{}, &ps)
				providerConfig := protocolModel(t, tfsdk.State{Schema: ps.Schema}, providerModel{Endpoint: types.StringValue(httpServer.URL), Username: types.StringValue("fixture-user"), Password: types.StringValue("fixture-secret"), OTP: types.StringNull(), CAFile: types.StringValue(ca)})
				configured, err := server.ConfigureProvider(ctx, &tfprotov6.ConfigureProviderRequest{TerraformVersion: "1.17.1", Config: providerConfig})
				if err != nil || protocolErrors(configured.Diagnostics) {
					t.Fatalf("configure failed: %v", err)
				}
				var r resource.Resource
				var plan, config any
				switch kind {
				case "shared_folder":
					r = NewSharedFolderResource()
					m := shareModel{Name: types.StringValue("fixture"), Volume: types.StringValue("/volume1"), Description: types.StringValue("initial"), UUID: types.StringUnknown(), Hidden: types.BoolValue(false), RecycleBin: types.BoolValue(true), RecycleBinAdminOnly: types.BoolValue(true), DeletionProtection: types.BoolValue(true)}
					plan = m
					m.UUID = types.StringNull()
					config = m
				case "group":
					r = NewGroupResource()
					m := groupModel{Name: types.StringValue("fixture"), Description: types.StringValue("initial"), GID: types.Int64Unknown(), DeletionProtection: types.BoolValue(true)}
					plan = m
					m.GID = types.Int64Null()
					config = m
				case "user":
					r = NewUserResource()
					m := userModel{Name: types.StringValue("fixture"), Description: types.StringValue("initial"), Email: types.StringValue(""), Expiration: types.StringValue("normal"), UID: types.Int64Unknown(), Groups: types.SetUnknown(types.StringType), Password: types.StringNull(), PasswordVersion: types.Int64Value(1), DeletionProtection: types.BoolValue(true)}
					plan = m
					m.UID = types.Int64Null()
					m.Groups = types.SetNull(types.StringType)
					m.Password = types.StringValue("fixture-account-secret")
					config = m
				}
				var rs resource.SchemaResponse
				r.Schema(ctx, resource.SchemaRequest{}, &rs)
				name := "synology_" + kind
				valueType := schemaResult.ResourceSchemas[name].ValueType()
				nullValue, err := tfprotov6.NewDynamicValue(valueType, tftypes.NewValue(valueType, nil))
				if err != nil {
					t.Fatal(err)
				}
				result, err := server.ApplyResourceChange(ctx, &tfprotov6.ApplyResourceChangeRequest{TypeName: name, PriorState: &nullValue, PlannedState: protocolModel(t, tfsdk.State{Schema: rs.Schema}, plan), Config: protocolModel(t, tfsdk.State{Schema: rs.Schema}, config)})
				if err != nil {
					t.Fatal(err)
				}
				if !protocolErrors(result.Diagnostics) {
					t.Fatal("expected a failed creation")
				}
				if result.NewState == nil {
					t.Fatal("missing protocol state result")
				}
				state, err := result.NewState.Unmarshal(valueType)
				if err != nil {
					t.Fatal(err)
				}
				if mode == "reject" {
					if !state.IsNull() {
						t.Fatal("definite rejection adopted the other actor's object")
					}
					return
				}
				if state.IsNull() {
					t.Fatal("unconfirmed or acknowledged partial creation lost its provisional identity")
				}
				if mode == "read_failure" {
					f.mu.Lock()
					f.createMode = ""
					f.mu.Unlock()
				}
				read, err := server.ReadResource(ctx, &tfprotov6.ReadResourceRequest{TypeName: name, CurrentState: result.NewState, Private: result.Private})
				if err != nil {
					t.Fatal(err)
				}
				if mode == "uncertain" {
					if !protocolErrors(read.Diagnostics) {
						t.Fatal("uncertain creation adopted a same-name object during refresh")
					}
					deleted, err := server.ApplyResourceChange(ctx, &tfprotov6.ApplyResourceChangeRequest{TypeName: name, PriorState: result.NewState, PlannedState: &nullValue, Config: &nullValue, PlannedPrivate: result.Private})
					if err != nil {
						t.Fatal(err)
					}
					if !protocolErrors(deleted.Diagnostics) {
						t.Fatal("uncertain creation allowed deletion")
					}
				} else if protocolErrors(read.Diagnostics) {
					t.Fatalf("acknowledged creation could not refresh: %v", read.Diagnostics)
				}
				f.mu.Lock()
				writes := f.writes
				f.mu.Unlock()
				if writes != 1 {
					t.Fatalf("failure path repeated or deleted a mutation: %d writes", writes)
				}
			})
		}
	}
}

func configuredFixtureProtocol(t *testing.T, f *fixture) tfprotov6.ProviderServer {
	t.Helper()
	ctx := context.Background()
	httpServer := httptest.NewTLSServer(http.HandlerFunc(f.serve))
	t.Cleanup(httpServer.Close)
	ca := filepath.Join(t.TempDir(), "ca.pem")
	if err := os.WriteFile(ca, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: httpServer.Certificate().Raw}), 0o600); err != nil {
		t.Fatal(err)
	}
	p := New("test")()
	server := providerserver.NewProtocol6(p)()
	var ps tfprovider.SchemaResponse
	p.Schema(ctx, tfprovider.SchemaRequest{}, &ps)
	config := protocolModel(t, tfsdk.State{Schema: ps.Schema}, providerModel{Endpoint: types.StringValue(httpServer.URL), Username: types.StringValue("fixture-user"), Password: types.StringValue("fixture-secret"), OTP: types.StringNull(), CAFile: types.StringValue(ca)})
	result, err := server.ConfigureProvider(ctx, &tfprotov6.ConfigureProviderRequest{TerraformVersion: "1.17.1", Config: config})
	if err != nil || protocolErrors(result.Diagnostics) {
		t.Fatalf("configure failed: %v", err)
	}
	return server
}

func TestReplacementIdentityRefusesSavedPlanMutation(t *testing.T) {
	for _, kind := range []string{"shared_folder", "group", "user"} {
		for _, operation := range []string{"update", "delete"} {
			t.Run(kind+"/"+operation, func(t *testing.T) {
				ctx := context.Background()
				f := &fixture{shares: map[string]map[string]any{"fixture": {"name": "fixture", "uuid": "replacement", "vol_path": "/volume1", "desc": "initial", "hidden": false, "enable_recycle_bin": true, "recycle_bin_admin_only": true}}, users: map[string]map[string]any{"fixture": {"name": "fixture", "uid": 9001, "description": "initial", "email": "", "expired": "normal", "groups": []string{"users"}}}, groups: map[string]map[string]any{"users": {"name": "users", "gid": 100, "description": "System default group"}, "fixture": {"name": "fixture", "gid": 9001, "description": "initial"}}, permissions: map[string]map[string]any{}}
				server := configuredFixtureProtocol(t, f)
				var r resource.Resource
				var prior, planned any
				switch kind {
				case "shared_folder":
					r = NewSharedFolderResource()
					m := shareModel{Name: types.StringValue("fixture"), Volume: types.StringValue("/volume1"), Description: types.StringValue("initial"), UUID: types.StringValue("original"), Hidden: types.BoolValue(false), RecycleBin: types.BoolValue(true), RecycleBinAdminOnly: types.BoolValue(true), DeletionProtection: types.BoolValue(false)}
					prior = m
					m.Description = types.StringValue("updated")
					planned = m
				case "group":
					r = NewGroupResource()
					m := groupModel{Name: types.StringValue("fixture"), Description: types.StringValue("initial"), GID: types.Int64Value(1001), DeletionProtection: types.BoolValue(false)}
					prior = m
					m.Description = types.StringValue("updated")
					planned = m
				case "user":
					r = NewUserResource()
					m := userModel{Name: types.StringValue("fixture"), Description: types.StringValue("initial"), Email: types.StringValue(""), Expiration: types.StringValue("normal"), UID: types.Int64Value(1001), Groups: types.SetValueMust(types.StringType, []attr.Value{types.StringValue("users")}), Password: types.StringNull(), PasswordVersion: types.Int64Value(0), DeletionProtection: types.BoolValue(false)}
					prior = m
					m.Description = types.StringValue("updated")
					planned = m
				}
				var rs resource.SchemaResponse
				r.Schema(ctx, resource.SchemaRequest{}, &rs)
				priorValue := protocolModel(t, tfsdk.State{Schema: rs.Schema}, prior)
				planValue := protocolModel(t, tfsdk.State{Schema: rs.Schema}, planned)
				configValue := planValue
				if operation == "delete" {
					null, err := tfprotov6.NewDynamicValue(rs.Schema.Type().TerraformType(ctx), tftypes.NewValue(rs.Schema.Type().TerraformType(ctx), nil))
					if err != nil {
						t.Fatal(err)
					}
					planValue = &null
					configValue = &null
				}
				result, err := server.ApplyResourceChange(ctx, &tfprotov6.ApplyResourceChangeRequest{TypeName: "synology_" + kind, PriorState: priorValue, PlannedState: planValue, Config: configValue})
				if err != nil {
					t.Fatal(err)
				}
				if !protocolErrors(result.Diagnostics) {
					t.Fatal("replacement identity allowed a mutation")
				}
				f.mu.Lock()
				writes := f.writes
				f.mu.Unlock()
				if writes != 0 {
					t.Fatalf("replacement object received %d writes", writes)
				}
			})
		}
	}
}
