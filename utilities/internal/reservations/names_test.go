package reservations

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/gherlein/gogl/src/mock"
	"github.com/gherlein/gogl/src/types"
)

// A host declaration is two writes to two unrelated tables: a static bind for the
// address, and a host-file entry for the name. The firmware joins them for nobody,
// so every write path has to do both or the addresses it creates cannot be found by
// name -- which was the whole point of importing the file.
//
// These tests assert on the host file, not on the return value, because the bug
// they exist to catch is a path that succeeds while writing only half of it.

// --- goglps --set ------------------------------------------------------------

func TestRunSetWritesNamesAndBindings(t *testing.T) {
	s, c := mockClient(t, nil)
	s.SetHostFile(hostFileWith("lab.example"))

	path := writeHostFile(t, `host nas { hardware ethernet aa:bb:cc:dd:ee:01; fixed-address 192.168.8.13; }
host pi { hardware ethernet aa:bb:cc:dd:ee:02; fixed-address 192.168.8.14; }
`)

	if err := Set(context.Background(), c, path, Modes{}); err != nil {
		t.Fatalf("Set: %v", err)
	}

	if got := s.Reservations(); len(got) != 2 {
		t.Errorf("device holds %d reservations, want 2", len(got))
	}

	hosts := s.HostFile()
	for _, want := range []string{
		"192.168.8.13 nas nas.lab.example",
		"192.168.8.14 pi pi.lab.example",
	} {
		if !strings.Contains(hosts, want) {
			t.Errorf("host file missing %q:\n%s", want, hosts)
		}
	}
	// The router resolves its own name from this file.
	if !strings.Contains(hosts, "127.0.0.1 localhost") {
		t.Errorf("import clobbered unmanaged content:\n%s", hosts)
	}
}

// Without a domain there is nothing to qualify a name with, and the firmware will
// not derive one. Failing up front beats writing addresses nothing can find.
func TestRunSetRefusedWithoutDomain(t *testing.T) {
	s, c := mockClient(t, nil)
	s.SetHostFile(mock.FactoryHostFile)

	path := writeHostFile(t,
		"host nas { hardware ethernet aa:bb:cc:dd:ee:01; fixed-address 192.168.8.13; }\n")

	err := Set(context.Background(), c, path, Modes{})
	if !errors.Is(err, types.ErrDomainNotSet) {
		t.Fatalf("error = %v, want ErrDomainNotSet", err)
	}
	if got := s.Reservations(); len(got) != 0 {
		t.Errorf("device holds %d reservations after a refusal, want 0", len(got))
	}
	// The remedy has to be in the message: the operator cannot guess that the
	// domain lives in the host file.
	if !strings.Contains(err.Error(), "goglps --domain") {
		t.Errorf("error does not say how to proceed: %v", err)
	}
}

// Idempotence has to hold across both tables, or every re-run rewrites the file.
func TestRunSetIsIdempotentAcrossBothTables(t *testing.T) {
	s, c := mockClient(t, nil)
	s.SetHostFile(hostFileWith("lab.example"))

	path := writeHostFile(t,
		"host nas { hardware ethernet aa:bb:cc:dd:ee:01; fixed-address 192.168.8.13; }\n")
	ctx := context.Background()

	if err := Set(ctx, c, path, Modes{}); err != nil {
		t.Fatalf("first run: %v", err)
	}
	afterFirst := s.HostFile()

	if err := Set(ctx, c, path, Modes{}); err != nil {
		t.Fatalf("second run: %v", err)
	}

	if s.HostFile() != afterFirst {
		t.Errorf("second run rewrote the host file:\nbefore:\n%s\nafter:\n%s", afterFirst, s.HostFile())
	}
	if n := strings.Count(s.HostFile(), "nas.lab.example"); n != 1 {
		t.Errorf("name appears %d times after two runs, want 1", n)
	}
}

// A bind with no name is drift, and the fix is to write the missing name -- not to
// skip the entry because the bind already matches.
func TestRunSetRepairsAMissingName(t *testing.T) {
	s, c := mockClient(t, []types.Reservation{
		{Name: "nas", MAC: "aa:bb:cc:dd:ee:01", IP: "192.168.8.13"},
	})
	s.SetHostFile(hostFileWith("lab.example"))

	path := writeHostFile(t,
		"host nas { hardware ethernet aa:bb:cc:dd:ee:01; fixed-address 192.168.8.13; }\n")

	if err := Set(context.Background(), c, path, Modes{}); err != nil {
		t.Fatalf("Set: %v", err)
	}
	if !strings.Contains(s.HostFile(), "192.168.8.13 nas nas.lab.example") {
		t.Errorf("the missing name was not written:\n%s", s.HostFile())
	}
}

func TestRunSetDryRunWritesNeitherTable(t *testing.T) {
	s, c := mockClient(t, nil)
	s.SetHostFile(hostFileWith("lab.example"))
	before := s.HostFile()

	path := writeHostFile(t,
		"host nas { hardware ethernet aa:bb:cc:dd:ee:01; fixed-address 192.168.8.13; }\n")

	if err := Set(context.Background(), c, path, Modes{DryRun: true}); err != nil {
		t.Fatalf("dry run: %v", err)
	}
	if got := s.Reservations(); len(got) != 0 {
		t.Errorf("a dry run created %d reservations", len(got))
	}
	if s.HostFile() != before {
		t.Errorf("a dry run rewrote the host file:\n%s", s.HostFile())
	}
}

// --prune has to take the name with the binding, or the name keeps resolving to an
// address the router no longer reserves.
func TestRunSetPruneRemovesTheName(t *testing.T) {
	s, c := mockClient(t, []types.Reservation{
		{Name: "nas", MAC: "aa:bb:cc:dd:ee:01", IP: "192.168.8.13"},
		{Name: "gone", MAC: "aa:bb:cc:dd:ee:09", IP: "192.168.8.99"},
	})
	s.SetHostFile(hostFileWith("lab.example",
		"192.168.8.13 nas nas.lab.example",
		"192.168.8.99 gone gone.lab.example"))

	path := writeHostFile(t,
		"host nas { hardware ethernet aa:bb:cc:dd:ee:01; fixed-address 192.168.8.13; }\n")

	if err := Set(context.Background(), c, path, Modes{Prune: true}); err != nil {
		t.Fatalf("Set --prune: %v", err)
	}

	hosts := s.HostFile()
	if strings.Contains(hosts, "gone") {
		t.Errorf("pruned entry kept its DNS name:\n%s", hosts)
	}
	if !strings.Contains(hosts, "nas.lab.example") {
		t.Errorf("prune took the wrong name:\n%s", hosts)
	}
	if got := s.Reservations(); len(got) != 1 {
		t.Errorf("device holds %d reservations after prune, want 1", len(got))
	}
}

// Without --prune nothing is removed from either table.
func TestRunSetWithoutPruneKeepsDeviceOnlyName(t *testing.T) {
	s, c := mockClient(t, []types.Reservation{
		{Name: "gone", MAC: "aa:bb:cc:dd:ee:09", IP: "192.168.8.99"},
	})
	s.SetHostFile(hostFileWith("lab.example", "192.168.8.99 gone gone.lab.example"))

	path := writeHostFile(t,
		"host nas { hardware ethernet aa:bb:cc:dd:ee:01; fixed-address 192.168.8.13; }\n")

	if err := Set(context.Background(), c, path, Modes{}); err != nil {
		t.Fatalf("Set: %v", err)
	}
	if !strings.Contains(s.HostFile(), "gone.lab.example") {
		t.Errorf("a run without --prune removed a device-only name:\n%s", s.HostFile())
	}
}

// --- goglps --add ------------------------------------------------------------

func TestRunAddWritesTheName(t *testing.T) {
	s, c := mockClient(t, nil)
	s.SetHostFile(hostFileWith("lab.example"))

	err := Add(context.Background(), c,
		"host nas { hardware ethernet aa:bb:cc:dd:ee:01; fixed-address 192.168.8.13; }",
		Modes{})
	if err != nil {
		t.Fatalf("Add: %v", err)
	}

	if got := s.Reservations(); len(got) != 1 {
		t.Fatalf("device holds %d reservations, want 1", len(got))
	}
	if !strings.Contains(s.HostFile(), "192.168.8.13 nas nas.lab.example") {
		t.Errorf("--add wrote no DNS name:\n%s", s.HostFile())
	}
}

func TestRunAddRefusedWithoutDomain(t *testing.T) {
	s, c := mockClient(t, nil)
	s.SetHostFile(mock.FactoryHostFile)

	err := Add(context.Background(), c,
		"host nas { hardware ethernet aa:bb:cc:dd:ee:01; fixed-address 192.168.8.13; }",
		Modes{})
	if !errors.Is(err, types.ErrDomainNotSet) {
		t.Fatalf("error = %v, want ErrDomainNotSet", err)
	}
	if got := s.Reservations(); len(got) != 0 {
		t.Errorf("a refused --add wrote %d reservations", len(got))
	}
}

// Re-adding the same MAC at a new address must move the name with it, or the old
// address keeps answering.
func TestRunAddMovesTheName(t *testing.T) {
	s, c := mockClient(t, []types.Reservation{
		{Name: "nas", MAC: "aa:bb:cc:dd:ee:01", IP: "192.168.8.13"},
	})
	s.SetHostFile(hostFileWith("lab.example", "192.168.8.13 nas nas.lab.example"))

	err := Add(context.Background(), c,
		"host nas { hardware ethernet aa:bb:cc:dd:ee:01; fixed-address 192.168.8.20; }",
		Modes{Force: true})
	if err != nil {
		t.Fatalf("Add: %v", err)
	}

	hosts := s.HostFile()
	if strings.Contains(hosts, "192.168.8.13") {
		t.Errorf("the old address still answers for the name:\n%s", hosts)
	}
	if !strings.Contains(hosts, "192.168.8.20 nas nas.lab.example") {
		t.Errorf("the name did not move:\n%s", hosts)
	}
}

// --- goglps --del ------------------------------------------------------------

func TestRunDelRemovesTheName(t *testing.T) {
	s, c := mockClient(t, []types.Reservation{
		{Name: "nas", MAC: "aa:bb:cc:dd:ee:01", IP: "192.168.8.13"},
		{Name: "pi", MAC: "aa:bb:cc:dd:ee:02", IP: "192.168.8.14"},
	})
	s.SetHostFile(hostFileWith("lab.example",
		"192.168.8.13 nas nas.lab.example",
		"192.168.8.14 pi pi.lab.example"))

	err := Del(context.Background(), c, Modes{Name: "nas", Force: true})
	if err != nil {
		t.Fatalf("Del: %v", err)
	}

	hosts := s.HostFile()
	if strings.Contains(hosts, "nas") {
		t.Errorf("--del left the DNS name behind:\n%s", hosts)
	}
	if !strings.Contains(hosts, "pi.lab.example") {
		t.Errorf("--del took the wrong name:\n%s", hosts)
	}
	if got := s.Reservations(); len(got) != 1 {
		t.Errorf("device holds %d reservations, want 1", len(got))
	}
}

func TestRunDelDryRunRemovesNothing(t *testing.T) {
	s, c := mockClient(t, []types.Reservation{
		{Name: "nas", MAC: "aa:bb:cc:dd:ee:01", IP: "192.168.8.13"},
	})
	s.SetHostFile(hostFileWith("lab.example", "192.168.8.13 nas nas.lab.example"))
	before := s.HostFile()

	err := Del(context.Background(), c, Modes{Name: "nas", DryRun: true})
	if err != nil {
		t.Fatalf("dry run: %v", err)
	}
	if s.HostFile() != before {
		t.Errorf("a dry run rewrote the host file:\n%s", s.HostFile())
	}
	if got := s.Reservations(); len(got) != 1 {
		t.Errorf("a dry run deleted the reservation")
	}
}
