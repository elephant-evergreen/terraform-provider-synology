package dsm

import (
	"encoding/json"
	"math/rand"
	"testing"
)

func FuzzPermissionDecode(f *testing.F) {
	f.Add([]byte(`{"name":"alice","is_readonly":false,"is_writable":true,"is_deny":false}`))
	f.Add([]byte(`{"name":"","is_readonly":false,"is_writable":false,"is_deny":false}`))
	f.Add([]byte(`{"name":"a","is_readonly":true,"is_writable":true,"is_deny":false}`))
	f.Add([]byte(`{"name":"a","is_readonly":null,"is_writable":false,"is_deny":false}`))
	f.Fuzz(func(t *testing.T, data []byte) {
		var raw map[string]json.RawMessage
		if json.Unmarshal(data, &raw) != nil {
			return
		}
		p, err := decodePermission(raw)
		if err != nil {
			return
		}
		access, err := p.Access()
		if err != nil {
			t.Fatalf("decode accepted a permission that Access rejects: %v", err)
		}
		switch access {
		case "inherit", "deny", "read_only", "read_write":
		default:
			t.Fatalf("decode returned unknown access %q", access)
		}
	})
}

func TestAccessTargetConvergesToDesired(t *testing.T) {
	rng := rand.New(rand.NewSource(1))
	values := []string{"", "deny", "read_only", "read_write"}
	names := []string{"a", "b", "c", "d"}
	for i := 0; i < 2000; i++ {
		current, desired := map[string]string{}, map[string]string{}
		for _, name := range names {
			if rng.Intn(2) == 0 {
				current[name] = values[1+rng.Intn(3)]
			}
			if rng.Intn(2) == 0 {
				desired[name] = values[1+rng.Intn(3)]
			}
		}
		restrictive := accessTarget(current, current, desired, true)
		for _, name := range names {
			if accessRank(restrictive[name]) > accessRank(current[name]) {
				t.Fatalf("restrictive phase granted access to %q: current=%q target=%q", name, current[name], restrictive[name])
			}
		}
		final := accessTarget(restrictive, current, desired, false)
		for _, name := range names {
			if final[name] != desired[name] {
				t.Fatalf("final policy for %q = %q, want %q (current=%q)", name, final[name], desired[name], current[name])
			}
		}
	}
}
