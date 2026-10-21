package reservations

import (
	"context"
	"encoding/json"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	gogl "github.com/gherlein/gogl/src"
	"github.com/gherlein/gogl/src/mock"
	"github.com/gherlein/gogl/src/types"
)

// These tests exercise the actual write path end to end through a real client
// pointed at the mock router. That matters more than any other test in this
// package: Set is the only thing in gogl that mutates a device, and its
// four-phase ordering is what keeps a malformed file from half-writing a router.

// Shaped as lan.get_config_list actually replies.
const lanFixture = `{
  "interfaces": [
    {
      "interface": "lan",
      "ip": "192.168.8.1",
      "netmask": "255.255.255.0",
      "enable": 1,
      "start": "192.168.8.100",
      "end": "192.168.8.249",
      "leasetime": "12h",
      "dns": [],
      "gateway": ""
    }
  ]
}`

func mockClient(t *testing.T, seed []types.Reservation) (*mock.Server, *gogl.Client) {
	t.Helper()

	s := mock.NewServer(t, mock.Options{Password: "secret"})
	s.LoadFixture(mock.NetworkGroup, mock.MethodGetConfigList, json.RawMessage(lanFixture))
	s.SetReservations(seed)
	// Reservation writes are gated on a configured DNS domain. These tests are
	// about goglps's own logic, so the domain is a precondition rather than the
	// subject; the gate is covered in src/services/guards_test.go.
	s.SetHostFile(mock.HostFileWith("lab.example"))

	u, err := url.Parse(s.URL())
	if err != nil {
		t.Fatalf("parse mock URL: %v", err)
	}
	port, err := strconv.Atoi(u.Port())
	if err != nil {
		t.Fatalf("parse port: %v", err)
	}

	c, err := gogl.New(gogl.Config{
		Host: u.Hostname(), Port: port, Password: "secret",
		KeepaliveInterval: -1,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() {
		if err := c.Close(); err != nil {
			t.Errorf("Close: %v", err)
		}
	})
	return s, c
}

// writeHostFile puts content in a temp file and returns its path.
func writeHostFile(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "hosts")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write host file: %v", err)
	}
	return path
}

func TestRunSetCreates(t *testing.T) {
	s, c := mockClient(t, nil)
	path := writeHostFile(t, `host nas {
    hardware ethernet aa:bb:cc:dd:ee:01;
    fixed-address 192.168.8.13;
}
host printer {
    hardware ethernet aa:bb:cc:dd:ee:02;
    fixed-address 192.168.8.14;
}
`)

	if err := Set(context.Background(), c, path, Modes{}); err != nil {
		t.Fatalf("Set error: %v", err)
	}

	got := s.Reservations()
	if len(got) != 2 {
		t.Fatalf("device holds %d reservations, want 2", len(got))
	}
}

// Running twice must leave the device unchanged: idempotence is what makes a host
// file usable as a checked-in description of a network.
func TestRunSetIsIdempotent(t *testing.T) {
	s, c := mockClient(t, nil)
	path := writeHostFile(t, `host nas {
    hardware ethernet aa:bb:cc:dd:ee:01;
    fixed-address 192.168.8.13;
}
`)

	for i := 0; i < 2; i++ {
		if err := Set(context.Background(), c, path, Modes{}); err != nil {
			t.Fatalf("Set pass %d: %v", i+1, err)
		}
	}
	if got := s.Reservations(); len(got) != 1 {
		t.Errorf("device holds %d reservations after two runs, want 1", len(got))
	}
}

func TestRunSetUpdates(t *testing.T) {
	s, c := mockClient(t, []types.Reservation{
		{Name: "nas", MAC: "aa:bb:cc:dd:ee:01", IP: "192.168.8.99"},
	})
	path := writeHostFile(t, `host nas {
    hardware ethernet aa:bb:cc:dd:ee:01;
    fixed-address 192.168.8.13;
}
`)

	if err := Set(context.Background(), c, path, Modes{}); err != nil {
		t.Fatalf("Set error: %v", err)
	}

	got := s.Reservations()
	if len(got) != 1 || got[0].IP != "192.168.8.13" {
		t.Errorf("device state = %v, want the updated address", got)
	}
}

// Without --prune, a device-only reservation survives.
func TestRunSetLeavesExtrasWithoutPrune(t *testing.T) {
	s, c := mockClient(t, []types.Reservation{
		{Name: "keeper", MAC: "aa:bb:cc:dd:ee:09", IP: "192.168.8.50"},
	})
	path := writeHostFile(t, `host nas {
    hardware ethernet aa:bb:cc:dd:ee:01;
    fixed-address 192.168.8.13;
}
`)

	if err := Set(context.Background(), c, path, Modes{}); err != nil {
		t.Fatalf("Set error: %v", err)
	}
	if got := s.Reservations(); len(got) != 2 {
		t.Errorf("device holds %d reservations, want 2 (the extra should survive)", len(got))
	}
}

func TestRunSetPrunes(t *testing.T) {
	s, c := mockClient(t, []types.Reservation{
		{Name: "gone", MAC: "aa:bb:cc:dd:ee:09", IP: "192.168.8.50"},
	})
	path := writeHostFile(t, `host nas {
    hardware ethernet aa:bb:cc:dd:ee:01;
    fixed-address 192.168.8.13;
}
`)

	if err := Set(context.Background(), c, path, Modes{Prune: true}); err != nil {
		t.Fatalf("Set error: %v", err)
	}

	got := s.Reservations()
	if len(got) != 1 || got[0].Name != "nas" {
		t.Errorf("device state = %v, want only nas", got)
	}
}

// --dry-run must not write.
func TestRunSetDryRunWritesNothing(t *testing.T) {
	s, c := mockClient(t, nil)
	path := writeHostFile(t, `host nas {
    hardware ethernet aa:bb:cc:dd:ee:01;
    fixed-address 192.168.8.13;
}
`)

	if err := Set(context.Background(), c, path, Modes{DryRun: true}); err != nil {
		t.Fatalf("Set error: %v", err)
	}
	if got := s.Reservations(); len(got) != 0 {
		t.Errorf("--dry-run wrote %d reservations, want 0", len(got))
	}
}

// A file whose addresses are in a different subnet must be refused before any
// write. This is the gofips-export-meets-factory-router case.
func TestRunSetRefusesSubnetMismatch(t *testing.T) {
	s, c := mockClient(t, nil)
	path := writeHostFile(t, `host nas {
    hardware ethernet aa:bb:cc:dd:ee:01;
    fixed-address 192.168.4.13;
}
`)

	if err := Set(context.Background(), c, path, Modes{}); err == nil {
		t.Fatal("Set accepted an out-of-subnet file")
	}
	if got := s.Reservations(); len(got) != 0 {
		t.Errorf("a refused run wrote %d reservations, want 0", len(got))
	}
}

// A malformed file must be rejected before any device contact, so a bad file can
// never half-write a router.
func TestRunSetRefusesMalformedFileWithoutWriting(t *testing.T) {
	s, c := mockClient(t, nil)
	path := writeHostFile(t, `host good {
    hardware ethernet aa:bb:cc:dd:ee:01;
    fixed-address 192.168.8.13;
}
host bad_name {
    hardware ethernet aa:bb:cc:dd:ee:02;
    fixed-address 192.168.8.14;
}
`)

	if err := Set(context.Background(), c, path, Modes{}); err == nil {
		t.Fatal("Set accepted a file with an invalid hostname")
	}
	// Not even the valid entry may be written: validation is all-or-nothing.
	if got := s.Reservations(); len(got) != 0 {
		t.Errorf("a refused run wrote %d reservations, want 0", len(got))
	}
}

func TestRunSetRefusesDuplicatesWithoutWriting(t *testing.T) {
	s, c := mockClient(t, nil)
	path := writeHostFile(t, `host a {
    hardware ethernet aa:bb:cc:dd:ee:01;
    fixed-address 192.168.8.13;
}
host b {
    hardware ethernet aa:bb:cc:dd:ee:01;
    fixed-address 192.168.8.14;
}
`)

	if err := Set(context.Background(), c, path, Modes{}); err == nil {
		t.Fatal("Set accepted a duplicate MAC")
	}
	if got := s.Reservations(); len(got) != 0 {
		t.Errorf("a refused run wrote %d reservations, want 0", len(got))
	}
}

func TestRunSetMissingFile(t *testing.T) {
	_, c := mockClient(t, nil)
	if err := Set(context.Background(), c, "/nonexistent/hosts", Modes{}); err == nil {
		t.Error("Set succeeded with a missing file")
	}
}

// A pooled address warns but still writes.
func TestRunSetWritesPooledAddressWithWarning(t *testing.T) {
	s, c := mockClient(t, nil)
	path := writeHostFile(t, `host nas {
    hardware ethernet aa:bb:cc:dd:ee:01;
    fixed-address 192.168.8.150;
}
`)

	if err := Set(context.Background(), c, path, Modes{}); err != nil {
		t.Fatalf("Set refused a pooled address: %v", err)
	}
	if got := s.Reservations(); len(got) != 1 {
		t.Errorf("device holds %d reservations, want 1", len(got))
	}
}

func TestRunAddCreates(t *testing.T) {
	s, c := mockClient(t, nil)
	const fragment = `host camera {
    hardware ethernet aa:bb:cc:dd:ee:07;
    fixed-address 192.168.8.17;
}`

	if err := Add(context.Background(), c, fragment, Modes{}); err != nil {
		t.Fatalf("Add error: %v", err)
	}
	got := s.Reservations()
	if len(got) != 1 || got[0].Name != "camera" {
		t.Errorf("device state = %v, want camera", got)
	}
}

// Re-adding an unchanged entry is an update, not a conflict, so --add is
// idempotent for a host that is already correct.
func TestRunAddIsIdempotentForUnchangedEntry(t *testing.T) {
	s, c := mockClient(t, []types.Reservation{
		{Name: "camera", MAC: "aa:bb:cc:dd:ee:07", IP: "192.168.8.17"},
	})
	const fragment = `host camera {
    hardware ethernet aa:bb:cc:dd:ee:07;
    fixed-address 192.168.8.17;
}`

	if err := Add(context.Background(), c, fragment, Modes{}); err != nil {
		t.Fatalf("Add error: %v", err)
	}
	got := s.Reservations()
	if len(got) != 1 || got[0].IP != "192.168.8.17" {
		t.Errorf("device state = %v, want the entry unchanged", got)
	}
}

// Moving an existing MAC to a different address is a conflict without --force,
// matching gofips: it means overwriting a binding the operator may not have meant
// to touch. With --force it updates in place rather than failing.
func TestRunAddMovingMACRequiresForce(t *testing.T) {
	s, c := mockClient(t, []types.Reservation{
		{Name: "camera", MAC: "aa:bb:cc:dd:ee:07", IP: "192.168.8.17"},
	})
	const fragment = `host camera {
    hardware ethernet aa:bb:cc:dd:ee:07;
    fixed-address 192.168.8.18;
}`

	if err := Add(context.Background(), c, fragment, Modes{}); err == nil {
		t.Fatal("Add moved an existing MAC without --force")
	}
	if got := s.Reservations(); got[0].IP != "192.168.8.17" {
		t.Errorf("a refused add changed the device: %v", got)
	}

	if err := Add(context.Background(), c, fragment, Modes{Force: true}); err != nil {
		t.Fatalf("Add with --force: %v", err)
	}
	got := s.Reservations()
	if len(got) != 1 || got[0].IP != "192.168.8.18" {
		t.Errorf("device state = %v, want the updated address", got)
	}
}

func TestRunAddRefusesConflict(t *testing.T) {
	s, c := mockClient(t, []types.Reservation{
		{Name: "nas", MAC: "aa:bb:cc:dd:ee:01", IP: "192.168.8.13"},
	})
	// Same address, different MAC.
	const fragment = `host other {
    hardware ethernet aa:bb:cc:dd:ee:99;
    fixed-address 192.168.8.13;
}`

	if err := Add(context.Background(), c, fragment, Modes{}); err == nil {
		t.Fatal("Add accepted a conflicting address")
	}
	if got := s.Reservations(); len(got) != 1 {
		t.Errorf("a refused add changed the device: %v", got)
	}
}

func TestRunAddForceOverridesConflict(t *testing.T) {
	s, c := mockClient(t, []types.Reservation{
		{Name: "nas", MAC: "aa:bb:cc:dd:ee:01", IP: "192.168.8.13"},
	})
	const fragment = `host other {
    hardware ethernet aa:bb:cc:dd:ee:99;
    fixed-address 192.168.8.13;
}`

	if err := Add(context.Background(), c, fragment, Modes{Force: true}); err != nil {
		t.Fatalf("Add with --force: %v", err)
	}
	if got := s.Reservations(); len(got) != 2 {
		t.Errorf("device holds %d reservations, want 2 with --force", len(got))
	}
}

func TestRunAddDryRunWritesNothing(t *testing.T) {
	s, c := mockClient(t, nil)
	const fragment = `host camera {
    hardware ethernet aa:bb:cc:dd:ee:07;
    fixed-address 192.168.8.17;
}`

	if err := Add(context.Background(), c, fragment, Modes{DryRun: true}); err != nil {
		t.Fatalf("Add error: %v", err)
	}
	if got := s.Reservations(); len(got) != 0 {
		t.Errorf("--dry-run wrote %d reservations, want 0", len(got))
	}
}

func TestRunAddRefusesOutOfSubnet(t *testing.T) {
	s, c := mockClient(t, nil)
	const fragment = `host camera {
    hardware ethernet aa:bb:cc:dd:ee:07;
    fixed-address 192.168.4.17;
}`

	if err := Add(context.Background(), c, fragment, Modes{}); err == nil {
		t.Fatal("Add accepted an out-of-subnet address")
	}
	if got := s.Reservations(); len(got) != 0 {
		t.Errorf("a refused add wrote %d reservations, want 0", len(got))
	}
}

func TestRunAddRejectsBadFragment(t *testing.T) {
	_, c := mockClient(t, nil)
	if err := Add(context.Background(), c, "not a host declaration", Modes{}); err == nil {
		t.Error("Add accepted a malformed fragment")
	}
}

// --force skips the interactive prompt, which is what makes --del scriptable.
func TestRunDelDeletes(t *testing.T) {
	s, c := mockClient(t, []types.Reservation{
		{Name: "nas", MAC: "aa:bb:cc:dd:ee:01", IP: "192.168.8.13"},
		{Name: "printer", MAC: "aa:bb:cc:dd:ee:02", IP: "192.168.8.14"},
	})

	if err := Del(context.Background(), c, Modes{Name: "nas", Force: true}); err != nil {
		t.Fatalf("Del error: %v", err)
	}
	got := s.Reservations()
	if len(got) != 1 || got[0].Name != "printer" {
		t.Errorf("device state = %v, want only printer", got)
	}
}

func TestRunDelByMAC(t *testing.T) {
	s, c := mockClient(t, []types.Reservation{
		{Name: "nas", MAC: "aa:bb:cc:dd:ee:01", IP: "192.168.8.13"},
	})

	if err := Del(context.Background(), c, Modes{MAC: "AA:BB:CC:DD:EE:01", Force: true}); err != nil {
		t.Fatalf("Del error: %v", err)
	}
	if got := s.Reservations(); len(got) != 0 {
		t.Errorf("device holds %d reservations, want 0", len(got))
	}
}

func TestRunDelByIP(t *testing.T) {
	s, c := mockClient(t, []types.Reservation{
		{Name: "nas", MAC: "aa:bb:cc:dd:ee:01", IP: "192.168.8.13"},
	})

	if err := Del(context.Background(), c, Modes{IP: "192.168.8.13", Force: true}); err != nil {
		t.Fatalf("Del error: %v", err)
	}
	if got := s.Reservations(); len(got) != 0 {
		t.Errorf("device holds %d reservations, want 0", len(got))
	}
}

func TestRunDelNotFound(t *testing.T) {
	s, c := mockClient(t, []types.Reservation{
		{Name: "nas", MAC: "aa:bb:cc:dd:ee:01", IP: "192.168.8.13"},
	})

	if err := Del(context.Background(), c, Modes{Name: "ghost", Force: true}); err == nil {
		t.Fatal("Del succeeded for a missing host")
	}
	if got := s.Reservations(); len(got) != 1 {
		t.Errorf("a failed delete changed the device: %v", got)
	}
}

func TestRunDelDryRunWritesNothing(t *testing.T) {
	s, c := mockClient(t, []types.Reservation{
		{Name: "nas", MAC: "aa:bb:cc:dd:ee:01", IP: "192.168.8.13"},
	})

	if err := Del(context.Background(), c, Modes{Name: "nas", DryRun: true}); err != nil {
		t.Fatalf("Del error: %v", err)
	}
	if got := s.Reservations(); len(got) != 1 {
		t.Errorf("--dry-run deleted a reservation")
	}
}

func TestRunDelRequiresIdentifier(t *testing.T) {
	_, c := mockClient(t, nil)
	if err := Del(context.Background(), c, Modes{Force: true}); err == nil {
		t.Error("Del succeeded with no identifier")
	}
}

// The export-edit-import round trip is the workflow the whole tool exists for.
func TestGetThenSetRoundTrip(t *testing.T) {
	s, c := mockClient(t, []types.Reservation{
		{Name: "nas", MAC: "aa:bb:cc:dd:ee:01", IP: "192.168.8.13"},
		{Name: "printer", MAC: "aa:bb:cc:dd:ee:02", IP: "192.168.8.14"},
	})

	// Export.
	exported := filepath.Join(t.TempDir(), "exported.hosts")
	f, err := os.Create(exported)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := Get(context.Background(), f, c.Reservations(), c.Network(), "2026-07-27"); err != nil {
		t.Fatalf("Get: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	// Re-import the untouched export: every entry should be skipped.
	if err := Set(context.Background(), c, exported, Modes{Prune: true}); err != nil {
		t.Fatalf("Set on our own export: %v", err)
	}

	got := s.Reservations()
	if len(got) != 2 {
		t.Fatalf("device holds %d reservations after a round trip, want 2", len(got))
	}
	byName := map[string]string{}
	for _, r := range got {
		byName[r.Name] = r.IP
	}
	if byName["nas"] != "192.168.8.13" || byName["printer"] != "192.168.8.14" {
		t.Errorf("round trip altered the device: %v", byName)
	}
}
