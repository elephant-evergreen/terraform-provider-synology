package provider

import (
	"context"
	"encoding/json"
	"testing"
)

func FuzzFirewallProfileDecode(f *testing.F) {
	f.Add([]byte(`{"name":"fixture","global":{"policy":"none","rules":[]}}`))
	f.Add([]byte(`{"name":"fixture","eth0":{"policy":"drop","rules":[{"enable":true,"name":"r","policy":"allow","protocol":"tcp","port_group":"all","ports":"all","port_direction":"","source_ip_group":"all","source_ip":"","log":false}]}}`))
	f.Add([]byte(`{"name":"fixture","global":{"policy":"none","rules":[{"enable":true}]}}`))
	f.Add([]byte(`{"name":"fixture","global":{"policy":"none","rules":[],"extra":1}}`))
	f.Fuzz(func(t *testing.T, data []byte) {
		var raw map[string]json.RawMessage
		if json.Unmarshal(data, &raw) != nil || raw == nil {
			return
		}
		raw["name"] = json.RawMessage(`"fixture"`)
		profile, err := decodeFirewallProfile(context.Background(), raw, "fixture")
		if err != nil {
			return
		}
		elements := profile.Elements()
		if _, ok := elements["global"]; !ok {
			t.Fatal("accepted a profile without its global rule table")
		}
		if len(elements) != len(raw)-1 {
			t.Fatalf("accepted a profile that dropped adapters: %d elements for %d adapters", len(elements), len(raw)-1)
		}
	})
}
