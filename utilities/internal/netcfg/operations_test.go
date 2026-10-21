package netcfg

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/gherlein/gogl/src/types"
)

// Stubs let BuildReport be tested without a client, which keeps this test fast
// and independent of the mock server.
type stubNetwork struct {
	n   *types.Network
	err error
}

func (s stubNetwork) Get(context.Context) (*types.Network, error) { return s.n, s.err }

type stubSystem struct {
	i   *types.SystemInfo
	err error
}

func (s stubSystem) Info(context.Context) (*types.SystemInfo, error) { return s.i, s.err }

type stubReservations struct {
	r   []types.Reservation
	err error
}

func (s stubReservations) List(context.Context) ([]types.Reservation, error) { return s.r, s.err }

func testLAN() *types.Network {
	return &types.Network{
		LANIP: "192.168.8.1", Netmask: "255.255.255.0",
		DHCPEnabled: true, DHCPStart: "192.168.8.100", DHCPStop: "192.168.8.249",
		DHCPLease: types.LeaseTime(12 * time.Hour), Interface: types.InterfaceLAN,
		DNS: []string{"192.168.8.1"},
	}
}

func TestBuildReport(t *testing.T) {
	reservations := []types.Reservation{
		{Name: "nas", MAC: "aa:bb:cc:dd:ee:01", IP: "192.168.8.13"},
		{Name: "printer", MAC: "aa:bb:cc:dd:ee:02", IP: "192.168.8.14"},
	}

	got, err := BuildReport(context.Background(),
		stubNetwork{n: testLAN()},
		stubSystem{i: &types.SystemInfo{Model: "gl-sft1200", Firmware: "4.3.28"}},
		stubReservations{r: reservations},
	)
	if err != nil {
		t.Fatalf("BuildReport error: %v", err)
	}

	if got.Subnet != "192.168.8.0/24" {
		t.Errorf("Subnet = %q, want 192.168.8.0/24", got.Subnet)
	}
	if got.PoolSize != 150 {
		t.Errorf("PoolSize = %d, want 150", got.PoolSize)
	}
	if got.ReservedCount != 2 {
		t.Errorf("ReservedCount = %d, want 2", got.ReservedCount)
	}
	if got.Model != "gl-sft1200" {
		t.Errorf("Model = %q, want gl-sft1200", got.Model)
	}
	// 254 usable, minus 150 pooled, minus 2 reserved, minus the router itself.
	if got.AvailableCount != 101 {
		t.Errorf("AvailableCount = %d, want 101", got.AvailableCount)
	}
}

// A reservation inside the pool must not be double-counted against available.
func TestBuildReportDoesNotDoubleCountPooledReservations(t *testing.T) {
	got, err := BuildReport(context.Background(),
		stubNetwork{n: testLAN()},
		stubSystem{i: &types.SystemInfo{}},
		stubReservations{r: []types.Reservation{
			{Name: "x", MAC: "aa:bb:cc:dd:ee:01", IP: "192.168.8.150"},
		}},
	)
	if err != nil {
		t.Fatalf("BuildReport error: %v", err)
	}
	// 254 - 150 pooled - 1 router = 103. The reservation is already inside the
	// pool, so subtracting it again would understate what is free.
	if got.AvailableCount != 103 {
		t.Errorf("AvailableCount = %d, want 103", got.AvailableCount)
	}
}

// A reservation outside the subnet is not counted against this subnet's
// availability.
func TestBuildReportIgnoresOutOfSubnetReservations(t *testing.T) {
	got, err := BuildReport(context.Background(),
		stubNetwork{n: testLAN()},
		stubSystem{i: &types.SystemInfo{}},
		stubReservations{r: []types.Reservation{
			{Name: "elsewhere", MAC: "aa:bb:cc:dd:ee:01", IP: "192.168.4.10"},
			{Name: "unparseable", MAC: "aa:bb:cc:dd:ee:02", IP: "garbage"},
		}},
	)
	if err != nil {
		t.Fatalf("BuildReport error: %v", err)
	}
	if got.AvailableCount != 103 {
		t.Errorf("AvailableCount = %d, want 103", got.AvailableCount)
	}
}

func TestBuildReportWithDHCPDisabled(t *testing.T) {
	network := testLAN()
	network.DHCPEnabled = false

	got, err := BuildReport(context.Background(),
		stubNetwork{n: network},
		stubSystem{i: &types.SystemInfo{}},
		stubReservations{},
	)
	if err != nil {
		t.Fatalf("BuildReport error: %v", err)
	}
	if got.PoolSize != 0 {
		t.Errorf("PoolSize = %d, want 0", got.PoolSize)
	}
	// 254 usable, minus the router itself.
	if got.AvailableCount != 253 {
		t.Errorf("AvailableCount = %d, want 253", got.AvailableCount)
	}
}

// A router that will not report its model is still worth reporting the network
// of, so a system-info failure must not fail the whole report.
func TestBuildReportToleratesSystemInfoFailure(t *testing.T) {
	got, err := BuildReport(context.Background(),
		stubNetwork{n: testLAN()},
		stubSystem{err: errors.New("no system info")},
		stubReservations{},
	)
	if err != nil {
		t.Fatalf("BuildReport error: %v", err)
	}
	if got.Model != "" {
		t.Errorf("Model = %q, want empty", got.Model)
	}
	if got.Subnet != "192.168.8.0/24" {
		t.Error("the rest of the report should still be populated")
	}
}

func TestBuildReportPropagatesNetworkFailure(t *testing.T) {
	_, err := BuildReport(context.Background(),
		stubNetwork{err: errors.New("boom")},
		stubSystem{i: &types.SystemInfo{}},
		stubReservations{},
	)
	if err == nil {
		t.Error("BuildReport succeeded despite a network read failure")
	}
}

func TestBuildReportPropagatesReservationFailure(t *testing.T) {
	_, err := BuildReport(context.Background(),
		stubNetwork{n: testLAN()},
		stubSystem{i: &types.SystemInfo{}},
		stubReservations{err: errors.New("boom")},
	)
	if err == nil {
		t.Error("BuildReport succeeded despite a reservation read failure")
	}
}

func TestCountAvailableWithUnusableSubnet(t *testing.T) {
	network := &types.Network{LANIP: "nope", Netmask: "255.255.255.0"}
	if got := countAvailable(network, nil); got != 0 {
		t.Errorf("countAvailable on an unusable subnet = %d, want 0", got)
	}
}
