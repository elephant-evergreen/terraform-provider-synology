package provider

import (
	"encoding/json"
	"errors"
	"github.com/elephant-evergreen/terraform-provider-synology/internal/dsm"
	"net/netip"
	"net/url"
	"regexp"
	"strconv"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

func extendedServices() []func() resource.Resource {
	return []func() resource.Resource{NewNetworkProxyResource, NewPackageCenterResource,
		service(serviceDefinition{name: "time_machine", api: "SYNO.Core.FileServ.ServiceDiscovery", version: 1, description: "Bonjour Time Machine advertisement and backup-share selection. Share data, quotas and backup execution are managed separately. Import adopts current policy; destroy never removes shares or backups.", fields: map[string]serviceField{
			"smb_enabled": {"enable_smb_time_machine", types.BoolType, "Advertise Time Machine over SMB. Keep the SMB service enabled."},
			"afp_enabled": {"enable_afp_time_machine", types.BoolType, "Advertise Time Machine over AFP."},
			"shares":      {"time_machine_shares", types.SetType{ElemType: types.StringType}, "Complete set of existing shared-folder names advertised for Time Machine."},
		}}),
		service(serviceDefinition{name: "network", api: "SYNO.Core.Network", version: 2, description: "DSM hostname, DNS and general network policy. Interface addresses, DHCP leases and default routes have separate owners. A change can interrupt DSM connectivity; retain an independent management path and apply separately from router changes. Destroy leaves network settings unchanged.", prepareWrite: prepareNetworkWrite, fields: map[string]serviceField{
			"hostname":              {"server_name", types.StringType, "NAS NetBIOS hostname. Changing it can affect discovery and certificate names."},
			"manual_dns":            {"dns_manual", types.BoolType, "Use explicitly configured DNS servers rather than DHCP DNS."},
			"primary_dns":           {"dns_primary", types.StringType, "Primary DNS address. Omission retains the freshly observed value."},
			"secondary_dns":         {"dns_secondary", types.StringType, "Secondary DNS address, or an empty string."},
			"ipv4_first":            {"ipv4_first", types.BoolType, "Prefer IPv4 when resolving remote hosts."},
			"multiple_gateways":     {"multi_gateway", types.BoolType, "Enable multiple gateways. Routes and interface addresses are not configured here."},
			"arp_ignore":            {"arp_ignore", types.BoolType, "Use DSM ARP reply isolation."},
			"ip_conflict_detection": {"enable_ip_conflict_detect", types.BoolType, "Enable IP address conflict detection."},
		}}),
		service(serviceDefinition{name: "quickconnect_upnp", api: "SYNO.Core.QuickConnect.Upnp", version: 1, description: "QuickConnect automatic router port-forwarding policy. Enabling this can create router mappings. It never implicitly enables UPnP while configuring another QuickConnect setting. Destroy leaves DSM policy unchanged.", fields: map[string]serviceField{
			"enabled": {"enabled", types.BoolType, "Allow QuickConnect to configure automatic router port forwarding."},
		}}),
	}
}

var networkHostnamePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9-]{0,13}[A-Za-z0-9]$|^[A-Za-z0-9]$`)

func prepareNetworkWrite(actual types.Object, args url.Values) error {
	if args.Has("server_name") && args.Get("server_name") != networkWireValue(actual, "hostname") && !networkHostnamePattern.MatchString(args.Get("server_name")) {
		return errors.New("hostname must contain 1 to 15 ASCII letters, digits or interior hyphens")
	}
	fields := map[string]string{"dns_manual": "manual_dns", "dns_primary": "primary_dns", "dns_secondary": "secondary_dns"}
	changed := false
	for wire, name := range fields {
		if args.Has(wire) && args.Get(wire) != networkWireValue(actual, name) {
			changed = true
		}
	}
	if !changed {
		for wire := range fields {
			delete(args, wire)
		}
		return nil
	}
	for wire, name := range fields {
		if !args.Has(wire) {
			args.Set(wire, networkWireValue(actual, name))
		}
	}
	if args.Get("dns_manual") == "true" {
		if _, err := netip.ParseAddr(args.Get("dns_primary")); err != nil {
			return errors.New("manual DNS requires a primary IP address")
		}
		if s := args.Get("dns_secondary"); s != "" {
			if _, err := netip.ParseAddr(s); err != nil {
				return errors.New("secondary DNS must be an IP address or empty")
			}
		}
	}
	return nil
}

func networkWireValue(actual types.Object, name string) string {
	switch v := actual.Attributes()[name].(type) {
	case types.String:
		return v.ValueString()
	case types.Bool:
		if v.ValueBool() {
			return "true"
		}
		return "false"
	}
	return ""
}

func retainServiceFields(actual types.Object, args url.Values, fields map[string]string) {
	for wire, name := range fields {
		if args.Has(wire) {
			continue
		}
		switch v := actual.Attributes()[name].(type) {
		case types.Bool:
			args.Set(wire, strconv.FormatBool(v.ValueBool()))
		case types.Int64:
			args.Set(wire, strconv.FormatInt(v.ValueInt64(), 10))
		case types.String:
			args.Set(wire, v.ValueString())
		case types.Set:
			entries := []string{}
			for _, entry := range v.Elements() {
				entries = append(entries, entry.(types.String).ValueString())
			}
			args.Set(wire, dsm.JSON(entries))
		}
	}
}

func normalizeTerminal(raw map[string]json.RawMessage) error {
	for _, wire := range []string{"ssh_cipher", "ssh_kex", "ssh_mac"} {
		var entries []struct {
			Name     *string `json:"name"`
			Selected *bool   `json:"in_use"`
		}
		value, ok := raw[wire]
		if !ok || string(value) == "null" || json.Unmarshal(value, &entries) != nil {
			return errors.New("DSM returned malformed SSH algorithm capabilities")
		}
		selected := []string{}
		seen := map[string]bool{}
		for _, entry := range entries {
			if entry.Name == nil || *entry.Name == "" || entry.Selected == nil || seen[*entry.Name] {
				return errors.New("DSM returned incomplete or duplicate SSH algorithms")
			}
			seen[*entry.Name] = true
			if *entry.Selected {
				selected = append(selected, *entry.Name)
			}
		}
		raw[wire] = json.RawMessage(dsm.JSON(selected))
	}
	return nil
}

func prepareTerminalWrite(actual types.Object, args url.Values) error {
	retainServiceFields(actual, args, map[string]string{"enable_ssh": "ssh_enabled", "enable_telnet": "telnet_enabled", "ssh_port": "ssh_port", "forbid_console": "console_forbidden"})
	if err := validateServicePort(args.Get("ssh_port")); err != nil {
		return err
	}
	crypto := map[string]string{"ssh_cipher": "ssh_ciphers", "ssh_kex": "ssh_key_exchanges", "ssh_mac": "ssh_macs"}
	changed := false
	for wire := range crypto {
		changed = changed || args.Has(wire)
	}
	if changed {
		retainServiceFields(actual, args, crypto)
		for wire := range crypto {
			var names []string
			if json.Unmarshal([]byte(args.Get(wire)), &names) != nil || (args.Get("enable_ssh") == "true" && len(names) == 0) {
				return errors.New("enabled SSH requires nonempty cipher, key-exchange and MAC selections")
			}
			for _, name := range names {
				if name == "" || strings.ContainsAny(name, "\r\n\x00") {
					return errors.New("SSH algorithm names must be nonempty and contain no control characters")
				}
			}
		}
	}
	return nil
}

func prepareRsyncWrite(actual types.Object, args url.Values) error {
	retainServiceFields(actual, args, map[string]string{"enable": "enabled", "enable_rsync_account": "accounts_enabled", "rsync_sshd_port": "ssh_port", "enable_custom_config": "custom_configuration_enabled"})
	return validateServicePort(args.Get("rsync_sshd_port"))
}

func validateServicePort(value string) error {
	port, err := strconv.ParseInt(value, 10, 64)
	if err != nil || port < 1 || port > 65535 {
		return errors.New("listening port must be from 1 to 65535")
	}
	return nil
}
