package netpolicy

import (
	"fmt"
	"sort"
	"strings"
)

// DnsmasqHosts renders the static name→address records for the forwarder in
// /etc/hosts syntax ("IP name"), one line per DNS entry, sorted by name so the
// output is byte-stable for a given policy (a changed file is a real change).
//
// The file is written into the forwarder's `hostsdir`, which dnsmasq watches
// with inotify and reloads automatically on change — no signal or sidecar. It
// renders only the records; the upstream resolver, cache and listen address
// live in the forwarder's base config, set from the operator's .env, so the
// gateway never learns or writes the upstream address (the local-DNS rule).
func (v *Validated) DnsmasqHosts() string {
	entries := append([]DNSEntry{}, v.DNS...)
	sort.Slice(entries, func(i, j int) bool {
		return strings.ToLower(entries[i].Name) < strings.ToLower(entries[j].Name)
	})
	var b strings.Builder
	b.WriteString("# Managed by termiplus gateway. Do not edit; rewritten on every policy change.\n")
	for _, e := range entries {
		fmt.Fprintf(&b, "%s %s\n", e.IP, strings.ToLower(strings.TrimSpace(e.Name)))
	}
	return b.String()
}

// EgressRules renders the allowed internal destinations for the sandbox
// firewall, one `CIDR PORT PROTO` line per rule, sorted for a stable file. A
// Port of 0 is written as the literal "any". firewall.sh reads these lines and
// re-validates each against its own never-allowed ranges before turning it into
// a RETURN rule, so the file is advice the firewall checks, not trusted input.
func (v *Validated) EgressRules() string {
	rules := append([]EgressRule{}, v.Egress...)
	sort.Slice(rules, func(i, j int) bool {
		if rules[i].CIDR != rules[j].CIDR {
			return rules[i].CIDR < rules[j].CIDR
		}
		if rules[i].Port != rules[j].Port {
			return rules[i].Port < rules[j].Port
		}
		return rules[i].Proto < rules[j].Proto
	})
	var b strings.Builder
	b.WriteString("# Managed by termiplus gateway. Do not edit; rewritten on every policy change.\n")
	b.WriteString("# Fields: CIDR PORT PROTO  (PORT \"any\" means every port)\n")
	for _, r := range rules {
		port := "any"
		if r.Port != 0 {
			port = fmt.Sprintf("%d", r.Port)
		}
		fmt.Fprintf(&b, "%s %s %s\n", r.CIDR, port, r.Proto)
	}
	return b.String()
}
