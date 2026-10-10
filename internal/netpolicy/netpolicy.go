// Package netpolicy validates the admin-configurable network policy for
// terminals: custom DNS entries and per-destination egress allowances.
//
// The security rule is simple and enforced here, not in the firewall script:
// public egress stays allowed by default, internal destinations are denied by
// default and opened only by an explicit, validated entry, and a fixed
// deny-list (loopback, link-local incl. the cloud metadata address, and the
// sandbox subnet itself) can never be opened, whatever the admin enters.
//
// Standard library only.
package netpolicy

import (
	"fmt"
	"net/netip"
	"sort"
	"strings"
)

// Proto is a transport protocol an egress rule may name.
type Proto string

const (
	TCP Proto = "tcp"
	UDP Proto = "udp"
)

// DNSEntry is a static name→address record served by the forwarder.
type DNSEntry struct {
	Name string `json:"name"`
	IP   string `json:"ip"`
}

// EgressRule allows terminals to reach one internal destination. CIDR is a
// single host (/32, /128) or a subnet; Port 0 means every port.
type EgressRule struct {
	CIDR  string `json:"cidr"`
	Port  int    `json:"port"`
	Proto Proto  `json:"proto"`
}

// Policy is the whole admin-set network policy.
type Policy struct {
	DNS    []DNSEntry   `json:"dns_entries"`
	Egress []EgressRule `json:"egress_allow"`
}

// denyList are destinations an egress rule may never cover. The host's own
// addresses are not listed here: traffic from a terminal to the host is
// delivered locally and dropped by the firewall's INPUT chain, which this
// package never relaxes. These are the routed destinations that must stay
// unreachable regardless of admin input.
var denyList = mustPrefixes(
	"127.0.0.0/8",    // loopback
	"::1/128",        // loopback v6
	"169.254.0.0/16", // link-local incl. the metadata address 169.254.169.254
	"fe80::/10",      // link-local v6
	"100.64.0.0/10",  // CGNAT / tailnet range
	"172.30.0.0/24",  // the sandbox subnet itself (other terminals, gateway, forwarder, host bridge)
)

// Validated is a policy that passed Validate: every value is well-formed and
// no egress rule overlaps the deny-list.
type Validated struct {
	DNS    []DNSEntry
	Egress []EgressRule
}

// Validate checks a policy and returns its validated form or an error naming
// the first problem. extraDeny lets the operator add never-openable prefixes
// (for example a management subnet); it is added to the built-in deny-list.
func Validate(p Policy, extraDeny ...netip.Prefix) (*Validated, error) {
	deny := append(append([]netip.Prefix{}, denyList...), extraDeny...)

	seenName := map[string]bool{}
	for i, e := range p.DNS {
		name := strings.TrimSpace(e.Name)
		if !validHostname(name) {
			return nil, fmt.Errorf("dns_entries[%d]: invalid name %q", i, e.Name)
		}
		if seenName[strings.ToLower(name)] {
			return nil, fmt.Errorf("dns_entries[%d]: duplicate name %q", i, name)
		}
		seenName[strings.ToLower(name)] = true
		ip, err := netip.ParseAddr(e.IP)
		if err != nil {
			return nil, fmt.Errorf("dns_entries[%d]: invalid ip %q", i, e.IP)
		}
		// Unmap so an IPv4-mapped IPv6 form (::ffff:a.b.c.d) is compared as the
		// IPv4 address it denotes, not skipped on an address-family mismatch.
		ip = ip.Unmap()
		for _, d := range deny {
			if d.Contains(ip) {
				return nil, fmt.Errorf("dns_entries[%d]: ip %s is in the never-allowed range %s", i, ip, d)
			}
		}
	}

	for i, r := range p.Egress {
		pfx, err := netip.ParsePrefix(r.CIDR)
		if err != nil {
			return nil, fmt.Errorf("egress_allow[%d]: invalid cidr %q", i, r.CIDR)
		}
		// The sandbox network is IPv4-only, and an IPv4-mapped IPv6 prefix
		// (::ffff:a.b.c.d/n) does not overlap an IPv4 deny entry under
		// netip.Prefix.Overlaps (different address family), which would let a
		// mapped spelling of a never-allowed target slip through. Require plain
		// IPv4, which is what the deny-list and the firewall both speak.
		if !pfx.Addr().Is4() {
			return nil, fmt.Errorf("egress_allow[%d]: cidr %q must be plain IPv4 (the sandbox is IPv4-only; IPv4-mapped IPv6 is refused)", i, r.CIDR)
		}
		if pfx.Addr() != pfx.Masked().Addr() {
			return nil, fmt.Errorf("egress_allow[%d]: cidr %q has host bits set, use %s", i, r.CIDR, pfx.Masked())
		}
		if r.Port < 0 || r.Port > 65535 {
			return nil, fmt.Errorf("egress_allow[%d]: port %d out of range", i, r.Port)
		}
		if r.Proto != TCP && r.Proto != UDP {
			return nil, fmt.Errorf("egress_allow[%d]: proto must be tcp or udp, got %q", i, r.Proto)
		}
		if d, ok := overlapsAny(pfx, deny); ok {
			return nil, fmt.Errorf("egress_allow[%d]: %s overlaps the never-allowed range %s", i, pfx, d)
		}
	}
	return &Validated{DNS: append([]DNSEntry{}, p.DNS...), Egress: append([]EgressRule{}, p.Egress...)}, nil
}

// overlapsAny reports whether pfx intersects any prefix in list.
func overlapsAny(pfx netip.Prefix, list []netip.Prefix) (netip.Prefix, bool) {
	for _, d := range list {
		if d.Overlaps(pfx) {
			return d, true
		}
	}
	return netip.Prefix{}, false
}

// DenyList returns a copy of the built-in never-allowed prefixes (for tests
// and for documentation surfaces).
func DenyList() []netip.Prefix { return append([]netip.Prefix{}, denyList...) }

func mustPrefixes(ss ...string) []netip.Prefix {
	out := make([]netip.Prefix, 0, len(ss))
	for _, s := range ss {
		out = append(out, netip.MustParsePrefix(s))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].String() < out[j].String() })
	return out
}

// validHostname accepts a DNS name of 1..253 chars, labels of letters, digits
// and hyphens, not starting or ending a label with a hyphen.
func validHostname(s string) bool {
	if s == "" || len(s) > 253 {
		return false
	}
	for _, label := range strings.Split(s, ".") {
		if label == "" || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for _, c := range label {
			if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-') {
				return false
			}
		}
	}
	return true
}
