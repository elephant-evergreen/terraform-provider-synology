package provider

import (
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"sync"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/providerserver"
	"github.com/hashicorp/terraform-plugin-go/tfprotov6"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
)

type packageFixture struct {
	center                                                    map[string]any
	mu                                                        sync.Mutex
	records                                                   map[string]installedPackage
	writes                                                    map[string]int
	extraQueue, lostControl, lostUninstall, failFinalReadOnce bool
}

func fixturePackage() installedPackage {
	return installedPackage{ID: "FixturePackage", Name: "Fixture package", Version: "1.2-10", Status: "running", Volume: "system", Startable: true, CanUninstall: true, Apps: "Fixture.Application"}
}
func newPackageFixture() *packageFixture {
	return &packageFixture{center: map[string]any{"enable_autoupdate": false, "autoupdateall": false, "autoupdateimportant": false, "update_channel": false, "enable_email": false, "enable_dsm": true, "mailset": false, "trust_level": 1}, records: map[string]installedPackage{}, writes: map[string]int{}}
}
func (f *packageFixture) serve(w http.ResponseWriter, r *http.Request) {
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
		for _, id := range []string{packageAPI, packageAPI + ".Control", packageAPI + ".Uninstallation", packageAPI + ".Setting"} {
			catalog[id] = map[string]any{"path": "entry.cgi", "minVersion": 1, "maxVersion": 2}
		}
		data = catalog
	case "SYNO.API.Auth":
		data = map[string]any{"sid": "fixture-session"}
	case packageAPI:
		switch method {
		case "list":
			if f.failFinalReadOnce && f.writes["stop"]+f.writes["start"] > 0 {
				f.failFinalReadOnce = false
				w.WriteHeader(503)
				return
			}
			records := []map[string]any{}
			for _, record := range f.records {
				path := record.Volume + "/@appstore/" + record.ID
				if record.Volume == "system" {
					path = "/usr/local/packages/@appstore/" + record.ID
				}
				records = append(records, map[string]any{"id": record.ID, "name": record.Name, "version": record.Version, "additional": map[string]any{"status": record.Status, "startable": record.Startable, "ctl_uninstall": record.CanUninstall, "is_uninstall_pages": record.CustomUninstall, "dsm_apps": record.Apps, "installed_info": map[string]any{"path": path, "is_broken": false}, "autoupdate": record.AutomaticUpdates, "autoupdate_important": record.ImportantUpdates, "silent_upgrade": record.SilentUpgrade, "beta": record.Beta}})
			}
			data = map[string]any{"packages": records, "total": len(records)}
		case "get_queue":
			var operations []struct {
				ID        string `json:"pkg"`
				Operation string `json:"operation"`
			}
			if json.Unmarshal([]byte(r.Form.Get("operations")), &operations) != nil || len(operations) != 1 {
				w.WriteHeader(400)
				return
			}
			steps := []map[string]any{{"pkg": operations[0].ID, "operation": operations[0].Operation}}
			if f.extraQueue {
				steps = append(steps, map[string]any{"pkg": "FixtureDependency", "operation": "stop"})
			}
			data = map[string]any{"steps": steps}
		case "feasibility_check":
			data = map[string]any{}
		default:
			w.WriteHeader(400)
			return
		}
	case packageAPI + ".Setting":
		if method == "get" {
			data = f.center
		} else if method == "set" {
			if r.Form.Get("update_channel") != "stable" && r.Form.Get("update_channel") != "beta" || r.Form.Has("trust_level") || r.Form.Has("mailset") {
				w.WriteHeader(400)
				return
			}
			f.writes["settings"]++
			for _, key := range []string{"enable_autoupdate", "autoupdateall", "autoupdateimportant", "enable_email", "enable_dsm"} {
				if !r.Form.Has(key) {
					w.WriteHeader(400)
					return
				}
				f.center[key] = r.Form.Get(key) == "true"
			}
			f.center["update_channel"] = r.Form.Get("update_channel") == "beta"
			if r.Form.Has("packages") {
				var all, important []string
				if json.Unmarshal([]byte(r.Form.Get("packages")), &all) != nil || json.Unmarshal([]byte(r.Form.Get("packages_important")), &important) != nil {
					w.WriteHeader(400)
					return
				}
				selected := map[string]bool{}
				for _, id := range all {
					selected[id] = true
				}
				selectedImportant := map[string]bool{}
				for _, id := range important {
					selectedImportant[id] = true
				}
				for id, record := range f.records {
					record.AutomaticUpdates = selected[id]
					record.ImportantUpdates = selectedImportant[id]
					f.records[id] = record
				}
			}
		} else {
			w.WriteHeader(400)
			return
		}
	case packageAPI + ".Control":
		f.writes[method]++
		record, ok := f.records[r.Form.Get("id")]
		if !ok {
			w.WriteHeader(400)
			return
		}
		if method == "start" {
			if !r.Form.Has("dsm_apps") {
				w.WriteHeader(400)
				return
			}
			record.Status = "running"
		} else if method == "stop" {
			record.Status = "stop"
		} else {
			w.WriteHeader(400)
			return
		}
		f.records[record.ID] = record
		if f.lostControl {
			w.WriteHeader(503)
			return
		}
		data = map[string]any{"success": true}
	case packageAPI + ".Uninstallation":
		if method != "uninstall" {
			w.WriteHeader(400)
			return
		}
		f.writes[method]++
		delete(f.records, r.Form.Get("id"))
		if f.lostUninstall {
			w.WriteHeader(503)
			return
		}
		data = map[string]any{"id": "FixturePackage", "success": true}
	default:
		w.WriteHeader(400)
		return
	}
	envelope := map[string]any{"success": true, "data": data}
	json.NewEncoder(w).Encode(envelope)
}
func packageTestConfig(prefix string, running, protection bool) string {
	return prefix + fmt.Sprintf("resource \"synology_package\" \"test\" {\npackage_id=\"FixturePackage\"\nrunning=%t\ndeletion_protection=%t\n}\n", running, protection)
}
func packageAdoptConfig(prefix string) string {
	return prefix + "resource \"synology_package\" \"test\" {\npackage_id=\"FixturePackage\"\n}\nimport {\nto=synology_package.test\nid=\"FixturePackage\"\n}\n"
}
func TestAccPackageLifecycle(t *testing.T) {
	f := newPackageFixture()
	record := fixturePackage()
	f.records[record.ID] = record
	prefix := fixtureTLSConfig(t, f.serve)
	check := func(stops int) resource.TestCheckFunc {
		return func(_ *terraform.State) error {
			f.mu.Lock()
			defer f.mu.Unlock()
			if f.writes["stop"] != stops {
				return fmt.Errorf("unexpected package writes: %v", f.writes)
			}
			return nil
		}
	}
	resource.Test(t, resource.TestCase{ProtoV6ProviderFactories: map[string]func() (tfprotov6.ProviderServer, error){"synology": providerserver.NewProtocol6WithError(New("test")())}, CheckDestroy: func(_ *terraform.State) error {
		f.mu.Lock()
		defer f.mu.Unlock()
		if len(f.records) != 0 || f.writes["uninstall"] != 1 {
			return fmt.Errorf("requested uninstall was not completed")
		}
		return nil
	}, Steps: []resource.TestStep{
		{Config: packageAdoptConfig(prefix), Check: check(0)},
		{ResourceName: "synology_package.test", ImportState: true, ImportStateId: "FixturePackage", ImportStateVerify: true},
		{Config: packageTestConfig(prefix, false, true), Check: check(1)},
		{Config: packageTestConfig(prefix, false, true), PlanOnly: true, ExpectNonEmptyPlan: true, PreConfig: func() {
			f.mu.Lock()
			r := f.records["FixturePackage"]
			r.Status = "running"
			f.records[r.ID] = r
			f.mu.Unlock()
		}},
		{Config: packageTestConfig(prefix, false, true), Check: check(2)},
		{Config: packageTestConfig(prefix, true, true), Check: resource.TestCheckResourceAttr("synology_package.test", "running", "true")},
		{Config: packageTestConfig(prefix, true, false), Check: check(2)},
	}})
}
func TestAccPackageAdoptionAndProtection(t *testing.T) {
	f := newPackageFixture()
	record := fixturePackage()
	f.records[record.ID] = record
	prefix := fixtureTLSConfig(t, f.serve)
	adopt := prefix + "resource \"synology_package\" \"test\" {\npackage_id=\"FixturePackage\"\n}\nimport {\nto=synology_package.test\nid=\"FixturePackage\"\n}\n"
	resource.Test(t, resource.TestCase{ProtoV6ProviderFactories: map[string]func() (tfprotov6.ProviderServer, error){"synology": providerserver.NewProtocol6WithError(New("test")())}, Steps: []resource.TestStep{
		{Config: packageTestConfig(prefix, true, true), ExpectError: regexp.MustCompile("Package import required")},
		{Config: adopt},
		{Config: adopt, Destroy: true, ExpectError: regexp.MustCompile("Package is protected")},
		{Config: prefix + "data \"synology_packages\" \"test\" {}\nremoved {\nfrom=synology_package.test\nlifecycle{destroy=false}\n}\n", Check: func(s *terraform.State) error {
			f.mu.Lock()
			defer f.mu.Unlock()
			if len(f.writes) != 0 {
				return fmt.Errorf("adoption or management release changed software")
			}
			return resource.TestCheckResourceAttr("data.synology_packages.test", "packages.FixturePackage.version", "1.2-10")(s)
		}},
	}})
}
func TestAccPackageControlFailureRecovery(t *testing.T) {
	for _, mode := range []string{"ack_read_failure", "lost_ack"} {
		t.Run(mode, func(t *testing.T) {
			f := newPackageFixture()
			record := fixturePackage()
			f.records[record.ID] = record
			prefix := fixtureTLSConfig(t, f.serve)
			cfg := packageTestConfig(prefix, false, true)
			steps := []resource.TestStep{
				{Config: packageAdoptConfig(prefix)},
				{Config: cfg, ExpectError: regexp.MustCompile("Cannot update package"), PreConfig: func() {
					f.mu.Lock()
					f.lostControl = mode == "lost_ack"
					f.failFinalReadOnce = mode == "ack_read_failure"
					f.mu.Unlock()
				}},
			}
			if mode == "lost_ack" {
				steps = append(steps, resource.TestStep{Config: cfg, ExpectError: regexp.MustCompile("acknowledgement is uncertain|Pending package")}, resource.TestStep{Config: prefix + "removed {\nfrom=synology_package.test\nlifecycle{destroy=false}\n}\n", RefreshState: false}, resource.TestStep{Config: packageAdoptConfig(prefix)})
			} else {
				steps = append(steps, resource.TestStep{Config: cfg, Check: resource.TestCheckResourceAttr("synology_package.test", "reconciliation_required", "false")})
			}
			steps = append(steps, resource.TestStep{Config: prefix + "removed {\nfrom=synology_package.test\nlifecycle{destroy=false}\n}\n", Check: func(_ *terraform.State) error {
				f.mu.Lock()
				defer f.mu.Unlock()
				if f.writes["stop"] != 1 {
					return fmt.Errorf("uncertain or acknowledged control was repeated")
				}
				return nil
			}})
			resource.Test(t, resource.TestCase{ProtoV6ProviderFactories: map[string]func() (tfprotov6.ProviderServer, error){"synology": providerserver.NewProtocol6WithError(New("test")())}, Steps: steps})
		})
	}
}
func TestAccPackageExtraDependencyActionsAreRefused(t *testing.T) {
	f := newPackageFixture()
	record := fixturePackage()
	f.records[record.ID] = record
	prefix := fixtureTLSConfig(t, f.serve)
	adopt := prefix + "resource \"synology_package\" \"test\" {\npackage_id=\"FixturePackage\"\n}\nimport {\nto=synology_package.test\nid=\"FixturePackage\"\n}\n"
	resource.Test(t, resource.TestCase{ProtoV6ProviderFactories: map[string]func() (tfprotov6.ProviderServer, error){"synology": providerserver.NewProtocol6WithError(New("test")())}, Steps: []resource.TestStep{
		{Config: adopt},
		{Config: packageTestConfig(prefix, false, true), ExpectError: regexp.MustCompile("additional packages"), PreConfig: func() { f.mu.Lock(); f.extraQueue = true; f.mu.Unlock() }},
		{Config: prefix + "removed {\nfrom=synology_package.test\nlifecycle{destroy=false}\n}\n", Check: func(_ *terraform.State) error {
			f.mu.Lock()
			defer f.mu.Unlock()
			if len(f.writes) != 0 {
				return fmt.Errorf("dependency preflight changed software")
			}
			return nil
		}},
	}})
}
func TestAccPackageCenterPolicy(t *testing.T) {
	f := newPackageFixture()
	record := fixturePackage()
	f.records[record.ID] = record
	prefix := fixtureTLSConfig(t, f.serve)
	adopt := prefix + "resource \"synology_package_center\" \"test\" {}\nimport {\nto=synology_package_center.test\nid=\"package_center\"\n}\n"
	cfg := prefix + "resource \"synology_package_center\" \"test\" {\nenabled=true\nupdate_all=false\nimportant_only=false\npackages=[\"FixturePackage\"]\nimportant_packages=[]\nbeta_channel=true\n}\n"
	check := func(count int) resource.TestCheckFunc {
		return func(_ *terraform.State) error {
			f.mu.Lock()
			defer f.mu.Unlock()
			if f.writes["settings"] != count || f.center["enable_dsm"] != true || f.center["enable_email"] != false || f.center["trust_level"] != 1 {
				return fmt.Errorf("Package Center write or coupled-setting preservation failed")
			}
			return nil
		}
	}
	resource.Test(t, resource.TestCase{ProtoV6ProviderFactories: map[string]func() (tfprotov6.ProviderServer, error){"synology": providerserver.NewProtocol6WithError(New("test")())}, CheckDestroy: check(2), Steps: []resource.TestStep{
		{Config: adopt, Check: check(0)},
		{ResourceName: "synology_package_center.test", ImportState: true, ImportStateId: "package_center", ImportStateVerify: true},
		{Config: cfg, Check: check(1)},
		{Config: cfg, PlanOnly: true, ExpectNonEmptyPlan: true, PreConfig: func() { f.mu.Lock(); f.center["update_channel"] = false; f.mu.Unlock() }},
		{Config: cfg, Check: check(2)},
	}})
}

func TestAccPackageNeverInstallsSoftware(t *testing.T) {
	for _, present := range []bool{false, true} {
		t.Run(fmt.Sprint(present), func(t *testing.T) {
			f := newPackageFixture()
			if present {
				r := fixturePackage()
				f.records[r.ID] = r
			}
			prefix := fixtureTLSConfig(t, f.serve)
			resource.Test(t, resource.TestCase{ProtoV6ProviderFactories: map[string]func() (tfprotov6.ProviderServer, error){"synology": providerserver.NewProtocol6WithError(New("test")())}, Steps: []resource.TestStep{
				{Config: packageTestConfig(prefix, true, true), ExpectError: regexp.MustCompile("Package import required")},
				{Config: prefix + "data \"synology_packages\" \"test\" {}\n", Check: func(_ *terraform.State) error {
					f.mu.Lock()
					defer f.mu.Unlock()
					if len(f.writes) != 0 {
						return fmt.Errorf("unsupported creation changed software")
					}
					return nil
				}},
			}})
		})
	}
}
func TestAccPackageVersionAndPlacementAreObservationOnly(t *testing.T) {
	f := newPackageFixture()
	r := fixturePackage()
	f.records[r.ID] = r
	prefix := fixtureTLSConfig(t, f.serve)
	resource.Test(t, resource.TestCase{ProtoV6ProviderFactories: map[string]func() (tfprotov6.ProviderServer, error){"synology": providerserver.NewProtocol6WithError(New("test")())}, Steps: []resource.TestStep{
		{Config: packageAdoptConfig(prefix)},
		{Config: prefix + "resource \"synology_package\" \"test\" {\npackage_id=\"FixturePackage\"\nversion=\"2.0-1\"\nvolume=\"/volume2\"\n}\n", ExpectError: regexp.MustCompile("Read-Only Attribute")},
		{Config: packageAdoptConfig(prefix), PreConfig: func() {
			f.mu.Lock()
			r := f.records["FixturePackage"]
			r.Version = "2.0-1"
			f.records[r.ID] = r
			f.mu.Unlock()
		}, Check: resource.TestCheckResourceAttr("synology_package.test", "version", "2.0-1")},
		{Config: prefix + "removed {\nfrom=synology_package.test\nlifecycle{destroy=false}\n}\n", Check: func(_ *terraform.State) error {
			f.mu.Lock()
			defer f.mu.Unlock()
			if len(f.writes) != 0 {
				return fmt.Errorf("version observation changed software")
			}
			return nil
		}},
	}})
}
func TestAccPackageUninstallAcknowledgementLossConfirmsAbsence(t *testing.T) {
	f := newPackageFixture()
	record := fixturePackage()
	f.records[record.ID] = record
	prefix := fixtureTLSConfig(t, f.serve)
	cfg := prefix + "resource \"synology_package\" \"test\" {\npackage_id=\"FixturePackage\"\ndeletion_protection=false\n}\nimport {\nto=synology_package.test\nid=\"FixturePackage\"\n}\n"
	resource.Test(t, resource.TestCase{ProtoV6ProviderFactories: map[string]func() (tfprotov6.ProviderServer, error){"synology": providerserver.NewProtocol6WithError(New("test")())}, Steps: []resource.TestStep{
		{Config: cfg},
		{Config: cfg, Destroy: true, ExpectError: regexp.MustCompile("Cannot uninstall package"), PreConfig: func() { f.mu.Lock(); f.lostUninstall = true; f.mu.Unlock() }},
		{Config: cfg, Destroy: true, Check: func(_ *terraform.State) error {
			f.mu.Lock()
			defer f.mu.Unlock()
			if f.writes["uninstall"] != 1 || len(f.records) != 0 {
				return fmt.Errorf("uncertain package deletion was repeated")
			}
			return nil
		}},
	}})
}

func TestAccPackageUnknownStatusRefusesControl(t *testing.T) {
	f := newPackageFixture()
	r := fixturePackage()
	r.Status = "broken"
	f.records[r.ID] = r
	prefix := fixtureTLSConfig(t, f.serve)
	resource.Test(t, resource.TestCase{ProtoV6ProviderFactories: map[string]func() (tfprotov6.ProviderServer, error){"synology": providerserver.NewProtocol6WithError(New("test")())}, Steps: []resource.TestStep{
		{Config: packageAdoptConfig(prefix)},
		{Config: packageTestConfig(prefix, true, true), ExpectError: regexp.MustCompile("eligible stable package")},
		{Config: prefix + "removed {\nfrom=synology_package.test\nlifecycle{destroy=false}\n}\n", Check: func(_ *terraform.State) error {
			f.mu.Lock()
			defer f.mu.Unlock()
			if len(f.writes) != 0 {
				return fmt.Errorf("unknown status allowed package control")
			}
			return nil
		}},
	}})
}
