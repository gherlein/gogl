package reservations

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"testing"

	"github.com/gherlein/gogl/src/mock"
	"github.com/gherlein/gogl/src/types"
)

// hostFileWith returns factory host-file content with gogl's block appended.
func hostFileWith(domain string, entries ...string) string {
	return mock.HostFileWith(domain, entries...)
}

// ---------------------------------------------------------------------------
// --domain
// ---------------------------------------------------------------------------

func TestRunSetDomainOnFactoryRouter(t *testing.T) {
	s, c := mockClient(t, nil)
	s.SetHostFile(mock.FactoryHostFile)

	if err := SetDomain(context.Background(), c, "lab.example"); err != nil {
		t.Fatalf("SetDomain: %v", err)
	}

	got := s.HostFile()
	if !strings.Contains(got, "domain lab.example") {
		t.Errorf("domain not recorded on the device:\n%s", got)
	}
	// The firmware's own loopback entries must survive: the router resolves its
	// own name from this file.
	if !strings.Contains(got, "127.0.0.1 localhost") {
		t.Errorf("factory content was clobbered:\n%s", got)
	}
}

// Changing the domain has to rewrite the names, or half the file keeps the old
// suffix and resolution splits between two domains.
func TestRunSetDomainRequalifiesExistingNames(t *testing.T) {
	s, c := mockClient(t, nil)
	s.SetHostFile(hostFileWith("old.example", "192.168.8.13 nas nas.old.example"))

	if err := SetDomain(context.Background(), c, "new.example"); err != nil {
		t.Fatalf("SetDomain: %v", err)
	}

	got := s.HostFile()
	if !strings.Contains(got, "nas.new.example") {
		t.Errorf("name not requalified:\n%s", got)
	}
	if strings.Contains(got, "old.example") {
		t.Errorf("stale suffix left behind:\n%s", got)
	}
	// The bare name must still be there: asking for "nas" alone is the common
	// case on a travel router.
	if !strings.Contains(got, "192.168.8.13 nas nas.new.example") {
		t.Errorf("entry is not %q:\n%s", "192.168.8.13 nas nas.new.example", got)
	}
}

func TestRunSetDomainRejectsNonsense(t *testing.T) {
	s, c := mockClient(t, nil)
	s.SetHostFile(mock.FactoryHostFile)
	before := s.HostFile()

	if err := SetDomain(context.Background(), c, "not a domain"); err == nil {
		t.Fatal("accepted a domain with a space in it")
	}
	if s.HostFile() != before {
		t.Error("a rejected domain still wrote to the device")
	}
}

// ---------------------------------------------------------------------------
// --clear
// ---------------------------------------------------------------------------

func seededRouter(t *testing.T) (*mock.Server, func(context.Context, Modes) error) {
	t.Helper()
	s, c := mockClient(t, []types.Reservation{
		{Name: "nas", MAC: "aa:bb:cc:dd:ee:01", IP: "192.168.8.13"},
		{Name: "pi", MAC: "aa:bb:cc:dd:ee:02", IP: "192.168.8.14"},
	})
	s.SetHostFile(hostFileWith("lab.example",
		"192.168.8.13 nas nas.lab.example",
		"192.168.8.14 pi pi.lab.example"))

	return s, func(ctx context.Context, modes Modes) error {
		return Clear(ctx, c, modes)
	}
}

// Both tables, because they are one intent. Leaving the names behind would strand
// DNS records pointing into a subnet the operator is about to renumber away from.
func TestRunClearRemovesReservationsAndNames(t *testing.T) {
	s, clear := seededRouter(t)

	if err := clear(context.Background(), Modes{Force: true}); err != nil {
		t.Fatalf("Clear: %v", err)
	}

	if got := s.Reservations(); len(got) != 0 {
		t.Errorf("%d reservations survived the clear", len(got))
	}
	got := s.HostFile()
	for _, name := range []string{"nas", "pi"} {
		if strings.Contains(got, name) {
			t.Errorf("DNS name %q survived the clear:\n%s", name, got)
		}
	}
	// The domain is configuration, not content, and must survive.
	if !strings.Contains(got, "domain lab.example") {
		t.Errorf("the clear discarded the domain:\n%s", got)
	}
	if !strings.Contains(got, "127.0.0.1 localhost") {
		t.Errorf("the clear touched unmanaged content:\n%s", got)
	}
}

func TestRunClearDryRunChangesNothing(t *testing.T) {
	s, clear := seededRouter(t)
	before := s.HostFile()

	if err := clear(context.Background(), Modes{DryRun: true}); err != nil {
		t.Fatalf("dry run: %v", err)
	}

	if got := s.Reservations(); len(got) != 2 {
		t.Errorf("a dry run deleted reservations: %d remain, want 2", len(got))
	}
	if s.HostFile() != before {
		t.Errorf("a dry run rewrote the host file:\n%s", s.HostFile())
	}
}

// "Make sure there is nothing there" is a reasonable request, so an already-empty
// router is a no-op rather than an error.
func TestRunClearOnEmptyRouter(t *testing.T) {
	s, c := mockClient(t, nil)
	s.SetHostFile(hostFileWith("lab.example"))

	if err := Clear(context.Background(), c, Modes{Force: true}); err != nil {
		t.Errorf("Clear on an empty router: %v", err)
	}
}

// A router with names but no binds is a real state -- a half-finished import, or
// a clear that failed between its two calls -- and clearing must still work.
func TestRunClearWithNamesButNoReservations(t *testing.T) {
	s, c := mockClient(t, nil)
	s.SetHostFile(hostFileWith("lab.example", "192.168.8.13 nas nas.lab.example"))

	if err := Clear(context.Background(), c, Modes{Force: true}); err != nil {
		t.Fatalf("Clear: %v", err)
	}
	if strings.Contains(s.HostFile(), "nas") {
		t.Errorf("orphaned name survived:\n%s", s.HostFile())
	}
}

// The exact scenario that failed on hardware: 27 reservations, no domain ever set,
// no DNS names. Clear called Hosts().Clear() unconditionally, which rendered a
// block containing "(domain: )" and the firmware refused it with -32602 -- so the
// reservations were never deleted either.
func TestRunClearOnRouterWithNoDomainAndNoNames(t *testing.T) {
	seed := make([]types.Reservation, 0, 27)
	for i := 0; i < 27; i++ {
		seed = append(seed, types.Reservation{
			Name: "h" + strconv.Itoa(i),
			MAC:  fmt.Sprintf("aa:bb:cc:00:00:%02x", i),
			IP:   "192.168.2." + strconv.Itoa(10+i),
		})
	}

	s, c := mockClient(t, seed)
	s.SetHostFile(mock.FactoryHostFile)

	if err := Clear(context.Background(), c, Modes{Force: true}); err != nil {
		t.Fatalf("Clear on a router with no domain: %v", err)
	}

	if got := s.Reservations(); len(got) != 0 {
		t.Errorf("%d reservations survived, want 0", len(got))
	}
	// Nothing needed writing to the host file, so it must be untouched -- not
	// rewritten with an empty block.
	if s.HostFile() != mock.FactoryHostFile {
		t.Errorf("the host file was rewritten when there was nothing to clear:\n%s", s.HostFile())
	}
}

// Setting the domain on a factory router is the first thing anyone does, and it was
// broken for the same reason.
func TestRunSetDomainProducesWritableContent(t *testing.T) {
	s, c := mockClient(t, nil)
	s.SetHostFile(mock.FactoryHostFile)

	if err := SetDomain(context.Background(), c, "herlein.me"); err != nil {
		t.Fatalf("SetDomain: %v", err)
	}
	if err := types.ValidateContent(s.HostFile()); err != nil {
		t.Errorf("wrote content the firmware would reject: %v\n%s", err, s.HostFile())
	}
	if got := types.ParseHostFile(s.HostFile()).Domain; got != "herlein.me" {
		t.Errorf("domain = %q, want herlein.me", got)
	}
}
