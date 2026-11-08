package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/elephant-evergreen/terraform-provider-synology/internal/dsm"
	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/providerserver"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-go/tfprotov6"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
)

func TestNetworkCoupledDNS(t *testing.T) {
	actual := types.ObjectValueMust(map[string]attr.Type{"hostname": types.StringType, "manual_dns": types.BoolType, "primary_dns": types.StringType, "secondary_dns": types.StringType}, map[string]attr.Value{"hostname": types.StringValue("fixture"), "manual_dns": types.BoolValue(false), "primary_dns": types.StringValue("192.0.2.53"), "secondary_dns": types.StringValue("192.0.2.54")})
	for _, tc := range []struct {
		name string
		args url.Values
		err  bool
		dns  bool
	}{
		{"hostname_only", url.Values{"server_name": {"fixture-new"}}, false, false},
		{"enable_manual", url.Values{"dns_manual": {"true"}}, false, true},
		{"invalid_dns", url.Values{"dns_manual": {"true"}, "dns_primary": {"bad"}}, true, false},
		{"invalid_hostname", url.Values{"server_name": {"bad/name"}}, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := prepareNetworkWrite(actual, tc.args)
			if (err != nil) != tc.err {
				t.Fatalf("validation: %v", err)
			}
			if tc.dns && (tc.args.Get("dns_primary") != "192.0.2.53" || tc.args.Get("dns_secondary") != "192.0.2.54") {
				t.Fatal("omitted DNS settings were not retained")
			}
			if !tc.dns && !tc.err && tc.args.Has("dns_manual") {
				t.Fatal("hostname change acquired DNS ownership")
			}
		})
	}
}
func TestTerminalCapabilityNormalization(t *testing.T) {
	for _, value := range []string{`null`, `[{"name":"a"}]`, `[{"name":"a","in_use":true},{"name":"a","in_use":false}]`} {
		raw := map[string]json.RawMessage{"ssh_cipher": json.RawMessage(value)}
		if normalizeTerminal(raw) == nil {
			t.Fatal("malformed capabilities accepted")
		}
	}
	raw := map[string]json.RawMessage{}
	for _, wire := range []string{"ssh_cipher", "ssh_kex", "ssh_mac"} {
		raw[wire] = json.RawMessage(`[{"name":"a","in_use":false},{"name":"b","in_use":true}]`)
	}
	if err := normalizeTerminal(raw); err != nil {
		t.Fatal(err)
	}
	for _, value := range raw {
		if string(value) != `["b"]` {
			t.Fatal("selection was not read from DSM")
		}
	}
}
func TestAccConfigurationBackupDiscovery(t *testing.T) {
	enabled := false
	writes := 0
	var mu sync.Mutex
	prefix := fixtureTLSConfig(t, func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		if r.ParseForm() != nil {
			w.WriteHeader(400)
			return
		}
		var data any = map[string]any{}
		switch r.Form.Get("api") {
		case "SYNO.API.Info":
			data = map[string]any{"SYNO.API.Auth": map[string]any{"path": "entry.cgi", "minVersion": 1, "maxVersion": 7}, "SYNO.Backup.Config.AutoBackup": map[string]any{"path": "entry.cgi", "minVersion": 1, "maxVersion": 1}}
		case "SYNO.API.Auth":
			data = map[string]any{"sid": "fixture-session"}
		case "SYNO.Backup.Config.AutoBackup":
			switch r.Form.Get("method") {
			case "get":
				data = map[string]any{"enable": enabled, "enc_method": "manual", "pwd": "fixture-backup-secret", "last_status": "failed", "last_check_time": "fixture-check", "last_success_time": "", "fail_reason_code": int64(1)}
			case "set":
				if r.Form.Get("enc_method") != "manual" || r.Form.Get("overwrite") != "true" || r.Form.Has("pwd") || r.Form.Has("last_status") {
					w.WriteHeader(400)
					return
				}
				writes++
				enabled = r.Form.Get("enable") == "true"
			default:
				w.WriteHeader(400)
				return
			}
		default:
			w.WriteHeader(400)
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"success": true, "data": data})
	})
	config := prefix + "data \"synology_configuration_backup\" \"test\" {}\n"
	resource.Test(t, resource.TestCase{ProtoV6ProviderFactories: map[string]func() (tfprotov6.ProviderServer, error){"synology": providerserver.NewProtocol6WithError(New("test")())}, Steps: []resource.TestStep{
		{Config: config, Check: resource.ComposeAggregateTestCheckFunc(resource.TestCheckResourceAttr("data.synology_configuration_backup.test", "enabled", "false"), resource.TestCheckResourceAttr("data.synology_configuration_backup.test", "last_status", "failed"))},
		{Config: config, PreConfig: func() { mu.Lock(); enabled = true; mu.Unlock() }, Check: func(s *terraform.State) error {
			mu.Lock()
			defer mu.Unlock()
			if writes != 0 {
				return fmt.Errorf("discovery changed backup policy")
			}
			for _, r := range s.RootModule().Resources {
				for _, v := range r.Primary.Attributes {
					if strings.Contains(v, "fixture-backup-secret") {
						return fmt.Errorf("backup password entered state")
					}
				}
			}
			return nil
		}},
	}})
}

func TestLiveExtendedSettingsReadOnly(t *testing.T) {
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
	selected := map[string]bool{"terminal": true, "rsync": true, "network": true, "time_machine": true, "quickconnect_upnp": true}
	for _, constructor := range serviceResources() {
		r, ok := constructor().(*serviceResource)
		if !ok || !selected[r.definition.name] {
			continue
		}
		r.client = c
		t.Run(r.definition.name, func(t *testing.T) {
			if _, err := r.read(ctx); err != nil {
				t.Fatal(err)
			}
		})
	}
	proxy := NewNetworkProxyResource().(*networkProxyResource)
	proxy.client = c
	if _, err := proxy.read(ctx); err != nil {
		t.Fatal(err)
	}
	backup := NewConfigurationBackupDataSource().(*configurationBackupDataSource)
	backup.client = c
	if _, err := backup.read(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestLiveRemotePolicyReadOnly(t *testing.T) {
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
	quickconnect := NewQuickConnectResource().(*quickConnectResource)
	quickconnect.client = c
	if _, err := quickconnect.readQuickConnect(ctx); err != nil {
		t.Fatal(err)
	}
	firewall := NewFirewallResource().(*firewallResource)
	firewall.client = c
	if _, err := firewall.readFirewall(ctx); err != nil {
		t.Fatal(err)
	}
}
