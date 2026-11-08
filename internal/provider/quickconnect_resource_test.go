package provider

import (
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/providerserver"
	"github.com/hashicorp/terraform-plugin-go/tfprotov6"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
)

type quickConnectFixture struct {
	mu                  sync.Mutex
	enabled, relay      bool
	identifier          string
	services            map[string]bool
	writes, aliasWrites int
	failRelay           bool
}

func newQuickConnectFixture() *quickConnectFixture {
	return &quickConnectFixture{identifier: "fixture-old", services: map[string]bool{"dsm_portal": true, "mobile_apps": false, "file_sharing": false}}
}
func (f *quickConnectFixture) serve(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if r.ParseForm() != nil {
		w.WriteHeader(400)
		return
	}
	var data any = map[string]any{}
	method := r.Form.Get("method")
	switch api := r.Form.Get("api"); api {
	case "SYNO.API.Info":
		data = map[string]any{"SYNO.API.Auth": map[string]any{"path": "entry.cgi", "minVersion": 1, "maxVersion": 7}, quickConnectAPI: map[string]any{"path": "entry.cgi", "minVersion": 1, "maxVersion": 3}, quickConnectAPI + ".Permission": map[string]any{"path": "entry.cgi", "minVersion": 1, "maxVersion": 1}}
	case "SYNO.API.Auth":
		data = map[string]any{"sid": "fixture-session"}
	case quickConnectAPI:
		switch method {
		case "get":
			if r.Form.Get("version") != "2" {
				w.WriteHeader(400)
				return
			}
			data = map[string]any{"enabled": f.enabled, "server_alias": f.identifier, "myds_account": "fixture-account@example.invalid"}
		case "get_misc_config":
			if r.Form.Get("version") != "3" {
				w.WriteHeader(400)
				return
			}
			data = map[string]any{"relay_enabled": f.relay}
		case "set":
			if r.Form.Has("server_alias") || r.Form.Get("version") != "2" {
				w.WriteHeader(400)
				return
			}
			f.writes++
			f.enabled = r.Form.Get("enabled") == "true"
		case "set_server_alias":
			if r.Form.Has("enabled") || r.Form.Get("version") != "2" {
				w.WriteHeader(400)
				return
			}
			f.writes++
			f.aliasWrites++
			f.identifier = r.Form.Get("server_alias")
		case "set_misc_config":
			f.writes++
			f.relay = r.Form.Get("relay_enabled") == "true"
			if f.failRelay {
				w.WriteHeader(503)
				fmt.Fprint(w, "fixture-session")
				return
			}
		default:
			w.WriteHeader(400)
			return
		}
	case quickConnectAPI + ".Permission":
		switch method {
		case "get":
			entries := []map[string]any{}
			for id, enabled := range f.services {
				entries = append(entries, map[string]any{"id": id, "enabled": enabled})
			}
			data = map[string]any{"services": entries}
		case "set":
			var entries []struct {
				ID      string `json:"id"`
				Enabled bool   `json:"enabled"`
			}
			if json.Unmarshal([]byte(r.Form.Get("services")), &entries) != nil {
				w.WriteHeader(400)
				return
			}
			f.writes++
			f.services = map[string]bool{}
			for _, e := range entries {
				f.services[e.ID] = e.Enabled
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
func TestAccQuickConnectLifecycle(t *testing.T) {
	f := newQuickConnectFixture()
	prefix := fixtureTLSConfig(t, f.serve)
	adopt := prefix + "resource \"synology_quickconnect\" \"test\" {}\nimport {\nto=synology_quickconnect.test\nid=\"quickconnect\"\n}\n"
	config := func(enabled bool) string {
		return prefix + fmt.Sprintf("resource \"synology_quickconnect\" \"test\" {\nenabled=%t\nidentifier=\"fixture-new\"\nrelay_enabled=true\nservices={dsm_portal=true,mobile_apps=true,file_sharing=false}\n}\n", enabled)
	}
	check := func(writes, alias int) resource.TestCheckFunc {
		return func(_ *terraform.State) error {
			f.mu.Lock()
			defer f.mu.Unlock()
			if f.writes != writes || f.aliasWrites != alias {
				return fmt.Errorf("unexpected QuickConnect writes %d/%d", f.writes, f.aliasWrites)
			}
			return nil
		}
	}
	resource.Test(t, resource.TestCase{ProtoV6ProviderFactories: map[string]func() (tfprotov6.ProviderServer, error){"synology": providerserver.NewProtocol6WithError(New("test")())}, CheckDestroy: check(6, 1), Steps: []resource.TestStep{
		{Config: adopt, Check: check(0, 0)},
		{ResourceName: "synology_quickconnect.test", ImportState: true, ImportStateId: "quickconnect", ImportStateVerify: true},
		{Config: config(true), Check: check(4, 1)},
		{Config: config(true), PlanOnly: true, ExpectNonEmptyPlan: true, PreConfig: func() { f.mu.Lock(); f.relay = false; f.mu.Unlock() }},
		{Config: config(true), Check: check(5, 1)},
		{Config: config(false), Check: check(6, 1)},
	}})
}
func TestAccQuickConnectPartialFailure(t *testing.T) {
	f := newQuickConnectFixture()
	prefix := fixtureTLSConfig(t, f.serve)
	adopt := prefix + "resource \"synology_quickconnect\" \"test\" {}\nimport {\nto=synology_quickconnect.test\nid=\"quickconnect\"\n}\n"
	config := prefix + "resource \"synology_quickconnect\" \"test\" {\nrelay_enabled=true\nservices={dsm_portal=true,mobile_apps=true,file_sharing=false}\n}\n"
	resource.Test(t, resource.TestCase{ProtoV6ProviderFactories: map[string]func() (tfprotov6.ProviderServer, error){"synology": providerserver.NewProtocol6WithError(New("test")())}, Steps: []resource.TestStep{
		{Config: adopt},
		{Config: config, ExpectError: regexp.MustCompile("Cannot update QuickConnect"), PreConfig: func() { f.mu.Lock(); f.failRelay = true; f.mu.Unlock() }},
		{Config: strings.Replace(config, "relay_enabled=true", "relay_enabled=false", 1), ExpectError: regexp.MustCompile("requires reconciliation")},
		{Config: prefix + "removed {\nfrom=synology_quickconnect.test\nlifecycle {destroy=false}\n}\n"},
		{Config: adopt, Check: func(s *terraform.State) error {
			f.mu.Lock()
			defer f.mu.Unlock()
			if f.writes != 2 || !f.relay || !f.services["mobile_apps"] {
				return fmt.Errorf("partial failure lost actual state or replayed writes")
			}
			for _, r := range s.RootModule().Resources {
				for _, v := range r.Primary.Attributes {
					if strings.Contains(v, "fixture-session") {
						return fmt.Errorf("session entered state")
					}
				}
			}
			return nil
		}},
	}})
}
