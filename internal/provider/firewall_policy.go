package provider

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/netip"
	"strconv"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

const firewallAPI = "SYNO.Core.Security.Firewall"
const firewallProfileAPI = firewallAPI + ".Profile"
const firewallApplyAPI = firewallProfileAPI + ".Apply"

type firewallRule struct {
	Enabled       bool   `json:"enable" tfsdk:"enabled"`
	Name          string `json:"name" tfsdk:"name"`
	Action        string `json:"policy" tfsdk:"action"`
	Protocol      string `json:"protocol" tfsdk:"protocol"`
	PortGroup     string `json:"port_group" tfsdk:"port_group"`
	Ports         string `json:"ports" tfsdk:"ports"`
	PortDirection string `json:"port_direction" tfsdk:"port_direction"`
	SourceGroup   string `json:"source_ip_group" tfsdk:"source_group"`
	Source        string `json:"source_ip" tfsdk:"source"`
	Log           bool   `json:"log" tfsdk:"log"`
}
type firewallAdapter struct {
	DefaultPolicy string         `json:"policy" tfsdk:"default_policy"`
	Rules         []firewallRule `json:"rules" tfsdk:"rules"`
}

var firewallRuleType = types.ObjectType{AttrTypes: map[string]attr.Type{"enabled": types.BoolType, "name": types.StringType, "action": types.StringType, "protocol": types.StringType, "port_group": types.StringType, "ports": types.StringType, "port_direction": types.StringType, "source_group": types.StringType, "source": types.StringType, "log": types.BoolType}}
var firewallAdapterType = types.ObjectType{AttrTypes: map[string]attr.Type{"default_policy": types.StringType, "rules": types.ListType{ElemType: firewallRuleType}}}

func decodeFirewallProfile(ctx context.Context, raw map[string]json.RawMessage, name string) (types.Map, error) {
	var returned string
	if json.Unmarshal(raw["name"], &returned) != nil || returned != name {
		return types.Map{}, errors.New("DSM returned a different or missing firewall profile identity")
	}
	adapters := map[string]firewallAdapter{}
	for key, value := range raw {
		if key == "name" {
			continue
		}
		if key == "rules" || key == "adapterPolicyMap" {
			return types.Map{}, errors.New("DSM returned an unsupported firewall profile shape; no rules were adopted or rewritten")
		}
		var block map[string]json.RawMessage
		if json.Unmarshal(value, &block) != nil || block == nil || len(block) != 2 || block["policy"] == nil || block["rules"] == nil {
			return types.Map{}, errors.New("DSM returned an unrecognized firewall adapter block")
		}
		var adapter firewallAdapter
		var rules []map[string]json.RawMessage
		if string(block["rules"]) == "null" || json.Unmarshal(block["rules"], &rules) != nil {
			return types.Map{}, errors.New("DSM returned malformed ordered firewall rules")
		}
		for _, rule := range rules {
			expected := map[string]bool{"enable": true, "name": false, "policy": false, "protocol": false, "port_group": false, "ports": false, "port_direction": false, "source_ip_group": false, "source_ip": false, "log": true}
			if len(rule) != len(expected) {
				return types.Map{}, errors.New("DSM returned unmodeled firewall rule fields; refusing lossy policy management")
			}
			for field, boolean := range expected {
				v := rule[field]
				if v == nil || string(v) == "null" {
					return types.Map{}, errors.New("DSM omitted a required firewall rule field")
				}
				if boolean {
					var b bool
					if json.Unmarshal(v, &b) != nil {
						return types.Map{}, errors.New("DSM returned a nonboolean firewall rule flag")
					}
				} else {
					var s string
					if json.Unmarshal(v, &s) != nil {
						return types.Map{}, errors.New("DSM returned a nonstring firewall selector")
					}
				}
			}
		}
		if json.Unmarshal(value, &adapter) != nil {
			return types.Map{}, errors.New("DSM returned malformed firewall adapter policy")
		}
		if adapter.DefaultPolicy != "none" && adapter.DefaultPolicy != "allow" && adapter.DefaultPolicy != "drop" {
			return types.Map{}, errors.New("DSM returned an unsupported firewall default policy")
		}
		adapters[key] = adapter
	}
	if _, ok := adapters["global"]; !ok {
		return types.Map{}, errors.New("DSM firewall profile omitted its global rule table")
	}
	result, d := types.MapValueFrom(ctx, firewallAdapterType, adapters)
	if d.HasError() {
		return types.Map{}, errors.New("cannot decode complete firewall policy")
	}
	return result, nil
}
func firewallAdapters(ctx context.Context, value types.Map) (map[string]firewallAdapter, error) {
	if value.IsUnknown() || value.IsNull() {
		return nil, errors.New("firewall adapters must be known before apply")
	}
	result := map[string]firewallAdapter{}
	if d := value.ElementsAs(ctx, &result, false); d.HasError() {
		return nil, errors.New("firewall selectors and ordered rules must be known")
	}
	return result, nil
}
func validateFirewallRules(adapters map[string]firewallAdapter) error {
	if _, ok := adapters["global"]; !ok {
		return errors.New("firewall adapters must include the global rule table")
	}
	for name, adapter := range adapters {
		if name == "" || name == "name" || name == "rules" || name == "adapterPolicyMap" || strings.ContainsAny(name, "/\\\r\n\x00") {
			return errors.New("invalid firewall adapter name")
		}
		if adapter.DefaultPolicy != "none" && adapter.DefaultPolicy != "allow" && adapter.DefaultPolicy != "drop" {
			return errors.New("firewall default_policy must be none, allow or drop")
		}
		if len(adapter.Rules) > 100 {
			return errors.New("DSM permits at most 100 firewall rules per adapter")
		}
		for _, rule := range adapter.Rules {
			if rule.Action != "allow" && rule.Action != "drop" {
				return errors.New("firewall action must be allow or drop")
			}
			switch rule.Protocol {
			case "all", "tcp", "udp", "icmp", "igmp":
			default:
				return errors.New("unsupported firewall protocol")
			}
			switch rule.SourceGroup {
			case "all":
				if rule.Source != "all" {
					return errors.New("all-source rules must use source = all")
				}
			case "ip":
				if _, err := netip.ParseAddr(rule.Source); err != nil {
					return errors.New("invalid firewall source IP")
				}
			case "netmask":
				if _, err := firewallPrefix(rule.Source); err != nil {
					return err
				}
			case "iprange":
				p := strings.Split(rule.Source, "-")
				if len(p) != 2 {
					return errors.New("invalid firewall source range")
				}
				a, e1 := netip.ParseAddr(p[0])
				b, e2 := netip.ParseAddr(p[1])
				if e1 != nil || e2 != nil || a.BitLen() != b.BitLen() || a.Compare(b) > 0 {
					return errors.New("invalid firewall source range")
				}
			case "geoip":
				for _, country := range strings.Split(rule.Source, ",") {
					if len(country) != 2 || country[0] < 'A' || country[0] > 'Z' || country[1] < 'A' || country[1] > 'Z' {
						return errors.New("GeoIP selectors require comma-separated two-letter uppercase country codes")
					}
				}
			default:
				return errors.New("unsupported firewall source selector")
			}
			switch rule.PortGroup {
			case "all":
				if rule.Ports != "all" || rule.Protocol != "all" || (rule.PortDirection != "" && rule.PortDirection != "destination") {
					return errors.New("all-port rules require ports = all, protocol = all and an empty or destination port_direction")
				}
			case "custom":
				if rule.Protocol == "icmp" || rule.Protocol == "igmp" {
					if rule.Ports != "" || rule.PortDirection != "" {
						return errors.New("ICMP and IGMP do not select ports")
					}
				} else {
					if rule.PortDirection != "destination" && rule.PortDirection != "source" {
						return errors.New("custom ports require destination or source direction")
					}
					if _, err := firewallPortMatches(rule.Ports, 1); err != nil {
						return err
					}
				}
			case "service", "reserved":
				if rule.Ports == "" {
					return errors.New("service port selectors must be nonempty")
				}
			default:
				return errors.New("unsupported firewall port selector")
			}
		}
	}
	return nil
}
func firewallPrefix(value string) (netip.Prefix, error) {
	if p, err := netip.ParsePrefix(value); err == nil {
		return p.Masked(), nil
	}
	parts := strings.Split(value, "/")
	if len(parts) == 2 {
		a, err := netip.ParseAddr(parts[0])
		mask := net.ParseIP(parts[1]).To4()
		if err == nil && a.Is4() && mask != nil {
			ones, bits := net.IPMask(mask).Size()
			if bits == 32 {
				return netip.PrefixFrom(a, ones).Masked(), nil
			}
		}
	}
	return netip.Prefix{}, errors.New("invalid firewall source subnet or prefix")
}
func firewallPortMatches(value string, port int) (bool, error) {
	matched := false
	for _, part := range strings.Split(value, ",") {
		bounds := strings.Split(part, "-")
		if len(bounds) > 2 {
			return false, errors.New("invalid firewall port range")
		}
		from, err := strconv.Atoi(bounds[0])
		if err != nil || from < 1 || from > 65535 {
			return false, errors.New("firewall ports must be 1 to 65535")
		}
		to := from
		if len(bounds) == 2 {
			to, err = strconv.Atoi(bounds[1])
			if err != nil || to < from || to > 65535 {
				return false, errors.New("invalid firewall port range")
			}
		}
		matched = matched || (from <= port && port <= to)
	}
	return matched, nil
}
func firewallSourceCoverage(rule firewallRule, network netip.Prefix) int {
	switch rule.SourceGroup {
	case "all":
		return 1
	case "ip":
		address, err := netip.ParseAddr(rule.Source)
		if err != nil {
			return -1
		}
		if !network.Contains(address) {
			return 0
		}
		if network.Bits() == address.BitLen() {
			return 1
		}
		return -1
	case "netmask":
		source, err := firewallPrefix(rule.Source)
		if err != nil {
			return -1
		}
		if !source.Overlaps(network) {
			return 0
		}
		if source.Bits() <= network.Bits() {
			return 1
		}
		return -1
	case "geoip", "iprange":
		return -1
	}
	return -1
}
func admitFirewall(adapters map[string]firewallAdapter, networks []string, port int) error {
	if len(networks) == 0 {
		return errors.New("firewall changes require management_networks identifying the actual source networks seen by DSM; retain an independent management path")
	}
	for _, value := range networks {
		network, err := netip.ParsePrefix(value)
		if err != nil {
			return errors.New("management_networks must contain IPv4 or IPv6 CIDR prefixes")
		}
		network = network.Masked()
		permitted := false
		for _, rule := range adapters["global"].Rules {
			if !rule.Enabled || rule.Protocol == "udp" || rule.Protocol == "icmp" || rule.Protocol == "igmp" {
				continue
			}
			if rule.PortGroup == "custom" {
				if rule.PortDirection != "destination" {
					return errors.New("cannot prove management access through source-port selectors")
				}
				match, err := firewallPortMatches(rule.Ports, port)
				if err != nil {
					return err
				}
				if !match {
					continue
				}
			} else if rule.PortGroup != "all" {
				return errors.New("cannot prove management access through service-name port selectors")
			}
			coverage := firewallSourceCoverage(rule, network)
			if coverage == 0 {
				continue
			}
			if coverage < 0 {
				return errors.New("cannot prove management access through a partial, range or GeoIP source selector")
			}
			if rule.Action != "allow" {
				return errors.New("firewall policy would deny a declared management network")
			}
			permitted = true
			break
		}
		if !permitted {
			return errors.New("each declared management network requires an explicit matching global allow rule for the DSM HTTPS port before any matching deny or unmodeled selector")
		}
	}
	return nil
}
