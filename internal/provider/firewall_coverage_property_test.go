package provider

import (
	"math/rand"
	"net/netip"
	"testing"
)

func TestFirewallSourceCoverageMatchesSetSemantics(t *testing.T) {
	rng := rand.New(rand.NewSource(11))
	for i := 0; i < 400; i++ {
		network := randomPrefix(rng, 24)
		rule := randomPrefix(rng, 8)
		coverage := firewallSourceCoverage(firewallRule{SourceGroup: "netmask", Source: rule.String()}, network)
		inside, outside := 0, 0
		for addr := network.Masked().Addr(); network.Contains(addr); addr = addr.Next() {
			if rule.Contains(addr) {
				inside++
			} else {
				outside++
			}
		}
		switch coverage {
		case 1:
			if outside != 0 {
				t.Fatalf("coverage=1 for %s and %s with %d addresses outside the rule", rule, network, outside)
			}
		case 0:
			if inside != 0 {
				t.Fatalf("coverage=0 for %s and %s with %d addresses inside the rule", rule, network, inside)
			}
		case -1:
			if inside == 0 || outside == 0 {
				t.Fatalf("coverage=-1 for %s and %s with %d inside and %d outside", rule, network, inside, outside)
			}
		default:
			t.Fatalf("unexpected coverage %d for %s and %s", coverage, rule, network)
		}
	}
}

func TestFirewallSourceCoverageForSingleAddress(t *testing.T) {
	rng := rand.New(rand.NewSource(12))
	for i := 0; i < 200; i++ {
		network := randomPrefix(rng, 24)
		address := netip.AddrFrom4([4]byte{10, byte(rng.Intn(4)), byte(rng.Intn(256)), byte(rng.Intn(256))})
		coverage := firewallSourceCoverage(firewallRule{SourceGroup: "ip", Source: address.String()}, network)
		switch {
		case !network.Contains(address):
			if coverage != 0 {
				t.Fatalf("coverage=%d for address %s outside %s", coverage, address, network)
			}
		case network.Bits() == address.BitLen():
			if coverage != 1 {
				t.Fatalf("coverage=%d for address %s covering %s", coverage, address, network)
			}
		default:
			if coverage != -1 {
				t.Fatalf("coverage=%d for a partial single-address rule %s and %s", coverage, address, network)
			}
		}
	}
}

func randomPrefix(rng *rand.Rand, minBits int) netip.Prefix {
	address := netip.AddrFrom4([4]byte{10, byte(rng.Intn(4)), byte(rng.Intn(256)), byte(rng.Intn(256))})
	return netip.PrefixFrom(address, minBits+rng.Intn(33-minBits)).Masked()
}
