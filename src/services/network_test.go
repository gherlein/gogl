package services_test

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"testing"
	"time"

	"github.com/gherlein/gogl/src/mock"
	"github.com/gherlein/gogl/src/services"
	"github.com/gherlein/gogl/src/types"
)

// Captured verbatim from lan.get_config_list on a GL-SFT1200 running firmware
// 4.3.28. Note "enable" is a number, not a boolean, and both the LAN and the guest
// interface come back in one call.
const networkFixture = `{
  "interfaces": [
    {
      "dns": [],
      "netmask": "255.255.255.0",
      "ip": "192.168.8.1",
      "lpr": [],
      "leasetime": "12h",
      "end": "192.168.8.249",
      "start": "192.168.8.100",
      "gateway": "",
      "enable": 1,
      "interface": "lan"
    },
    {
      "dns": [],
      "netmask": "255.255.255.0",
      "ip": "192.168.9.1",
      "lpr": [],
      "leasetime": "12h",
      "end": "192.168.9.249",
      "start": "192.168.9.100",
      "gateway": "",
      "enable": 1,
      "interface": "guest"
    }
  ]
}`

func networkServer(t *testing.T) *mock.Server {
	t.Helper()
	s := mock.NewServer(t, mock.Options{Password: "secret"})
	s.LoadFixture(mock.NetworkGroup, mock.MethodGetConfigList, json.RawMessage(networkFixture))
	return s
}

// Get must return the main LAN, not whichever interface happens to come first.
func TestNetworkGet(t *testing.T) {
	s := networkServer(t)

	got, err := services.NewNetworkService(newTransport(t, s)).Get(context.Background())
	if err != nil {
		t.Fatalf("Get error: %v", err)
	}

	if got.Interface != types.InterfaceLAN {
		t.Errorf("Interface = %q, want %q", got.Interface, types.InterfaceLAN)
	}
	if got.LANIP != "192.168.8.1" {
		t.Errorf("LANIP = %q, want 192.168.8.1", got.LANIP)
	}
	if !got.DHCPEnabled {
		t.Error("DHCPEnabled = false; the firmware sent enable:1")
	}
	if got.DHCPLease != types.LeaseTime(12*time.Hour) {
		t.Errorf("DHCPLease = %v, want 12h", time.Duration(got.DHCPLease))
	}
	if got.IsGuest() {
		t.Error("the lan interface reports itself as guest")
	}
}

// The guest network is reported for address planning even though gogl never
// writes it.
func TestNetworkList(t *testing.T) {
	s := networkServer(t)

	got, err := services.NewNetworkService(newTransport(t, s)).List(context.Background())
	if err != nil {
		t.Fatalf("List error: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("List returned %d interfaces, want 2", len(got))
	}

	var guest *types.Network
	for i := range got {
		if got[i].IsGuest() {
			guest = &got[i]
		}
	}
	if guest == nil {
		t.Fatal("no guest interface reported")
	}
	if guest.LANIP != "192.168.9.1" {
		t.Errorf("guest LANIP = %q, want 192.168.9.1", guest.LANIP)
	}
}

// A router reporting no lan interface is an error rather than a zero value that
// would silently validate every reservation against 0.0.0.0/0.
func TestNetworkGetWithoutLAN(t *testing.T) {
	s := mock.NewServer(t, mock.Options{Password: "secret"})
	s.LoadFixture(mock.NetworkGroup, mock.MethodGetConfigList,
		json.RawMessage(`{"interfaces":[{"interface":"guest","ip":"192.168.9.1","netmask":"255.255.255.0","enable":1}]}`))

	_, err := services.NewNetworkService(newTransport(t, s)).Get(context.Background())
	if !errors.Is(err, types.ErrNotFound) {
		t.Errorf("error = %v, want ErrNotFound", err)
	}
}

// The service must return a Network whose arithmetic works, since goglps relies on
// Contains to reject out-of-subnet reservations.
func TestNetworkGetSubnetArithmetic(t *testing.T) {
	s := networkServer(t)

	got, err := services.NewNetworkService(newTransport(t, s)).Get(context.Background())
	if err != nil {
		t.Fatalf("Get error: %v", err)
	}

	inside, err := got.Contains(net.ParseIP("192.168.8.10"))
	if err != nil || !inside {
		t.Errorf("Contains(192.168.8.10) = %v, %v; want true, nil", inside, err)
	}
	outside, err := got.Contains(net.ParseIP("192.168.4.10"))
	if err != nil || outside {
		t.Errorf("Contains(192.168.4.10) = %v, %v; want false, nil", outside, err)
	}
	pooled, err := got.InDHCPPool(net.ParseIP("192.168.8.150"))
	if err != nil || !pooled {
		t.Errorf("InDHCPPool(192.168.8.150) = %v, %v; want true, nil", pooled, err)
	}
	if got.PoolSize() != 150 {
		t.Errorf("PoolSize() = %d, want 150", got.PoolSize())
	}
}

func TestNetworkGetPropagatesError(t *testing.T) {
	s := networkServer(t)
	s.FailNext(mock.NetworkGroup, mock.MethodGetConfigList, mock.CodeNotFound, "injected")

	if _, err := services.NewNetworkService(newTransport(t, s)).Get(context.Background()); err == nil {
		t.Error("Get succeeded, want error")
	}
}

// Captured verbatim from network.get_dhcp_leases. The published documentation
// calls this field "entries"; the device sends "leases".
const leaseFixture = `{
  "leases": [
    {"mac":"B4:0E:CF:2A:85:6F","expires":41984,"hostname":"Bouffalolab_bl606p-2a856f","ip":"192.168.8.185"},
    {"mac":"08:26:AE:35:D7:A6","expires":41979,"hostname":"helios","ip":"192.168.8.241"}
  ]
}`

func TestNetworkLeases(t *testing.T) {
	s := mock.NewServer(t, mock.Options{Password: "secret"})
	s.LoadFixture(mock.DHCPLeaseGroup, mock.MethodGetDHCPLeases, json.RawMessage(leaseFixture))

	got, err := services.NewNetworkService(newTransport(t, s)).Leases(context.Background())
	if err != nil {
		t.Fatalf("Leases error: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("Leases returned %d, want 2", len(got))
	}
	if got[1].Hostname != "helios" {
		t.Errorf("second lease hostname = %q, want helios", got[1].Hostname)
	}
	if got[1].IP != "192.168.8.241" {
		t.Errorf("second lease IP = %q", got[1].IP)
	}
	if got[0].Expires != 41984 {
		t.Errorf("first lease Expires = %d, want 41984", got[0].Expires)
	}
}

func TestNetworkLeasesPropagatesError(t *testing.T) {
	s := mock.NewServer(t, mock.Options{Password: "secret"})
	s.LoadFixture(mock.DHCPLeaseGroup, mock.MethodGetDHCPLeases, json.RawMessage(`{}`))
	s.FailNext(mock.DHCPLeaseGroup, mock.MethodGetDHCPLeases, mock.CodeNotFound, "injected")

	if _, err := services.NewNetworkService(newTransport(t, s)).Leases(context.Background()); err == nil {
		t.Error("Leases succeeded, want error")
	}
}
