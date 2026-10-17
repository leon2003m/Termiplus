package netpolicy

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func newTestStore(t *testing.T) (*Store, string) {
	t.Helper()
	dir := t.TempDir()
	s, err := NewStore(
		filepath.Join(dir, "policy.json"),
		filepath.Join(dir, "dnsmasq.hosts"),
		filepath.Join(dir, "egress.rules"),
	)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	return s, dir
}

func read(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(b)
}

func TestStoreRendersOnPut(t *testing.T) {
	s, dir := newTestStore(t)
	p := Policy{
		DNS:    []DNSEntry{{Name: "db.internal", IP: "10.0.0.5"}},
		Egress: []EgressRule{{CIDR: "10.0.0.5/32", Port: 5432, Proto: TCP}},
	}
	if err := s.Put(p); err != nil {
		t.Fatalf("Put: %v", err)
	}
	if got := read(t, filepath.Join(dir, "dnsmasq.hosts")); !strings.Contains(got, "10.0.0.5 db.internal") {
		t.Fatalf("dnsmasq hosts missing record:\n%s", got)
	}
	if got := read(t, filepath.Join(dir, "egress.rules")); !strings.Contains(got, "10.0.0.5/32 5432 tcp") {
		t.Fatalf("egress rules missing rule:\n%s", got)
	}
	if got := s.Get(); len(got.DNS) != 1 || len(got.Egress) != 1 {
		t.Fatalf("Get mismatch: %+v", got)
	}
}

func TestStoreRejectedPutChangesNothing(t *testing.T) {
	s, dir := newTestStore(t)
	good := Policy{Egress: []EgressRule{{CIDR: "10.0.0.5/32", Port: 5432, Proto: TCP}}}
	if err := s.Put(good); err != nil {
		t.Fatalf("Put good: %v", err)
	}
	before := read(t, filepath.Join(dir, "egress.rules"))

	// The deny-list must win over any admin entry: a rule covering the metadata
	// address is refused and must leave both the files and the in-memory policy
	// exactly as they were.
	bad := Policy{Egress: []EgressRule{{CIDR: "169.254.169.254/32", Port: 80, Proto: TCP}}}
	if err := s.Put(bad); err == nil {
		t.Fatal("deny-listed rule was accepted")
	}
	if after := read(t, filepath.Join(dir, "egress.rules")); after != before {
		t.Fatalf("rejected Put changed egress file:\nbefore=%q\nafter=%q", before, after)
	}
	if got := s.Get(); len(got.Egress) != 1 || got.Egress[0].CIDR != "10.0.0.5/32" {
		t.Fatalf("rejected Put changed in-memory policy: %+v", got)
	}
}

func TestStoreReloadsPersistedPolicy(t *testing.T) {
	s, dir := newTestStore(t)
	if err := s.Put(Policy{DNS: []DNSEntry{{Name: "a.internal", IP: "10.0.0.9"}}}); err != nil {
		t.Fatalf("Put: %v", err)
	}
	// A second store over the same files must come up with the same policy.
	s2, err := NewStore(
		filepath.Join(dir, "policy.json"),
		filepath.Join(dir, "dnsmasq.hosts"),
		filepath.Join(dir, "egress.rules"),
	)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	if got := s2.Get(); len(got.DNS) != 1 || got.DNS[0].Name != "a.internal" {
		t.Fatalf("reloaded policy mismatch: %+v", got)
	}
}

func TestStoreRejectsTamperedPolicyFile(t *testing.T) {
	_, dir := newTestStore(t)
	// Simulate a hand-edited policy file that tries to open a deny-listed range.
	pfile := filepath.Join(dir, "policy.json")
	if err := os.WriteFile(pfile, []byte(`{"dns_entries":[],"egress_allow":[{"cidr":"169.254.169.254/32","port":80,"proto":"tcp"}]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := NewStore(pfile, filepath.Join(dir, "dnsmasq.hosts"), filepath.Join(dir, "egress.rules")); err == nil {
		t.Fatal("NewStore accepted a policy file that opens the deny-list")
	}
}


// A staging failure must leave all live policy files and the in-memory policy
// untouched, even when an earlier file's temporary replacement was prepared.
func TestStoreStagingFailureChangesNothing(t *testing.T) {
	s, dir := newTestStore(t)
	initial := Policy{
		DNS: []DNSEntry{{Name:"before.internal", IP:"10.0.0.5"}},
		Egress: []EgressRule{{CIDR:"10.0.0.5/32", Port:5432, Proto:TCP}},
	}
	if err := s.Put(initial); err != nil { t.Fatal(err) }
	dnsPath := filepath.Join(dir, "dnsmasq.hosts")
	egressPath := filepath.Join(dir, "egress.rules")
	policyPath := filepath.Join(dir, "policy.json")
	beforeDNS, beforeEgress, beforePolicy := read(t,dnsPath), read(t,egressPath), read(t,policyPath)
	beforeCurrent := s.Get()

	// Make the third target's parent unavailable after initialization. Staging
	// must fail before any of the prepared temp files are published.
	s.policyPath = filepath.Join(dir, "removed-parent", "policy.json")
	next := Policy{
		DNS: []DNSEntry{{Name:"after.internal", IP:"10.0.0.6"}},
		Egress: []EgressRule{{CIDR:"10.0.0.6/32", Port:5432, Proto:TCP}},
	}
	if err := s.Put(next); err == nil {
		t.Fatal("Put succeeded despite an unavailable policy path")
	}
	if got := read(t,dnsPath); got != beforeDNS { t.Fatalf("DNS file changed on failed write: %q",got) }
	if got := read(t,egressPath); got != beforeEgress { t.Fatalf("egress file changed on failed write: %q",got) }
	if got := read(t,policyPath); got != beforePolicy { t.Fatalf("policy file changed on failed write: %q",got) }
	if got := s.Get(); len(got.DNS)!=len(beforeCurrent.DNS) || got.DNS[0].Name!=beforeCurrent.DNS[0].Name || got.Egress[0].CIDR!=beforeCurrent.Egress[0].CIDR {
		t.Fatalf("in-memory policy changed on failed write: %+v",got)
	}
}
