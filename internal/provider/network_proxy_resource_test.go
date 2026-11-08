package provider

import (
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

	"github.com/hashicorp/terraform-plugin-framework/providerserver"
	"github.com/hashicorp/terraform-plugin-go/tfprotov6"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
)

type proxyFixture struct {
	internalReply          map[string]any
	mu                     sync.Mutex
	settings               map[string]any
	writes, passwordWrites int
	lostAck, failRead      bool
}

func newProxyFixture() *proxyFixture {
	return &proxyFixture{settings: map[string]any{"enable": false, "http_host": "proxy.example.invalid", "http_port": int64(80), "enable_different_host": false, "https_host": "proxy.example.invalid", "https_port": int64(80), "enable_auth": false, "username": "", "enable_bypass": true, "password": "fixture-stored-secret", "unmanaged_setting": "preserved"}}
}
func (f *proxyFixture) serve(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if r.ParseForm() != nil {
		w.WriteHeader(400)
		return
	}
	var data any = map[string]any{}
	switch r.Form.Get("api") {
	case "SYNO.API.Info":
		data = map[string]any{"SYNO.API.Auth": map[string]any{"path": "entry.cgi", "minVersion": 1, "maxVersion": 7}, "SYNO.Core.Network.Proxy": map[string]any{"path": "entry.cgi", "minVersion": 1, "maxVersion": 1}}
	case "SYNO.API.Auth":
		data = map[string]any{"sid": "fixture-session"}
	case "SYNO.Core.Network.Proxy":
		switch r.Form.Get("method") {
		case "get":
			if f.failRead && f.writes > 0 {
				w.WriteHeader(503)
				return
			}
			raw := map[string]any{}
			for k, v := range f.settings {
				raw[k] = v
			}
			for _, k := range []string{"http_port", "https_port"} {
				raw[k] = fmt.Sprint(raw[k])
			}
			data = raw
		case "set":
			f.writes++
			for key, old := range f.settings {
				if !r.Form.Has(key) {
					continue
				}
				text := r.Form.Get(key)
				switch old.(type) {
				case bool:
					v, err := strconv.ParseBool(text)
					if err != nil {
						w.WriteHeader(400)
						return
					}
					f.settings[key] = v
				case int64:
					v, err := strconv.ParseInt(text, 10, 64)
					if err != nil {
						w.WriteHeader(400)
						return
					}
					f.settings[key] = v
				default:
					f.settings[key] = text
				}
			}
			if r.Form.Has("password") {
				f.passwordWrites++
			}
			if f.internalReply != nil {
				data = f.internalReply
			}
			if f.lostAck {
				w.WriteHeader(503)
				fmt.Fprint(w, "fixture-stored-secret fixture-session")
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
func fixtureTLSConfig(t *testing.T, handler http.HandlerFunc) string {
	t.Helper()
	s := httptest.NewTLSServer(handler)
	t.Cleanup(s.Close)
	ca := filepath.Join(t.TempDir(), "ca.pem")
	if err := os.WriteFile(ca, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: s.Certificate().Raw}), 0o600); err != nil {
		t.Fatal(err)
	}
	return fmt.Sprintf("provider \"synology\" {\nendpoint=%q\nca_file=%q\nusername=\"fixture-user\"\npassword=\"fixture-login-secret\"\n}\n", s.URL, ca)
}
func TestAccNetworkProxyLifecycle(t *testing.T) {
	f := newProxyFixture()
	prefix := fixtureTLSConfig(t, f.serve)
	adopted := prefix + "resource \"synology_network_proxy\" \"test\" {}\nimport {\nto=synology_network_proxy.test\nid=\"network_proxy\"\n}\n"
	config := func(version int) string {
		return prefix + fmt.Sprintf("resource \"synology_network_proxy\" \"test\" {\nenabled=true\nauthentication_enabled=true\nusername=\"fixture-proxy-user\"\nhttp_port=8080\npassword_wo=\"fixture-rotation-secret\"\npassword_version=%d\n}\n", version)
	}
	check := func(writes, passwords int) resource.TestCheckFunc {
		return func(s *terraform.State) error {
			f.mu.Lock()
			defer f.mu.Unlock()
			if f.writes != writes || f.passwordWrites != passwords || f.settings["unmanaged_setting"] != "preserved" {
				return fmt.Errorf("unexpected proxy writes %d/%d", f.writes, f.passwordWrites)
			}
			for _, r := range s.RootModule().Resources {
				for _, value := range r.Primary.Attributes {
					if strings.Contains(value, "secret") {
						return fmt.Errorf("proxy secret entered state")
					}
				}
			}
			return nil
		}
	}
	resource.Test(t, resource.TestCase{ProtoV6ProviderFactories: map[string]func() (tfprotov6.ProviderServer, error){"synology": providerserver.NewProtocol6WithError(New("test")())}, Steps: []resource.TestStep{
		{Config: adopted, Check: check(0, 0)},
		{ResourceName: "synology_network_proxy.test", ImportState: true, ImportStateId: "network_proxy", ImportStateVerify: true},
		{Config: config(1), Check: check(1, 1)},
		{Config: config(1), PlanOnly: true, ExpectNonEmptyPlan: true, PreConfig: func() { f.mu.Lock(); f.settings["http_port"] = int64(8000); f.mu.Unlock() }},
		{Config: config(1), Check: check(2, 1)},
		{Config: config(2), Check: check(3, 2)},
		{Config: prefix + "removed {\nfrom=synology_network_proxy.test\nlifecycle {destroy=false}\n}\n", Check: check(3, 2)},
	}})
}
func TestAccNetworkProxySecretFailure(t *testing.T) {
	for _, mode := range []string{"ack_read_failure", "lost_ack", "internal_failure", "untracked_task"} {
		t.Run(mode, func(t *testing.T) {
			f := newProxyFixture()
			f.settings["enable_auth"] = true
			f.settings["username"] = "fixture-user"
			prefix := fixtureTLSConfig(t, f.serve)
			adopt := prefix + "resource \"synology_network_proxy\" \"test\" {}\nimport {\nto=synology_network_proxy.test\nid=\"network_proxy\"\n}\n"
			cfg := prefix + "resource \"synology_network_proxy\" \"test\" {\nauthentication_enabled=true\nusername=\"fixture-user\"\npassword_wo=\"fixture-rotation-secret\"\npassword_version=1\n}\n"
			steps := []resource.TestStep{{Config: adopt}, {Config: cfg, ExpectError: regexp.MustCompile("Cannot update network proxy"), PreConfig: func() {
				f.mu.Lock()
				f.lostAck = mode == "lost_ack"
				f.failRead = mode == "ack_read_failure"
				if mode == "internal_failure" {
					f.internalReply = map[string]any{"success": false}
				}
				if mode == "untracked_task" {
					f.internalReply = map[string]any{"task_id": "proxy-pending"}
				}
				f.mu.Unlock()
			}}}
			if mode != "ack_read_failure" {
				steps = append(steps, resource.TestStep{Config: cfg, ExpectError: regexp.MustCompile("requires reconciliation")})
			} else {
				steps = append(steps, resource.TestStep{Config: cfg, PreConfig: func() { f.mu.Lock(); f.failRead = false; f.mu.Unlock() }})
			}
			steps = append(steps, resource.TestStep{Config: prefix + "removed {\nfrom=synology_network_proxy.test\nlifecycle {destroy=false}\n}\n", Check: func(_ *terraform.State) error {
				f.mu.Lock()
				defer f.mu.Unlock()
				if f.writes != 1 || f.passwordWrites != 1 {
					return fmt.Errorf("secret failure was replayed")
				}
				return nil
			}})
			resource.Test(t, resource.TestCase{ProtoV6ProviderFactories: map[string]func() (tfprotov6.ProviderServer, error){"synology": providerserver.NewProtocol6WithError(New("test")())}, Steps: steps})
		})
	}
}
