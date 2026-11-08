package provider

import (
	"encoding/json"
	"encoding/pem"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
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

func TestAccAdditionalServiceLifecycles(t *testing.T) {
	selected := map[string]string{"terminal": "ssh_enabled", "rsync": "enabled", "windows_discovery": "enabled", "file_transfer_logging": "smb_enabled", "network": "manual_dns", "time_machine": "smb_enabled", "quickconnect_upnp": "enabled"}
	for _, constructor := range serviceResources() {
		r, ok := constructor().(*serviceResource)
		if !ok {
			continue
		}
		definition := r.definition
		changed, ok := selected[definition.name]
		if !ok {
			continue
		}
		t.Run(definition.name, func(t *testing.T) {
			var mu sync.Mutex
			settings := map[string]any{"unmanaged_future_setting": "preserved"}
			fields := make([]string, 0, len(definition.fields))
			for name, field := range definition.fields {
				switch field.kind {
				case types.BoolType:
					settings[field.wire] = false
				case types.StringType:
					settings[field.wire] = "fixture"
				case types.Int64Type:
					settings[field.wire] = int64(2022)
				default:
					settings[field.wire] = []string{}
				}
				if name == "primary_dns" {
					settings[field.wire] = "192.0.2.53"
				}
				if name == "secondary_dns" {
					settings[field.wire] = ""
				}
				if definition.name == "terminal" && field.kind.Equal(types.SetType{ElemType: types.StringType}) {
					settings[field.wire] = []map[string]any{{"name": "fixture-old", "in_use": true}, {"name": "fixture-new", "in_use": false}}
				}
				fields = append(fields, name)
			}
			original := map[string]any{}
			for key, value := range settings {
				original[key] = value
			}
			writes, beforeImport := 0, 0
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				mu.Lock()
				defer mu.Unlock()
				if err := r.ParseForm(); err != nil {
					w.WriteHeader(400)
					return
				}
				var data any = map[string]any{}
				switch r.Form.Get("api") {
				case "SYNO.API.Info":
					data = map[string]any{"SYNO.API.Auth": map[string]any{"path": "entry.cgi", "minVersion": 1, "maxVersion": 7}, definition.api: map[string]any{"path": "entry.cgi", "minVersion": 1, "maxVersion": definition.version}}
				case "SYNO.API.Auth":
					data = map[string]any{"sid": "fixture-session"}
				case definition.api:
					switch r.Form.Get("method") {
					case "get":
						data = settings
					case "set":
						coupled := []string{}
						if definition.name == "terminal" {
							coupled = []string{"ssh_port", "forbid_console"}
						}
						if definition.name == "rsync" {
							coupled = []string{"rsync_sshd_port", "enable_custom_config"}
						}
						for _, wire := range coupled {
							if !r.Form.Has(wire) || r.Form.Get(wire) != fmt.Sprint(settings[wire]) {
								t.Error("coupled nondefault setting was not retained")
								w.WriteHeader(400)
								return
							}
						}
						writes++
						for _, field := range definition.fields {
							if !r.Form.Has(field.wire) {
								continue
							}
							text := r.Form.Get(field.wire)
							switch field.kind {
							case types.BoolType:
								v, err := strconv.ParseBool(text)
								if err != nil {
									w.WriteHeader(400)
									return
								}
								settings[field.wire] = v
							case types.Int64Type:
								v, err := strconv.ParseInt(text, 10, 64)
								if err != nil {
									w.WriteHeader(400)
									return
								}
								settings[field.wire] = v
							case types.StringType:
								settings[field.wire] = text
							default:
								var names []string
								if json.Unmarshal([]byte(text), &names) != nil {
									w.WriteHeader(400)
									return
								}
								if definition.name == "terminal" {
									entries := []map[string]any{}
									for _, name := range names {
										entries = append(entries, map[string]any{"name": name, "in_use": true})
									}
									settings[field.wire] = entries
								} else {
									settings[field.wire] = names
								}
							}
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
			}))
			defer server.Close()
			ca := filepath.Join(t.TempDir(), "ca.pem")
			if err := os.WriteFile(ca, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw}), 0o600); err != nil {
				t.Fatal(err)
			}
			config := func(enabled bool) string {
				return fmt.Sprintf(`
provider "synology" {
 endpoint = %q
 ca_file = %q
 username = "fixture-user"
 password = "fixture-password"
}
resource "synology_%s" "test" {
 %s = %t
}
`, server.URL, ca, definition.name, changed, enabled)
			}
			address := "synology_" + definition.name + ".test"
			resource.Test(t, resource.TestCase{ProtoV6ProviderFactories: map[string]func() (tfprotov6.ProviderServer, error){"synology": providerserver.NewProtocol6WithError(New("test")())}, CheckDestroy: func(_ *terraform.State) error {
				mu.Lock()
				defer mu.Unlock()
				if settings[definition.fields[changed].wire] != false || settings["unmanaged_future_setting"] != "preserved" {
					return fmt.Errorf("destroy changed DSM settings")
				}
				return nil
			}, Steps: []resource.TestStep{
				{Config: config(true), Check: resource.ComposeAggregateTestCheckFunc(resource.TestCheckResourceAttr(address, changed, "true"), func(_ *terraform.State) error {
					mu.Lock()
					defer mu.Unlock()
					if writes != 1 {
						return fmt.Errorf("Create did not apply exactly one change")
					}
					return nil
				})},
				{ResourceName: address, ImportState: true, ImportStateId: definition.name, ImportStateVerify: true, PreConfig: func() { mu.Lock(); beforeImport = writes; mu.Unlock() }, ImportStateCheck: func(_ []*terraform.InstanceState) error {
					mu.Lock()
					defer mu.Unlock()
					if writes != beforeImport {
						return fmt.Errorf("import changed a service")
					}
					return nil
				}},
				{Config: config(false), Check: resource.TestCheckResourceAttr(address, changed, "false")},
				{Config: config(false), PlanOnly: true, ExpectNonEmptyPlan: true, PreConfig: func() { mu.Lock(); settings[definition.fields[changed].wire] = true; mu.Unlock() }},
				{Config: config(false), Check: func(s *terraform.State) error {
					mu.Lock()
					defer mu.Unlock()
					for _, name := range fields {
						if name != changed && !reflect.DeepEqual(settings[definition.fields[name].wire], original[definition.fields[name].wire]) {
							return fmt.Errorf("unconfigured setting changed")
						}
					}
					for _, r := range s.RootModule().Resources {
						for _, value := range r.Primary.Attributes {
							if strings.Contains(value, "fixture-password") {
								return fmt.Errorf("credential in state")
							}
						}
					}
					return nil
				}},
			}})
		})
	}
}
