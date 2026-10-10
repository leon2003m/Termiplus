package netpolicy

import (
	"net/netip"
	"testing"
)

func TestValidateAccepts(t *testing.T) {
	p := Policy{
		DNS: []DNSEntry{
			{Name: "db.internal", IP: "10.0.0.5"},
			{Name: "cache.corp.lan", IP: "192.168.50.10"},
		},
		Egress: []EgressRule{
			{CIDR: "10.0.0.5/32", Port: 5432, Proto: TCP},  // one internal host
			{CIDR: "192.168.50.0/24", Port: 0, Proto: TCP}, // a whole internal subnet, all ports
			{CIDR: "10.1.0.0/16", Port: 53, Proto: UDP},
		},
	}
	v, err := Validate(p)
	if err != nil {
		t.Fatalf("expected ok, got %v", err)
	}
	if len(v.Egress) != 3 || len(v.DNS) != 2 {
		t.Fatalf("unexpected validated policy: %+v", v)
	}
}

func TestDenyListNeverOpens(t *testing.T) {
	// Every one of these must be refused however it is entered, because it
	// targets loopback, link-local/metadata, the CGNAT range or the sandbox
	// subnet (other terminals, gateway, forwarder, host bridge).
	cases := map[string]string{
		"metadata exact":       "169.254.169.254/32",
		"link-local subnet":    "169.254.0.0/16",
		"link-local supernet":  "169.0.0.0/8", // overlaps 169.254/16
		"loopback":             "127.0.0.1/32",
		"loopback range":       "127.0.0.0/8",
		"sandbox gateway":      "172.30.0.2/32",
		"sandbox forwarder":    "172.30.0.3/32",
		"sandbox host bridge":  "172.30.0.1/32",
		"whole sandbox subnet": "172.30.0.0/24",
		"supernet of sandbox":  "172.0.0.0/8", // overlaps 172.30.0.0/24
		"cgnat":                "100.64.0.1/32",
		"all v4":               "0.0.0.0/0", // overlaps everything in the deny-list
	}
	for name, cidr := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := Validate(Policy{Egress: []EgressRule{{CIDR: cidr, Port: 443, Proto: TCP}}})
			if err == nil {
				t.Fatalf("%s (%s) was accepted", name, cidr)
			}
		})
	}
}

func TestExtraDeny(t *testing.T) {
	mgmt := netip.MustParsePrefix("10.9.0.0/16")
	// allowed without the extra deny
	if _, err := Validate(Policy{Egress: []EgressRule{{CIDR: "10.9.1.5/32", Port: 22, Proto: TCP}}}); err != nil {
		t.Fatalf("baseline rejected: %v", err)
	}
	// refused once the operator protects that subnet
	if _, err := Validate(Policy{Egress: []EgressRule{{CIDR: "10.9.1.5/32", Port: 22, Proto: TCP}}}, mgmt); err == nil {
		t.Fatal("extra deny did not take effect")
	}
}

func TestValidateRejectsMalformed(t *testing.T) {
	cases := map[string]Policy{
		"bad cidr":       {Egress: []EgressRule{{CIDR: "10.0.0.0/33", Port: 1, Proto: TCP}}},
		"host bits set":  {Egress: []EgressRule{{CIDR: "10.0.0.5/24", Port: 1, Proto: TCP}}},
		"port high":      {Egress: []EgressRule{{CIDR: "10.0.0.5/32", Port: 70000, Proto: TCP}}},
		"port negative":  {Egress: []EgressRule{{CIDR: "10.0.0.5/32", Port: -1, Proto: TCP}}},
		"bad proto":      {Egress: []EgressRule{{CIDR: "10.0.0.5/32", Port: 1, Proto: "icmp"}}},
		"empty proto":    {Egress: []EgressRule{{CIDR: "10.0.0.5/32", Port: 1, Proto: ""}}},
		"bad dns name":   {DNS: []DNSEntry{{Name: "bad name!", IP: "10.0.0.5"}}},
		"empty dns name": {DNS: []DNSEntry{{Name: "", IP: "10.0.0.5"}}},
		"bad dns ip":     {DNS: []DNSEntry{{Name: "db", IP: "notanip"}}},
		"dup dns name":   {DNS: []DNSEntry{{Name: "db", IP: "10.0.0.5"}, {Name: "DB", IP: "10.0.0.6"}}},
	}
	for name, p := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := Validate(p); err == nil {
				t.Fatalf("%s was accepted", name)
			}
		})
	}
}

func TestEmptyPolicyOK(t *testing.T) {
	if _, err := Validate(Policy{}); err != nil {
		t.Fatalf("empty policy rejected: %v", err)
	}
}
