package provider

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/netip"
	"net/url"
	"regexp"
	"strconv"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

const ethernetAPI = "SYNO.Core.Network.Ethernet"

var physicalInterfacePattern = regexp.MustCompile(`^eth[0-9]+$`)

func interfaceDefinition() serviceDefinition {
	return serviceDefinition{name: "network_interface", api: ethernetAPI, version: 1, description: "Configuration of an existing physical DSM Ethernet interface. Import is mandatory; hardware, bonds, virtual switches, HA, NAT and IPv6 routing/tunnels are not created or reconfigured. DHCP/automatic addresses are observed unless a static policy is explicitly declared. MAC identity is bound at import. Read and destroy never change or disable an interface. Changes can interrupt the provider endpoint; keep an independent management path and apply separately from router/DNS changes.", fields: map[string]serviceField{
		"name":                 {"ifname", types.StringType, "Existing physical Ethernet name, such as eth0."},
		"mac_address":          {"mac", types.StringType, "Observed physical MAC identity. A changed binding requires explicit reconciliation instead of adoption."},
		"ipv4_dhcp":            {"use_dhcp", types.BoolType, "Use IPv4 DHCP. Static-address inputs must be omitted while enabled."},
		"ipv4_address":         {"ip", types.StringType, "Static IPv4 address when explicitly configured; otherwise the current observation, including a DHCP lease."},
		"ipv4_netmask":         {"mask", types.StringType, "Static IPv4 netmask. Omission observes and preserves the current policy."},
		"ipv4_gateway":         {"gateway", types.StringType, "Static IPv4 gateway, or an empty string. Omit while using DHCP."},
		"ipv4_default_gateway": {"is_default_gateway", types.BoolType, "Use this interface as an IPv4 default route."},
		"ipv4_dns":             {"dns", types.StringType, "Observed coupled DNS setting. General DNS policy has its own network resource; setters retain this freshly read value."},
		"mtu":                  {"mtu_config", types.Int64Type, "Configured MTU. DSM applies model-specific limits; values must be from 1100 to 9000."},
		"effective_mtu":        {"mtu", types.Int64Type, "Observed effective MTU."},
		"vlan_enabled":         {"enable_vlan", types.BoolType, "Enable 802.1Q VLAN tagging on this physical interface. Coordinate router policy in a separate apply."},
		"vlan_id":              {"vlan_id", types.Int64Type, "VLAN ID from 1 to 4094 when enabled. Omit when disabled."},
		"ipv6_type":            {"ipv6_type", types.StringType, "Native IPv6 policy: auto, static or off. Router and tunnel modes require a separate workflow."},
		"ipv6_address":         {"ipv6_ip", types.StringType, "Static IPv6 address when explicitly configured; otherwise an observation."},
		"ipv6_prefix_length":   {"ipv6_prefix_length", types.Int64Type, "Static IPv6 prefix length from 0 to 128; omit in automatic/off mode."},
		"ipv6_router":          {"ipv6_router", types.StringType, "Static IPv6 gateway, or an empty string; omit in automatic/off mode."},
		"ipv6_default_gateway": {"ipv6_is_default_gateway", types.BoolType, "Use this interface as an IPv6 default route."},
		"ipv6_dns":             {"ipv6_dns", types.StringType, "Observed coupled IPv6 DNS setting, retained during static updates."},
		"ipv6_addresses":       {"ipv6", types.SetType{ElemType: types.StringType}, "Observed IPv6 addresses reported for the interface, including automatic addresses."},
		"status":               {"status", types.StringType, "Observed link status; configuration readback does not guarantee future connectivity."},
		"interface_type":       {"interface_type", types.StringType, "Observed DSM interface type."},
		"nat_enabled":          {"nat", types.BoolType, "Observed NAT role. Mutations are refused when enabled."},
		"ha_enabled":           {"enable_ha_ip", types.BoolType, "Observed high-availability address role."},
		"ha_main":              {"is_main_ha_ip", types.BoolType, "Observed main high-availability address role."},
		"ha_local_address":     {"ha_local_ip", types.StringType, "Observed HA local address."},
		"ha_local_netmask":     {"ha_local_mask", types.StringType, "Observed HA local netmask."},
		"block":                {"block", types.Int64Type, "Observed DSM interface edit restriction. Nonzero blocks mutations."},
		"ipv6_router_type":     {"ipv6_router_type", types.StringType, "Observed IPv6 router/tunnel type. Mutations require off."},
	}}
}

var interfaceObserved = map[string]bool{"mac_address": true, "ipv4_dns": true, "effective_mtu": true, "ipv6_dns": true, "ipv6_addresses": true, "status": true, "interface_type": true, "nat_enabled": true, "ha_enabled": true, "ha_main": true, "ha_local_address": true, "ha_local_netmask": true, "block": true, "ipv6_router_type": true}

func (r *networkInterfaceResource) readInterface(ctx context.Context, name string) (types.Object, error) {
	if !physicalInterfacePattern.MatchString(name) {
		return types.Object{}, errors.New("only existing physical ethN interfaces are supported")
	}
	var raw map[string]json.RawMessage
	if err := r.client.Call(ctx, ethernetAPI, 1, "get", url.Values{"ifname": {name}}, &raw); err != nil {
		return types.Object{}, err
	}
	var returned string
	if json.Unmarshal(raw["ifname"], &returned) != nil || returned != name {
		return types.Object{}, errors.New("DSM returned a different interface identity")
	}
	var system struct {
		Interfaces *[]struct {
			ID  *string `json:"id"`
			MAC *string `json:"mac"`
		} `json:"nif"`
	}
	if err := r.client.Call(ctx, "SYNO.Core.System", 1, "info", url.Values{"type": {"network"}}, &system); err != nil {
		return types.Object{}, err
	}
	if system.Interfaces == nil {
		return types.Object{}, errors.New("DSM omitted physical interface identity metadata")
	}
	mac := ""
	for _, device := range *system.Interfaces {
		if device.ID == nil {
			return types.Object{}, errors.New("DSM returned an incomplete interface identity")
		}
		if *device.ID != name {
			continue
		}
		if mac != "" || device.MAC == nil {
			return types.Object{}, errors.New("DSM returned missing or duplicate physical interface bindings")
		}
		parsed, err := net.ParseMAC(*device.MAC)
		if err != nil || len(parsed) != 6 {
			return types.Object{}, errors.New("DSM returned malformed Ethernet MAC identity")
		}
		mac = parsed.String()
	}
	if mac == "" {
		return types.Object{}, errors.New("DSM did not confirm the physical interface MAC binding")
	}
	raw["mac"] = json.RawMessage(strconv.Quote(mac))
	raw["interface_type"] = raw["type"]
	var ipv6 map[string]json.RawMessage
	if err := r.client.Call(ctx, "SYNO.Core.Network.IPv6", 1, "get", url.Values{"ifname": {name}}, &ipv6); err != nil {
		return types.Object{}, err
	}
	if json.Unmarshal(ipv6["ifname"], &returned) != nil || returned != name {
		return types.Object{}, errors.New("DSM returned a different IPv6 interface identity")
	}
	for key, value := range ipv6 {
		if key != "ifname" {
			raw["ipv6_"+key] = value
		}
	}
	var router struct {
		Type *string `json:"type"`
	}
	if err := r.client.Call(ctx, "SYNO.Core.Network.IPv6.Router", 1, "get", url.Values{"wan": {name}}, &router); err != nil {
		return types.Object{}, err
	}
	if router.Type == nil {
		return types.Object{}, errors.New("DSM omitted IPv6 router policy")
	}
	raw["ipv6_router_type"] = json.RawMessage(strconv.Quote(*router.Type))
	actual, err := r.decodeSettings(ctx, raw)
	if err != nil {
		return types.Object{}, err
	}
	values := actual.Attributes()
	values["id"] = types.StringValue(name)
	return types.ObjectValueMust(actual.AttributeTypes(ctx), values), nil
}
func interfaceTarget(actual types.Object, desired map[string]attr.Value) (types.Object, error) {
	values := actual.Attributes()
	for name, value := range desired {
		values[name] = value
	}
	target := types.ObjectValueMust(actual.AttributeTypes(context.Background()), values)
	if !target.Attributes()["name"].Equal(actual.Attributes()["name"]) {
		return types.Object{}, errors.New("a physical interface cannot be renamed or acquired through Update; import the requested interface separately")
	}
	flag := func(name string) bool { return values[name].(types.Bool).ValueBool() }
	text := func(name string) string { return values[name].(types.String).ValueString() }
	integer := func(name string) int64 { return values[name].(types.Int64).ValueInt64() }
	if serviceMatches(actual, desired) {
		return target, nil
	}
	if flag("nat_enabled") || flag("ha_enabled") || flag("ha_main") || (text("ha_local_address") != "" && text("ha_local_address") != "0.0.0.0") || integer("block") != 0 || (text("interface_type") != "lan" && text("interface_type") != "wan") || text("ipv6_router_type") != "off" {
		return types.Object{}, errors.New("interface role requires a separate HA, NAT, virtual-network or IPv6-router workflow; no coupled role was reset")
	}
	if integer("mtu") < 1100 || integer("mtu") > 9000 {
		return types.Object{}, errors.New("MTU must be from 1100 to 9000 and within DSM model-specific support")
	}
	if flag("vlan_enabled") {
		if integer("vlan_id") < 1 || integer("vlan_id") > 4094 {
			return types.Object{}, errors.New("enabled VLAN requires vlan_id from 1 to 4094")
		}
	} else if value, configured := desired["vlan_id"]; configured && value.(types.Int64).ValueInt64() != 0 {
		return types.Object{}, errors.New("omit vlan_id while VLAN tagging is disabled")
	}
	static4 := false
	for _, name := range []string{"ipv4_address", "ipv4_netmask", "ipv4_gateway"} {
		_, exists := desired[name]
		static4 = static4 || exists
	}
	if flag("ipv4_dhcp") && static4 {
		return types.Object{}, errors.New("static IPv4 fields cannot own DHCP lease observations; omit them while ipv4_dhcp is true")
	}
	if !flag("ipv4_dhcp") {
		ip, err := netip.ParseAddr(text("ipv4_address"))
		if err != nil || !ip.Is4() || ip.IsUnspecified() || ip.IsMulticast() {
			return types.Object{}, errors.New("a static interface requires a valid unicast IPv4 address")
		}
		mask := net.ParseIP(text("ipv4_netmask")).To4()
		if mask == nil {
			return types.Object{}, errors.New("invalid static IPv4 netmask")
		}
		ones, bits := net.IPMask(mask).Size()
		if bits != 32 {
			return types.Object{}, errors.New("IPv4 netmask must be contiguous")
		}
		network := netip.PrefixFrom(ip, ones).Masked()
		if gateway := text("ipv4_gateway"); gateway != "" {
			address, err := netip.ParseAddr(gateway)
			if err != nil || !address.Is4() || !network.Contains(address) || address.IsUnspecified() || address.IsMulticast() {
				return types.Object{}, errors.New("static IPv4 gateway must be an address in the interface subnet")
			}
		} else if flag("ipv4_default_gateway") {
			return types.Object{}, errors.New("a static IPv4 default route requires a gateway")
		}
		if actual.Attributes()["ipv4_dhcp"].(types.Bool).ValueBool() {
			for _, name := range []string{"ipv4_address", "ipv4_netmask"} {
				if _, exists := desired[name]; !exists {
					return types.Object{}, errors.New("disabling DHCP requires explicitly declared static address and netmask; observed leases are not silently frozen")
				}
			}
			if flag("ipv4_default_gateway") {
				if _, exists := desired["ipv4_gateway"]; !exists {
					return types.Object{}, errors.New("disabling DHCP with a default route requires an explicitly declared gateway")
				}
			}
		}
	}
	mode := text("ipv6_type")
	if mode != "auto" && mode != "static" && mode != "off" {
		return types.Object{}, errors.New("unsupported native IPv6 type; router and tunnel workflows remain separate")
	}
	static6 := false
	for _, name := range []string{"ipv6_address", "ipv6_prefix_length", "ipv6_router"} {
		_, exists := desired[name]
		static6 = static6 || exists
	}
	if mode != "static" && static6 {
		return types.Object{}, errors.New("static IPv6 fields must be omitted for automatic or disabled IPv6")
	}
	if mode == "static" {
		address, err := netip.ParseAddr(text("ipv6_address"))
		if err != nil || !address.Is6() || address.Is4In6() || address.Zone() != "" || address.IsUnspecified() || address.IsMulticast() {
			return types.Object{}, errors.New("static IPv6 requires a valid unicast IPv6 address without a zone")
		}
		if integer("ipv6_prefix_length") < 0 || integer("ipv6_prefix_length") > 128 {
			return types.Object{}, errors.New("IPv6 prefix length must be 0 to 128")
		}
		if gateway := text("ipv6_router"); gateway != "" {
			address, err := netip.ParseAddr(gateway)
			if err != nil || !address.Is6() || address.Is4In6() || address.Zone() != "" || address.IsUnspecified() || address.IsMulticast() {
				return types.Object{}, errors.New("invalid static IPv6 gateway")
			}
		}
		if flag("ipv6_default_gateway") && text("ipv6_router") == "" {
			return types.Object{}, errors.New("a static IPv6 default route requires an explicit gateway")
		}
		if actual.Attributes()["ipv6_type"].(types.String).ValueString() != "static" {
			if flag("ipv6_default_gateway") {
				if _, exists := desired["ipv6_router"]; !exists {
					return types.Object{}, errors.New("static IPv6 with a default route requires an explicitly declared gateway; router advertisements are not frozen")
				}
			}
			for _, name := range []string{"ipv6_address", "ipv6_prefix_length"} {
				if _, exists := desired[name]; !exists {
					return types.Object{}, errors.New("static IPv6 requires an explicitly declared address and prefix; automatic observations are not frozen")
				}
			}
		}
	}
	return target, nil
}
