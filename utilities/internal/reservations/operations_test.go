package reservations

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/gherlein/gogl/src/types"
)

type stubReservations struct {
	list []types.Reservation
	err  error
}

func (s *stubReservations) List(context.Context) ([]types.Reservation, error) {
	return s.list, s.err
}

type stubNetwork struct {
	n   *types.Network
	err error
}

func (s stubNetwork) Get(context.Context) (*types.Network, error) { return s.n, s.err }

func testLAN() *types.Network {
	return &types.Network{
		LANIP: "192.168.8.1", Netmask: "255.255.255.0",
		DHCPEnabled: true, DHCPStart: "192.168.8.100", DHCPStop: "192.168.8.249",
		Interface: types.InterfaceLAN,
	}
}

func errsText(errs []error) string {
	parts := make([]string, len(errs))
	for i, err := range errs {
		parts[i] = err.Error()
	}
	return strings.Join(parts, "\n")
}

func TestRunGet(t *testing.T) {
	res := &stubReservations{list: []types.Reservation{
		{Name: "nas", MAC: "aa:bb:cc:dd:ee:01", IP: "192.168.8.13"},
	}}

	var buf bytes.Buffer
	if err := Get(context.Background(), &buf, res, stubNetwork{n: testLAN()}, "2026-07-27"); err != nil {
		t.Fatalf("Get error: %v", err)
	}

	out := buf.String()
	if !strings.Contains(out, "host nas") {
		t.Errorf("output missing the reservation:\n%s", out)
	}
	// Output must re-parse: that is the whole point of the format.
	parsed, errs := ParseHosts(strings.NewReader(out))
	if len(errs) != 0 || len(parsed) != 1 {
		t.Errorf("output does not round trip: %d declarations, %v", len(parsed), errs)
	}
}

// A router that will not report its network is still worth exporting from.
func TestRunGetToleratesNetworkFailure(t *testing.T) {
	res := &stubReservations{list: []types.Reservation{
		{Name: "nas", MAC: "aa:bb:cc:dd:ee:01", IP: "192.168.8.13"},
	}}

	var buf bytes.Buffer
	if err := Get(context.Background(), &buf, res, stubNetwork{err: errors.New("nope")}, "2026-07-27"); err != nil {
		t.Fatalf("Get error: %v", err)
	}
	if !strings.Contains(buf.String(), "host nas") {
		t.Errorf("declarations missing:\n%s", buf.String())
	}
}

func TestRunGetPropagatesReservationFailure(t *testing.T) {
	res := &stubReservations{err: errors.New("boom")}
	var buf bytes.Buffer
	if err := Get(context.Background(), &buf, res, stubNetwork{n: testLAN()}, "2026-07-27"); err == nil {
		t.Error("Get succeeded despite a reservation read failure")
	}
}

func TestPlanChanges(t *testing.T) {
	device := []types.Reservation{
		{Name: "nas", MAC: "aa:bb:cc:dd:ee:01", IP: "192.168.8.13"},
		{Name: "printer", MAC: "aa:bb:cc:dd:ee:02", IP: "192.168.8.99"},
		{Name: "gone", MAC: "aa:bb:cc:dd:ee:03", IP: "192.168.8.30"},
	}
	file := []types.Reservation{
		{Name: "nas", MAC: "aa:bb:cc:dd:ee:01", IP: "192.168.8.13"},     // unchanged
		{Name: "printer", MAC: "aa:bb:cc:dd:ee:02", IP: "192.168.8.14"}, // IP changed
		{Name: "camera", MAC: "aa:bb:cc:dd:ee:04", IP: "192.168.8.15"},  // new
	}

	plan := planChanges(file, device)

	if len(plan.Skip) != 1 || plan.Skip[0].MAC != "aa:bb:cc:dd:ee:01" {
		t.Errorf("Skip = %v, want just nas", plan.Skip)
	}
	if len(plan.Update) != 1 || plan.Update[0].IP != "192.168.8.14" {
		t.Errorf("Update = %v, want printer at .14", plan.Update)
	}
	if len(plan.Create) != 1 || plan.Create[0].Name != "camera" {
		t.Errorf("Create = %v, want camera", plan.Create)
	}
	if len(plan.Prune) != 1 || plan.Prune[0].Name != "gone" {
		t.Errorf("Prune = %v, want gone", plan.Prune)
	}
}

// A name change with the same address is still an update.
func TestPlanChangesDetectsRename(t *testing.T) {
	device := []types.Reservation{{Name: "old", MAC: "aa:bb:cc:dd:ee:01", IP: "192.168.8.13"}}
	file := []types.Reservation{{Name: "new", MAC: "aa:bb:cc:dd:ee:01", IP: "192.168.8.13"}}

	plan := planChanges(file, device)
	if len(plan.Update) != 1 || plan.Update[0].Name != "new" {
		t.Errorf("Update = %v, want a rename to new", plan.Update)
	}
	if len(plan.Skip) != 0 {
		t.Errorf("Skip = %v, want empty", plan.Skip)
	}
}

// MAC matching is case-insensitive, so a case difference is not mistaken for a
// new entry.
func TestPlanChangesIsCaseInsensitiveOnMAC(t *testing.T) {
	device := []types.Reservation{{Name: "nas", MAC: "AA:BB:CC:DD:EE:01", IP: "192.168.8.13"}}
	file := []types.Reservation{{Name: "nas", MAC: "aa:bb:cc:dd:ee:01", IP: "192.168.8.13"}}

	plan := planChanges(file, device)
	if len(plan.Skip) != 1 {
		t.Errorf("Skip = %v, want one; case should not create a false diff", plan.Skip)
	}
	if len(plan.Create) != 0 || len(plan.Prune) != 0 {
		t.Errorf("case difference produced spurious changes: %+v", plan)
	}
}

// Running twice must be a no-op the second time; idempotence is what makes a host
// file usable as a checked-in description of a network.
func TestPlanChangesIsIdempotent(t *testing.T) {
	file := []types.Reservation{{Name: "nas", MAC: "aa:bb:cc:dd:ee:01", IP: "192.168.8.13"}}
	plan := planChanges(file, file)

	if len(plan.Create) != 0 || len(plan.Update) != 0 || len(plan.Prune) != 0 {
		t.Errorf("applying a file to itself is not a no-op: %+v", plan)
	}
	if len(plan.Skip) != 1 {
		t.Errorf("Skip = %v, want one entry", plan.Skip)
	}
}

func TestPlanChangesEmptyDevice(t *testing.T) {
	file := []types.Reservation{{Name: "nas", MAC: "aa:bb:cc:dd:ee:01", IP: "192.168.8.13"}}
	plan := planChanges(file, nil)
	if len(plan.Create) != 1 {
		t.Errorf("Create = %v, want one entry against an empty device", plan.Create)
	}
}

func TestPlanChangesEmptyFile(t *testing.T) {
	device := []types.Reservation{{Name: "nas", MAC: "aa:bb:cc:dd:ee:01", IP: "192.168.8.13"}}
	plan := planChanges(nil, device)
	if len(plan.Prune) != 1 {
		t.Errorf("Prune = %v, want one entry for an empty file", plan.Prune)
	}
}

func TestValidateAgainstDeviceAccepts(t *testing.T) {
	file := []types.Reservation{{Name: "a", MAC: "aa:bb:cc:dd:ee:01", IP: "192.168.8.10"}}
	warnings, errs := validateAgainstDevice(file, testLAN())
	if len(errs) != 0 {
		t.Errorf("errors: %v", errs)
	}
	if len(warnings) != 0 {
		t.Errorf("warnings: %v", warnings)
	}
}

func TestValidateAgainstDeviceRejectsOutsideSubnet(t *testing.T) {
	file := []types.Reservation{
		{Name: "a", MAC: "aa:bb:cc:dd:ee:01", IP: "192.168.4.10"},
		{Name: "b", MAC: "aa:bb:cc:dd:ee:02", IP: "192.168.4.11"},
	}
	_, errs := validateAgainstDevice(file, testLAN())
	if len(errs) == 0 {
		t.Fatal("accepted addresses outside the LAN subnet")
	}
	joined := errsText(errs)
	if !strings.Contains(joined, "192.168.8.0/24") || !strings.Contains(joined, "2 of 2") {
		t.Errorf("subnet mismatch report is unhelpful:\n%s", joined)
	}
	// Both remedies must be named, because both are the operator's to choose.
	if !strings.Contains(joined, "admin panel") || !strings.Contains(strings.ToLower(joined), "renumber") {
		t.Errorf("report does not name both remedies:\n%s", joined)
	}
	// And it must suggest the right LAN address to move to.
	if !strings.Contains(joined, "192.168.4.1/24") {
		t.Errorf("report does not suggest the file's own gateway:\n%s", joined)
	}
}

// An address inside the dynamic pool warns but never blocks: dnsmasq honors a
// static lease inside the range and excludes it from allocation.
func TestValidateAgainstDeviceWarnsOnPooledAddress(t *testing.T) {
	file := []types.Reservation{{Name: "a", MAC: "aa:bb:cc:dd:ee:01", IP: "192.168.8.150"}}
	warnings, errs := validateAgainstDevice(file, testLAN())
	if len(errs) != 0 {
		t.Fatalf("pooled address must not be an error: %v", errs)
	}
	if len(warnings) != 1 || !strings.Contains(warnings[0], "192.168.8.150") {
		t.Errorf("warnings = %v, want one naming the address", warnings)
	}
}

func TestValidateAgainstDeviceRejectsRouterAddress(t *testing.T) {
	file := []types.Reservation{{Name: "a", MAC: "aa:bb:cc:dd:ee:01", IP: "192.168.8.1"}}
	_, errs := validateAgainstDevice(file, testLAN())
	if len(errs) == 0 {
		t.Fatal("accepted the router's own address as a reservation")
	}
	if !strings.Contains(errsText(errs), "router's own address") {
		t.Errorf("error does not explain the problem: %v", errs)
	}
}

func TestValidateAgainstDeviceRejectsBadAddress(t *testing.T) {
	file := []types.Reservation{{Name: "a", MAC: "aa:bb:cc:dd:ee:01", IP: "garbage"}}
	_, errs := validateAgainstDevice(file, testLAN())
	if len(errs) == 0 {
		t.Error("accepted an unparseable address")
	}
}

func TestValidateAgainstDeviceRejectsUnusableLAN(t *testing.T) {
	file := []types.Reservation{{Name: "a", MAC: "aa:bb:cc:dd:ee:01", IP: "192.168.8.10"}}
	_, errs := validateAgainstDevice(file, &types.Network{LANIP: "nope", Netmask: "255.255.255.0"})
	if len(errs) == 0 {
		t.Error("accepted an unusable router LAN configuration")
	}
}

func TestFindDuplicates(t *testing.T) {
	declarations := []Declaration{
		{Reservation: types.Reservation{Name: "a", MAC: "aa:bb:cc:dd:ee:01", IP: "192.168.8.10"}, Line: 1},
		{Reservation: types.Reservation{Name: "a", MAC: "aa:bb:cc:dd:ee:02", IP: "192.168.8.11"}, Line: 5},
		{Reservation: types.Reservation{Name: "c", MAC: "aa:bb:cc:dd:ee:01", IP: "192.168.8.12"}, Line: 9},
		{Reservation: types.Reservation{Name: "d", MAC: "aa:bb:cc:dd:ee:04", IP: "192.168.8.10"}, Line: 13},
	}
	errs := findDuplicates(declarations)
	if len(errs) != 3 {
		t.Fatalf("got %d duplicate errors, want 3 (name, MAC, IP): %v", len(errs), errs)
	}
	joined := errsText(errs)
	for _, want := range []string{"line 5", "line 9", "line 13"} {
		if !strings.Contains(joined, want) {
			t.Errorf("duplicate report missing %q:\n%s", want, joined)
		}
	}
}

func TestFindDuplicatesClean(t *testing.T) {
	declarations := []Declaration{
		{Reservation: types.Reservation{Name: "a", MAC: "aa:bb:cc:dd:ee:01", IP: "192.168.8.10"}, Line: 1},
		{Reservation: types.Reservation{Name: "b", MAC: "aa:bb:cc:dd:ee:02", IP: "192.168.8.11"}, Line: 5},
	}
	if errs := findDuplicates(declarations); len(errs) != 0 {
		t.Errorf("clean input produced errors: %v", errs)
	}
}

// Duplicate MACs differing only in case must still be caught.
func TestFindDuplicatesCaseInsensitiveMAC(t *testing.T) {
	declarations := []Declaration{
		{Reservation: types.Reservation{Name: "a", MAC: "aa:bb:cc:dd:ee:01", IP: "192.168.8.10"}, Line: 1},
		{Reservation: types.Reservation{Name: "b", MAC: "AA:BB:CC:DD:EE:01", IP: "192.168.8.11"}, Line: 5},
	}
	if errs := findDuplicates(declarations); len(errs) != 1 {
		t.Errorf("got %d errors, want 1 for a case-differing duplicate MAC: %v", len(errs), errs)
	}
}

func TestReservationsOf(t *testing.T) {
	declarations := []Declaration{
		{Reservation: types.Reservation{Name: "a"}, Line: 1},
		{Reservation: types.Reservation{Name: "b"}, Line: 5},
	}
	got := reservationsOf(declarations)
	if len(got) != 2 || got[0].Name != "a" || got[1].Name != "b" {
		t.Errorf("reservationsOf = %v", got)
	}
}
