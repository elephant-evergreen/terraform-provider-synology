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
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/providerserver"
	"github.com/hashicorp/terraform-plugin-go/tfprotov6"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
)

func TestProviderSchema(t *testing.T) {
	server := providerserver.NewProtocol6(New("test")())()
	result, err := server.GetProviderSchema(context.Background(), &tfprotov6.GetProviderSchemaRequest{})
	if err != nil {
		t.Fatal(err)
	}
	for _, diag := range result.Diagnostics {
		if diag.Severity == tfprotov6.DiagnosticSeverityError {
			t.Fatalf("schema: %s: %s", diag.Summary, diag.Detail)
		}
	}
	if len(result.ResourceSchemas) != 10 {
		t.Fatalf("expected ten typed resources, got %d", len(result.ResourceSchemas))
	}
}

type fixture struct {
	mu             sync.Mutex
	shares         map[string]map[string]any
	users          map[string]map[string]any
	groups         map[string]map[string]any
	permissions    map[string]map[string]any
	createMode     string
	createAPI      string
	createObserved bool
	writes         int
	reads          int
}

func (f *fixture) serve(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := r.ParseForm(); err != nil {
		w.WriteHeader(400)
		return
	}
	api, method := r.Form.Get("api"), r.Form.Get("method")
	if f.createObserved && f.createMode == "read_failure" && (method == "get" || method == "list") {
		w.WriteHeader(503)
		return
	}
	if method == "create" && api == f.createAPI {
		f.createObserved = true
		if f.createMode == "reject" || f.createMode == "uncertain" {
			f.writes++
			name := r.Form.Get("name")
			switch api {
			case "SYNO.Core.Share":
				f.shares[name] = map[string]any{"name": name, "uuid": "foreign-folder", "vol_path": "/volume1", "desc": "foreign", "hidden": false, "enable_recycle_bin": true, "recycle_bin_admin_only": true}
			case "SYNO.Core.Group":
				f.groups[name] = map[string]any{"name": name, "gid": 9001, "description": "foreign"}
			case "SYNO.Core.User":
				f.users[name] = map[string]any{"name": name, "uid": 9001, "description": "foreign", "email": "", "expired": "normal", "groups": []string{"users"}}
			}
			if f.createMode == "uncertain" {
				w.WriteHeader(502)
				return
			}
			json.NewEncoder(w).Encode(map[string]any{"success": false, "error": map[string]any{"code": 3301}})
			return
		}
	}
	var data any = map[string]any{}
	switch api {
	case "SYNO.API.Info":
		catalog := map[string]any{}
		for _, api := range []string{"SYNO.Core.Share", "SYNO.Core.User", "SYNO.Core.Group", "SYNO.Core.Group.Member", "SYNO.Core.User.Group", "SYNO.Core.Share.Permission"} {
			catalog[api] = map[string]any{"path": "entry.cgi", "minVersion": 1, "maxVersion": 1}
		}
		catalog["SYNO.API.Auth"] = map[string]any{"path": "entry.cgi", "minVersion": 1, "maxVersion": 7}
		data = catalog
	case "SYNO.API.Auth":
		data = map[string]any{"sid": "fixture-session", "synotoken": "fixture-token"}
	case "SYNO.Core.Share":
		if r.Form.Get("_sid") != "fixture-session" {
			json.NewEncoder(w).Encode(map[string]any{"success": false, "error": map[string]any{"code": 105}})
			return
		}
		switch method {
		case "list":
			f.reads++
			shares := []map[string]any{}
			for _, s := range f.shares {
				shares = append(shares, s)
			}
			data = map[string]any{"shares": shares, "total": len(shares)}
		case "get":
			f.reads++
			s, ok := f.shares[r.Form.Get("name")]
			if !ok {
				json.NewEncoder(w).Encode(map[string]any{"success": false, "error": map[string]any{"code": 3300}})
				return
			}
			data = map[string]any{"shares": []map[string]any{s}}
		case "create", "set":
			f.writes++
			var s map[string]any
			if json.Unmarshal([]byte(r.Form.Get("shareinfo")), &s) != nil {
				w.WriteHeader(400)
				return
			}
			name := r.Form.Get("name")
			if method == "create" {
				s["uuid"] = "fixture-uuid-" + name
				f.shares[name] = s
			} else {
				for k, v := range s {
					f.shares[name][k] = v
				}
			}
		case "delete":
			f.writes++
			delete(f.shares, r.Form.Get("name"))
		default:
			w.WriteHeader(400)
			return
		}
	case "SYNO.Core.Group":
		switch method {
		case "list":
			items := []map[string]any{}
			for _, g := range f.groups {
				items = append(items, map[string]any{"name": g["name"], "description": ""})
			}
			data = map[string]any{"groups": items, "total": len(items)}
		case "get":
			var names []string
			json.Unmarshal([]byte(r.Form.Get("name")), &names)
			items := []map[string]any{}
			for _, name := range names {
				if g, ok := f.groups[name]; ok {
					items = append(items, g)
				}
			}
			data = map[string]any{"groups": items, "errors": []any{}}
		case "create":
			f.writes++
			name := r.Form.Get("name")
			f.groups[name] = map[string]any{"name": name, "gid": 1001, "description": r.Form.Get("description")}
		case "set":
			f.writes++
			f.groups[r.Form.Get("name")]["description"] = r.Form.Get("description")
		case "delete":
			f.writes++
			var names []string
			json.Unmarshal([]byte(r.Form.Get("name")), &names)
			for _, name := range names {
				delete(f.groups, name)
			}
		default:
			w.WriteHeader(400)
			return
		}
	case "SYNO.Core.User":
		switch method {
		case "list":
			items := []map[string]any{}
			for _, u := range f.users {
				copy := map[string]any{}
				for k, v := range u {
					if k != "groups" {
						copy[k] = v
					}
				}
				items = append(items, copy)
			}
			data = map[string]any{"users": items, "total": len(items)}
		case "create":
			f.writes++
			name := r.Form.Get("name")
			f.users[name] = map[string]any{"name": name, "uid": 1027, "description": r.Form.Get("description"), "email": r.Form.Get("email"), "expired": r.Form.Get("expired"), "groups": []string{"users"}, "unmanaged_future_setting": "preserved"}
		case "set":
			f.writes++
			u := f.users[r.Form.Get("name")]
			for _, key := range []string{"description", "email", "expired"} {
				if r.Form.Has(key) {
					u[key] = r.Form.Get(key)
				}
			}
		case "delete":
			f.writes++
			delete(f.users, r.Form.Get("name"))
		default:
			w.WriteHeader(400)
			return
		}
	case "SYNO.Core.Group.Member":
		items := []map[string]any{}
		for _, u := range f.users {
			if slices.Contains(u["groups"].([]string), r.Form.Get("group")) {
				items = append(items, map[string]any{"name": u["name"], "uid": u["uid"]})
			}
		}
		data = map[string]any{"users": items, "total": len(items)}
	case "SYNO.Core.User.Group":
		if method == "join" {
			f.writes++
			u := f.users[r.Form.Get("name")]
			groups := u["groups"].([]string)
			var join, leave []string
			json.Unmarshal([]byte(r.Form.Get("join_group")), &join)
			json.Unmarshal([]byte(r.Form.Get("leave_group")), &leave)
			for _, g := range join {
				if !slices.Contains(groups, g) {
					groups = append(groups, g)
				}
			}
			groups = slices.DeleteFunc(groups, func(g string) bool { return slices.Contains(leave, g) })
			u["groups"] = groups
			data = map[string]any{"task_id": "fixture-task"}
		} else if method == "join_status" {
			data = map[string]any{"finish": true, "success": true}
		} else {
			w.WriteHeader(400)
			return
		}
	case "SYNO.Core.Share.Permission":
		share, kind := r.Form.Get("name"), r.Form.Get("user_group_type")
		if method == "list" {
			items := []map[string]any{}
			principals := f.groups
			if kind == "local_user" {
				principals = f.users
			}
			for name := range principals {
				key := share + ":" + kind + ":" + name
				p, ok := f.permissions[key]
				if !ok {
					p = map[string]any{"name": name, "is_readonly": false, "is_writable": false, "is_deny": false, "unmanaged_future_setting": "preserved"}
				}
				items = append(items, p)
			}
			data = map[string]any{"items": items, "total": len(items)}
		} else if method == "set" {
			f.writes++
			var items []map[string]any
			json.Unmarshal([]byte(r.Form.Get("permissions")), &items)
			for _, p := range items {
				f.permissions[share+":"+kind+":"+p["name"].(string)] = p
			}
		} else {
			w.WriteHeader(400)
			return
		}
	default:
		w.WriteHeader(400)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{"success": true, "data": data})
}

func TestAccAccountsAndPermissions(t *testing.T) {
	f := &fixture{shares: map[string]map[string]any{}, users: map[string]map[string]any{}, groups: map[string]map[string]any{"users": {"name": "users", "gid": 100, "description": "System default group"}, "administrators": {"name": "administrators", "gid": 101, "description": "System default admin group"}}, permissions: map[string]map[string]any{}}
	server := httptest.NewTLSServer(http.HandlerFunc(f.serve))
	defer server.Close()
	ca := filepath.Join(t.TempDir(), "ca.pem")
	os.WriteFile(ca, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw}), 0o600)
	config := func(description string, protect bool) string {
		return fmt.Sprintf(`
provider "synology" {
 endpoint = %q
 ca_file = %q
 username = "fixture-user"
 password = "fixture-provider-password"
}
resource "synology_shared_folder" "test" {
 name = "fixture"
 volume = "/volume1"
 deletion_protection = %t
}
resource "synology_group" "test" {
 name = "fixture-group"
 description = %q
 deletion_protection = %t
}
resource "synology_user" "test" {
 name = "fixture-account"
 description = %q
 groups = ["users", synology_group.test.name]
 password_wo = "fixture-account-password"
 password_version = 1
 deletion_protection = %t
}
resource "synology_share_permissions" "test" {
 share = synology_shared_folder.test.name
 users = {}
 groups = {
   administrators = "read_write"
   (synology_group.test.name) = "read_write"
 }
}
`, server.URL, ca, protect, description, protect, description, protect)
	}
	secretCheck := func(s *terraform.State) error {
		for _, r := range s.RootModule().Resources {
			for _, value := range r.Primary.Attributes {
				if strings.Contains(value, "fixture-account-password") || strings.Contains(value, "fixture-provider-password") {
					return fmt.Errorf("credential persisted in state")
				}
			}
		}
		return nil
	}
	resource.Test(t, resource.TestCase{ProtoV6ProviderFactories: map[string]func() (tfprotov6.ProviderServer, error){"synology": providerserver.NewProtocol6WithError(New("test")())}, CheckDestroy: func(_ *terraform.State) error {
		f.mu.Lock()
		defer f.mu.Unlock()
		if len(f.users) != 0 || len(f.shares) != 0 || len(f.groups) != 2 {
			return fmt.Errorf("custom objects remain after destroy")
		}
		return nil
	}, Steps: []resource.TestStep{
		{Config: config("initial", true), Check: resource.ComposeAggregateTestCheckFunc(secretCheck, resource.TestCheckResourceAttr("synology_user.test", "groups.#", "2"), resource.TestCheckResourceAttr("synology_share_permissions.test", "groups.fixture-group", "read_write"))},
		{ResourceName: "synology_group.test", ImportState: true, ImportStateId: "fixture-group", ImportStateVerify: true, ImportStateVerifyIdentifierAttribute: "name"},
		{ResourceName: "synology_user.test", ImportState: true, ImportStateId: "fixture-account", ImportStateVerify: true, ImportStateVerifyIdentifierAttribute: "name", ImportStateVerifyIgnore: []string{"password_version"}},
		{ResourceName: "synology_share_permissions.test", ImportState: true, ImportStateId: "fixture", ImportStateVerify: true, ImportStateVerifyIdentifierAttribute: "share"},
		{Config: config("updated", true), Check: secretCheck},
		{Config: config("updated", true), PlanOnly: true, ExpectNonEmptyPlan: true, PreConfig: func() {
			f.mu.Lock()
			f.users["fixture-account"]["description"] = "external"
			f.groups["fixture-group"]["description"] = "external"
			p := f.permissions["fixture:local_group:fixture-group"]
			p["is_writable"] = false
			p["is_readonly"] = true
			f.mu.Unlock()
		}},
		{Config: config("updated", false), Check: secretCheck},
	}})
}

func TestAccSharedFolderLifecycle(t *testing.T) {
	f := &fixture{shares: map[string]map[string]any{}}
	server := httptest.NewTLSServer(http.HandlerFunc(f.serve))
	defer server.Close()
	ca := filepath.Join(t.TempDir(), "ca.pem")
	if err := os.WriteFile(ca, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw}), 0o600); err != nil {
		t.Fatal(err)
	}
	config := func(desc string, protect bool) string {
		return fmt.Sprintf(`
provider "synology" {
  endpoint = %q
  ca_file = %q
  username = "fixture-user"
  password = "fixture-password"
}
resource "synology_shared_folder" "test" {
  name = "fixture"
  volume = "/volume1"
  description = %q
  deletion_protection = %t
}
`, server.URL, ca, desc, protect)
	}
	var beforeImport int
	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: map[string]func() (tfprotov6.ProviderServer, error){"synology": providerserver.NewProtocol6WithError(New("test")())},
		CheckDestroy: func(_ *terraform.State) error {
			f.mu.Lock()
			defer f.mu.Unlock()
			if len(f.shares) != 0 {
				return fmt.Errorf("shared folder still exists")
			}
			return nil
		},
		Steps: []resource.TestStep{
			{Config: config("initial", true), Check: resource.ComposeAggregateTestCheckFunc(resource.TestCheckResourceAttr("synology_shared_folder.test", "description", "initial"), resource.TestCheckResourceAttr("synology_shared_folder.test", "recycle_bin", "true"))},
			{ResourceName: "synology_shared_folder.test", ImportState: true, ImportStateId: "fixture", ImportStateVerify: true, ImportStateVerifyIdentifierAttribute: "name", PreConfig: func() { f.mu.Lock(); beforeImport = f.writes; f.mu.Unlock() }, ImportStateCheck: func(_ []*terraform.InstanceState) error {
				f.mu.Lock()
				defer f.mu.Unlock()
				if f.writes != beforeImport {
					return fmt.Errorf("import mutated DSM")
				}
				return nil
			}},
			{Config: config("updated", true), Check: resource.TestCheckResourceAttr("synology_shared_folder.test", "description", "updated")},
			{Config: config("updated", true), PlanOnly: true, ExpectNonEmptyPlan: true, PreConfig: func() { f.mu.Lock(); f.shares["fixture"]["desc"] = "external change"; f.mu.Unlock() }},
			{Config: config("updated", false), Check: resource.TestCheckResourceAttr("synology_shared_folder.test", "description", "updated")},
		},
	})
}

func TestAccUserOmittedMembershipPreservesExistingGroups(t *testing.T) {
	f := &fixture{shares: map[string]map[string]any{}, users: map[string]map[string]any{}, groups: map[string]map[string]any{"users": {"name": "users", "gid": 100, "description": "System default group"}, "readers": {"name": "readers", "gid": 1001, "description": "Archive readers"}}, permissions: map[string]map[string]any{}}
	server := httptest.NewTLSServer(http.HandlerFunc(f.serve))
	defer server.Close()
	ca := filepath.Join(t.TempDir(), "ca.pem")
	if err := os.WriteFile(ca, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw}), 0o600); err != nil {
		t.Fatal(err)
	}
	config := func(description string) string {
		return fmt.Sprintf(`
provider "synology" {
 endpoint = %q
 ca_file = %q
 username = "fixture-user"
 password = "fixture-provider-password"
}
resource "synology_user" "test" {
 name = "fixture-account"
 description = %q
 password_wo = "fixture-account-password"
 password_version = 1
 deletion_protection = false
}
`, server.URL, ca, description)
	}
	resource.Test(t, resource.TestCase{ProtoV6ProviderFactories: map[string]func() (tfprotov6.ProviderServer, error){"synology": providerserver.NewProtocol6WithError(New("test")())}, CheckDestroy: func(_ *terraform.State) error {
		f.mu.Lock()
		defer f.mu.Unlock()
		if len(f.users) != 0 {
			return fmt.Errorf("fixture user still exists")
		}
		return nil
	}, Steps: []resource.TestStep{
		{Config: config("initial"), Check: resource.TestCheckResourceAttr("synology_user.test", "groups.#", "1")},
		{Config: config("updated"), PreConfig: func() {
			f.mu.Lock()
			f.users["fixture-account"]["groups"] = []string{"users", "readers"}
			f.mu.Unlock()
		}, Check: resource.ComposeAggregateTestCheckFunc(resource.TestCheckResourceAttr("synology_user.test", "description", "updated"), resource.TestCheckResourceAttr("synology_user.test", "groups.#", "2"))},
		{Config: config("updated"), PlanOnly: true},
	}})
}
