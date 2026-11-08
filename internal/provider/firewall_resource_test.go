package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/providerserver"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-go/tfprotov6"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
)

type firewallFixture struct {
	mu                                      sync.Mutex
	enabled, portCheck                      bool
	profileName                             string
	profiles                                map[string]map[string]firewallAdapter
	active                                  map[string]firewallAdapter
	pending                                 string
	statusReads, writes, starts, port       int
	lostStart, failProfileRead, discardSave bool
	failStatusOnce, failFinalReadOnce       bool
}

func newFirewallFixture() *firewallFixture {
	policy := map[string]firewallAdapter{"global": {DefaultPolicy: "none", Rules: []firewallRule{}}}
	return &firewallFixture{enabled: true, portCheck: true, profileName: "custom", profiles: map[string]map[string]firewallAdapter{"custom": policy}, active: policy}
}
func (f *firewallFixture) serve(w http.ResponseWriter, r *http.Request) {
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
		catalog := map[string]any{"SYNO.API.Auth": map[string]any{"path": "entry.cgi", "minVersion": 1, "maxVersion": 7}}
		for _, a := range []string{firewallAPI, firewallAPI + ".Conf", firewallProfileAPI, firewallApplyAPI, "SYNO.Core.Web.DSM"} {
			catalog[a] = map[string]any{"path": "entry.cgi", "minVersion": 1, "maxVersion": 2}
		}
		data = catalog
	case "SYNO.API.Auth":
		data = map[string]any{"sid": "fixture-session"}
	case "SYNO.Core.Web.DSM":
		data = map[string]any{"https_port": f.port}
	case firewallAPI:
		switch method {
		case "get":
			data = map[string]any{"enable_firewall": f.enabled, "profile_name": f.profileName}
		case "set":
			if r.Form.Get("set_type") != "disable" {
				w.WriteHeader(400)
				return
			}
			f.writes++
			f.enabled = false
		default:
			w.WriteHeader(400)
			return
		}
	case firewallAPI + ".Conf":
		if method == "get" {
			data = map[string]any{"enable_port_check": f.portCheck}
		} else if method == "set" {
			f.writes++
			f.portCheck = r.Form.Get("enable_port_check") == "true"
		} else {
			w.WriteHeader(400)
			return
		}
	case firewallProfileAPI:
		switch method {
		case "get":
			if f.failFinalReadOnce && f.statusReads >= 2 && f.starts > 0 {
				f.failFinalReadOnce = false
				w.WriteHeader(503)
				return
			}
			if f.failProfileRead && f.writes > 0 {
				w.WriteHeader(503)
				return
			}
			profile, ok := f.profiles[r.Form.Get("name")]
			if !ok {
				json.NewEncoder(w).Encode(map[string]any{"success": false, "error": map[string]int{"code": 120}})
				return
			}
			raw := map[string]any{"name": r.Form.Get("name")}
			for key, value := range profile {
				raw[key] = value
			}
			data = raw
		case "set":
			var raw map[string]json.RawMessage
			if json.Unmarshal([]byte(r.Form.Get("profile")), &raw) != nil {
				w.WriteHeader(400)
				return
			}
			var name string
			if json.Unmarshal(raw["name"], &name) != nil {
				w.WriteHeader(400)
				return
			}
			profile := map[string]firewallAdapter{}
			for key, value := range raw {
				if key == "name" {
					continue
				}
				var adapter firewallAdapter
				if json.Unmarshal(value, &adapter) != nil {
					w.WriteHeader(400)
					return
				}
				profile[key] = adapter
			}
			f.writes++
			if !f.discardSave {
				f.profiles[name] = profile
			}
		default:
			w.WriteHeader(400)
			return
		}
	case firewallApplyAPI:
		switch method {
		case "start":
			f.writes++
			f.starts++
			f.pending = r.Form.Get("name")
			f.statusReads = 0
			if f.lostStart {
				w.WriteHeader(503)
				return
			}
			data = map[string]any{"task_id": "firewall-fixture"}
		case "status":
			if r.Form.Get("task_id") != "firewall-fixture" {
				w.WriteHeader(400)
				return
			}
			f.statusReads++
			if f.failStatusOnce {
				f.failStatusOnce = false
				w.WriteHeader(503)
				return
			}
			if f.statusReads >= 2 {
				f.profileName = f.pending
				f.enabled = true
				f.active = f.profiles[f.pending]
				data = map[string]any{"success": true}
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
func firewallFixtureConfig(t *testing.T, f *firewallFixture) string {
	prefix := fixtureTLSConfig(t, f.serve)
	for _, line := range strings.Split(prefix, "\n") {
		if strings.HasPrefix(line, "endpoint=") {
			var endpoint string
			if json.Unmarshal([]byte(strings.TrimPrefix(line, "endpoint=")), &endpoint) != nil {
				t.Fatal("invalid fixture endpoint")
			}
			u, err := url.Parse(endpoint)
			if err != nil {
				t.Fatal(err)
			}
			f.port, _ = strconv.Atoi(u.Port())
		}
	}
	return prefix
}
func firewallTestPolicy(prefix string, port int, name string) string {
	return prefix + fmt.Sprintf(`
resource "synology_firewall" "test" {
 enabled=true
 port_check_enabled=true
 profile_name="custom"
 management_networks=["192.0.2.0/24","2001:db8::/64"]
 adapters={global={default_policy="none",rules=[
 {name=%q,protocol="tcp",port_group="custom",ports=%q,port_direction="destination",source_group="netmask",source="192.0.2.0/255.255.255.0"},
 {name="IPv6 management",protocol="tcp",port_group="custom",ports=%q,port_direction="destination",source_group="netmask",source="2001:db8::/64"}
 ]}}
}
`, name, strconv.Itoa(port), strconv.Itoa(port))
}
func TestAccFirewallLifecycle(t *testing.T) {
	f := newFirewallFixture()
	prefix := firewallFixtureConfig(t, f)
	adopt := prefix + "resource \"synology_firewall\" \"test\" {}\nimport {\nto=synology_firewall.test\nid=\"firewall\"\n}\n"
	check := func(writes, starts int) resource.TestCheckFunc {
		return func(_ *terraform.State) error {
			f.mu.Lock()
			defer f.mu.Unlock()
			if f.writes != writes || f.starts != starts {
				return fmt.Errorf("unexpected firewall writes %d/%d", f.writes, f.starts)
			}
			if starts > 0 && f.active["global"].Rules[0].Name != "fixture-managed" {
				return fmt.Errorf("stored policy was mistaken for activated policy")
			}
			return nil
		}
	}
	policy := firewallTestPolicy(prefix, f.port, "fixture-managed")
	resource.Test(t, resource.TestCase{ProtoV6ProviderFactories: map[string]func() (tfprotov6.ProviderServer, error){"synology": providerserver.NewProtocol6WithError(New("test")())}, CheckDestroy: check(4, 2), Steps: []resource.TestStep{
		{Config: adopt, Check: check(0, 0)},
		{ResourceName: "synology_firewall.test", ImportState: true, ImportStateId: "firewall", ImportStateVerify: true},
		{Config: policy, Check: check(2, 1)},
		{Config: policy, PlanOnly: true, ExpectNonEmptyPlan: true, PreConfig: func() {
			f.mu.Lock()
			a := f.profiles["custom"]["global"]
			a.Rules[0].Name = "outside"
			f.profiles["custom"]["global"] = a
			f.mu.Unlock()
		}},
		{Config: policy, Check: check(4, 2)},
	}})
}
func TestAccFirewallPartialFailure(t *testing.T) {
	for _, mode := range []string{"lost_start", "saved_read_failure", "discarded_save"} {
		t.Run(mode, func(t *testing.T) {
			f := newFirewallFixture()
			prefix := firewallFixtureConfig(t, f)
			adopt := prefix + "resource \"synology_firewall\" \"test\" {}\nimport {\nto=synology_firewall.test\nid=\"firewall\"\n}\n"
			policy := firewallTestPolicy(prefix, f.port, "fixture-managed")
			resource.Test(t, resource.TestCase{ProtoV6ProviderFactories: map[string]func() (tfprotov6.ProviderServer, error){"synology": providerserver.NewProtocol6WithError(New("test")())}, Steps: []resource.TestStep{
				{Config: adopt},
				{Config: policy, ExpectError: regexp.MustCompile("Cannot update firewall"), PreConfig: func() {
					f.mu.Lock()
					f.lostStart = mode == "lost_start"
					f.failProfileRead = mode == "saved_read_failure"
					f.discardSave = mode == "discarded_save"
					f.mu.Unlock()
				}},
				{Config: policy, ExpectError: regexp.MustCompile("Cannot read firewall|Pending firewall operation"), PreConfig: func() { f.mu.Lock(); f.failProfileRead = false; f.mu.Unlock() }},
				{Config: prefix + "removed {\nfrom=synology_firewall.test\nlifecycle {destroy=false}\n}\n", Check: func(_ *terraform.State) error {
					f.mu.Lock()
					defer f.mu.Unlock()
					want := 1
					if mode == "lost_start" {
						want = 2
					}
					if f.writes != want {
						return fmt.Errorf("firewall failure replayed a write")
					}
					if mode != "lost_start" && f.starts != 0 {
						return fmt.Errorf("unconfirmed storage was activated")
					}
					return nil
				}},
			}})
		})
	}
}
func TestFirewallAdmissionAndWireContract(t *testing.T) {
	allow := firewallRule{Enabled: true, Action: "allow", Protocol: "tcp", PortGroup: "custom", Ports: "5001", PortDirection: "destination", SourceGroup: "netmask", Source: "192.0.2.0/255.255.255.0"}
	global := func(rules ...firewallRule) map[string]firewallAdapter {
		return map[string]firewallAdapter{"global": {DefaultPolicy: "none", Rules: rules}}
	}
	deny := allow
	deny.Action = "drop"
	for _, tc := range []struct {
		name     string
		rules    map[string]firewallAdapter
		networks []string
		err      bool
	}{
		{"allowed", global(allow), []string{"192.0.2.0/24"}, false},
		{"deny_before_allow", global(deny, allow), []string{"192.0.2.0/24"}, true},
		{"wrong_network", global(allow), []string{"198.51.100.0/24"}, true},
		{"missing_networks", global(allow), nil, true},
		{"ipv6_not_allowed", global(allow), []string{"2001:db8::/64"}, true},
		{"implicit_default_insufficient", global(), []string{"192.0.2.0/24"}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := admitFirewall(tc.rules, tc.networks, 5001)
			if (err != nil) != tc.err {
				t.Fatalf("admission: %v", err)
			}
		})
	}
	raw := map[string]json.RawMessage{"name": json.RawMessage(`"custom"`), "global": json.RawMessage(`{"policy":"none","rules":[]}`)}
	value, err := decodeFirewallProfile(context.Background(), raw, "custom")
	if err != nil {
		t.Fatal(err)
	}
	adapters, err := firewallAdapters(context.Background(), value)
	if err != nil || adapters["global"].Rules == nil {
		t.Fatalf("empty rules did not round trip: %v", err)
	}
	raw["global"] = json.RawMessage(`{"policy":"none","rules":[{"enable":true}]}`)
	if _, err := decodeFirewallProfile(context.Background(), raw, "custom"); err == nil {
		t.Fatal("incomplete rules adopted")
	}
	if _, err := firewallAdapters(context.Background(), types.MapUnknown(firewallAdapterType)); err == nil {
		t.Fatal("unknown policy admitted")
	}
}

func TestAccFirewallAcknowledgedTaskResumesReadOnly(t *testing.T) {
	for _, mode := range []string{"status_failure", "final_read_failure"} {
		t.Run(mode, func(t *testing.T) {
			f := newFirewallFixture()
			prefix := firewallFixtureConfig(t, f)
			adopt := prefix + "resource \"synology_firewall\" \"test\" {}\nimport {\nto=synology_firewall.test\nid=\"firewall\"\n}\n"
			policy := firewallTestPolicy(prefix, f.port, "fixture-managed")
			resource.Test(t, resource.TestCase{ProtoV6ProviderFactories: map[string]func() (tfprotov6.ProviderServer, error){"synology": providerserver.NewProtocol6WithError(New("test")())}, Steps: []resource.TestStep{
				{Config: adopt},
				{Config: policy, ExpectError: regexp.MustCompile("Cannot update firewall"), PreConfig: func() {
					f.mu.Lock()
					f.failStatusOnce = mode == "status_failure"
					f.failFinalReadOnce = mode == "final_read_failure"
					f.mu.Unlock()
				}},
				{Config: policy, Check: resource.ComposeAggregateTestCheckFunc(resource.TestCheckResourceAttr("synology_firewall.test", "reconciliation_required", "false"), func(_ *terraform.State) error {
					f.mu.Lock()
					defer f.mu.Unlock()
					if f.writes != 2 || f.starts != 1 || f.statusReads < 2 {
						return fmt.Errorf("acknowledged firewall apply was replayed or not polled")
					}
					return nil
				})},
			}})
		})
	}
}
