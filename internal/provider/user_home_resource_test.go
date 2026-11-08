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
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/elephant-evergreen/terraform-provider-synology/internal/dsm"
	tfprovider "github.com/hashicorp/terraform-plugin-framework/provider"
	"github.com/hashicorp/terraform-plugin-framework/providerserver"
	frameworkresource "github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-go/tfprotov6"
	"github.com/hashicorp/terraform-plugin-go/tftypes"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
)

type homeFixture struct {
	validationShape     string
	unfinishedWithoutID bool
	mu                  sync.Mutex
	settings            map[string]any
	pending             map[string]any
	writes              int
	statusReads         int
	async               bool
	lostAck             bool
	failRead            bool
	warning             bool
	background          bool
}

func newHomeFixture() *homeFixture {
	return &homeFixture{settings: map[string]any{
		"enable": false, "location": "", "enable_domain": false, "enable_ldap": false,
		"enable_recycle_bin": true, "encryption": 0, "remote_location": "",
		"additional": map[string]any{"personal_photo_enable": false}, "unmanaged_future_setting": "preserved",
	}}
}

func (f *homeFixture) writeCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.writes
}

func (f *homeFixture) serve(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := r.ParseForm(); err != nil {
		w.WriteHeader(400)
		return
	}
	var data any = map[string]any{}
	switch r.Form.Get("api") {
	case "SYNO.API.Info":
		data = map[string]any{
			"SYNO.API.Auth":              map[string]any{"path": "entry.cgi", "minVersion": 1, "maxVersion": 7},
			homeAPI:                      map[string]any{"path": "entry.cgi", "minVersion": 1, "maxVersion": 1},
			"SYNO.Entry.Request.Polling": map[string]any{"path": "entry.cgi", "minVersion": 1, "maxVersion": 1},
		}
	case "SYNO.API.Auth":
		data = map[string]any{"sid": "fixture-session"}
	case "SYNO.Entry.Request.Polling":
		if r.Form.Get("method") != "list" || r.Form.Get("task_id_prefix") != "userhome" {
			w.WriteHeader(400)
			return
		}
		var ids []string
		if f.background || f.pending != nil {
			ids = []string{"userhome-fixture"}
		}
		data = map[string]any{"admin": ids, "user": nil}
	case homeAPI:
		switch r.Form.Get("method") {
		case "get":
			if f.failRead && f.writes > 0 {
				w.WriteHeader(503)
				return
			}
			data = f.settings
		case "validate_set":
			reasons := []string{}
			if f.warning {
				reasons = append(reasons, "fixture restriction")
			}
			data = map[string]any{"hard_reasons": []string{}, "soft_reasons": reasons}
			switch f.validationShape {
			case "missing":
				data = map[string]any{}
			case "null":
				data = map[string]any{"hard_reasons": nil, "soft_reasons": nil}
			case "wrong_type":
				data = map[string]any{"hard_reasons": false, "soft_reasons": []string{}}
			}
		case "set":
			f.writes++
			if r.Form.Has("force") || r.Form.Has("enable_recycle_bin") || r.Form.Has("encryption") {
				w.WriteHeader(400)
				return
			}
			target := map[string]any{}
			for _, key := range []string{"enable", "enable_domain", "enable_ldap"} {
				if key != "enable" && !r.Form.Has(key) {
					continue
				}
				value, err := strconv.ParseBool(r.Form.Get(key))
				if err != nil {
					w.WriteHeader(400)
					return
				}
				target[key] = value
			}
			if target["enable"] == true {
				target["location"] = r.Form.Get("location")
			}
			if f.async {
				f.pending = target
				f.statusReads = 0
				data = map[string]any{"task_id": "userhome-fixture"}
			} else {
				for k, v := range target {
					f.settings[k] = v
				}
			}
			if f.unfinishedWithoutID {
				data = map[string]any{"finish": false}
			}
			if f.lostAck {
				w.WriteHeader(503)
				fmt.Fprint(w, "fixture-login-secret fixture-session")
				return
			}
		case "status":
			if r.Form.Get("task_id") != "userhome-fixture" {
				w.WriteHeader(400)
				return
			}
			f.statusReads++
			finished := !f.background && f.statusReads >= 2
			if finished {
				for k, v := range f.pending {
					f.settings[k] = v
				}
				f.pending = nil
			}
			data = map[string]any{"task_id": "userhome-fixture", "finish": finished, "success": true}
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

func homeTestEndpoint(t *testing.T, f *homeFixture) (string, string) {
	t.Helper()
	s := httptest.NewTLSServer(http.HandlerFunc(f.serve))
	t.Cleanup(s.Close)
	ca := filepath.Join(t.TempDir(), "ca.pem")
	if err := os.WriteFile(ca, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: s.Certificate().Raw}), 0o600); err != nil {
		t.Fatal(err)
	}
	return s.URL, ca
}

func TestAccUserHomeLifecycle(t *testing.T) {
	for _, async := range []bool{false, true} {
		t.Run(fmt.Sprint(async), func(t *testing.T) {
			f := newHomeFixture()
			f.async = async
			endpoint, ca := homeTestEndpoint(t, f)
			config := func(enabled bool) string {
				location := ""
				if enabled {
					location = `location = "/volume1"`
				}
				return fmt.Sprintf(`
provider "synology" {
 endpoint = %q
 ca_file = %q
 username = "fixture-user"
 password = "fixture-login-secret"
}
resource "synology_user_home" "test" {
 enabled = %t
 %s
}
import {
 to = synology_user_home.test
 id = "user_home"
}
`, endpoint, ca, enabled, location)
			}
			checkWrites := func(count int) resource.TestCheckFunc {
				return func(_ *terraform.State) error {
					f.mu.Lock()
					defer f.mu.Unlock()
					if f.writes != count || f.settings["unmanaged_future_setting"] != "preserved" || f.settings["enable_recycle_bin"] != true {
						return fmt.Errorf("expected %d deliberate writes and preserved unmanaged settings; got %d", count, f.writes)
					}
					return nil
				}
			}
			resource.Test(t, resource.TestCase{
				ProtoV6ProviderFactories: map[string]func() (tfprotov6.ProviderServer, error){"synology": providerserver.NewProtocol6WithError(New("test")())},
				CheckDestroy:             checkWrites(3),
				Steps: []resource.TestStep{
					{Config: config(false), Check: resource.ComposeAggregateTestCheckFunc(checkWrites(0), resource.TestCheckResourceAttr("synology_user_home.test", "reconciliation_required", "false"))},
					{ResourceName: "synology_user_home.test", ImportState: true, ImportStateId: "user_home", ImportStateVerify: true, ImportStateCheck: func(_ []*terraform.InstanceState) error { return checkWrites(0)(nil) }},
					{Config: config(true), Check: resource.ComposeAggregateTestCheckFunc(checkWrites(1), resource.TestCheckResourceAttr("synology_user_home.test", "location", "/volume1"))},
					{Config: config(true), PlanOnly: true, ExpectNonEmptyPlan: true, PreConfig: func() { f.mu.Lock(); f.settings["enable"] = false; f.mu.Unlock() }},
					{Config: config(true), Check: checkWrites(2)},
					{Config: config(false), Check: checkWrites(3)},
				},
			})
		})
	}
}

func TestUserHomeFailureStateRetainsOperation(t *testing.T) {
	for _, mode := range []string{"lost_ack", "ack_read_failure", "preflight_warning", "orphaned_task", "unfinished_without_identity"} {
		t.Run(mode, func(t *testing.T) {
			ctx := context.Background()
			f := newHomeFixture()
			f.lostAck, f.failRead, f.warning, f.background = mode == "lost_ack", mode == "ack_read_failure", mode == "preflight_warning", mode == "orphaned_task"
			f.unfinishedWithoutID = mode == "unfinished_without_identity"
			endpoint, ca := homeTestEndpoint(t, f)
			p := New("test")()
			server := providerserver.NewProtocol6(p)()
			if schemas, err := server.GetProviderSchema(ctx, &tfprotov6.GetProviderSchemaRequest{}); err != nil || protocolErrors(schemas.Diagnostics) {
				t.Fatalf("provider schema: %v", err)
			}
			var ps tfprovider.SchemaResponse
			p.Schema(ctx, tfprovider.SchemaRequest{}, &ps)
			configured, err := server.ConfigureProvider(ctx, &tfprotov6.ConfigureProviderRequest{TerraformVersion: "1.17.1", Config: protocolModel(t, tfsdk.State{Schema: ps.Schema}, providerModel{Endpoint: types.StringValue(endpoint), Username: types.StringValue("fixture-user"), Password: types.StringValue("fixture-login-secret"), CAFile: types.StringValue(ca)})})
			if err != nil || protocolErrors(configured.Diagnostics) {
				t.Fatalf("configure: %v", err)
			}
			r := NewUserHomeResource().(*userHomeResource)
			var rs frameworkresource.SchemaResponse
			r.Schema(ctx, frameworkresource.SchemaRequest{}, &rs)
			baseline := userHomeModel{ID: types.StringValue("user_home"), Enabled: types.BoolValue(false), Location: types.StringValue(""), DomainEnabled: types.BoolValue(false), LDAPEnabled: types.BoolValue(false), RecycleBinEnabled: types.BoolValue(true), Encryption: types.Int64Value(0), RemoteLocation: types.StringValue(""), PersonalPhotoEnabled: types.BoolValue(false), ReconciliationRequired: types.BoolValue(false), OperationID: types.StringValue("")}
			planned := baseline
			planned.Enabled, planned.Location = types.BoolValue(true), types.StringValue("/volume1")
			config := userHomeModel{Enabled: types.BoolValue(true), Location: types.StringValue("/volume1")}
			prior, plan, cfg := protocolModel(t, tfsdk.State{Schema: rs.Schema}, baseline), protocolModel(t, tfsdk.State{Schema: rs.Schema}, planned), protocolModel(t, tfsdk.State{Schema: rs.Schema}, config)
			failed, err := server.ApplyResourceChange(ctx, &tfprotov6.ApplyResourceChangeRequest{TypeName: "synology_user_home", PriorState: prior, PlannedState: plan, Config: cfg})
			if err != nil || !protocolErrors(failed.Diagnostics) {
				t.Fatalf("expected failed apply: %v", err)
			}
			if mode == "preflight_warning" || mode == "orphaned_task" {
				if f.writeCount() != 0 {
					t.Fatal("preflight failure changed DSM")
				}
				return
			}
			value, err := failed.NewState.Unmarshal(rs.Schema.Type().TerraformType(ctx))
			if err != nil || value.IsNull() {
				t.Fatalf("partial update lost state: %v", err)
			}
			var retained userHomeModel
			if d := (&tfsdk.State{Schema: rs.Schema, Raw: value}).Get(ctx, &retained); d.HasError() || !retained.ReconciliationRequired.ValueBool() || len(failed.Private) == 0 {
				t.Fatal("operation guard or private checkpoint was lost")
			}
			if strings.Contains(fmt.Sprint(failed.Diagnostics), "fixture-login-secret") || strings.Contains(fmt.Sprint(failed.Diagnostics), "fixture-session") {
				t.Fatal("diagnostic leaked remote secret material")
			}
			f.mu.Lock()
			f.failRead = false
			f.mu.Unlock()
			refreshed, err := server.ReadResource(ctx, &tfprotov6.ReadResourceRequest{TypeName: "synology_user_home", CurrentState: failed.NewState, Private: failed.Private})
			if err != nil {
				t.Fatal(err)
			}
			if mode == "ack_read_failure" {
				if protocolErrors(refreshed.Diagnostics) || f.writeCount() != 1 {
					t.Fatal("acknowledged write did not resume read-only convergence")
				}
				value, err := refreshed.NewState.Unmarshal(rs.Schema.Type().TerraformType(ctx))
				if err != nil {
					t.Fatal(err)
				}
				if d := (&tfsdk.State{Schema: rs.Schema, Raw: value}).Get(ctx, &retained); d.HasError() || retained.ReconciliationRequired.ValueBool() || !retained.Enabled.ValueBool() {
					t.Fatal("confirmed convergence did not release the pending guard")
				}
				return
			}
			if !protocolErrors(refreshed.Diagnostics) {
				t.Fatal("refresh discarded an unacknowledged operation")
			}
			retried, err := server.ApplyResourceChange(ctx, &tfprotov6.ApplyResourceChangeRequest{TypeName: "synology_user_home", PriorState: failed.NewState, PlannedState: plan, Config: cfg, PlannedPrivate: failed.Private})
			if err != nil || !protocolErrors(retried.Diagnostics) || f.writeCount() != 1 {
				t.Fatal("retry replayed an uncertain mutation")
			}
			nullValue, err := tfprotov6.NewDynamicValue(rs.Schema.Type().TerraformType(ctx), tftypes.NewValue(rs.Schema.Type().TerraformType(ctx), nil))
			if err != nil {
				t.Fatal(err)
			}
			create, err := server.ApplyResourceChange(ctx, &tfprotov6.ApplyResourceChangeRequest{TypeName: "synology_user_home", PriorState: &nullValue, PlannedState: plan, Config: cfg})
			if err != nil || !protocolErrors(create.Diagnostics) {
				t.Fatal("replacement acquired a new baseline without import")
			}
			deleted, err := server.ApplyResourceChange(ctx, &tfprotov6.ApplyResourceChangeRequest{TypeName: "synology_user_home", PriorState: failed.NewState, PlannedState: &nullValue, Config: &nullValue, PlannedPrivate: failed.Private})
			if err != nil || !protocolErrors(deleted.Diagnostics) {
				t.Fatal("destroy discarded an unacknowledged operation")
			}
			if f.writeCount() != 1 {
				t.Fatal("replacement replayed an uncertain mutation")
			}
		})
	}
}

func TestUserHomeUnsafeChangesAreRejected(t *testing.T) {
	base := userHomeModel{Enabled: types.BoolValue(true), Location: types.StringValue("/volume1"), DomainEnabled: types.BoolValue(false), LDAPEnabled: types.BoolValue(false), RemoteLocation: types.StringValue(""), PersonalPhotoEnabled: types.BoolValue(false)}
	for _, tc := range []struct {
		name   string
		config userHomeModel
		actual userHomeModel
		match  string
	}{
		{"relocation", userHomeModel{Location: types.StringValue("/volume2")}, base, "moves user data"},
		{"unknown", userHomeModel{Enabled: types.BoolUnknown()}, base, "must be known"},
		{"domain_dependency", userHomeModel{Enabled: types.BoolValue(false), DomainEnabled: types.BoolValue(true)}, base, "dependent"},
		{"photo_dependency", userHomeModel{Enabled: types.BoolValue(false)}, func() userHomeModel { m := base; m.PersonalPhotoEnabled = types.BoolValue(true); return m }(), "dependent"},
		{"remote", userHomeModel{Enabled: types.BoolValue(false)}, func() userHomeModel { m := base; m.RemoteLocation = types.StringValue("fixture-remote"); return m }(), "remote user homes"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := homeTarget(tc.config, tc.actual)
			if err == nil || !regexp.MustCompile(tc.match).MatchString(err.Error()) {
				t.Fatalf("unsafe transition did not fail correctly: %v", err)
			}
		})
	}
}

func TestLiveUserHomeReadOnly(t *testing.T) {
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
	r := NewUserHomeResource().(*userHomeResource)
	r.client = c
	if _, err := r.readHome(ctx); err != nil {
		t.Fatal(err)
	}
	if err := r.checkHomeTasks(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestAccUserHomeFailureRecovery(t *testing.T) {
	for _, mode := range []string{"lost_ack", "ack_read_failure", "unfinished_without_identity"} {
		t.Run(mode, func(t *testing.T) {
			f := newHomeFixture()
			endpoint, ca := homeTestEndpoint(t, f)
			prefix := fmt.Sprintf("provider \"synology\" {\n endpoint=%q\n ca_file=%q\n username=\"fixture-user\"\n password=\"fixture-login-secret\"\n}\n", endpoint, ca)
			configured := prefix + "resource \"synology_user_home\" \"test\" {\n enabled=true\n location=\"/volume1\"\n}\nimport {\n to=synology_user_home.test\n id=\"user_home\"\n}\n"
			release := prefix + "removed {\n from=synology_user_home.test\n lifecycle { destroy=false }\n}\n"
			resource.Test(t, resource.TestCase{
				ProtoV6ProviderFactories: map[string]func() (tfprotov6.ProviderServer, error){"synology": providerserver.NewProtocol6WithError(New("test")())},
				CheckDestroy: func(_ *terraform.State) error {
					if f.writeCount() != 1 {
						return fmt.Errorf("failure recovery replayed a mutation: %d", f.writeCount())
					}
					return nil
				},
				Steps: []resource.TestStep{
					{Config: configured, ExpectError: regexp.MustCompile("Cannot update user-home"), PreConfig: func() {
						f.mu.Lock()
						defer f.mu.Unlock()
						f.lostAck = mode == "lost_ack"
						f.failRead = mode == "ack_read_failure"
						f.unfinishedWithoutID = mode == "unfinished_without_identity"
					}},
					{Config: configured, ExpectError: regexp.MustCompile("Cannot read user-home|Pending user-home operation"), PreConfig: func() { f.mu.Lock(); f.failRead = mode == "ack_read_failure"; f.mu.Unlock() }},
					{Config: release, RefreshState: false, PreConfig: func() { f.mu.Lock(); f.failRead = false; f.mu.Unlock() }},
					{Config: configured, Check: resource.TestCheckResourceAttr("synology_user_home.test", "reconciliation_required", "false")},
				},
			})
		})
	}
}

func TestAccUserHomeRequiresCompleteFeasibility(t *testing.T) {
	for _, mode := range []string{"missing", "null", "wrong_type"} {
		t.Run(mode, func(t *testing.T) {
			f := newHomeFixture()
			f.validationShape = mode
			endpoint, ca := homeTestEndpoint(t, f)
			prefix := fmt.Sprintf("provider \"synology\" {\nendpoint=%q\nca_file=%q\nusername=\"fixture-user\"\npassword=\"fixture-login-secret\"\n}\n", endpoint, ca)
			cfg := prefix + "resource \"synology_user_home\" \"test\" {\nenabled=true\nlocation=\"/volume1\"\n}\nimport {\nto=synology_user_home.test\nid=\"user_home\"\n}\n"
			resource.Test(t, resource.TestCase{ProtoV6ProviderFactories: map[string]func() (tfprotov6.ProviderServer, error){"synology": providerserver.NewProtocol6WithError(New("test")())}, Steps: []resource.TestStep{
				{Config: cfg, ExpectError: regexp.MustCompile("feasibility results|malformed data")},
				{Config: prefix + "removed {\nfrom=synology_user_home.test\nlifecycle {destroy=false}\n}\n", Check: func(_ *terraform.State) error {
					if f.writeCount() != 0 {
						return fmt.Errorf("incomplete feasibility allowed a home write")
					}
					return nil
				}},
			}})
		})
	}
}
