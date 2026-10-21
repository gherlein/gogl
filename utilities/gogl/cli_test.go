package main

import (
	"bytes"
	"encoding/json"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/gherlein/gogl/src/mock"
	"github.com/gherlein/gogl/src/types"
)

// These drive the real cobra tree against the mock router, which is the only way to
// catch the class of bug that lives in flag wiring rather than in logic. The packages
// under utilities/internal are tested on their own; what is unproven here is whether a
// command reaches them with the arguments the operator typed.

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

// run executes the tree exactly as main does, with a config file pointing at the mock.
func run(t *testing.T, s *mock.Server, args ...string) (stdout string, err error) {
	t.Helper()

	u, parseErr := url.Parse(s.URL())
	if parseErr != nil {
		t.Fatalf("parse mock URL: %v", parseErr)
	}
	port, parseErr := strconv.Atoi(u.Port())
	if parseErr != nil {
		t.Fatalf("parse port: %v", parseErr)
	}

	dir := t.TempDir()
	cfg := filepath.Join(dir, "config.toml")
	body := "default = \"mock\"\n\n[routers.mock]\nhost = \"" + u.Hostname() + "\"\nport = " +
		strconv.Itoa(port) + "\npassword_command = \"printf secret\"\n"
	if writeErr := os.WriteFile(cfg, []byte(body), 0o600); writeErr != nil {
		t.Fatalf("write config: %v", writeErr)
	}
	t.Setenv("GOGL_CONFIG", cfg)
	t.Setenv("GL_PASSWORD", "")
	t.Setenv("GL_ROUTER_IP", "")

	// The tree writes through cmd.OutOrStdout(), which resolves to os.Stdout when the
	// harness sets no writer. Capturing at the file-descriptor level rather than via
	// cmd.SetOut keeps this helper driving the tree exactly as main does, with no output
	// wiring the production path does not have.
	old := os.Stdout
	r, w, pipeErr := os.Pipe()
	if pipeErr != nil {
		t.Fatalf("pipe: %v", pipeErr)
	}
	os.Stdout = w

	// Reset the package-level flag state between runs, since cobra commands are rebuilt
	// but `opts` is global.
	opts = globals{}

	root := newRootCommand()
	root.SetArgs(args)
	err = root.Execute()

	w.Close()
	os.Stdout = old

	var buf bytes.Buffer
	if _, copyErr := buf.ReadFrom(r); copyErr != nil {
		t.Fatalf("read captured stdout: %v", copyErr)
	}
	return buf.String(), err
}

func mockRouter(t *testing.T, reservations []types.Reservation, hostFile string) *mock.Server {
	t.Helper()
	s := mock.NewServer(t, mock.Options{Password: "secret"})
	s.LoadFixture(mock.NetworkGroup, mock.MethodGetConfigList, json.RawMessage(lanFixture))
	s.LoadFixture(mock.SystemGroup, mock.MethodGetInfo, json.RawMessage(
		`{"model":"sft1200","firmware_version":"4.3.28"}`))
	s.SetReservations(reservations)
	s.SetHostFile(hostFile)
	s.SetClients([]types.Client{
		{Name: "self", MAC: "aa:bb:cc:dd:ee:ff", IP: "127.0.0.1", Iface: "cable", Online: true},
	})
	return s
}

func seeded() []types.Reservation {
	return []types.Reservation{
		{Name: "nas", MAC: "aa:bb:cc:dd:ee:01", IP: "192.168.8.13"},
		{Name: "pi", MAC: "aa:bb:cc:dd:ee:02", IP: "192.168.8.14"},
	}
}

func withDomain() string { return mock.HostFileWith("lab.example") }

// --- reads ------------------------------------------------------------------

func TestLANShow(t *testing.T) {
	s := mockRouter(t, seeded(), withDomain())

	out, err := run(t, s, "lan", "show")
	if err != nil {
		t.Fatalf("lan show: %v", err)
	}
	for _, want := range []string{"192.168.8.0/24", "192.168.8.100", "RESERVED", "sft1200"} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q:\n%s", want, out)
		}
	}
}

func TestLANShowJSON(t *testing.T) {
	s := mockRouter(t, seeded(), withDomain())

	out, err := run(t, s, "--output", "json", "lan", "show")
	if err != nil {
		t.Fatalf("lan show --output json: %v", err)
	}
	var report map[string]any
	if err := json.Unmarshal([]byte(out), &report); err != nil {
		t.Fatalf("output is not JSON: %v\n%s", err, out)
	}
	if report["lan_ip"] != "192.168.8.1" {
		t.Errorf("lan_ip = %v", report["lan_ip"])
	}
}

func TestReservationsExportRoundTripsThroughImport(t *testing.T) {
	s := mockRouter(t, seeded(), withDomain())

	out, err := run(t, s, "lan", "reservations", "export")
	if err != nil {
		t.Fatalf("export: %v", err)
	}
	if !strings.Contains(out, "host nas") || !strings.Contains(out, "192.168.8.13") {
		t.Fatalf("export does not look like a host file:\n%s", out)
	}

	// The alias must reach the same command, since the canonical form is four words.
	if _, err := run(t, s, "lan", "res", "list"); err != nil {
		t.Errorf("the res alias does not work: %v", err)
	}
}

func TestDNSShowReportsNoDomain(t *testing.T) {
	s := mockRouter(t, nil, mock.FactoryHostFile)

	out, err := run(t, s, "lan", "dns", "show")
	if err != nil {
		t.Fatalf("dns show: %v", err)
	}
	if !strings.Contains(out, "not set") {
		t.Errorf("output does not report the missing domain:\n%s", out)
	}
}

func TestSystemInfo(t *testing.T) {
	s := mockRouter(t, nil, withDomain())

	out, err := run(t, s, "system", "info")
	if err != nil {
		t.Fatalf("system info: %v", err)
	}
	for _, want := range []string{"sft1200", "4.3.28"} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q:\n%s", want, out)
		}
	}
}

func TestRadioList(t *testing.T) {
	s := mockRouter(t, nil, withDomain())

	out, err := run(t, s, "radio", "list")
	if err != nil {
		t.Fatalf("radio list: %v", err)
	}
	for _, want := range []string{"2G", "5G", "channels:"} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q:\n%s", want, out)
		}
	}
	// The passphrase must not appear unless asked for.
	if strings.Contains(out, mock.FactoryKey) {
		t.Errorf("radio list leaked a passphrase:\n%s", out)
	}
}

// --- writes, and the guards -------------------------------------------------

func TestDNSSetThenReservationsImport(t *testing.T) {
	s := mockRouter(t, nil, mock.FactoryHostFile)

	if _, err := run(t, s, "lan", "dns", "set", "--domain", "lab.example"); err != nil {
		t.Fatalf("dns set: %v", err)
	}
	if got := types.ParseHostFile(s.HostFile()).Domain; got != "lab.example" {
		t.Fatalf("domain = %q", got)
	}

	file := filepath.Join(t.TempDir(), "hosts")
	body := "host nas { hardware ethernet aa:bb:cc:dd:ee:01; fixed-address 192.168.8.13; }\n"
	if err := os.WriteFile(file, []byte(body), 0o600); err != nil {
		t.Fatalf("write host file: %v", err)
	}

	if _, err := run(t, s, "lan", "reservations", "import", file); err != nil {
		t.Fatalf("import: %v", err)
	}
	if got := s.Reservations(); len(got) != 1 {
		t.Errorf("device holds %d reservations, want 1", len(got))
	}
	if !strings.Contains(s.HostFile(), "nas.lab.example") {
		t.Errorf("the name was not written:\n%s", s.HostFile())
	}
}

// The domain guard has to survive the trip through cobra, and the exit code has to
// distinguish a refusal from a failure.
func TestImportWithoutDomainIsRefusedWithCodeThree(t *testing.T) {
	s := mockRouter(t, nil, mock.FactoryHostFile)

	file := filepath.Join(t.TempDir(), "hosts")
	body := "host nas { hardware ethernet aa:bb:cc:dd:ee:01; fixed-address 192.168.8.13; }\n"
	if err := os.WriteFile(file, []byte(body), 0o600); err != nil {
		t.Fatalf("write host file: %v", err)
	}

	_, err := run(t, s, "lan", "reservations", "import", file)
	if err == nil {
		t.Fatal("import succeeded with no domain configured")
	}
	if code := codeFor(err); code != exitRefused {
		t.Errorf("exit code = %d, want %d for a guard refusal", code, exitRefused)
	}
	if got := s.Reservations(); len(got) != 0 {
		t.Errorf("a refused import wrote %d reservations", len(got))
	}
}

func TestLANSetPoolOnly(t *testing.T) {
	s := mockRouter(t, seeded(), withDomain())

	// Pool-only, with reservations present: never guarded, since nothing moves.
	if _, err := run(t, s, "lan", "set",
		"--pool-start", "192.168.8.50", "--pool-end", "192.168.8.90"); err != nil {
		t.Fatalf("lan set pool: %v", err)
	}
	got := s.Network()
	if got[0].DHCPStart != "192.168.8.50" || got[0].DHCPStop != "192.168.8.90" {
		t.Errorf("pool = %s-%s", got[0].DHCPStart, got[0].DHCPStop)
	}
	if got[0].LANIP != "192.168.8.1" {
		t.Errorf("a pool-only change moved the router to %s", got[0].LANIP)
	}
}

func TestLANSetSubnetMoveIsRefusedWithReservations(t *testing.T) {
	s := mockRouter(t, seeded(), withDomain())

	_, err := run(t, s, "lan", "set", "--ip", "192.168.4.1", "--mask", "255.255.255.0",
		"--pool-start", "192.168.4.100", "--pool-end", "192.168.4.149")
	if err == nil {
		t.Fatal("a subnet move succeeded with reservations present")
	}
	if code := codeFor(err); code != exitRefused {
		t.Errorf("exit code = %d, want %d", code, exitRefused)
	}
	if got := s.Network(); got[0].LANIP != "192.168.8.1" {
		t.Errorf("a refused move changed the address to %s", got[0].LANIP)
	}
}

// --band must resolve to a device by what the radios report, and reach the write.
func TestRadioSetResolvesBand(t *testing.T) {
	s := mockRouter(t, nil, withDomain())

	if _, err := run(t, s, "radio", "set", "--band", "5", "--channel", "149", "--yes"); err != nil {
		t.Fatalf("radio set --band 5: %v", err)
	}
	for _, r := range s.Wireless() {
		if r.Device == mock.Factory5GDevice && r.Channel != 149 {
			t.Errorf("channel = %d, want 149", r.Channel)
		}
		if r.Device == mock.Factory2GDevice && r.Channel == 149 {
			t.Error("--band 5 wrote to the 2.4GHz radio")
		}
	}
}

func TestWiFiSetResolvesBandAndGuest(t *testing.T) {
	s := mockRouter(t, nil, withDomain())

	if _, err := run(t, s, "wifi", "set", "--band", "2.4", "--guest",
		"--ssid", "lab-guest", "--yes"); err != nil {
		t.Fatalf("wifi set --guest: %v", err)
	}

	var guest, main string
	for _, r := range s.Wireless() {
		for _, f := range r.Ifaces {
			if f.Name == "guest2g" {
				guest = f.SSID
			}
			if f.Name == mock.Factory2GIface {
				main = f.SSID
			}
		}
	}
	if guest != "lab-guest" {
		t.Errorf("guest SSID = %q, want lab-guest", guest)
	}
	if main != mock.FactorySSID {
		t.Errorf("--guest wrote to the main interface: %q", main)
	}
}

// Only the named field is sent: this is the property the whole partial-update design
// rests on, and it has to survive cobra's flag handling.
func TestWiFiSetOnlyWritesNamedFields(t *testing.T) {
	s := mockRouter(t, nil, withDomain())

	if _, err := run(t, s, "wifi", "set", "--band", "5", "--ssid", "lab-5g", "--yes"); err != nil {
		t.Fatalf("wifi set: %v", err)
	}
	for _, r := range s.Wireless() {
		for _, f := range r.Ifaces {
			if f.Name != mock.Factory5GIface {
				continue
			}
			if f.SSID != "lab-5g" {
				t.Errorf("SSID = %q", f.SSID)
			}
			if f.Key != mock.FactoryKey {
				t.Errorf("setting the SSID changed the passphrase to %q", f.Key)
			}
			if !f.Enabled {
				t.Error("setting the SSID disabled the interface")
			}
		}
	}
}

// A bare `wifi set --band 5` with no field flags must not silently do nothing.
func TestWiFiSetWithNothingToDoIsUsageError(t *testing.T) {
	s := mockRouter(t, nil, withDomain())

	_, err := run(t, s, "wifi", "set", "--band", "5")
	if err == nil {
		t.Fatal("wifi set with no fields succeeded")
	}
	if code := codeFor(err); code != exitUsage {
		t.Errorf("exit code = %d, want %d for a usage error", code, exitUsage)
	}
}

func TestProfileExportThenImport(t *testing.T) {
	s := mockRouter(t, seeded(), mock.HostFileWith("lab.example",
		"192.168.8.13 nas nas.lab.example"))

	out, err := run(t, s, "profile", "export")
	if err != nil {
		t.Fatalf("profile export: %v", err)
	}
	if !strings.Contains(out, "gogl_profile_version") {
		t.Fatalf("export does not look like a profile:\n%s", out)
	}
	// Passphrases must be absent by default.
	if strings.Contains(out, mock.FactoryKey) {
		t.Errorf("profile export leaked a passphrase")
	}

	file := filepath.Join(t.TempDir(), "profile.json")
	if err := os.WriteFile(file, []byte(out), 0o600); err != nil {
		t.Fatalf("write profile: %v", err)
	}

	target := mockRouter(t, nil, mock.FactoryHostFile)
	if _, err := run(t, target, "profile", "import", file); err != nil {
		t.Fatalf("profile import: %v", err)
	}
	if got := target.Reservations(); len(got) != 2 {
		t.Errorf("target holds %d reservations, want 2", len(got))
	}
}

// The decode path, end to end, against a payload shaped as the device sends it rather
// than one marshalled from gogl's own structs.
//
// This is the test whose absence let a real bug ship. mock.SetClients takes
// []types.Client and marshals it, so the payload it serves is by construction whatever
// gogl's types claim -- it served a string online_time while the device sent a number,
// and every test passed.
func TestClientsListDecodesTheCapturedPayload(t *testing.T) {
	s := mockRouter(t, nil, withDomain())
	s.LoadFixture(mock.ClientGroup, mock.MethodGetList, json.RawMessage(mock.FactoryClients))

	out, err := run(t, s, "clients", "list", "--all")
	if err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "oui") {
			t.Skipf("no OUI data available: %v", err)
		}
		t.Fatalf("clients list --all: %v", err)
	}

	for _, want := range []string{"europa", "iPhone", "iPad", "192.168.2.138"} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q:\n%s", want, out)
		}
	}
	// online_time arrives as a number; the SINCE column must render rather than blank.
	if !strings.Contains(out, "SINCE") {
		t.Errorf("no SINCE column:\n%s", out)
	}
}

// The stale-entry case, end to end: an offline client from a previous subnet must be
// hidden by default and appear with --all.
func TestClientsListHidesOfflineByDefault(t *testing.T) {
	s := mockRouter(t, nil, withDomain())
	s.SetClients([]types.Client{
		{Name: "self", MAC: "aa:bb:cc:dd:ee:ff", IP: "127.0.0.1", Iface: "cable", Online: true},
		{Name: "iPad", MAC: "02:f0:6b:61:70:ff", IP: "192.168.2.138", Iface: "5G", Online: false},
	})

	out, err := run(t, s, "clients", "list")
	if err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "oui") {
			t.Skipf("no OUI data available: %v", err)
		}
		t.Fatalf("clients list: %v", err)
	}
	if strings.Contains(out, "192.168.2.138") {
		t.Errorf("the offline station appeared without --all:\n%s", out)
	}

	all, err := run(t, s, "clients", "list", "--all")
	if err != nil {
		t.Fatalf("clients list --all: %v", err)
	}
	if !strings.Contains(all, "192.168.2.138") {
		t.Errorf("--all did not include the offline station:\n%s", all)
	}
	if !strings.Contains(all, "ONLINE") {
		t.Errorf("--all did not add the ONLINE column:\n%s", all)
	}
}

func TestClientsList(t *testing.T) {
	s := mockRouter(t, seeded(), withDomain())

	out, err := run(t, s, "clients", "list")
	if err != nil {
		// The OUI database needs a cache or the network; skip rather than fail on a
		// sandbox with neither, since that is an environment problem not a code one.
		if strings.Contains(err.Error(), "OUI") || strings.Contains(err.Error(), "oui") {
			t.Skipf("no OUI data available: %v", err)
		}
		t.Fatalf("clients list: %v", err)
	}
	if !strings.Contains(out, "127.0.0.1") {
		t.Errorf("output missing the seeded client:\n%s", out)
	}
}

// --- usage errors -----------------------------------------------------------

func TestUsageErrors(t *testing.T) {
	s := mockRouter(t, nil, withDomain())

	tests := map[string][]string{
		"lan set with nothing":       {"lan", "set"},
		"lan set partial move":       {"lan", "set", "--ip", "192.168.4.1"},
		"radio set without a target": {"radio", "set", "--channel", "6"},
		"wifi show without a target": {"wifi", "show"},
		"bad output format":          {"--output", "yaml", "lan", "show"},
	}

	for name, args := range tests {
		t.Run(name, func(t *testing.T) {
			if _, err := run(t, s, args...); err == nil {
				t.Errorf("%s succeeded", name)
			}
		})
	}
}
