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

type interfaceFixture struct {
	mu                    sync.Mutex
	ipv4, ipv6            map[string]any
	mac                   string
	writes4, writes6      int
	lostAck, failReadOnce bool
}

func newInterfaceFixture() *interfaceFixture {
	return &interfaceFixture{mac: "02:00:00:00:00:01", ipv4: map[string]any{"ifname": "eth0", "use_dhcp": true, "ip": "192.0.2.10", "mask": "255.255.255.0", "gateway": "192.0.2.1", "dns": "192.0.2.53", "is_default_gateway": true, "mtu_config": 1500, "mtu": 1500, "enable_vlan": false, "vlan_id": 0, "ipv6": []string{"2001:db8::10/64"}, "status": "connected", "type": "lan", "nat": false, "enable_ha_ip": false, "is_main_ha_ip": false, "ha_local_ip": "0.0.0.0", "ha_local_mask": "0.0.0.0", "block": 0, "unmanaged_future_setting": "preserved"}, ipv6: map[string]any{"ifname": "eth0", "type": "auto", "ip": "2001:db8::10", "prefix_length": 64, "router": "2001:db8::1", "dns": "2001:db8::53", "is_default_gateway": true}}
}
func (f *interfaceFixture) serve(w http.ResponseWriter, r *http.Request) {
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
		data = map[string]any{"SYNO.API.Auth": map[string]any{"path": "entry.cgi", "minVersion": 1, "maxVersion": 7}, ethernetAPI: map[string]any{"path": "entry.cgi", "minVersion": 1, "maxVersion": 2}, "SYNO.Core.System": map[string]any{"path": "entry.cgi", "minVersion": 1, "maxVersion": 3}, "SYNO.Core.Network.IPv6": map[string]any{"path": "entry.cgi", "minVersion": 1, "maxVersion": 1}, "SYNO.Core.Network.IPv6.Router": map[string]any{"path": "entry.cgi", "minVersion": 1, "maxVersion": 1}}
	case "SYNO.API.Auth":
		data = map[string]any{"sid": "fixture-session"}
	case "SYNO.Core.System":
		if method != "info" || r.Form.Get("type") != "network" {
			w.WriteHeader(400)
			return
		}
		data = map[string]any{"nif": []map[string]any{{"id": "eth0", "mac": f.mac}}}
	case ethernetAPI:
		switch method {
		case "list":
			data = []map[string]any{f.ipv4}
		case "get":
			if r.Form.Get("ifname") != "eth0" {
				w.WriteHeader(400)
				return
			}
			if f.failReadOnce && f.writes4+f.writes6 > 0 {
				f.failReadOnce = false
				w.WriteHeader(503)
				return
			}
			data = f.ipv4
		case "set":
			var configs []map[string]any
			if json.Unmarshal([]byte(r.Form.Get("configs")), &configs) != nil || len(configs) != 1 {
				w.WriteHeader(400)
				return
			}
			config := configs[0]
			for _, key := range []string{"ifname", "use_dhcp", "enable_ha_ip", "is_default_gateway", "mtu", "enable_vlan"} {
				if _, ok := config[key]; !ok {
					w.WriteHeader(http.StatusBadRequest)
					return
				}
			}
			if config["ifname"] != "eth0" || config["enable_ha_ip"] != false {
				w.WriteHeader(400)
				return
			}
			if config["use_dhcp"] == true {
				for _, key := range []string{"ip", "mask", "gateway", "dns"} {
					if _, exists := config[key]; exists {
						w.WriteHeader(400)
						return
					}
				}
			} else if config["dns"] != f.ipv4["dns"] {
				w.WriteHeader(400)
				return
			}
			f.writes4++
			for key, value := range config {
				if key == "mtu" {
					f.ipv4["mtu_config"], f.ipv4["mtu"] = value, value
				} else {
					f.ipv4[key] = value
				}
			}
			if f.lostAck {
				w.WriteHeader(503)
				return
			}
		default:
			w.WriteHeader(400)
			return
		}
	case "SYNO.Core.Network.IPv6":
		if method == "get" {
			data = f.ipv6
		} else if method == "set" {
			if r.Form.Get("force") != "true" || r.Form.Get("ifname") != "eth0" {
				w.WriteHeader(400)
				return
			}
			if r.Form.Get("type") == "static" {
				if r.Form.Get("dns") != f.ipv6["dns"] {
					w.WriteHeader(400)
					return
				}
				f.ipv6["ip"], f.ipv6["router"] = r.Form.Get("ip"), r.Form.Get("router")
				var prefix int
				if _, err := fmt.Sscan(r.Form.Get("prefix_length"), &prefix); err != nil {
					w.WriteHeader(400)
					return
				}
				f.ipv6["prefix_length"] = prefix
			} else {
				for _, key := range []string{"ip", "router", "prefix_length", "dns"} {
					if r.Form.Has(key) {
						w.WriteHeader(400)
						return
					}
				}
			}
			f.writes6++
			f.ipv6["type"] = r.Form.Get("type")
			f.ipv6["is_default_gateway"] = r.Form.Get("is_default_gateway") == "true"
		} else {
			w.WriteHeader(400)
			return
		}
	case "SYNO.Core.Network.IPv6.Router":
		if method != "get" {
			w.WriteHeader(400)
			return
		}
		data = map[string]any{"type": "off", "config": map[string]any{}, "is_default_gateway": false}
	default:
		w.WriteHeader(400)
		return
	}
	json.NewEncoder(w).Encode(map[string]any{"success": true, "data": data})
}
func TestAccNetworkInterfaceLifecycle(t *testing.T) {
	f := newInterfaceFixture()
	prefix := fixtureTLSConfig(t, f.serve)
	adopt := prefix + "resource \"synology_network_interface\" \"test\" {name=\"eth0\"}\nimport {\nto=synology_network_interface.test\nid=\"eth0\"\n}\n"
	cfg := prefix + "resource \"synology_network_interface\" \"test\" {\nname=\"eth0\"\nipv4_dhcp=true\nmtu=1600\nipv6_type=\"auto\"\n}\n"
	static := prefix + "resource \"synology_network_interface\" \"test\" {\nname=\"eth0\"\nipv4_dhcp=false\nipv4_address=\"192.0.2.20\"\nipv4_netmask=\"255.255.255.0\"\nipv4_gateway=\"192.0.2.1\"\nipv6_type=\"static\"\nipv6_address=\"2001:db8::20\"\nipv6_prefix_length=64\nipv6_router=\"2001:db8::1\"\n}\n"
	check := func(v4, v6 int) resource.TestCheckFunc {
		return func(_ *terraform.State) error {
			f.mu.Lock()
			defer f.mu.Unlock()
			if f.writes4 != v4 || f.writes6 != v6 || f.ipv4["unmanaged_future_setting"] != "preserved" {
				return fmt.Errorf("unexpected interface writes %d/%d", f.writes4, f.writes6)
			}
			return nil
		}
	}
	resource.Test(t, resource.TestCase{ProtoV6ProviderFactories: map[string]func() (tfprotov6.ProviderServer, error){"synology": providerserver.NewProtocol6WithError(New("test")())}, CheckDestroy: check(3, 2), Steps: []resource.TestStep{
		{Config: adopt, Check: check(0, 0)},
		{ResourceName: "synology_network_interface.test", ImportState: true, ImportStateId: "eth0", ImportStateVerify: true},
		{Config: cfg, Check: check(1, 0)},
		{Config: cfg, PreConfig: func() { f.mu.Lock(); f.ipv4["ip"] = "192.0.2.50"; f.mu.Unlock() }, Check: resource.ComposeAggregateTestCheckFunc(check(1, 0), resource.TestCheckResourceAttr("synology_network_interface.test", "ipv4_address", "192.0.2.50"))},
		{Config: static, Check: check(2, 1)},
		{Config: static, PlanOnly: true, ExpectNonEmptyPlan: true, PreConfig: func() { f.mu.Lock(); f.ipv4["ip"] = "192.0.2.30"; f.ipv6["ip"] = "2001:db8::30"; f.mu.Unlock() }},
		{Config: static, Check: check(3, 2)},
	}})
}
func TestAccNetworkInterfaceInterruptedUpdate(t *testing.T) {
	for _, lost := range []bool{false, true} {
		t.Run(fmt.Sprint(lost), func(t *testing.T) {
			f := newInterfaceFixture()
			prefix := fixtureTLSConfig(t, f.serve)
			adopt := prefix + "resource \"synology_network_interface\" \"test\" {name=\"eth0\"}\nimport {\nto=synology_network_interface.test\nid=\"eth0\"\n}\n"
			cfg := prefix + "resource \"synology_network_interface\" \"test\" {\nname=\"eth0\"\nmtu=1600\n}\n"
			steps := []resource.TestStep{{Config: adopt}, {Config: cfg, ExpectError: regexp.MustCompile("Cannot update network interface"), PreConfig: func() { f.mu.Lock(); f.lostAck = lost; f.failReadOnce = !lost; f.mu.Unlock() }}}
			if lost {
				steps = append(steps, resource.TestStep{Config: cfg, ExpectError: regexp.MustCompile("uncertain|Pending interface")})
			} else {
				steps = append(steps, resource.TestStep{Config: cfg, Check: resource.TestCheckResourceAttr("synology_network_interface.test", "reconciliation_required", "false")})
			}
			steps = append(steps, resource.TestStep{Config: prefix + "data \"synology_network_interfaces\" \"test\" {}\nremoved {\nfrom=synology_network_interface.test\nlifecycle{destroy=false}\n}\n", Check: func(s *terraform.State) error {
				f.mu.Lock()
				defer f.mu.Unlock()
				if f.writes4 != 1 || f.writes6 != 0 {
					return fmt.Errorf("interrupted interface write was replayed")
				}
				return resource.TestCheckResourceAttr("data.synology_network_interfaces.test", "interfaces.eth0.mtu", "1600")(s)
			}})
			resource.Test(t, resource.TestCase{ProtoV6ProviderFactories: map[string]func() (tfprotov6.ProviderServer, error){"synology": providerserver.NewProtocol6WithError(New("test")())}, Steps: steps})
		})
	}
}
func TestAccNetworkInterfaceIdentityAndLeaseValidation(t *testing.T) {
	f := newInterfaceFixture()
	prefix := fixtureTLSConfig(t, f.serve)
	adopt := prefix + "resource \"synology_network_interface\" \"test\" {name=\"eth0\"}\nimport {\nto=synology_network_interface.test\nid=\"eth0\"\n}\n"
	resource.Test(t, resource.TestCase{ProtoV6ProviderFactories: map[string]func() (tfprotov6.ProviderServer, error){"synology": providerserver.NewProtocol6WithError(New("test")())}, Steps: []resource.TestStep{
		{Config: adopt},
		{Config: prefix + "resource \"synology_network_interface\" \"test\" {\nname=\"eth0\"\nipv4_dhcp=true\nipv4_address=\"192.0.2.10\"\n}\n", ExpectError: regexp.MustCompile("DHCP observations cannot be configured")},
		{Config: adopt, ExpectError: regexp.MustCompile("Physical interface identity changed"), PreConfig: func() { f.mu.Lock(); f.mac = "02:00:00:00:00:02"; f.mu.Unlock() }},
		{Config: prefix + "removed {\nfrom=synology_network_interface.test\nlifecycle{destroy=false}\n}\n", Check: func(_ *terraform.State) error {
			f.mu.Lock()
			defer f.mu.Unlock()
			if f.writes4+f.writes6 != 0 {
				return fmt.Errorf("identity or validation failure changed the interface")
			}
			return nil
		}},
	}})
}
