package netcfg

import (
	"bytes"
	"context"
	"errors"
	"net/url"
	"strconv"
	"strings"
	"testing"

	gogl "github.com/gherlein/gogl/src"
	"github.com/gherlein/gogl/src/mock"
	"github.com/gherlein/gogl/src/types"
)

// wirelessClient builds a client whose session appears to the router as arriving
// over iface.
//
// gogl decides whether an SSID write is safe by finding its own local address in the
// router's client list, so seeding that list is how a test says "the caller is on
// ethernet" or "the caller is on 5G". The local address is whatever the host really
// uses to reach the mock's loopback listener, so it is discovered rather than
// assumed.
func wirelessClient(t *testing.T, iface string) (*mock.Server, *gogl.Client) {
	t.Helper()

	s := mock.NewServer(t, mock.Options{Password: "secret"})

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

	// The mock listens on loopback, so the address gogl will report for itself is
	// 127.0.0.1. Claim that address for a client on the requested interface.
	s.SetClients([]types.Client{
		{Name: "self", MAC: "aa:bb:cc:dd:ee:01", IP: "127.0.0.1", Iface: iface, Online: true},
	})
	return s, c
}

func ssidOf(t *testing.T, s *mock.Server, name string) string {
	t.Helper()
	for _, r := range s.Wireless() {
		for _, f := range r.Ifaces {
			if f.Name == name {
				return f.SSID
			}
		}
	}
	t.Fatalf("no interface named %q in the mock", name)
	return ""
}

func TestRunSetSSIDOverEthernet(t *testing.T) {
	s, c := wirelessClient(t, "cable")

	err := SetSSID(context.Background(), c, "default_radio0", "player-test",
		WirelessModes{Yes: true})
	if err != nil {
		t.Fatalf("SetSSID: %v", err)
	}
	if got := ssidOf(t, s, "default_radio0"); got != "player-test" {
		t.Errorf("SSID = %q, want player-test", got)
	}
}

// The rule the user asked for: never over WiFi, whichever radio.
func TestRunSetSSIDRefusedOverWireless(t *testing.T) {
	for _, iface := range []string{"2.4G", "5G"} {
		t.Run(iface, func(t *testing.T) {
			s, c := wirelessClient(t, iface)

			err := SetSSID(context.Background(), c, "default_radio0", "player-test",
				WirelessModes{Yes: true})
			if !errors.Is(err, types.ErrWirelessSession) {
				t.Fatalf("error = %v, want ErrWirelessSession", err)
			}
			if got := ssidOf(t, s, "default_radio0"); got != mock.FactorySSID {
				t.Errorf("a refused write changed the SSID to %q", got)
			}
		})
	}
}

// --yes must not be a way around the session guard. It waives the prompt, which is
// about intent; the guard is about reachability, and no amount of intent makes a
// severed session recoverable.
func TestRunSetSSIDYesDoesNotBypassTheSessionGuard(t *testing.T) {
	s, c := wirelessClient(t, "5G")

	err := SetSSID(context.Background(), c, "default_radio0", "player-test",
		WirelessModes{Yes: true, DryRun: false})
	if !errors.Is(err, types.ErrWirelessSession) {
		t.Errorf("--yes bypassed the session guard: %v", err)
	}
	if got := ssidOf(t, s, "default_radio0"); got != mock.FactorySSID {
		t.Errorf("SSID changed to %q", got)
	}
}

func TestRunSetSSIDDryRunWritesNothing(t *testing.T) {
	s, c := wirelessClient(t, "cable")

	err := SetSSID(context.Background(), c, "default_radio0", "player-test",
		WirelessModes{DryRun: true})
	if err != nil {
		t.Fatalf("dry run: %v", err)
	}
	if got := ssidOf(t, s, "default_radio0"); got != mock.FactorySSID {
		t.Errorf("a dry run changed the SSID to %q", got)
	}
}

// A dry run must report the refusal too, not approve what the write would reject.
func TestRunSetSSIDDryRunReportsTheRefusal(t *testing.T) {
	_, c := wirelessClient(t, "5G")

	err := SetSSID(context.Background(), c, "default_radio0", "player-test",
		WirelessModes{DryRun: true})
	if !errors.Is(err, types.ErrWirelessSession) {
		t.Errorf("dry run error = %v, want ErrWirelessSession", err)
	}
}

// Setting the SSID to what it already is should not disconnect anybody.
func TestRunSetSSIDUnchangedIsANoOp(t *testing.T) {
	_, c := wirelessClient(t, "cable")

	// No --yes: an unchanged SSID must not even reach the confirmation, since there
	// is nothing to confirm.
	err := SetSSID(context.Background(), c, "default_radio0", mock.FactorySSID,
		WirelessModes{})
	if err != nil {
		t.Errorf("setting the SSID to its current value: %v", err)
	}
}

func TestRunSetSSIDUnknownInterface(t *testing.T) {
	_, c := wirelessClient(t, "cable")

	err := SetSSID(context.Background(), c, "wlan0", "player-test",
		WirelessModes{Yes: true})
	if !errors.Is(err, types.ErrNotFound) {
		t.Errorf("error = %v, want ErrNotFound", err)
	}
}

func TestRunSetSSIDRejectsBadSSID(t *testing.T) {
	s, c := wirelessClient(t, "cable")

	err := SetSSID(context.Background(), c, "default_radio0", strings.Repeat("x", 33),
		WirelessModes{Yes: true})
	if !errors.Is(err, types.ErrInvalidInput) {
		t.Errorf("error = %v, want ErrInvalidInput", err)
	}
	if got := ssidOf(t, s, "default_radio0"); got != mock.FactorySSID {
		t.Errorf("a rejected SSID was written: %q", got)
	}
}

// ---------------------------------------------------------------------------
// Reporting
// ---------------------------------------------------------------------------

func testRadios() []types.WirelessRadio {
	return []types.WirelessRadio{{
		Band: types.Band2G, Device: "radio0",
		Ifaces: []types.WirelessInterface{
			{Name: "default_radio0", SSID: mock.FactorySSID, Key: mock.FactoryKey,
				Encryption: "psk2", Enabled: true, Band: types.Band2G},
			{Name: "guest2g", SSID: mock.FactoryGuestSSID, Key: mock.FactoryKey,
				Encryption: "psk2", Guest: true, Hidden: true, Band: types.Band2G},
		},
	}}
}

// The passphrase must not appear unless it was asked for: goglnet output ends up in
// terminals, scrollback, and pasted bug reports.
func TestFormatWirelessMasksTheKeyByDefault(t *testing.T) {
	var buf bytes.Buffer
	if err := FormatWireless(&buf, testRadios(), false); err != nil {
		t.Fatalf("FormatWireless: %v", err)
	}
	got := buf.String()

	if strings.Contains(got, mock.FactoryKey) {
		t.Errorf("output leaked the passphrase:\n%s", got)
	}
	for _, want := range []string{mock.FactorySSID, "default_radio0", "2G", "psk2", "disabled", "guest", "hidden"} {
		if !strings.Contains(got, want) {
			t.Errorf("output missing %q:\n%s", want, got)
		}
	}
}

func TestFormatWirelessShowsTheKeyWhenAsked(t *testing.T) {
	var buf bytes.Buffer
	if err := FormatWireless(&buf, testRadios(), true); err != nil {
		t.Fatalf("FormatWireless: %v", err)
	}
	if !strings.Contains(buf.String(), mock.FactoryKey) {
		t.Errorf("--show-key did not reveal the passphrase:\n%s", buf.String())
	}
}

func TestFormatWirelessWithNoRadios(t *testing.T) {
	var buf bytes.Buffer
	if err := FormatWireless(&buf, nil, false); err != nil {
		t.Fatalf("FormatWireless: %v", err)
	}
	if !strings.Contains(buf.String(), "no wireless") {
		t.Errorf("output does not say there are no radios: %q", buf.String())
	}
}

// ---------------------------------------------------------------------------
// Partial updates: interface-scoped
// ---------------------------------------------------------------------------

func ifaceOf(t *testing.T, s *mock.Server, name string) types.WirelessInterface {
	t.Helper()
	for _, r := range s.Wireless() {
		for _, f := range r.Ifaces {
			if f.Name == name {
				return f
			}
		}
	}
	t.Fatalf("no interface named %q in the mock", name)
	return types.WirelessInterface{}
}

func radioOf(t *testing.T, s *mock.Server, device string) types.WirelessRadio {
	t.Helper()
	for _, r := range s.Wireless() {
		if r.Device == device {
			return r
		}
	}
	t.Fatalf("no radio named %q in the mock", device)
	return types.WirelessRadio{}
}

func str(v string) *string { return &v }
func boolp(v bool) *bool   { return &v }
func intp(v int) *int      { return &v }

// A partial update must leave every unmentioned field alone. If it does not, setting
// a passphrase silently resets the SSID, and the operator finds out from a device
// that will not associate.
func TestRunSetWirelessKeyLeavesEverythingElseAlone(t *testing.T) {
	s, c := wirelessClient(t, "cable")
	before := ifaceOf(t, s, "default_radio0")

	err := SetWireless(context.Background(), c,
		"default_radio0", types.InterfaceChanges{Key: str("newpassphrase")},
		"", types.RadioChanges{}, WirelessModes{Yes: true})
	if err != nil {
		t.Fatalf("SetWireless: %v", err)
	}

	after := ifaceOf(t, s, "default_radio0")
	if after.Key != "newpassphrase" {
		t.Errorf("passphrase = %q, want newpassphrase", after.Key)
	}
	if after.SSID != before.SSID {
		t.Errorf("SSID changed from %q to %q", before.SSID, after.SSID)
	}
	if after.Encryption != before.Encryption || after.Enabled != before.Enabled ||
		after.Hidden != before.Hidden || after.Guest != before.Guest {
		t.Errorf("a passphrase write disturbed other fields:\nbefore %+v\nafter  %+v", before, after)
	}
}

// False must be writable and distinguishable from absent. The CLI gets that from
// pflag's Changed(); this asserts the layer underneath honours the distinction.
func TestRunSetWirelessWritesFalse(t *testing.T) {
	s, c := wirelessClient(t, "cable")

	err := SetWireless(context.Background(), c,
		"default_radio0", types.InterfaceChanges{Enabled: boolp(false)},
		"", types.RadioChanges{}, WirelessModes{Yes: true})
	if err != nil {
		t.Fatalf("SetWireless: %v", err)
	}
	if ifaceOf(t, s, "default_radio0").Enabled {
		t.Error("--set-enabled=false did not disable the interface")
	}
}

func TestRunSetWirelessSeveralFieldsAtOnce(t *testing.T) {
	s, c := wirelessClient(t, "cable")

	err := SetWireless(context.Background(), c,
		"guest2g", types.InterfaceChanges{
			SSID:    str("player-guest"),
			Key:     str("guestpass123"),
			Hidden:  boolp(false),
			Enabled: boolp(true),
		},
		"", types.RadioChanges{}, WirelessModes{Yes: true})
	if err != nil {
		t.Fatalf("SetWireless: %v", err)
	}

	got := ifaceOf(t, s, "guest2g")
	if got.SSID != "player-guest" || got.Key != "guestpass123" || got.Hidden || !got.Enabled {
		t.Errorf("combined write did not apply: %+v", got)
	}
	// The other interface on the same radio must be untouched.
	if other := ifaceOf(t, s, "default_radio0"); other.SSID != mock.FactorySSID {
		t.Errorf("writing guest2g changed default_radio0 to %q", other.SSID)
	}
}

func TestRunSetWirelessRejectsShortKey(t *testing.T) {
	s, c := wirelessClient(t, "cable")

	err := SetWireless(context.Background(), c,
		"default_radio0", types.InterfaceChanges{Key: str("short")},
		"", types.RadioChanges{}, WirelessModes{Yes: true})
	if !errors.Is(err, types.ErrInvalidInput) {
		t.Errorf("error = %v, want ErrInvalidInput", err)
	}
	if ifaceOf(t, s, "default_radio0").Key != mock.FactoryKey {
		t.Error("a rejected passphrase was written")
	}
}

// The radio advertises its supported encryptions, so an unsupported one is caught
// with the valid ones named rather than as a bare firmware error.
func TestRunSetWirelessRejectsUnsupportedEncryption(t *testing.T) {
	_, c := wirelessClient(t, "cable")

	err := SetWireless(context.Background(), c,
		"default_radio0", types.InterfaceChanges{Encryption: str("wep")},
		"", types.RadioChanges{}, WirelessModes{Yes: true})
	if !errors.Is(err, types.ErrInvalidInput) {
		t.Fatalf("error = %v, want ErrInvalidInput", err)
	}
	if !strings.Contains(err.Error(), "psk2") {
		t.Errorf("error does not list the supported encryptions: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Partial updates: radio-scoped
// ---------------------------------------------------------------------------

func TestRunSetWirelessRadioTuning(t *testing.T) {
	s, c := wirelessClient(t, "cable")
	before := radioOf(t, s, "radio1")

	err := SetWireless(context.Background(), c,
		"", types.InterfaceChanges{},
		"radio1", types.RadioChanges{Channel: intp(149), TXPower: str("Low")},
		WirelessModes{Yes: true})
	if err != nil {
		t.Fatalf("SetWireless: %v", err)
	}

	after := radioOf(t, s, "radio1")
	if after.Channel != 149 || after.TXPower != "Low" {
		t.Errorf("tuning not applied: channel %d, power %s", after.Channel, after.TXPower)
	}
	// Unmentioned tuning, and the interfaces on the radio, must survive.
	if after.HTMode != before.HTMode || after.HWMode != before.HWMode {
		t.Errorf("a channel change disturbed the mode: %+v", after)
	}
	if len(after.Ifaces) != len(before.Ifaces) || after.Ifaces[0].SSID != before.Ifaces[0].SSID {
		t.Errorf("a radio write disturbed its interfaces")
	}
}

// Channel 0 means auto, so it cannot double as "unset" -- which is why the CLI decides
// by Changed() rather than by comparing against zero.
func TestRunSetWirelessChannelZeroMeansAuto(t *testing.T) {
	s, c := wirelessClient(t, "cable")

	err := SetWireless(context.Background(), c,
		"", types.InterfaceChanges{},
		"radio1", types.RadioChanges{Channel: intp(0)},
		WirelessModes{Yes: true})
	if err != nil {
		t.Fatalf("SetWireless: %v", err)
	}
	if got := radioOf(t, s, "radio1").Channel; got != 0 {
		t.Errorf("channel = %d, want 0 (auto)", got)
	}
}

func TestRunSetWirelessRejectsUnavailableChannel(t *testing.T) {
	s, c := wirelessClient(t, "cable")
	before := radioOf(t, s, mock.Factory5GDevice).Channel

	// Channel 6 is a 2.4GHz channel; radio1 is the 5GHz radio.
	err := SetWireless(context.Background(), c,
		"", types.InterfaceChanges{},
		mock.Factory5GDevice, types.RadioChanges{Channel: intp(6)},
		WirelessModes{Yes: true})
	if !errors.Is(err, types.ErrInvalidInput) {
		t.Fatalf("error = %v, want ErrInvalidInput", err)
	}
	if !strings.Contains(err.Error(), "36") {
		t.Errorf("error does not list the available channels: %v", err)
	}
	if got := radioOf(t, s, mock.Factory5GDevice).Channel; got != before {
		t.Errorf("a rejected channel was written: %d", got)
	}
}

func TestRunSetWirelessRejectsUnsupportedBandwidthAndPower(t *testing.T) {
	_, c := wirelessClient(t, "cable")
	ctx := context.Background()

	err := SetWireless(ctx, c, "", types.InterfaceChanges{},
		"radio0", types.RadioChanges{HTMode: str("VHT80")}, WirelessModes{Yes: true})
	if !errors.Is(err, types.ErrInvalidInput) {
		t.Errorf("2.4GHz accepted VHT80: %v", err)
	}

	err = SetWireless(ctx, c, "", types.InterfaceChanges{},
		"radio0", types.RadioChanges{TXPower: str("Maximum")}, WirelessModes{Yes: true})
	if !errors.Is(err, types.ErrInvalidInput) {
		t.Errorf("accepted a bogus power level: %v", err)
	}
}

func TestRunSetWirelessUnknownRadio(t *testing.T) {
	_, c := wirelessClient(t, "cable")

	err := SetWireless(context.Background(), c, "", types.InterfaceChanges{},
		"radio9", types.RadioChanges{Channel: intp(1)}, WirelessModes{Yes: true})
	if !errors.Is(err, types.ErrNotFound) {
		t.Fatalf("error = %v, want ErrNotFound", err)
	}
	if !strings.Contains(err.Error(), "radio0") {
		t.Errorf("error does not list the real radios: %v", err)
	}
}

// Interface and radio changes in one invocation reach two different calls, because
// the firmware scopes them by different keys.
func TestRunSetWirelessBothScopesAtOnce(t *testing.T) {
	s, c := wirelessClient(t, "cable")

	err := SetWireless(context.Background(), c,
		"default_radio1", types.InterfaceChanges{SSID: str("player-5g")},
		"radio1", types.RadioChanges{Channel: intp(40)},
		WirelessModes{Yes: true})
	if err != nil {
		t.Fatalf("SetWireless: %v", err)
	}
	if got := ifaceOf(t, s, "default_radio1").SSID; got != "player-5g" {
		t.Errorf("SSID = %q, want player-5g", got)
	}
	if got := radioOf(t, s, "radio1").Channel; got != 40 {
		t.Errorf("channel = %d, want 40", got)
	}
}

// Every wireless write is gated, not only the SSID: retuning a radio drops its
// clients too.
func TestRunSetWirelessRadioRefusedOverWireless(t *testing.T) {
	s, c := wirelessClient(t, "5G")
	before := radioOf(t, s, mock.Factory5GDevice).Channel

	err := SetWireless(context.Background(), c, "", types.InterfaceChanges{},
		mock.Factory5GDevice, types.RadioChanges{Channel: intp(149)}, WirelessModes{Yes: true})
	if !errors.Is(err, types.ErrWirelessSession) {
		t.Fatalf("error = %v, want ErrWirelessSession", err)
	}
	if got := radioOf(t, s, mock.Factory5GDevice).Channel; got != before {
		t.Errorf("a refused retune changed the channel to %d", got)
	}
}

// Asking for values that are already set is not an error, and must not disconnect
// anybody.
func TestRunSetWirelessNoEffectiveChange(t *testing.T) {
	s, c := wirelessClient(t, "cable")
	radio := radioOf(t, s, mock.Factory2GDevice)

	// Ask for exactly what is already set. No --yes: an ineffective request must not
	// reach the confirmation, because there is nothing to confirm and nobody should
	// be disconnected.
	err := SetWireless(context.Background(), c,
		mock.Factory2GIface, types.InterfaceChanges{SSID: str(mock.FactorySSID)},
		mock.Factory2GDevice, types.RadioChanges{Channel: intp(radio.Channel)},
		WirelessModes{})
	if err != nil {
		t.Fatalf("SetWireless: %v", err)
	}
	if got := ifaceOf(t, s, mock.Factory2GIface).SSID; got != mock.FactorySSID {
		t.Errorf("SSID changed to %q", got)
	}
}
