package reservations

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/gherlein/gogl/src/types"
)

func formatFixture() ([]types.Reservation, Header) {
	return []types.Reservation{
			{Name: "printer", MAC: "aa:bb:cc:dd:ee:02", IP: "192.168.8.11"},
			{Name: "myserver", MAC: "aa:bb:cc:dd:ee:01", IP: "192.168.8.10"},
		}, Header{
			Host: "192.168.8.1",
			Date: "2026-07-27",
			Network: &types.Network{
				LANIP: "192.168.8.1", Netmask: "255.255.255.0",
				DHCPEnabled: true, DHCPStart: "192.168.8.100", DHCPStop: "192.168.8.249",
				DHCPLease: types.LeaseTime(12 * time.Hour), Interface: types.InterfaceLAN,
			},
		}
}

func TestFormatHostsSortsByIP(t *testing.T) {
	res, header := formatFixture()
	var buf bytes.Buffer
	if err := FormatHosts(&buf, res, header); err != nil {
		t.Fatalf("FormatHosts error: %v", err)
	}

	out := buf.String()
	serverAt := strings.Index(out, "host myserver")
	printerAt := strings.Index(out, "host printer")
	if serverAt < 0 || printerAt < 0 {
		t.Fatalf("output missing a host block:\n%s", out)
	}
	// .10 must precede .11: numeric order, not the input order.
	if serverAt > printerAt {
		t.Errorf("output is not sorted by IP:\n%s", out)
	}
}

// Numeric, not lexical: .9 precedes .10.
func TestFormatHostsSortsNumerically(t *testing.T) {
	res := []types.Reservation{
		{Name: "ten", MAC: "aa:bb:cc:dd:ee:01", IP: "192.168.8.10"},
		{Name: "nine", MAC: "aa:bb:cc:dd:ee:02", IP: "192.168.8.9"},
	}
	var buf bytes.Buffer
	if err := FormatHosts(&buf, res, Header{}); err != nil {
		t.Fatalf("FormatHosts error: %v", err)
	}
	if strings.Index(buf.String(), "host nine") > strings.Index(buf.String(), "host ten") {
		t.Errorf("not sorted numerically:\n%s", buf.String())
	}
}

// FormatHosts must not reorder the caller's slice.
func TestFormatHostsDoesNotMutateInput(t *testing.T) {
	res, header := formatFixture()
	first := res[0].Name

	var buf bytes.Buffer
	if err := FormatHosts(&buf, res, header); err != nil {
		t.Fatalf("FormatHosts error: %v", err)
	}
	if res[0].Name != first {
		t.Errorf("input slice was reordered: first entry is now %q, was %q", res[0].Name, first)
	}
}

func TestFormatHostsUsesFourSpaceIndent(t *testing.T) {
	res, header := formatFixture()
	var buf bytes.Buffer
	if err := FormatHosts(&buf, res, header); err != nil {
		t.Fatalf("FormatHosts error: %v", err)
	}
	if !strings.Contains(buf.String(), "\n    hardware ethernet ") {
		t.Errorf("hardware ethernet is not indented four spaces:\n%s", buf.String())
	}
}

func TestFormatHostsHeader(t *testing.T) {
	res, header := formatFixture()
	var buf bytes.Buffer
	if err := FormatHosts(&buf, res, header); err != nil {
		t.Fatalf("FormatHosts error: %v", err)
	}

	out := buf.String()
	for _, want := range []string{
		"# goglps reservations", "192.168.8.1", "192.168.8.0/24",
		"192.168.8.100-192.168.8.249", "12h", "lan", "2026-07-27",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("header missing %q:\n%s", want, out)
		}
	}
}

func TestFormatHostsHeaderWithDisabledDHCP(t *testing.T) {
	res, header := formatFixture()
	header.Network.DHCPEnabled = false

	var buf bytes.Buffer
	if err := FormatHosts(&buf, res, header); err != nil {
		t.Fatalf("FormatHosts error: %v", err)
	}
	if !strings.Contains(buf.String(), "(disabled)") {
		t.Errorf("header does not mark DHCP disabled:\n%s", buf.String())
	}
}

func TestFormatHostsWithoutNetwork(t *testing.T) {
	res, _ := formatFixture()
	var buf bytes.Buffer
	if err := FormatHosts(&buf, res, Header{Host: "192.168.8.1", Date: "2026-07-27"}); err != nil {
		t.Fatalf("FormatHosts error: %v", err)
	}
	if !strings.Contains(buf.String(), "host myserver") {
		t.Errorf("declarations missing when no network is supplied:\n%s", buf.String())
	}
}

// Round-tripping is the property that makes the file a usable contract.
func TestFormatHostsRoundTrips(t *testing.T) {
	res, header := formatFixture()
	var buf bytes.Buffer
	if err := FormatHosts(&buf, res, header); err != nil {
		t.Fatalf("FormatHosts error: %v", err)
	}

	got, errs := ParseHosts(bytes.NewReader(buf.Bytes()))
	if len(errs) != 0 {
		t.Fatalf("re-parsing our own output failed: %v", errs)
	}
	if len(got) != 2 {
		t.Fatalf("re-parsed %d declarations, want 2", len(got))
	}
	if got[0].Reservation.Name != "myserver" || got[0].Reservation.IP != "192.168.8.10" {
		t.Errorf("round trip changed the first entry: %+v", got[0].Reservation)
	}
}

// A nameless reservation is emitted with a MAC-derived hostname plus a comment,
// because the format is keyed by hostname and cannot represent the absence of one.
func TestFormatHostsNamelessReservation(t *testing.T) {
	res := []types.Reservation{{MAC: "aa:bb:cc:dd:ee:09", IP: "192.168.8.30"}}
	var buf bytes.Buffer
	if err := FormatHosts(&buf, res, Header{Host: "192.168.8.1", Date: "2026-07-27"}); err != nil {
		t.Fatalf("FormatHosts error: %v", err)
	}

	out := buf.String()
	if !strings.Contains(out, "host aa-bb-cc-dd-ee-09") {
		t.Errorf("nameless reservation did not get a MAC-derived hostname:\n%s", out)
	}
	// The comment must warn that importing this file assigns the label for real.
	if !strings.Contains(strings.ToLower(out), "no label") {
		t.Errorf("nameless reservation is not annotated:\n%s", out)
	}
	// And the derived name must itself be valid, or the file would not re-import.
	parsed, errs := ParseHosts(strings.NewReader(out))
	if len(errs) != 0 || len(parsed) != 1 {
		t.Errorf("nameless entry does not round trip: %d declarations, %v", len(parsed), errs)
	}
}

// An empty device emits a commented example rather than nothing, so the expected
// format is discoverable.
func TestFormatHostsEmpty(t *testing.T) {
	var buf bytes.Buffer
	if err := FormatHosts(&buf, nil, Header{Host: "192.168.8.1", Date: "2026-07-27"}); err != nil {
		t.Fatalf("FormatHosts error: %v", err)
	}

	out := buf.String()
	if !strings.Contains(out, "# host example") {
		t.Errorf("empty output does not show a commented example:\n%s", out)
	}
	// The example must be entirely commented, so the file re-imports as empty.
	parsed, errs := ParseHosts(strings.NewReader(out))
	if len(errs) != 0 || len(parsed) != 0 {
		t.Errorf("empty output does not re-parse as empty: %d declarations, %v", len(parsed), errs)
	}
}

// The commented example should use the router's own subnet, not a misleading
// default from somewhere else.
func TestFormatHostsEmptyExampleUsesSubnet(t *testing.T) {
	header := Header{Network: &types.Network{LANIP: "10.20.30.1", Netmask: "255.255.255.0"}}
	var buf bytes.Buffer
	if err := FormatHosts(&buf, nil, header); err != nil {
		t.Fatalf("FormatHosts error: %v", err)
	}
	if !strings.Contains(buf.String(), "10.20.30.10") {
		t.Errorf("example address does not use the router's subnet:\n%s", buf.String())
	}
}
