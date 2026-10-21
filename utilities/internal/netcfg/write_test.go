package netcfg

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"strconv"
	"strings"
	"testing"

	gogl "github.com/gherlein/gogl/src"
	"github.com/gherlein/gogl/src/mock"
	"github.com/gherlein/gogl/src/types"
)

// These drive SetNetwork through a real client against the mock router,
// rather than calling its helpers directly.
//
// That distinction is not academic here. An earlier version of goglps had a
// --dry-run that performed a live write, because Go's flag package stops parsing
// at the first operand. Every unit test passed: they constructed the flag struct
// directly and so could not observe the parse. Only exercising the real path
// caught it.

const writeLANFixture = `{
  "interfaces": [
    {
      "interface": "lan",
      "ip": "192.168.8.1",
      "netmask": "255.255.255.0",
      "enable": 1,
      "start": "192.168.8.100",
      "end": "192.168.8.249",
      "leasetime": "12h"
    }
  ]
}`

func mockClient(t *testing.T, seed []types.Reservation) (*mock.Server, *gogl.Client) {
	t.Helper()

	s := mock.NewServer(t, mock.Options{Password: "secret"})
	s.LoadFixture(mock.NetworkGroup, mock.MethodGetConfigList, json.RawMessage(writeLANFixture))
	s.SetReservations(seed)

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

func targetNetwork() *types.Network {
	return &types.Network{
		Interface: types.InterfaceLAN,
		LANIP:     "192.168.2.1",
		Netmask:   "255.255.255.0",
		DHCPStart: "192.168.2.100",
		DHCPStop:  "192.168.2.149",
	}
}

func TestRunSetNetworkWrites(t *testing.T) {
	s, c := mockClient(t, nil)

	if err := SetNetwork(context.Background(), c, targetNetwork(), NetworkModes{}); err != nil {
		t.Fatalf("SetNetwork: %v", err)
	}

	got := s.Network()
	if len(got) != 1 {
		t.Fatalf("device reports %d interfaces, want 1", len(got))
	}
	if got[0].LANIP != "192.168.2.1" || got[0].Netmask != "255.255.255.0" {
		t.Errorf("address = %s/%s, want 192.168.2.1/255.255.255.0", got[0].LANIP, got[0].Netmask)
	}
	if got[0].DHCPStart != "192.168.2.100" || got[0].DHCPStop != "192.168.2.149" {
		t.Errorf("pool = %s-%s, want 192.168.2.100-192.168.2.149", got[0].DHCPStart, got[0].DHCPStop)
	}
}

// The whole point of a dry run: it must reach the device to report what would
// change, and must not change it.
func TestRunSetNetworkDryRunDoesNotWrite(t *testing.T) {
	s, c := mockClient(t, nil)

	if err := SetNetwork(context.Background(), c, targetNetwork(), NetworkModes{DryRun: true}); err != nil {
		t.Fatalf("dry run: %v", err)
	}

	got := s.Network()
	if len(got) != 1 || got[0].LANIP != "192.168.8.1" {
		t.Fatalf("a dry run changed the address: %+v", got)
	}
	if got[0].DHCPStart != "192.168.8.100" || got[0].DHCPStop != "192.168.8.249" {
		t.Errorf("a dry run changed the pool: %+v", got[0])
	}
}

func TestRunSetNetworkRefusedWithReservations(t *testing.T) {
	s, c := mockClient(t, []types.Reservation{
		{Name: "nas", MAC: "aa:bb:cc:dd:ee:01", IP: "192.168.8.13"},
		{Name: "pi", MAC: "aa:bb:cc:dd:ee:02", IP: "192.168.8.14"},
	})

	err := SetNetwork(context.Background(), c, targetNetwork(), NetworkModes{})
	if !errors.Is(err, types.ErrReservationsExist) {
		t.Fatalf("error = %v, want ErrReservationsExist", err)
	}
	if got := s.Network(); got[0].LANIP != "192.168.8.1" {
		t.Errorf("a refused write moved the router to %s", got[0].LANIP)
	}
	// The operator needs to know the way out, not just that there is a wall.
	if !strings.Contains(err.Error(), "goglps --clear") {
		t.Errorf("error does not say how to proceed: %v", err)
	}
	if !strings.Contains(err.Error(), "2 reservation") {
		t.Errorf("error does not say how many are in the way: %v", err)
	}
}

// A dry run that approves what the real write would refuse is worse than none.
func TestRunSetNetworkDryRunReportsTheRefusal(t *testing.T) {
	_, c := mockClient(t, []types.Reservation{
		{Name: "nas", MAC: "aa:bb:cc:dd:ee:01", IP: "192.168.8.13"},
	})

	if err := SetNetwork(context.Background(), c, targetNetwork(), NetworkModes{DryRun: true}); !errors.Is(err, types.ErrReservationsExist) {
		t.Errorf("dry run error = %v, want ErrReservationsExist", err)
	}
}

// Likewise for validation: the firmware accepts a pool outside its subnet and
// then serves nothing, so the dry run has to be the thing that catches it.
func TestRunSetNetworkDryRunRejectsBadPool(t *testing.T) {
	_, c := mockClient(t, nil)

	n := targetNetwork()
	n.DHCPStart, n.DHCPStop = "10.0.0.100", "10.0.0.149"

	err := SetNetwork(context.Background(), c, n, NetworkModes{DryRun: true})
	if !errors.Is(err, types.ErrOutsideSubnet) {
		t.Errorf("error = %v, want ErrOutsideSubnet", err)
	}
}

// ---------------------------------------------------------------------------
// --force, pool-only writes, and the in-pool warning
// ---------------------------------------------------------------------------

func TestRunSetNetworkForcedProceedsWithReservations(t *testing.T) {
	s, c := mockClient(t, []types.Reservation{
		{Name: "nas", MAC: "aa:bb:cc:dd:ee:01", IP: "192.168.8.13"},
	})

	err := SetNetwork(context.Background(), c, targetNetwork(), NetworkModes{Force: true})
	if err != nil {
		t.Fatalf("--force was refused: %v", err)
	}
	if got := s.Network(); got[0].LANIP != "192.168.2.1" {
		t.Errorf("network = %s, want the new address", got[0].LANIP)
	}
}

// The refusal must name the escape hatch, or the operator's only visible option is the
// clear-and-reimport that is no longer necessary.
func TestRunSetNetworkRefusalMentionsForce(t *testing.T) {
	_, c := mockClient(t, []types.Reservation{
		{Name: "nas", MAC: "aa:bb:cc:dd:ee:01", IP: "192.168.8.13"},
	})

	err := SetNetwork(context.Background(), c, targetNetwork(), NetworkModes{})
	if !errors.Is(err, types.ErrReservationsExist) {
		t.Fatalf("error = %v, want ErrReservationsExist", err)
	}
	for _, want := range []string{"--force", "goglps --clear", "rewrite"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("refusal does not mention %q: %v", want, err)
		}
	}
}

// A pool-only change needs neither --set-ip nor --set-mask: the address is not moving,
// so it is read from the device. Requiring them was pure friction.
func TestRunSetNetworkPoolOnlyFillsAddressFromDevice(t *testing.T) {
	s, c := mockClient(t, []types.Reservation{
		{Name: "nas", MAC: "aa:bb:cc:dd:ee:01", IP: "192.168.8.13"},
	})

	// Only the pool bounds, exactly as the CLI passes them.
	n := &types.Network{
		Interface: types.InterfaceLAN,
		DHCPStart: "192.168.8.200",
		DHCPStop:  "192.168.8.240",
	}

	if err := SetNetwork(context.Background(), c, n, NetworkModes{}); err != nil {
		t.Fatalf("pool-only change: %v", err)
	}

	got := s.Network()
	if got[0].LANIP != "192.168.8.1" || got[0].Netmask != "255.255.255.0" {
		t.Errorf("the address moved: %s/%s", got[0].LANIP, got[0].Netmask)
	}
	if got[0].DHCPStart != "192.168.8.200" || got[0].DHCPStop != "192.168.8.240" {
		t.Errorf("pool not written: %+v", got[0])
	}
}

// A pool-only change with reservations present must not be refused, since nothing
// moves.
func TestRunSetNetworkPoolOnlyIsNotGuarded(t *testing.T) {
	_, c := mockClient(t, []types.Reservation{
		{Name: "a", MAC: "aa:bb:cc:dd:ee:01", IP: "192.168.8.13"},
		{Name: "b", MAC: "aa:bb:cc:dd:ee:02", IP: "192.168.8.14"},
	})

	n := &types.Network{
		Interface: types.InterfaceLAN,
		DHCPStart: "192.168.8.200",
		DHCPStop:  "192.168.8.240",
	}
	if err := SetNetwork(context.Background(), c, n, NetworkModes{}); err != nil {
		t.Errorf("a pool-only change was refused with reservations present: %v", err)
	}
}

func TestReservationsInPool(t *testing.T) {
	n := &types.Network{
		Interface: types.InterfaceLAN,
		LANIP:     "192.168.8.1",
		Netmask:   "255.255.255.0",
		// DHCPEnabled matters: a disabled server has no pool, so InDHCPPool reports
		// false for every address and nothing is ever "in the pool".
		DHCPEnabled: true,
		DHCPStart:   "192.168.8.100",
		DHCPStop:    "192.168.8.249",
	}
	reservations := []types.Reservation{
		{Name: "below", MAC: "aa:bb:cc:dd:ee:01", IP: "192.168.8.10"},
		{Name: "inside", MAC: "aa:bb:cc:dd:ee:02", IP: "192.168.8.208"},
		{Name: "also-inside", MAC: "aa:bb:cc:dd:ee:03", IP: "192.168.8.228"},
		{Name: "elsewhere", MAC: "aa:bb:cc:dd:ee:04", IP: "10.0.0.5"},
		{Name: "unparseable", MAC: "aa:bb:cc:dd:ee:05", IP: "nonsense"},
	}

	got := ReservationsInPool(n, reservations)
	if len(got) != 2 {
		t.Fatalf("got %d in-pool reservations, want 2: %+v", len(got), got)
	}
	for _, r := range got {
		if r.Name != "inside" && r.Name != "also-inside" {
			t.Errorf("%q is not inside the pool", r.Name)
		}
	}
}

// This is the condition that arose silently on real hardware: a renumber moved 20 of
// 27 reservations into the pool, and nothing said so.
func TestBuildReportFlagsInPoolReservations(t *testing.T) {
	network := testLAN()
	reservations := []types.Reservation{
		{Name: "below", MAC: "aa:bb:cc:dd:ee:01", IP: "192.168.8.10"},
		{Name: "inside", MAC: "aa:bb:cc:dd:ee:02", IP: "192.168.8.208"},
	}

	report, err := BuildReport(context.Background(),
		stubNetwork{n: network},
		stubSystem{i: &types.SystemInfo{Model: "sft1200", Firmware: "4.3.28"}},
		stubReservations{r: reservations})
	if err != nil {
		t.Fatalf("BuildReport: %v", err)
	}

	if len(report.InPool) != 1 || report.InPool[0].Name != "inside" {
		t.Errorf("InPool = %+v, want just the in-pool one", report.InPool)
	}
	// Both are still reserved; only one is inside the pool.
	if report.ReservedCount != 2 {
		t.Errorf("ReservedCount = %d, want 2", report.ReservedCount)
	}
}

// The text report has to explain the in-pool case, because it is the reason an
// AVAILABLE count looks wrong.
func TestFormatTextExplainsInPoolReservations(t *testing.T) {
	report, err := BuildReport(context.Background(),
		stubNetwork{n: testLAN()},
		stubSystem{i: &types.SystemInfo{Model: "sft1200", Firmware: "4.3.28"}},
		stubReservations{r: []types.Reservation{
			{Name: "inside", MAC: "aa:bb:cc:dd:ee:02", IP: "192.168.8.208"},
		}})
	if err != nil {
		t.Fatalf("BuildReport: %v", err)
	}

	var buf bytes.Buffer
	if err := FormatText(&buf, report); err != nil {
		t.Fatalf("FormatText: %v", err)
	}
	got := buf.String()

	for _, want := range []string{"IN POOL", "192.168.8.208", "inside", "dnsmasq"} {
		if !strings.Contains(got, want) {
			t.Errorf("output missing %q:\n%s", want, got)
		}
	}
}

// And must say nothing when there is nothing to say.
func TestFormatTextSilentWhenNoInPoolReservations(t *testing.T) {
	report, err := BuildReport(context.Background(),
		stubNetwork{n: testLAN()},
		stubSystem{i: &types.SystemInfo{Model: "sft1200", Firmware: "4.3.28"}},
		stubReservations{r: []types.Reservation{
			{Name: "below", MAC: "aa:bb:cc:dd:ee:01", IP: "192.168.8.10"},
		}})
	if err != nil {
		t.Fatalf("BuildReport: %v", err)
	}

	var buf bytes.Buffer
	if err := FormatText(&buf, report); err != nil {
		t.Fatalf("FormatText: %v", err)
	}
	if strings.Contains(buf.String(), "IN POOL") {
		t.Errorf("output mentions the pool with nothing in it:\n%s", buf.String())
	}
}

// A disabled DHCP server has no pool, so nothing can be inside it. Worth pinning: the
// first version of TestReservationsInPool omitted DHCPEnabled and failed, which is the
// code being right rather than the test.
func TestReservationsInPoolWithDHCPDisabled(t *testing.T) {
	n := &types.Network{
		Interface: types.InterfaceLAN,
		LANIP:     "192.168.8.1",
		Netmask:   "255.255.255.0",
		DHCPStart: "192.168.8.100",
		DHCPStop:  "192.168.8.249",
	}
	got := ReservationsInPool(n, []types.Reservation{
		{Name: "inside", MAC: "aa:bb:cc:dd:ee:02", IP: "192.168.8.208"},
	})
	if len(got) != 0 {
		t.Errorf("got %d in-pool reservations with DHCP disabled, want 0", len(got))
	}
}
