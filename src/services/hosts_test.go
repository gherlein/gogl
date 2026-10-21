package services_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/gherlein/gogl/src/mock"
	"github.com/gherlein/gogl/src/services"
	"github.com/gherlein/gogl/src/types"
)

// HostsService is where gogl creates DNS records, and it does so by rewriting a
// file the router itself resolves from. Every test here checks the unmanaged part
// of that file survived: clobbering the loopback entries breaks name resolution on
// the device, which is a much worse failure than a missing reservation.

func hostsService(t *testing.T, content string) (*mock.Server, services.HostsService) {
	t.Helper()
	s := mock.NewServer(t, mock.Options{Password: "secret"})
	s.SetHostFile(content)
	return s, services.NewHostsService(newTransport(t, s))
}

// managedBlock delegates to the mock so the marker format has one definition.
func managedBlock(domain string, entries ...string) string {
	return mock.HostFileWith(domain, entries...)
}

func TestHostsDomainOnFactoryRouterIsEmpty(t *testing.T) {
	_, svc := hostsService(t, mock.FactoryHostFile)

	domain, err := svc.Domain(context.Background())
	if err != nil {
		t.Fatalf("Domain: %v", err)
	}
	if domain != "" {
		t.Errorf("domain = %q on a factory router, want empty", domain)
	}
}

func TestHostsSetDomainThenSet(t *testing.T) {
	s, svc := hostsService(t, mock.FactoryHostFile)
	ctx := context.Background()

	if err := svc.SetDomain(ctx, "lab.example"); err != nil {
		t.Fatalf("SetDomain: %v", err)
	}
	if err := svc.Set(ctx, "nas", "192.168.8.13"); err != nil {
		t.Fatalf("Set: %v", err)
	}

	// Both spellings, because clients ask for either.
	got := s.HostFile()
	for _, want := range []string{"192.168.8.13 nas nas.lab.example", "127.0.0.1 localhost"} {
		if !strings.Contains(got, want) {
			t.Errorf("host file missing %q:\n%s", want, got)
		}
	}

	ip, err := lookup(ctx, svc, "nas.lab.example")
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	if ip != "192.168.8.13" {
		t.Errorf("nas.lab.example = %s, want 192.168.8.13", ip)
	}
}

func lookup(ctx context.Context, svc services.HostsService, name string) (string, error) {
	f, err := svc.Get(ctx)
	if err != nil {
		return "", err
	}
	ip, ok := f.Lookup(name)
	if !ok {
		return "", errors.New("not found")
	}
	return ip, nil
}

// A name with no domain would be a bare hostname that resolves nowhere outside
// the LAN, and the operator would not find out until something failed.
func TestHostsSetRefusedWithoutDomain(t *testing.T) {
	s, svc := hostsService(t, mock.FactoryHostFile)
	before := s.HostFile()

	err := svc.Set(context.Background(), "nas", "192.168.8.13")
	if !errors.Is(err, types.ErrDomainNotSet) {
		t.Fatalf("error = %v, want ErrDomainNotSet", err)
	}
	if s.HostFile() != before {
		t.Error("a refused Set still wrote to the device")
	}
}

func TestHostsSetReplacesRatherThanDuplicates(t *testing.T) {
	s, svc := hostsService(t, managedBlock("lab.example", "192.168.8.13 nas nas.lab.example"))
	ctx := context.Background()

	if err := svc.Set(ctx, "nas", "192.168.8.20"); err != nil {
		t.Fatalf("Set: %v", err)
	}

	got := s.HostFile()
	if strings.Contains(got, "192.168.8.13") {
		t.Errorf("old address still present:\n%s", got)
	}
	if n := strings.Count(got, "nas.lab.example"); n != 1 {
		t.Errorf("name appears %d times, want 1:\n%s", n, got)
	}
}

func TestHostsRemoveByEitherSpelling(t *testing.T) {
	for _, name := range []string{"nas", "nas.lab.example"} {
		t.Run(name, func(t *testing.T) {
			s, svc := hostsService(t, managedBlock("lab.example",
				"192.168.8.13 nas nas.lab.example",
				"192.168.8.14 pi pi.lab.example"))

			if err := svc.Remove(context.Background(), name); err != nil {
				t.Fatalf("Remove(%q): %v", name, err)
			}

			got := s.HostFile()
			// Both forms must go: leaving the bare name behind would keep the
			// host resolving after it was asked to stop.
			if strings.Contains(got, "nas") {
				t.Errorf("removing %q left something behind:\n%s", name, got)
			}
			if !strings.Contains(got, "pi.lab.example") {
				t.Errorf("removing %q took the wrong entry:\n%s", name, got)
			}
		})
	}
}

func TestHostsRemoveUnknownNameIsNotFound(t *testing.T) {
	_, svc := hostsService(t, managedBlock("lab.example"))

	err := svc.Remove(context.Background(), "absent")
	if !errors.Is(err, types.ErrNotFound) {
		t.Errorf("error = %v, want ErrNotFound", err)
	}
}

func TestHostsListAndClear(t *testing.T) {
	s, svc := hostsService(t, managedBlock("lab.example",
		"192.168.8.13 nas nas.lab.example",
		"192.168.8.14 pi pi.lab.example"))
	ctx := context.Background()

	entries, err := svc.List(ctx)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("List returned %d entries, want 2", len(entries))
	}

	if err := svc.Clear(ctx); err != nil {
		t.Fatalf("Clear: %v", err)
	}

	got := s.HostFile()
	if strings.Contains(got, "nas") || strings.Contains(got, "pi ") {
		t.Errorf("Clear left entries behind:\n%s", got)
	}
	// Configuration survives content.
	if !strings.Contains(got, "domain lab.example") {
		t.Errorf("Clear discarded the domain:\n%s", got)
	}
	if !strings.Contains(got, "ff02::1 ip6-allnodes") {
		t.Errorf("Clear touched unmanaged content:\n%s", got)
	}
}

// An arbitrary FQDN is a legitimate request: gogl cannot set dnsmasq's own domain,
// so writing the fully-qualified name is the whole mechanism by which a suffix
// works at all.
func TestHostsSetAcceptsAnFQDNVerbatim(t *testing.T) {
	s, svc := hostsService(t, managedBlock("lab.example"))

	if err := svc.Set(context.Background(), "gw.other.test", "192.168.8.1"); err != nil {
		t.Fatalf("Set: %v", err)
	}
	got := s.HostFile()
	if !strings.Contains(got, "192.168.8.1 gw.other.test") {
		t.Errorf("FQDN not written verbatim:\n%s", got)
	}
	// It must not acquire a second suffix.
	if strings.Contains(got, "other.test.lab.example") {
		t.Errorf("FQDN was requalified:\n%s", got)
	}
}

func TestHostsSetRejectsBadInput(t *testing.T) {
	s, svc := hostsService(t, managedBlock("lab.example"))
	before := s.HostFile()
	ctx := context.Background()

	if err := svc.Set(ctx, "nas", "not-an-ip"); !errors.Is(err, types.ErrInvalidIP) {
		t.Errorf("bad IP: error = %v, want ErrInvalidIP", err)
	}
	if err := svc.Set(ctx, "has a space", "192.168.8.13"); err == nil {
		t.Error("accepted a name with a space in it")
	}
	// IPv6 is out of scope, and silently writing one would be worse than refusing.
	if err := svc.Set(ctx, "nas", "fe80::1"); !errors.Is(err, types.ErrInvalidIP) {
		t.Errorf("IPv6: error = %v, want ErrInvalidIP", err)
	}

	if s.HostFile() != before {
		t.Errorf("a rejected Set wrote to the device:\n%s", s.HostFile())
	}
}

func TestHostsSetDomainRejectsNonsense(t *testing.T) {
	s, svc := hostsService(t, mock.FactoryHostFile)
	before := s.HostFile()

	if err := svc.SetDomain(context.Background(), "not a domain"); err == nil {
		t.Fatal("accepted a domain with a space in it")
	}
	if s.HostFile() != before {
		t.Error("a rejected SetDomain still wrote to the device")
	}
}

// Put is the escape hatch for callers that want to edit the parsed file directly.
func TestHostsPutRoundTrips(t *testing.T) {
	s, svc := hostsService(t, managedBlock("lab.example", "192.168.8.13 nas nas.lab.example"))
	ctx := context.Background()

	f, err := svc.Get(ctx)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if err := f.Set("pi", "192.168.8.14"); err != nil {
		t.Fatalf("Set: %v", err)
	}
	if err := svc.Put(ctx, f); err != nil {
		t.Fatalf("Put: %v", err)
	}

	got := s.HostFile()
	for _, want := range []string{"nas.lab.example", "pi.lab.example", "127.0.0.1 localhost"} {
		if !strings.Contains(got, want) {
			t.Errorf("round trip lost %q:\n%s", want, got)
		}
	}
}
