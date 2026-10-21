package profile

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

const lanFixture = `{
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

// profileClient builds a client against a mock seeded with a plausible router.
//
// The session is reported as arriving over ethernet, because the wireless sections of
// a profile cannot be applied otherwise and a test about profiles should not be a test
// about that guard.
func profileClient(t *testing.T, reservations []types.Reservation, hostFile string) (*mock.Server, *gogl.Client) {
	t.Helper()

	s := mock.NewServer(t, mock.Options{Password: "secret"})
	s.LoadFixture(mock.NetworkGroup, mock.MethodGetConfigList, json.RawMessage(lanFixture))
	// The router's own MAC is here deliberately: the point of the identity test below is
	// that Capture reads this and copies none of it into the profile.
	s.LoadFixture(mock.SystemGroup, mock.MethodGetInfo, json.RawMessage(
		`{"model":"sft1200","firmware_version":"4.3.28","mac":"94:83:c4:84:bc:43","uptime":98765}`))
	s.SetReservations(reservations)
	s.SetHostFile(hostFile)
	s.SetClients([]types.Client{
		{Name: "self", MAC: "aa:bb:cc:dd:ee:ff", IP: "127.0.0.1", Iface: "cable", Online: true},
	})

	u, err := url.Parse(s.URL())
	if err != nil {
		t.Fatalf("parse mock URL: %v", err)
	}
	port, err := strconv.Atoi(u.Port())
	if err != nil {
		t.Fatalf("parse port: %v", err)
	}

	c, err := gogl.New(gogl.Config{
		Host: u.Hostname(), Port: port, Password: "secret", KeepaliveInterval: -1,
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

func seededHostFile() string {
	return mock.HostFileWith("lab.example",
		"192.168.8.13 nas nas.lab.example",
		"192.168.8.14 pi pi.lab.example")
}

func seededReservations() []types.Reservation {
	return []types.Reservation{
		{Name: "nas", MAC: "aa:bb:cc:dd:ee:01", IP: "192.168.8.13"},
		{Name: "pi", MAC: "aa:bb:cc:dd:ee:02", IP: "192.168.8.14"},
	}
}

func capture(t *testing.T, c *gogl.Client, withKeys bool) *Profile {
	t.Helper()
	var warn bytes.Buffer
	p, err := Capture(context.Background(), c, CaptureOptions{
		WithKeys: withKeys, Host: "192.168.8.1", Captured: "2026-07-29T00:00:00Z",
	}, &warn)
	if err != nil {
		t.Fatalf("Capture: %v", err)
	}
	return p
}

func TestCaptureCollectsEverySection(t *testing.T) {
	_, c := profileClient(t, seededReservations(), seededHostFile())

	p := capture(t, c, false)

	if p.Version != ProfileVersion {
		t.Errorf("version = %d, want %d", p.Version, ProfileVersion)
	}
	if p.Network == nil || p.Network.IP != "192.168.8.1" {
		t.Fatalf("network = %+v", p.Network)
	}
	if p.Network.DHCPStart != "192.168.8.100" || p.Network.DHCPStop != "192.168.8.249" {
		t.Errorf("pool = %s-%s", p.Network.DHCPStart, p.Network.DHCPStop)
	}
	if p.Domain != "lab.example" {
		t.Errorf("domain = %q", p.Domain)
	}
	if len(p.Reservations) != 2 {
		t.Errorf("reservations = %d, want 2", len(p.Reservations))
	}
	if len(p.Hosts) != 2 {
		t.Errorf("hosts = %d, want 2", len(p.Hosts))
	}
	if len(p.Wireless) != 4 {
		t.Errorf("wireless interfaces = %d, want 4", len(p.Wireless))
	}
	if len(p.Radios) != 2 {
		t.Errorf("radios = %d, want 2", len(p.Radios))
	}
	if p.Source.Model == "" || p.Source.Host != "192.168.8.1" {
		t.Errorf("source = %+v", p.Source)
	}
}

// A profile is a file people commit, so the default must not carry passphrases.
func TestCaptureOmitsKeysByDefault(t *testing.T) {
	_, c := profileClient(t, nil, seededHostFile())

	p := capture(t, c, false)

	var buf bytes.Buffer
	if err := p.Write(&buf); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if strings.Contains(buf.String(), mock.FactoryKey) {
		t.Errorf("profile leaked a passphrase:\n%s", buf.String())
	}
	for _, w := range p.Wireless {
		if w.Key != "" {
			t.Errorf("interface %s carries a key", w.Name)
		}
	}
}

func TestCaptureWithKeysIncludesThem(t *testing.T) {
	_, c := profileClient(t, nil, seededHostFile())

	p := capture(t, c, true)

	found := false
	for _, w := range p.Wireless {
		if w.Key == mock.FactoryKey {
			found = true
		}
	}
	if !found {
		t.Error("--with-keys did not include any passphrase")
	}
}

// The identifying fields of a particular unit are what make a full config dump useless
// on a second router, so a profile must not carry them.
//
// Client MAC addresses are a different thing entirely and must be present: a
// reservation is a MAC-to-IP binding, so a profile without them would be worthless.
// The line this test draws is the router's own identity, not MAC addresses generally.
func TestCaptureOmitsPerUnitIdentity(t *testing.T) {
	_, c := profileClient(t, seededReservations(), seededHostFile())

	p := capture(t, c, true)
	var buf bytes.Buffer
	if err := p.Write(&buf); err != nil {
		t.Fatalf("Write: %v", err)
	}
	encoded := buf.String()

	// The router's own MAC and uptime come back from system.get_info; neither may be
	// copied into the profile.
	for _, forbidden := range []string{"94:83:c4:84:bc:43", "98765", `"uptime"`, `"leases"`, `"sn"`} {
		if strings.Contains(encoded, forbidden) {
			t.Errorf("profile carries per-unit value %s:\n%s", forbidden, encoded)
		}
	}

	// And the client MACs must be there, or the profile reproduces nothing.
	for _, required := range []string{"aa:bb:cc:dd:ee:01", "aa:bb:cc:dd:ee:02"} {
		if !strings.Contains(encoded, required) {
			t.Errorf("profile is missing client MAC %s", required)
		}
	}
}

func TestProfileRoundTripsThroughJSON(t *testing.T) {
	_, c := profileClient(t, seededReservations(), seededHostFile())
	original := capture(t, c, true)

	var buf bytes.Buffer
	if err := original.Write(&buf); err != nil {
		t.Fatalf("Write: %v", err)
	}

	decoded, err := ReadProfile(&buf)
	if err != nil {
		t.Fatalf("ReadProfile: %v", err)
	}

	if decoded.Domain != original.Domain {
		t.Errorf("domain = %q, want %q", decoded.Domain, original.Domain)
	}
	if len(decoded.Reservations) != len(original.Reservations) {
		t.Errorf("reservations = %d, want %d", len(decoded.Reservations), len(original.Reservations))
	}
	if len(decoded.Wireless) != len(original.Wireless) {
		t.Errorf("wireless = %d, want %d", len(decoded.Wireless), len(original.Wireless))
	}
	if *decoded.Network != *original.Network {
		t.Errorf("network = %+v, want %+v", decoded.Network, original.Network)
	}
}

func TestReadProfileRejectsBadInput(t *testing.T) {
	tests := map[string]string{
		"not JSON":       `{`,
		"no version":     `{"network":{"interface":"lan","ip":"192.168.8.1"}}`,
		"wrong version":  `{"gogl_profile_version":99,"network":{"interface":"lan"}}`,
		"no network":     `{"gogl_profile_version":1}`,
		"unknown field":  `{"gogl_profile_version":1,"network":{"interface":"lan"},"surprise":1}`,
		"empty document": ``,
	}

	for name, body := range tests {
		t.Run(name, func(t *testing.T) {
			if _, err := ReadProfile(strings.NewReader(body)); err == nil {
				t.Errorf("accepted %s", name)
			}
		})
	}
}

// A newer gogl might add a section this build would silently drop, and silently
// dropping part of a network is worse than refusing the file.
func TestReadProfileRefusesUnknownFieldsRatherThanIgnoringThem(t *testing.T) {
	body := `{"gogl_profile_version":1,"network":{"interface":"lan","ip":"192.168.8.1",
	          "netmask":"255.255.255.0","dhcp_start":"192.168.8.100","dhcp_end":"192.168.8.249"},
	          "firewall":{"rules":[]}}`

	_, err := ReadProfile(strings.NewReader(body))
	if err == nil {
		t.Fatal("a profile with an unknown section was accepted")
	}
	if !strings.Contains(err.Error(), "firewall") {
		t.Errorf("error does not name the unknown field: %v", err)
	}
}

func TestReadProfileVersionErrorIsInvalidInput(t *testing.T) {
	_, err := ReadProfile(strings.NewReader(`{"gogl_profile_version":99,"network":{}}`))
	if !errors.Is(err, types.ErrInvalidInput) {
		t.Errorf("error = %v, want ErrInvalidInput", err)
	}
}

// Capture must not fail whole when an optional section is unreadable: a router that
// will not report wireless is still worth capturing addresses from.
func TestCaptureToleratesMissingWireless(t *testing.T) {
	s, c := profileClient(t, seededReservations(), seededHostFile())
	s.FailNext(mock.WirelessGroup, mock.MethodGetConfig, mock.CodeNotFound, "no wireless")

	var warn bytes.Buffer
	p, err := Capture(context.Background(), c, CaptureOptions{}, &warn)
	if err != nil {
		t.Fatalf("Capture: %v", err)
	}
	if len(p.Wireless) != 0 {
		t.Errorf("wireless = %+v, want none", p.Wireless)
	}
	if len(p.Reservations) != 2 {
		t.Errorf("reservations lost when wireless failed: %d", len(p.Reservations))
	}
	if !strings.Contains(warn.String(), "wireless") {
		t.Errorf("no warning about the missing section: %q", warn.String())
	}
}

// The network is the one section a profile is useless without.
func TestCaptureFailsWithoutTheNetwork(t *testing.T) {
	s, c := profileClient(t, nil, seededHostFile())
	s.FailNext(mock.NetworkGroup, mock.MethodGetConfigList, mock.CodeNotFound, "gone")

	var warn bytes.Buffer
	if _, err := Capture(context.Background(), c, CaptureOptions{}, &warn); err == nil {
		t.Error("Capture succeeded with no network")
	}
}
