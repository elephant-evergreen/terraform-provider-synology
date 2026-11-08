package provider

import (
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/providerserver"
	"github.com/hashicorp/terraform-plugin-go/tfprotov6"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
)

type ddnsFixture struct {
	lostDelete                        bool
	mu                                sync.Mutex
	records                           map[string]ddnsRecord
	writes, passwordWrites, refreshes int
	failRead, lostAck                 bool
}

func newDDNSFixture() *ddnsFixture { return &ddnsFixture{records: map[string]ddnsRecord{}} }
func (f *ddnsFixture) serve(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if r.ParseForm() != nil {
		w.WriteHeader(400)
		return
	}
	var data any = map[string]any{}
	switch r.Form.Get("api") {
	case "SYNO.API.Info":
		data = map[string]any{"SYNO.API.Auth": map[string]any{"path": "entry.cgi", "minVersion": 1, "maxVersion": 7}, ddnsAPI: map[string]any{"path": "entry.cgi", "minVersion": 1, "maxVersion": 1}, "SYNO.Core.DDNS.Provider": map[string]any{"path": "entry.cgi", "minVersion": 1, "maxVersion": 1}}
	case "SYNO.API.Auth":
		data = map[string]any{"sid": "fixture-session"}
	case "SYNO.Core.DDNS.Provider":
		data = map[string]any{"providers": []map[string]any{{"id": "FixtureDNS", "url": "fixture-not-exported-secret"}}}
	case ddnsAPI:
		switch method := r.Form.Get("method"); method {
		case "list":
			if f.failRead && f.writes > 0 {
				w.WriteHeader(503)
				return
			}
			records := []map[string]any{}
			for _, record := range f.records {
				encoded, _ := json.Marshal(record)
				var raw map[string]any
				json.Unmarshal(encoded, &raw)
				raw["passwd"] = "fixture-stored-ddns-secret"
				records = append(records, raw)
			}
			data = map[string]any{"records": records, "next_update_time": 0}
		case "create", "set":
			id := r.Form.Get("id")
			if method == "create" {
				id = r.Form.Get("provider")
				if _, exists := f.records[id]; exists {
					json.NewEncoder(w).Encode(map[string]any{"success": false, "error": map[string]int{"code": 120}})
					return
				}
			}
			enable, err := strconv.ParseBool(r.Form.Get("enable"))
			if err != nil {
				w.WriteHeader(400)
				return
			}
			heartbeat, err := strconv.ParseBool(r.Form.Get("heartbeat"))
			if err != nil {
				w.WriteHeader(400)
				return
			}
			f.writes++
			if r.Form.Has("passwd") {
				f.passwordWrites++
			}
			f.records[id] = ddnsRecord{id, r.Form.Get("provider"), enable, r.Form.Get("hostname"), r.Form.Get("username"), r.Form.Get("net"), r.Form.Get("ip"), r.Form.Get("ipv6"), r.Form.Get("interface_v4"), r.Form.Get("interface_v6"), heartbeat, "service_ddns_normal"}
			if f.lostAck {
				w.WriteHeader(503)
				fmt.Fprint(w, "fixture-session fixture-stored-ddns-secret")
				return
			}
		case "update_ip_address":
			f.refreshes++
		case "delete":
			var providers []string
			if json.Unmarshal([]byte(r.Form.Get("id")), &providers) != nil || len(providers) != 1 {
				w.WriteHeader(400)
				return
			}
			f.writes++
			for id, record := range f.records {
				if record.ProviderID == providers[0] {
					delete(f.records, id)
				}
			}
			if f.lostDelete {
				w.WriteHeader(503)
				return
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
func ddnsFixtureRecord() ddnsRecord {
	return ddnsRecord{"FixtureDNS", "FixtureDNS", true, "nas.example.invalid", "fixture-user", "DEFAULT", "192.0.2.10", "2001:db8::10", "default", "default", false, "service_ddns_normal"}
}
func ddnsTestConfig(prefix, hostname string, version int, protection bool) string {
	return prefix + fmt.Sprintf("resource \"synology_ddns_record\" \"test\" {\nprovider_id=\"FixtureDNS\"\nhostname=%q\nusername=\"fixture-user\"\npassword_wo=\"fixture-rotation-secret\"\npassword_version=%d\ndeletion_protection=%t\n}\n", hostname, version, protection)
}
func TestAccDDNSLifecycle(t *testing.T) {
	f := newDDNSFixture()
	prefix := fixtureTLSConfig(t, f.serve)
	check := func(writes, passwords, refreshes int) resource.TestCheckFunc {
		return func(s *terraform.State) error {
			f.mu.Lock()
			defer f.mu.Unlock()
			if f.writes != writes || f.passwordWrites != passwords || f.refreshes != refreshes {
				return fmt.Errorf("unexpected DDNS writes %d/%d/%d", f.writes, f.passwordWrites, f.refreshes)
			}
			for _, r := range s.RootModule().Resources {
				for _, v := range r.Primary.Attributes {
					if strings.Contains(v, "secret") {
						return fmt.Errorf("DDNS secret entered state")
					}
				}
			}
			return nil
		}
	}
	resource.Test(t, resource.TestCase{ProtoV6ProviderFactories: map[string]func() (tfprotov6.ProviderServer, error){"synology": providerserver.NewProtocol6WithError(New("test")())}, CheckDestroy: func(_ *terraform.State) error {
		f.mu.Lock()
		defer f.mu.Unlock()
		if len(f.records) != 0 {
			return fmt.Errorf("DDNS destroy did not remove requested record")
		}
		return nil
	}, Steps: []resource.TestStep{
		{Config: ddnsTestConfig(prefix, "nas.example.invalid", 1, true), Check: check(1, 1, 1)},
		{ResourceName: "synology_ddns_record.test", ImportState: true, ImportStateId: "FixtureDNS", ImportStateVerify: true, ImportStateVerifyIgnore: []string{"password_version"}},
		{Config: ddnsTestConfig(prefix, "nas2.example.invalid", 1, true), Check: check(2, 1, 2)},
		{Config: ddnsTestConfig(prefix, "nas2.example.invalid", 1, true), PlanOnly: true, ExpectNonEmptyPlan: true, PreConfig: func() {
			f.mu.Lock()
			record := f.records["FixtureDNS"]
			record.Hostname = "outside.example.invalid"
			f.records["FixtureDNS"] = record
			f.mu.Unlock()
		}},
		{Config: ddnsTestConfig(prefix, "nas2.example.invalid", 1, true), Check: check(3, 1, 3)},
		{Config: ddnsTestConfig(prefix, "nas2.example.invalid", 2, true), Check: check(4, 2, 4)},
		{Config: ddnsTestConfig(prefix, "nas2.example.invalid", 2, false), Check: check(4, 2, 4)},
	}})
}
func TestAccDDNSAdoptionAndCollisions(t *testing.T) {
	f := newDDNSFixture()
	f.records["FixtureDNS"] = ddnsFixtureRecord()
	prefix := fixtureTLSConfig(t, f.serve)
	cfg := prefix + "resource \"synology_ddns_record\" \"test\" {\nprovider_id=\"FixtureDNS\"\n}\n"
	adopt := cfg + "import {\nto=synology_ddns_record.test\nid=\"FixtureDNS\"\n}\n"
	resource.Test(t, resource.TestCase{ProtoV6ProviderFactories: map[string]func() (tfprotov6.ProviderServer, error){"synology": providerserver.NewProtocol6WithError(New("test")())}, Steps: []resource.TestStep{
		{Config: ddnsTestConfig(prefix, "nas.example.invalid", 1, true), ExpectError: regexp.MustCompile("import its DSM record ID")},
		{Config: adopt},
		{Config: prefix + "data \"synology_ddns\" \"test\" {}\nremoved {\nfrom=synology_ddns_record.test\nlifecycle{destroy=false}\n}\n", Check: func(s *terraform.State) error {
			f.mu.Lock()
			defer f.mu.Unlock()
			if f.writes != 0 || f.refreshes != 0 {
				return fmt.Errorf("adoption changed DDNS")
			}
			return resource.TestCheckResourceAttr("data.synology_ddns.test", "records.FixtureDNS.hostname", "nas.example.invalid")(s)
		}},
	}})
}
func TestAccDDNSCredentialFailure(t *testing.T) {
	for _, lost := range []bool{false, true} {
		t.Run(fmt.Sprint(lost), func(t *testing.T) {
			f := newDDNSFixture()
			f.records["FixtureDNS"] = ddnsFixtureRecord()
			prefix := fixtureTLSConfig(t, f.serve)
			adopt := prefix + "resource \"synology_ddns_record\" \"test\" {\nprovider_id=\"FixtureDNS\"\n}\nimport {\nto=synology_ddns_record.test\nid=\"FixtureDNS\"\n}\n"
			cfg := ddnsTestConfig(prefix, "nas.example.invalid", 1, true)
			steps := []resource.TestStep{{Config: adopt}, {Config: cfg, ExpectError: regexp.MustCompile("Cannot update DDNS"), PreConfig: func() { f.mu.Lock(); f.lostAck = lost; f.failRead = !lost; f.mu.Unlock() }}}
			if lost {
				steps = append(steps, resource.TestStep{Config: cfg, ExpectError: regexp.MustCompile("requires reconciliation")})
			} else {
				steps = append(steps, resource.TestStep{Config: cfg, PreConfig: func() { f.mu.Lock(); f.failRead = false; f.mu.Unlock() }})
			}
			steps = append(steps, resource.TestStep{Config: prefix + "removed {\nfrom=synology_ddns_record.test\nlifecycle{destroy=false}\n}\n", Check: func(_ *terraform.State) error {
				f.mu.Lock()
				defer f.mu.Unlock()
				if f.writes != 1 || f.passwordWrites != 1 {
					return fmt.Errorf("ambiguous DDNS write replayed")
				}
				return nil
			}})
			resource.Test(t, resource.TestCase{ProtoV6ProviderFactories: map[string]func() (tfprotov6.ProviderServer, error){"synology": providerserver.NewProtocol6WithError(New("test")())}, Steps: steps})
		})
	}
}

func TestAccDDNSUncertainCreationRequiresImport(t *testing.T) {
	f := newDDNSFixture()
	prefix := fixtureTLSConfig(t, f.serve)
	cfg := ddnsTestConfig(prefix, "nas.example.invalid", 1, true)
	adopt := prefix + "resource \"synology_ddns_record\" \"test\" {\nprovider_id=\"FixtureDNS\"\n}\nimport {\nto=synology_ddns_record.test\nid=\"FixtureDNS\"\n}\n"
	resource.Test(t, resource.TestCase{ProtoV6ProviderFactories: map[string]func() (tfprotov6.ProviderServer, error){"synology": providerserver.NewProtocol6WithError(New("test")())}, Steps: []resource.TestStep{
		{Config: cfg, ExpectError: regexp.MustCompile("Cannot create DDNS record"), PreConfig: func() { f.mu.Lock(); f.lostAck = true; f.mu.Unlock() }},
		{Config: cfg, ExpectError: regexp.MustCompile("requires reconciliation")},
		{Config: prefix + "removed {\nfrom=synology_ddns_record.test\nlifecycle{destroy=false}\n}\n"},
		{Config: adopt, Check: func(_ *terraform.State) error {
			f.mu.Lock()
			defer f.mu.Unlock()
			if f.writes != 1 || f.passwordWrites != 1 {
				return fmt.Errorf("unacknowledged DDNS creation was adopted or replayed")
			}
			return nil
		}},
		{Config: prefix + "removed {\nfrom=synology_ddns_record.test\nlifecycle{destroy=false}\n}\n"},
	}})
}
func TestAccDDNSDeleteAcknowledgementLossConfirmsAbsence(t *testing.T) {
	f := newDDNSFixture()
	f.records["FixtureDNS"] = ddnsFixtureRecord()
	prefix := fixtureTLSConfig(t, f.serve)
	cfg := prefix + "resource \"synology_ddns_record\" \"test\" {\nprovider_id=\"FixtureDNS\"\ndeletion_protection=false\n}\nimport {\nto=synology_ddns_record.test\nid=\"FixtureDNS\"\n}\n"
	resource.Test(t, resource.TestCase{ProtoV6ProviderFactories: map[string]func() (tfprotov6.ProviderServer, error){"synology": providerserver.NewProtocol6WithError(New("test")())}, Steps: []resource.TestStep{
		{Config: cfg},
		{Config: cfg, Destroy: true, ExpectError: regexp.MustCompile("Cannot delete DDNS record"), PreConfig: func() { f.mu.Lock(); f.lostDelete = true; f.mu.Unlock() }},
		{Config: cfg, Destroy: true, Check: func(_ *terraform.State) error {
			f.mu.Lock()
			defer f.mu.Unlock()
			if f.writes != 1 || len(f.records) != 0 {
				return fmt.Errorf("uncertain DDNS deletion was repeated")
			}
			return nil
		}},
	}})
}

func TestAccDDNSDeleteUsesProviderArray(t *testing.T) {
	f := newDDNSFixture()
	record := ddnsFixtureRecord()
	record.ID = "fixture-record-7"
	f.records[record.ID] = record
	prefix := fixtureTLSConfig(t, f.serve)
	cfg := prefix + "resource \"synology_ddns_record\" \"test\" {\nprovider_id=\"FixtureDNS\"\nhostname=\"nas.example.invalid\"\ndeletion_protection=false\n}\nimport {\nto=synology_ddns_record.test\nid=\"fixture-record-7\"\n}\n"
	resource.Test(t, resource.TestCase{ProtoV6ProviderFactories: map[string]func() (tfprotov6.ProviderServer, error){"synology": providerserver.NewProtocol6WithError(New("test")())}, Steps: []resource.TestStep{
		{Config: cfg},
		{Config: cfg, Destroy: true, Check: func(_ *terraform.State) error {
			f.mu.Lock()
			defer f.mu.Unlock()
			if len(f.records) != 0 || f.writes != 1 {
				return fmt.Errorf("DDNS delete did not identify the provider array")
			}
			return nil
		}},
	}})
}
