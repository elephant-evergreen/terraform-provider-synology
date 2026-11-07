package provider

import (
	"context"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/elephant-evergreen/terraform-provider-synology/internal/dsm"
	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/providerserver"
	frameworkresource "github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-go/tfprotov6"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
)

type snmpFixture struct {
	mu                     sync.Mutex
	settings               map[string]any
	writes, passwordWrites int
	privacyWrites          int
	failReadAfterWrite     bool
	omitActiveProtocol     bool
}

func newSNMPFixture() *snmpFixture {
	return &snmpFixture{settings: map[string]any{"enable_snmp": false, "enable_snmp_v1v2": false, "enable_snmp_v3": false, "enable_privacy": false, "name": "fixture-name", "contact": "initial", "location": "retained", "rouser": "", "rocommunity": "fixture-community-secret", "auth_type": "SHA", "privacy_type": "AES", "unmanaged_future_setting": "preserved"}}
}

func (f *snmpFixture) serve(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if r.ParseForm() != nil {
		w.WriteHeader(400)
		return
	}
	var data any = map[string]any{}
	switch r.Form.Get("api") {
	case "SYNO.API.Info":
		data = map[string]any{"SYNO.API.Auth": map[string]any{"path": "entry.cgi", "minVersion": 1, "maxVersion": 7}, "SYNO.Core.SNMP": map[string]any{"path": "entry.cgi", "minVersion": 1, "maxVersion": 1}}
	case "SYNO.API.Auth":
		data = map[string]any{"sid": "fixture-session"}
	case "SYNO.Core.SNMP":
		switch r.Form.Get("method") {
		case "get":
			if f.failReadAfterWrite && f.writes > 0 {
				w.WriteHeader(503)
				return
			}
			result := map[string]any{}
			for key, value := range f.settings {
				if key != "password" && key != "privacy_key" {
					result[key] = value
				}
			}
			if f.settings["enable_snmp"] != true || f.settings["enable_snmp_v3"] != true {
				delete(result, "auth_type")
				delete(result, "enable_privacy")
				delete(result, "privacy_type")
			} else if f.settings["enable_privacy"] != true {
				delete(result, "privacy_type")
			}
			if f.omitActiveProtocol {
				delete(result, "auth_type")
			}
			data = result
		case "set":
			f.writes++
			for _, key := range []string{"enable_snmp", "enable_snmp_v1v2", "enable_snmp_v3", "enable_privacy"} {
				if r.Form.Has(key) {
					f.settings[key] = r.Form.Get(key) == "true"
				}
			}
			for _, key := range []string{"name", "contact", "location", "rouser", "rocommunity", "auth_type", "privacy_type", "password", "privacy_key"} {
				if r.Form.Has(key) {
					f.settings[key] = r.Form.Get(key)
				}
			}
			if r.Form.Has("password") {
				f.passwordWrites++
			}
			if r.Form.Has("privacy_key") {
				f.privacyWrites++
			}
		default:
			w.WriteHeader(400)
			return
		}
	default:
		w.WriteHeader(400)
		return
	}
	json.NewEncoder(w).Encode(map[string]any{"success": true, "data": data})
}

func snmpTestEndpoint(t *testing.T, f *snmpFixture) (string, string) {
	t.Helper()
	server := httptest.NewTLSServer(http.HandlerFunc(f.serve))
	t.Cleanup(server.Close)
	ca := filepath.Join(t.TempDir(), "ca.pem")
	if err := os.WriteFile(ca, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw}), 0o600); err != nil {
		t.Fatal(err)
	}
	return server.URL, ca
}

func TestAccSNMPLifecycle(t *testing.T) {
	f := newSNMPFixture()
	endpoint, ca := snmpTestEndpoint(t, f)
	config := func(body string) string {
		return fmt.Sprintf("provider \"synology\" {\nendpoint=%q\nca_file=%q\nusername=\"fixture-user\"\npassword=\"fixture-login-secret\"\n}\nresource \"synology_snmp\" \"test\" {\n%s\n}\n", endpoint, ca, body)
	}
	disabled := "enabled=false\nv1_v2_enabled=false\nv3_enabled=false\nprivacy_enabled=false\ncontact=\"managed\""
	v1 := "enabled=true\nv1_v2_enabled=true\nv3_enabled=false\nprivacy_enabled=false\ncontact=\"managed\"\ncommunity_wo=\"fixture-new-community\"\ncommunity_version=1"
	v3 := "enabled=true\nv1_v2_enabled=false\nv3_enabled=true\nprivacy_enabled=true\nauth_protocol=\"SHA\"\nprivacy_protocol=\"AES\"\nusername=\"fixture-v3\"\ncontact=\"managed\"\ncommunity_version=1\nauthentication_password_wo=\"fixture-auth-secret\"\nauthentication_password_version=1\nprivacy_password_wo=\"fixture-privacy-secret\"\nprivacy_password_version=1"
	check := func(expectedWrites int) resource.TestCheckFunc {
		return func(s *terraform.State) error {
			f.mu.Lock()
			defer f.mu.Unlock()
			if f.writes != expectedWrites || f.settings["unmanaged_future_setting"] != "preserved" {
				return fmt.Errorf("unexpected writes or changed unmanaged setting: %d", f.writes)
			}
			if len(s.Modules) == 0 {
				return nil
			}
			for _, r := range s.RootModule().Resources {
				for _, value := range r.Primary.Attributes {
					for _, secret := range []string{"fixture-login-secret", "fixture-community-secret", "fixture-new-community", "fixture-auth-secret", "fixture-privacy-secret"} {
						if strings.Contains(value, secret) {
							return fmt.Errorf("secret persisted in state")
						}
					}
				}
			}
			return nil
		}
	}
	resource.Test(t, resource.TestCase{ProtoV6ProviderFactories: map[string]func() (tfprotov6.ProviderServer, error){"synology": providerserver.NewProtocol6WithError(New("test")())}, CheckDestroy: func(_ *terraform.State) error {
		f.mu.Lock()
		defer f.mu.Unlock()
		if f.writes != 5 || f.settings["enable_snmp"] != false || f.passwordWrites != 1 || f.privacyWrites != 1 || f.settings["location"] != "external-location" {
			return fmt.Errorf("destroy changed settings, rotation replayed or omitted metadata was lost: writes=%d auth=%d privacy=%d enabled=%v location=%v", f.writes, f.passwordWrites, f.privacyWrites, f.settings["enable_snmp"], f.settings["location"])
		}
		return nil
	}, Steps: []resource.TestStep{
		{Config: config(disabled), Check: check(1)},
		{ResourceName: "synology_snmp.test", ImportState: true, ImportStateId: "snmp", ImportStateVerify: true, ImportStateCheck: func(_ []*terraform.InstanceState) error { return check(1)(&terraform.State{}) }},
		{Config: config(v1), Check: resource.ComposeAggregateTestCheckFunc(check(2), resource.TestCheckResourceAttr("synology_snmp.test", "community_version", "1"))},
		{Config: config(v3), Check: check(3)},
		{Config: config(v3), PlanOnly: true, ExpectNonEmptyPlan: true, PreConfig: func() {
			f.mu.Lock()
			f.settings["contact"] = "external-contact"
			f.settings["location"] = "external-location"
			f.mu.Unlock()
		}},
		{Config: config(v3), Check: check(4)},
		{Config: config(disabled + "\ncommunity_version=1\nauthentication_password_version=1\nprivacy_password_version=1"), Check: check(5)},
	}})
}

func TestSNMPAcknowledgedSecretVersionSurvivesReadFailure(t *testing.T) {
	f := newSNMPFixture()
	f.settings["enable_snmp"], f.settings["enable_snmp_v3"], f.settings["rouser"] = true, true, "fixture-v3"
	f.failReadAfterWrite = true
	endpoint, ca := snmpTestEndpoint(t, f)
	c, err := dsm.New(endpoint, "fixture-user", "fixture-login-secret", "", ca)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := c.Connect(ctx); err != nil {
		t.Fatal(err)
	}
	defer c.Close(ctx)
	r := NewSNMPResource().(*snmpResource)
	r.client = c
	actual, _, err := r.readSNMP(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var schemaResult frameworkresource.SchemaResponse
	r.Schema(ctx, frameworkresource.SchemaRequest{}, &schemaResult)
	versions := map[string]int64{"authentication_password_version": 1}
	configValues := snmpState(actual, versions).Attributes()
	configValues["authentication_password_version"] = types.Int64Value(2)
	configValues["authentication_password_wo"] = types.StringValue("fixture-rotated-secret")
	kinds := snmpState(actual, versions).AttributeTypes(ctx)
	configState := tfsdk.State{Schema: schemaResult.Schema}
	if d := configState.Set(ctx, types.ObjectValueMust(kinds, configValues)); d.HasError() {
		t.Fatal(d)
	}
	planValues := map[string]attr.Value{}
	for name, v := range configValues {
		planValues[name] = v
	}
	planValues["authentication_password_wo"] = types.StringNull()
	planState := tfsdk.State{Schema: schemaResult.Schema}
	if d := planState.Set(ctx, types.ObjectValueMust(kinds, planValues)); d.HasError() {
		t.Fatal(d)
	}
	checkpointed := tfsdk.State{Schema: schemaResult.Schema}
	config := tfsdk.Config{Schema: schemaResult.Schema, Raw: configState.Raw}
	plan := tfsdk.Plan{Schema: schemaResult.Schema, Raw: planState.Raw}
	checkpoint := func(v types.Object) diag.Diagnostics { return checkpointed.Set(ctx, v) }
	if _, err := r.applySNMP(ctx, config, plan, versions, checkpoint); err == nil {
		t.Fatal("expected readback failure")
	}
	retained, d := snmpVersions(ctx, checkpointed)
	if d.HasError() || retained["authentication_password_version"] != 2 {
		t.Fatal("acknowledged rotation lost its version checkpoint")
	}
	f.mu.Lock()
	f.failReadAfterWrite = false
	f.mu.Unlock()
	if _, err := r.applySNMP(ctx, config, plan, retained, checkpoint); err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.writes != 1 || f.passwordWrites != 1 {
		t.Fatal("retry replayed an acknowledged secret rotation")
	}
}

func TestSNMPActiveMissingProtocolIsNotAbsence(t *testing.T) {
	f := newSNMPFixture()
	f.settings["enable_snmp"], f.settings["enable_snmp_v3"], f.settings["rouser"] = true, true, "fixture-v3"
	f.omitActiveProtocol = true
	endpoint, ca := snmpTestEndpoint(t, f)
	c, err := dsm.New(endpoint, "fixture-user", "fixture-login-secret", "", ca)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := c.Connect(ctx); err != nil {
		t.Fatal(err)
	}
	defer c.Close(ctx)
	r := NewSNMPResource().(*snmpResource)
	r.client = c
	if _, _, err := r.readSNMP(ctx); err == nil {
		t.Fatal("missing active setting was accepted")
	}
	if f.writes != 0 {
		t.Fatal("read mutated settings")
	}
}

func TestSNMPInvalidTransitionsDoNotWrite(t *testing.T) {
	for _, mode := range []string{"v3_without_password", "v1_without_community", "privacy_without_v3", "decreasing_version", "unknown_version", "inactive_secret_rotation"} {
		t.Run(mode, func(t *testing.T) {
			f := newSNMPFixture()
			if mode == "v1_without_community" {
				f.settings["rocommunity"] = ""
			}
			endpoint, ca := snmpTestEndpoint(t, f)
			c, err := dsm.New(endpoint, "fixture-user", "fixture-login-secret", "", ca)
			if err != nil {
				t.Fatal(err)
			}
			ctx := context.Background()
			if err := c.Connect(ctx); err != nil {
				t.Fatal(err)
			}
			defer c.Close(ctx)
			r := NewSNMPResource().(*snmpResource)
			r.client = c
			actual, _, err := r.readSNMP(ctx)
			if err != nil {
				t.Fatal(err)
			}
			var schemaResult frameworkresource.SchemaResponse
			r.Schema(ctx, frameworkresource.SchemaRequest{}, &schemaResult)
			previous := map[string]int64{}
			values := snmpState(actual, previous).Attributes()
			switch mode {
			case "v3_without_password":
				values["enabled"], values["v3_enabled"] = types.BoolValue(true), types.BoolValue(true)
				values["auth_protocol"], values["username"] = types.StringValue("SHA"), types.StringValue("fixture-v3")
			case "v1_without_community":
				values["enabled"], values["v1_v2_enabled"] = types.BoolValue(true), types.BoolValue(true)
			case "privacy_without_v3":
				values["privacy_enabled"] = types.BoolValue(true)
			case "decreasing_version":
				previous["community_version"] = 1
			case "unknown_version":
				values["community_version"] = types.Int64Unknown()
			case "inactive_secret_rotation":
				values["community_version"] = types.Int64Value(1)
				values["community_wo"] = types.StringValue("fixture-new-community")
			}
			state := tfsdk.State{Schema: schemaResult.Schema}
			if d := state.Set(ctx, types.ObjectValueMust(snmpState(actual, previous).AttributeTypes(ctx), values)); d.HasError() {
				t.Fatal(d)
			}
			_, err = r.applySNMP(ctx, tfsdk.Config{Schema: schemaResult.Schema, Raw: state.Raw}, tfsdk.Plan{Schema: schemaResult.Schema, Raw: state.Raw}, previous, func(types.Object) diag.Diagnostics { return nil })
			if err == nil || f.writes != 0 {
				t.Fatal("invalid transition did not fail before mutation")
			}
		})
	}
}

func TestLiveSNMPReadOnly(t *testing.T) {
	if os.Getenv("SYNOLOGY_LIVE_READ") != "1" {
		t.Skip("explicit read-only live test not requested")
	}
	c, err := dsm.New(os.Getenv("SYNOLOGY_ENDPOINT"), os.Getenv("SYNOLOGY_USERNAME"), os.Getenv("SYNOLOGY_PASSWORD"), "", os.Getenv("SYNOLOGY_CA_FILE"))
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := c.Connect(ctx); err != nil {
		t.Fatal(err)
	}
	defer c.Close(ctx)
	r := NewSNMPResource().(*snmpResource)
	r.client = c
	if _, _, err := r.readSNMP(ctx); err != nil {
		t.Fatal(err)
	}
	t.Log("Verified typed SNMP readback without storing community or passwords")
}
