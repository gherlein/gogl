package clients

import (
	"testing"

	"github.com/gherlein/gogl/src/types"
)

// Iface values as firmware 4.3.28 reports them: "cable", "2.4G" or "5G".
func testClients() []types.Client {
	return []types.Client{
		{MAC: "d4:9a:20:00:00:02", IP: "192.168.8.20", Name: "laptop", Online: true, Iface: types.Iface5GHz},
		{MAC: "00:1b:63:00:00:01", IP: "192.168.8.13", Name: "nas", Online: true, Iface: types.IfaceCable, RXBytes: 100, TXBytes: 200},
		{MAC: "02:aa:bb:cc:dd:ee", Online: true, Iface: types.Iface2GHz},
	}
}

func testDB() OUIDatabase {
	return OUIDatabase{"00:1b:63": "Apple, Inc.", "d4:9a:20": "Dell Inc."}
}

// Entries sort numerically by IP, with address-less clients last.
func TestBuildEntriesSortsByIP(t *testing.T) {
	got := BuildEntries(testClients(), nil, testDB(), FilterAll)
	if len(got) != 3 {
		t.Fatalf("built %d entries, want 3", len(got))
	}
	if got[0].IP != "192.168.8.13" {
		t.Errorf("first IP = %q, want 192.168.8.13", got[0].IP)
	}
	if got[1].IP != "192.168.8.20" {
		t.Errorf("second IP = %q, want 192.168.8.20", got[1].IP)
	}
	if got[2].IP != "" {
		t.Errorf("third entry should have no IP, got %q", got[2].IP)
	}
}

// Numeric ordering, not lexical: .9 must precede .10.
func TestBuildEntriesSortsNumerically(t *testing.T) {
	clients := []types.Client{
		{MAC: "aa:bb:cc:00:00:01", IP: "192.168.8.10", Iface: types.Iface2GHz},
		{MAC: "aa:bb:cc:00:00:02", IP: "192.168.8.9", Iface: types.Iface2GHz},
	}
	got := BuildEntries(clients, nil, OUIDatabase{}, FilterAll)
	if got[0].IP != "192.168.8.9" {
		t.Errorf("first IP = %q, want 192.168.8.9 (numeric order)", got[0].IP)
	}
}

func TestBuildEntriesLooksUpManufacturer(t *testing.T) {
	got := BuildEntries(testClients(), nil, testDB(), FilterAll)
	if got[0].Manufacturer != "Apple, Inc." {
		t.Errorf("Manufacturer = %q, want Apple, Inc.", got[0].Manufacturer)
	}
	if got[2].Manufacturer != randomizedManufacturer {
		t.Errorf("locally-administered MAC Manufacturer = %q, want %q", got[2].Manufacturer, randomizedManufacturer)
	}
}

func TestBuildEntriesFilters(t *testing.T) {
	wired := BuildEntries(testClients(), nil, testDB(), FilterWired)
	if len(wired) != 1 || wired[0].Name != "nas" {
		t.Errorf("FilterWired gave %v, want just nas", wired)
	}

	wifi := BuildEntries(testClients(), nil, testDB(), FilterWiFi)
	if len(wifi) != 2 {
		t.Errorf("FilterWiFi gave %d entries, want 2", len(wifi))
	}

	all := BuildEntries(testClients(), nil, testDB(), FilterAll)
	if len(all) != 3 {
		t.Errorf("FilterAll gave %d entries, want 3", len(all))
	}
}

func TestBuildEntriesMarksReserved(t *testing.T) {
	reservations := []types.Reservation{{Name: "nas", MAC: "00:1B:63:00:00:01", IP: "192.168.8.13"}}
	got := BuildEntries(testClients(), reservations, testDB(), FilterAll)

	// Matching must be case-insensitive, since the two sides may disagree on case.
	if !got[0].Reserved {
		t.Error("nas should be marked reserved")
	}
	if got[1].Reserved {
		t.Error("laptop should not be marked reserved")
	}
}

// A client with no reported name shows "unknown" rather than an empty column.
func TestBuildEntriesUnknownName(t *testing.T) {
	got := BuildEntries(testClients(), nil, testDB(), FilterAll)
	if got[2].Name != types.UnknownHostname {
		t.Errorf("Name = %q, want %q", got[2].Name, types.UnknownHostname)
	}
}

func TestBuildEntriesCarriesOptionalFields(t *testing.T) {
	got := BuildEntries(testClients(), nil, testDB(), FilterAll)

	// nas is wired with byte counters.
	if got[0].RXBytes != 100 || got[0].TXBytes != 200 {
		t.Errorf("byte counters lost: rx=%d tx=%d", got[0].RXBytes, got[0].TXBytes)
	}
	// laptop is WiFi, so it carries a band rather than being wired.
	if got[1].Band != "5G" {
		t.Errorf("Band = %q, want 5G", got[1].Band)
	}
	if got[1].IsWired {
		t.Error("the 5G client is marked wired")
	}
}

func TestBuildEntriesLowercasesMAC(t *testing.T) {
	clients := []types.Client{{MAC: "AA:BB:CC:DD:EE:FF", IP: "192.168.8.10", Iface: types.Iface2GHz}}
	got := BuildEntries(clients, nil, OUIDatabase{}, FilterAll)
	if got[0].MAC != "aa:bb:cc:dd:ee:ff" {
		t.Errorf("MAC = %q, want lowercase", got[0].MAC)
	}
}

func TestBuildEntriesEmpty(t *testing.T) {
	got := BuildEntries(nil, nil, OUIDatabase{}, FilterAll)
	if len(got) != 0 {
		t.Errorf("built %d entries from no clients", len(got))
	}
}

// Two address-less clients fall back to MAC ordering so output is deterministic.
func TestBuildEntriesOrdersAddresslessByMAC(t *testing.T) {
	clients := []types.Client{
		{MAC: "bb:00:00:00:00:01", Iface: types.Iface2GHz},
		{MAC: "aa:00:00:00:00:01", Iface: types.Iface2GHz},
	}
	got := BuildEntries(clients, nil, OUIDatabase{}, FilterAll)
	if got[0].MAC != "aa:00:00:00:00:01" {
		t.Errorf("first MAC = %q, want the lower one", got[0].MAC)
	}
}
