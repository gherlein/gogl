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

// The wireless guard is the only thing in gogl protecting against a write that
// cannot be undone remotely: change the SSID over WiFi and the session dies with no
// address to reconnect at. So these tests care less about the happy path than about
// what happens when the session is on a radio, or when the path cannot be
// determined at all.

// wirelessFor builds a service whose session appears to arrive over iface.
//
// The real service discovers this by routing a UDP socket to the router and finding
// the resulting local address in client.get_list. Both halves are substituted here:
// a test cannot control the host's routing table, and hardcoding a local IP would
// make the suite depend on the machine running it.
func wirelessFor(t *testing.T, iface string) (*mock.Server, services.WirelessService) {
	t.Helper()
	s := mock.NewServer(t, mock.Options{Password: "secret"})
	svc := services.NewWirelessServiceForTest(newTransport(t, s), func(context.Context) (string, error) {
		return "192.168.8.50", nil
	})
	s.SetClients([]types.Client{
		{Name: "laptop", MAC: "aa:bb:cc:dd:ee:01", IP: "192.168.8.50", Iface: iface, Online: true},
	})
	return s, svc
}

func TestWirelessRadiosAndInterfaces(t *testing.T) {
	_, svc := wirelessFor(t, "cable")
	ctx := context.Background()

	radios, err := svc.Radios(ctx)
	if err != nil {
		t.Fatalf("Radios: %v", err)
	}
	if len(radios) != 2 {
		t.Fatalf("got %d radios, want 2", len(radios))
	}

	ifaces, err := svc.Interfaces(ctx)
	if err != nil {
		t.Fatalf("Interfaces: %v", err)
	}
	if len(ifaces) != 4 {
		t.Fatalf("got %d interfaces, want 4", len(ifaces))
	}

	// The firmware reports the band on the radio, not the interface. If that is not
	// stamped down, a caller holding one interface cannot tell which radio it is on.
	for _, f := range ifaces {
		if f.Band != types.Band2G && f.Band != types.Band5G {
			t.Errorf("interface %q has band %q, want a real band", f.Name, f.Band)
		}
	}
}

func TestWirelessGetByName(t *testing.T) {
	_, svc := wirelessFor(t, "cable")

	got, err := svc.Get(context.Background(), "guest5g")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !got.Guest {
		t.Error("guest5g does not report as a guest interface")
	}
	if got.Band != types.Band5G {
		t.Errorf("band = %q, want 5G", got.Band)
	}
}

// A wrong interface name is the likeliest mistake, so the error has to list the
// real ones rather than only reporting the miss.
func TestWirelessGetUnknownNamesTheValidOnes(t *testing.T) {
	_, svc := wirelessFor(t, "cable")

	_, err := svc.Get(context.Background(), "wlan0")
	if !errors.Is(err, types.ErrNotFound) {
		t.Fatalf("error = %v, want ErrNotFound", err)
	}
	if !strings.Contains(err.Error(), "default_radio0") {
		t.Errorf("error does not list the available interfaces: %v", err)
	}
}

// ---------------------------------------------------------------------------
// The guard
// ---------------------------------------------------------------------------

func TestSetSSIDOverEthernet(t *testing.T) {
	s, svc := wirelessFor(t, "cable")

	if err := svc.SetSSID(context.Background(), "default_radio0", "player-test"); err != nil {
		t.Fatalf("SetSSID over ethernet: %v", err)
	}

	radios := s.Wireless()
	if got := radios[0].Ifaces[0].SSID; got != "player-test" {
		t.Errorf("SSID = %q, want player-test", got)
	}
	// A partial update must not disturb the rest of the interface.
	if got := radios[0].Ifaces[0].Key; got != mock.FactoryKey {
		t.Errorf("writing the SSID changed the passphrase to %q", got)
	}
	if !radios[0].Ifaces[0].Enabled {
		t.Error("writing the SSID disabled the interface")
	}
	// And must not touch the other interfaces.
	if got := radios[1].Ifaces[0].SSID; got != mock.FactorySSID {
		t.Errorf("the 5G SSID changed to %q", got)
	}
}

func TestSetSSIDRefusedOverWireless(t *testing.T) {
	for _, iface := range []string{"2.4G", "5G"} {
		t.Run(iface, func(t *testing.T) {
			s, svc := wirelessFor(t, iface)

			err := svc.SetSSID(context.Background(), "default_radio0", "player-test")
			if !errors.Is(err, types.ErrWirelessSession) {
				t.Fatalf("error = %v, want ErrWirelessSession", err)
			}
			// The remedy has to be in the message: the operator has to physically
			// do something about it.
			if !strings.Contains(err.Error(), "ethernet") {
				t.Errorf("error does not say how to proceed: %v", err)
			}
			if got := s.Wireless()[0].Ifaces[0].SSID; got != mock.FactorySSID {
				t.Errorf("a refused write changed the SSID to %q", got)
			}
		})
	}
}

// A session from off-LAN cannot be arriving over this router's radio, so the write
// is allowed. Refusing would block the legitimate case of managing the device
// through an upstream router.
func TestSetSSIDAllowedWhenSessionIsOffLAN(t *testing.T) {
	s := mock.NewServer(t, mock.Options{Password: "secret"})
	svc := services.NewWirelessServiceForTest(newTransport(t, s), func(context.Context) (string, error) {
		return "10.9.9.9", nil
	})
	s.SetClients([]types.Client{
		{Name: "other", MAC: "aa:bb:cc:dd:ee:02", IP: "192.168.8.51", Iface: "5G"},
	})

	if err := svc.SetSSID(context.Background(), "default_radio0", "player-test"); err != nil {
		t.Fatalf("SetSSID from off-LAN: %v", err)
	}
}

// Not knowing the path is not permission to proceed. The operator cannot recover
// remotely if this guess is wrong, so an unanswerable question is a refusal.
func TestSetSSIDRefusedWhenPathIsUnknown(t *testing.T) {
	s := mock.NewServer(t, mock.Options{Password: "secret"})
	svc := services.NewWirelessServiceForTest(newTransport(t, s), func(context.Context) (string, error) {
		return "", errors.New("no route to host")
	})

	err := svc.SetSSID(context.Background(), "default_radio0", "player-test")
	if !errors.Is(err, types.ErrWirelessSession) {
		t.Fatalf("error = %v, want ErrWirelessSession", err)
	}
	if got := s.Wireless()[0].Ifaces[0].SSID; got != mock.FactorySSID {
		t.Errorf("a refused write changed the SSID to %q", got)
	}
}

func TestSessionInterface(t *testing.T) {
	for _, want := range []string{"cable", "2.4G", "5G"} {
		_, svc := wirelessFor(t, want)
		got, err := svc.SessionInterface(context.Background())
		if err != nil {
			t.Fatalf("SessionInterface: %v", err)
		}
		if got != want {
			t.Errorf("SessionInterface = %q, want %q", got, want)
		}
	}
}

// ---------------------------------------------------------------------------
// Validation, before the guard and before the device
// ---------------------------------------------------------------------------

// A bad SSID is reported as a bad SSID even when the session would also have been
// refused, because fixing the wrong problem first wastes a trip to the hardware.
func TestSetSSIDValidatesBeforeCheckingTheSession(t *testing.T) {
	_, svc := wirelessFor(t, "5G")

	err := svc.SetSSID(context.Background(), "default_radio0", strings.Repeat("x", 33))
	if !errors.Is(err, types.ErrInvalidInput) {
		t.Errorf("error = %v, want ErrInvalidInput", err)
	}
}

// A typo in the interface name is reported as a typo, not as a session problem, for
// the same reason.
func TestSetSSIDReportsUnknownInterfaceBeforeTheSession(t *testing.T) {
	_, svc := wirelessFor(t, "5G")

	err := svc.SetSSID(context.Background(), "wlan0", "player-test")
	if !errors.Is(err, types.ErrNotFound) {
		t.Errorf("error = %v, want ErrNotFound", err)
	}
}

func TestValidateSSID(t *testing.T) {
	valid := []string{"a", "player-test", mock.FactorySSID, strings.Repeat("x", 32), "café wifi"}
	for _, ssid := range valid {
		if err := types.ValidateSSID(ssid); err != nil {
			t.Errorf("ValidateSSID(%q) = %v, want nil", ssid, err)
		}
	}

	invalid := map[string]string{
		"empty":            "",
		"too long":         strings.Repeat("x", 33),
		"leading space":    " player-test",
		"trailing space":   "player-test ",
		"control char":     "player\x07test",
		"embedded newline": "player\ntest",
	}
	for name, ssid := range invalid {
		t.Run(name, func(t *testing.T) {
			if err := types.ValidateSSID(ssid); err == nil {
				t.Errorf("ValidateSSID(%q) = nil, want an error", ssid)
			}
		})
	}
}

func TestValidateWirelessKey(t *testing.T) {
	if err := types.ValidateWirelessKey(mock.FactoryKey); err != nil {
		t.Errorf("an 8-character key was rejected: %v", err)
	}
	for _, key := range []string{"", "short", strings.Repeat("x", 64)} {
		if err := types.ValidateWirelessKey(key); err == nil {
			t.Errorf("ValidateWirelessKey(%q) = nil, want an error", key)
		}
	}
}

// The passphrase should not land in output that gets pasted into a bug report.
func TestMaskedKey(t *testing.T) {
	w := types.WirelessInterface{Key: mock.FactoryKey}
	masked := w.MaskedKey()
	if strings.Contains(masked, mock.FactoryKey) {
		t.Errorf("MaskedKey leaked the passphrase: %q", masked)
	}
	if !strings.Contains(masked, "8") {
		t.Errorf("MaskedKey does not report the length: %q", masked)
	}

	empty := types.WirelessInterface{}
	if got := empty.MaskedKey(); got != "(none)" {
		t.Errorf("MaskedKey with no key = %q, want (none)", got)
	}
}
