package provider

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/elephant-evergreen/terraform-provider-synology/internal/dsm"
)

const ddnsAPI = "SYNO.Core.DDNS.Record"

type ddnsRecord struct {
	ID            string `json:"id" tfsdk:"id"`
	ProviderID    string `json:"provider" tfsdk:"provider_id"`
	Enabled       bool   `json:"enable" tfsdk:"enabled"`
	Hostname      string `json:"hostname" tfsdk:"hostname"`
	Username      string `json:"username" tfsdk:"username"`
	NetworkMode   string `json:"net" tfsdk:"network_mode"`
	IPv4Address   string `json:"ip" tfsdk:"ipv4_address"`
	IPv6Address   string `json:"ipv6" tfsdk:"ipv6_address"`
	IPv4Interface string `json:"interface_v4" tfsdk:"ipv4_interface"`
	IPv6Interface string `json:"interface_v6" tfsdk:"ipv6_interface"`
	Heartbeat     bool   `json:"heartbeat" tfsdk:"heartbeat"`
	Status        string `json:"status" tfsdk:"status"`
}

func readDDNSRecords(ctx context.Context, client *dsm.Client) (map[string]ddnsRecord, error) {
	var data struct {
		Records *[]map[string]json.RawMessage `json:"records"`
	}
	if err := client.Call(ctx, ddnsAPI, 1, "list", nil, &data); err != nil {
		return nil, err
	}
	if data.Records == nil {
		return nil, errors.New("DSM omitted the complete DDNS record list")
	}
	records := map[string]ddnsRecord{}
	providers := map[string]bool{}
	for _, raw := range *data.Records {
		for _, key := range []string{"id", "provider", "enable", "hostname", "username", "net", "ip", "ipv6", "interface_v4", "interface_v6", "heartbeat", "status"} {
			value, ok := raw[key]
			if !ok || string(value) == "null" {
				return nil, errors.New("DSM omitted a required DDNS record setting")
			}
			if key == "enable" || key == "heartbeat" {
				var b bool
				if json.Unmarshal(value, &b) != nil {
					return nil, errors.New("DSM returned a nonboolean DDNS flag")
				}
			} else {
				var text string
				if json.Unmarshal(value, &text) != nil {
					return nil, errors.New("DSM returned a nonstring DDNS setting")
				}
			}
		}
		var record ddnsRecord
		encoded, err := json.Marshal(raw)
		if err != nil || json.Unmarshal(encoded, &record) != nil {
			return nil, errors.New("cannot decode DSM DDNS record")
		}
		if record.ID == "" || record.ProviderID == "" || providers[record.ProviderID] {
			return nil, errors.New("DSM returned missing or duplicate DDNS identities")
		}
		if _, exists := records[record.ID]; exists {
			return nil, errors.New("DSM returned duplicate DDNS record IDs")
		}
		providers[record.ProviderID] = true
		records[record.ID] = record
	}
	return records, nil
}

func readDDNSProviders(ctx context.Context, client *dsm.Client) ([]string, error) {
	var data struct {
		Providers *[]struct {
			ID *string `json:"id"`
		} `json:"providers"`
	}
	if err := client.Call(ctx, "SYNO.Core.DDNS.Provider", 1, "list", nil, &data); err != nil {
		return nil, err
	}
	if data.Providers == nil {
		return nil, errors.New("DSM omitted its DDNS provider catalogue")
	}
	ids := []string{}
	seen := map[string]bool{}
	for _, provider := range *data.Providers {
		if provider.ID == nil || *provider.ID == "" {
			return nil, errors.New("DSM returned incomplete DDNS provider IDs")
		}
		if seen[*provider.ID] {
			continue
		}
		seen[*provider.ID] = true
		ids = append(ids, *provider.ID)
	}
	return ids, nil
}
