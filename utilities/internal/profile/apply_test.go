package profile

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	gogl "github.com/gherlein/gogl/src"
	"github.com/gherlein/gogl/src/mock"
	"github.com/gherlein/gogl/src/types"
)

// Apply's ordering is the whole design: domain, network, reservations, names, wireless.
// Every step is there because doing it later fails, and each rule came from hardware
// rather than from reasoning. These tests are mostly about the order and about what
// happens when a step cannot complete.

func apply(t *testing.T, c *gogl.Client, p *Profile, modes ApplyModes) (string, error) {
	t.Helper()
	var log bytes.Buffer
	err := Apply(context.Background(), c, p, modes, &log)
	return log.String(), err
}

// sameSubnetProfile targets the subnet the mock already has, so a load can complete in
// one run.
func sameSubnetProfile() *Profile {
	return &Profile{
		Version: ProfileVersion,
		Source:  Source{Model: "sft1200"},
		Network: &ProfileNetwork{
			Interface: types.InterfaceLAN,
			IP:        "192.168.8.1",
			Netmask:   "255.255.255.0",
			DHCPStart: "192.168.8.100",
			DHCPStop:  "192.168.8.249",
		},
		Domain: "lab.example",
		Reservations: []types.Reservation{
			{Name: "nas", MAC: "aa:bb:cc:dd:ee:01", IP: "192.168.8.13"},
			{Name: "pi", MAC: "aa:bb:cc:dd:ee:02", IP: "192.168.8.14"},
		},
		Hosts: []types.HostEntry{
			{IP: "192.168.8.13", Names: []string{"nas", "nas.lab.example"}},
			{IP: "192.168.8.14", Names: []string{"pi", "pi.lab.example"}},
		},
	}
}

func TestApplyToAFactoryRouter(t *testing.T) {
	s, c := profileClient(t, nil, mock.FactoryHostFile)

	if _, err := apply(t, c, sameSubnetProfile(), ApplyModes{}); err != nil {
		t.Fatalf("Apply: %v", err)
	}

	if got := s.Reservations(); len(got) != 2 {
		t.Errorf("device holds %d reservations, want 2", len(got))
	}
	hosts := s.HostFile()
	if types.ParseHostFile(hosts).Domain != "lab.example" {
		t.Errorf("domain not set:\n%s", hosts)
	}
	for _, want := range []string{"nas.lab.example", "pi.lab.example", "127.0.0.1 localhost"} {
		if !strings.Contains(hosts, want) {
			t.Errorf("host file missing %q:\n%s", want, hosts)
		}
	}
}

// The domain has to be written before the reservations, or every reservation write is
// refused. This is the ordering rule most likely to be got wrong by rearranging code.
func TestApplyWritesDomainBeforeReservations(t *testing.T) {
	_, c := profileClient(t, nil, mock.FactoryHostFile)

	log, err := apply(t, c, sameSubnetProfile(), ApplyModes{})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}

	domainAt := strings.Index(log, "domain:")
	reservationAt := strings.Index(log, "reservation:")
	if domainAt < 0 || reservationAt < 0 {
		t.Fatalf("log does not show both steps:\n%s", log)
	}
	if domainAt > reservationAt {
		t.Errorf("domain was written after reservations:\n%s", log)
	}
}

// A profile with reservations but no domain cannot be applied at all, and saying so up
// front beats a run that writes a network and then fails on every reservation.
func TestApplyRefusesReservationsWithNoDomain(t *testing.T) {
	s, c := profileClient(t, nil, mock.FactoryHostFile)

	p := sameSubnetProfile()
	p.Domain = ""

	_, err := apply(t, c, p, ApplyModes{})
	if !errors.Is(err, types.ErrDomainNotSet) {
		t.Fatalf("error = %v, want ErrDomainNotSet", err)
	}
	if got := s.Reservations(); len(got) != 0 {
		t.Errorf("a refused apply wrote %d reservations", len(got))
	}
}

func TestApplyIsIdempotent(t *testing.T) {
	s, c := profileClient(t, nil, mock.FactoryHostFile)
	p := sameSubnetProfile()

	if _, err := apply(t, c, p, ApplyModes{}); err != nil {
		t.Fatalf("first apply: %v", err)
	}
	firstHosts := s.HostFile()

	log, err := apply(t, c, p, ApplyModes{})
	if err != nil {
		t.Fatalf("second apply: %v", err)
	}

	if s.HostFile() != firstHosts {
		t.Errorf("second apply rewrote the host file:\n%s", s.HostFile())
	}
	if strings.Contains(log, "(new)") {
		t.Errorf("second apply created something:\n%s", log)
	}
	if got := s.Reservations(); len(got) != 2 {
		t.Errorf("device holds %d reservations after two applies", len(got))
	}
}

func TestApplyDryRunWritesNothing(t *testing.T) {
	s, c := profileClient(t, nil, mock.FactoryHostFile)
	before := s.HostFile()

	log, err := apply(t, c, sameSubnetProfile(), ApplyModes{DryRun: true})
	if err != nil {
		t.Fatalf("dry run: %v", err)
	}

	if got := s.Reservations(); len(got) != 0 {
		t.Errorf("a dry run created %d reservations", len(got))
	}
	if s.HostFile() != before {
		t.Errorf("a dry run rewrote the host file:\n%s", s.HostFile())
	}
	// It still has to say what it would do, or it is useless.
	if !strings.Contains(log, "reservation:") || !strings.Contains(log, "dry run") {
		t.Errorf("dry run did not report the plan:\n%s", log)
	}
}

// ---------------------------------------------------------------------------
// The subnet move, which cannot complete in one run
// ---------------------------------------------------------------------------

func movingProfile() *Profile {
	p := sameSubnetProfile()
	p.Network.IP = "192.168.4.1"
	p.Network.DHCPStart = "192.168.4.100"
	p.Network.DHCPStop = "192.168.4.149"
	p.Reservations = []types.Reservation{
		{Name: "nas", MAC: "aa:bb:cc:dd:ee:01", IP: "192.168.4.13"},
	}
	p.Hosts = []types.HostEntry{{IP: "192.168.4.13", Names: []string{"nas"}}}
	return p
}

// The router changes address mid-write, so Apply stops and says how to resume.
// Reporting success for a run that wrote a third of the profile would be a lie.
func TestApplyStopsAfterASubnetMove(t *testing.T) {
	s, c := profileClient(t, nil, mock.FactoryHostFile)

	log, err := apply(t, c, movingProfile(), ApplyModes{})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}

	if got := s.Network(); got[0].LANIP != "192.168.4.1" {
		t.Errorf("network = %s, want the new address", got[0].LANIP)
	}
	// Nothing after the network step may have run: those addresses are not reachable
	// from this session any more.
	if got := s.Reservations(); len(got) != 0 {
		t.Errorf("%d reservations were written after the router moved", len(got))
	}
	for _, want := range []string{"has moved", "resume with", "192.168.4.1"} {
		if !strings.Contains(log, want) {
			t.Errorf("log does not say how to resume (%q):\n%s", want, log)
		}
	}
}

// The guard still applies: a subnet move with reservations present is refused unless
// forced, exactly as goglnet refuses it.
func TestApplySubnetMoveIsGuarded(t *testing.T) {
	s, c := profileClient(t, seededReservations(), seededHostFile())

	_, err := apply(t, c, movingProfile(), ApplyModes{})
	if !errors.Is(err, types.ErrReservationsExist) {
		t.Fatalf("error = %v, want ErrReservationsExist", err)
	}
	if got := s.Network(); got[0].LANIP != "192.168.8.1" {
		t.Errorf("a refused apply moved the router to %s", got[0].LANIP)
	}
}

func TestApplySubnetMoveWithForce(t *testing.T) {
	s, c := profileClient(t, seededReservations(), seededHostFile())

	if _, err := apply(t, c, movingProfile(), ApplyModes{Force: true}); err != nil {
		t.Fatalf("--force was refused: %v", err)
	}
	if got := s.Network(); got[0].LANIP != "192.168.4.1" {
		t.Errorf("network = %s, want the new address", got[0].LANIP)
	}
}

// A pool-only difference is not a move, so the run continues through every section.
func TestApplyPoolOnlyChangeContinues(t *testing.T) {
	s, c := profileClient(t, nil, mock.FactoryHostFile)

	p := sameSubnetProfile()
	p.Network.DHCPStart = "192.168.8.50"
	p.Network.DHCPStop = "192.168.8.90"

	log, err := apply(t, c, p, ApplyModes{})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}

	if strings.Contains(log, "has moved") {
		t.Errorf("a pool-only change was treated as a move:\n%s", log)
	}
	if got := s.Network(); got[0].DHCPStart != "192.168.8.50" {
		t.Errorf("pool not written: %+v", got[0])
	}
	if got := s.Reservations(); len(got) != 2 {
		t.Errorf("the run stopped early: %d reservations", len(got))
	}
}

// ---------------------------------------------------------------------------
// Wireless
// ---------------------------------------------------------------------------

func wirelessProfile() *Profile {
	p := sameSubnetProfile()
	p.Wireless = []ProfileInterface{
		{Name: mock.Factory2GIface, SSID: "player-test", Encryption: "psk2", Enabled: true},
	}
	p.Radios = []ProfileRadio{
		{Device: mock.Factory5GDevice, Channel: 149, TXPower: "Low"},
	}
	return p
}

// Wireless is opt-in: it needs a wired session and is the least likely section to be
// wanted, so a load that silently retuned the radios would be a nasty surprise.
func TestApplySkipsWirelessByDefault(t *testing.T) {
	s, c := profileClient(t, nil, mock.FactoryHostFile)

	log, err := apply(t, c, wirelessProfile(), ApplyModes{})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}

	if !strings.Contains(log, "--wireless") {
		t.Errorf("log does not mention how to apply wireless:\n%s", log)
	}
	for _, r := range s.Wireless() {
		for _, f := range r.Ifaces {
			if f.SSID == "player-test" {
				t.Error("wireless was applied without --wireless")
			}
		}
	}
}

func TestApplyWirelessWhenAsked(t *testing.T) {
	s, c := profileClient(t, nil, mock.FactoryHostFile)

	if _, err := apply(t, c, wirelessProfile(), ApplyModes{Wireless: true}); err != nil {
		t.Fatalf("Apply: %v", err)
	}

	var ssid, power string
	var channel int
	for _, r := range s.Wireless() {
		if r.Device == mock.Factory5GDevice {
			channel, power = r.Channel, r.TXPower
		}
		for _, f := range r.Ifaces {
			if f.Name == mock.Factory2GIface {
				ssid = f.SSID
			}
		}
	}
	if ssid != "player-test" {
		t.Errorf("SSID = %q, want player-test", ssid)
	}
	if channel != 149 || power != "Low" {
		t.Errorf("radio tuning = channel %d, power %s", channel, power)
	}
}

// An omitted passphrase means "leave it alone", which is what makes a key-less profile
// safe rather than destructive. This is the property the default capture depends on.
func TestApplyWithNoKeyLeavesThePassphraseAlone(t *testing.T) {
	s, c := profileClient(t, nil, mock.FactoryHostFile)

	p := wirelessProfile()
	p.Wireless[0].Key = "" // as captured without --with-keys

	if _, err := apply(t, c, p, ApplyModes{Wireless: true}); err != nil {
		t.Fatalf("Apply: %v", err)
	}

	for _, r := range s.Wireless() {
		for _, f := range r.Ifaces {
			if f.Name == mock.Factory2GIface && f.Key != mock.FactoryKey {
				t.Errorf("passphrase became %q; a key-less profile must not touch it", f.Key)
			}
		}
	}
}

// A profile from another model may name interfaces this router does not have. Skipping
// them with a message beats failing the whole load over a radio that does not exist.
func TestApplySkipsUnknownWirelessInterfaces(t *testing.T) {
	_, c := profileClient(t, nil, mock.FactoryHostFile)

	p := sameSubnetProfile()
	p.Wireless = []ProfileInterface{{Name: "wlan9", SSID: "nope", Enabled: true}}
	p.Radios = []ProfileRadio{{Device: "radio9", Channel: 1}}

	log, err := apply(t, c, p, ApplyModes{Wireless: true})
	if err != nil {
		t.Fatalf("Apply failed on an interface this router lacks: %v", err)
	}
	if !strings.Contains(log, "wlan9") || !strings.Contains(log, "radio9") {
		t.Errorf("log does not report the skipped names:\n%s", log)
	}
}

func TestApplyWarnsOnModelMismatch(t *testing.T) {
	_, c := profileClient(t, nil, mock.FactoryHostFile)

	p := sameSubnetProfile()
	p.Source.Model = "mt3000"

	log, err := apply(t, c, p, ApplyModes{})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if !strings.Contains(log, "mt3000") || !strings.Contains(log, "sft1200") {
		t.Errorf("log does not warn about the model mismatch:\n%s", log)
	}
}

func TestApplyNoWarningWhenModelsMatch(t *testing.T) {
	_, c := profileClient(t, nil, mock.FactoryHostFile)

	log, err := apply(t, c, sameSubnetProfile(), ApplyModes{})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if strings.Contains(log, "warning: profile is from") {
		t.Errorf("warned about a matching model:\n%s", log)
	}
}

// ---------------------------------------------------------------------------
// Capture then apply, which is the actual use case
// ---------------------------------------------------------------------------

// The round trip that matters: dump one router, load onto a second, and the second ends
// up with the same addressing. Two mocks, so nothing is shared but the file.
func TestCaptureThenApplyToASecondRouter(t *testing.T) {
	_, source := profileClient(t, seededReservations(), seededHostFile())
	captured := capture(t, source, true)

	var file bytes.Buffer
	if err := captured.Write(&file); err != nil {
		t.Fatalf("Write: %v", err)
	}
	loaded, err := ReadProfile(&file)
	if err != nil {
		t.Fatalf("ReadProfile: %v", err)
	}

	target, targetClient := profileClient(t, nil, mock.FactoryHostFile)
	if _, err := apply(t, targetClient, loaded, ApplyModes{Wireless: true}); err != nil {
		t.Fatalf("Apply to the second router: %v", err)
	}

	if got := target.Reservations(); len(got) != 2 {
		t.Fatalf("target holds %d reservations, want 2", len(got))
	}
	hosts := target.HostFile()
	if types.ParseHostFile(hosts).Domain != "lab.example" {
		t.Errorf("domain did not carry over:\n%s", hosts)
	}
	for _, want := range []string{"192.168.8.13 nas", "192.168.8.14 pi"} {
		if !strings.Contains(hosts, want) {
			t.Errorf("host file missing %q:\n%s", want, hosts)
		}
	}
	// And the unmanaged part of the target's host file must survive.
	if !strings.Contains(hosts, "ff02::1 ip6-allnodes") {
		t.Errorf("apply clobbered the target's boilerplate:\n%s", hosts)
	}
}
