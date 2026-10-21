package reservations

import (
	"bytes"
	"strings"
	"testing"

	"github.com/gherlein/gogl/src/types"
)

func deviceState() []types.Reservation {
	return []types.Reservation{
		{Name: "nas", MAC: "aa:bb:cc:dd:ee:01", IP: "192.168.8.13"},
		{Name: "printer", MAC: "aa:bb:cc:dd:ee:02", IP: "192.168.8.14"},
	}
}

func TestFindTargetByName(t *testing.T) {
	got, err := findTarget(deviceState(), Modes{Name: "nas"})
	if err != nil {
		t.Fatalf("findTarget error: %v", err)
	}
	if len(got) != 1 || got[0].MAC != "aa:bb:cc:dd:ee:01" {
		t.Errorf("got %v, want nas", got)
	}
}

func TestFindTargetByMAC(t *testing.T) {
	// Case-insensitive: the caller should not have to normalize first.
	got, err := findTarget(deviceState(), Modes{MAC: "AA:BB:CC:DD:EE:02"})
	if err != nil {
		t.Fatalf("findTarget error: %v", err)
	}
	if len(got) != 1 || got[0].Name != "printer" {
		t.Errorf("got %v, want printer", got)
	}
}

func TestFindTargetByIP(t *testing.T) {
	got, err := findTarget(deviceState(), Modes{IP: "192.168.8.13"})
	if err != nil {
		t.Fatalf("findTarget error: %v", err)
	}
	if len(got) != 1 || got[0].Name != "nas" {
		t.Errorf("got %v, want nas", got)
	}
}

func TestFindTargetRequiresExactlyOneIdentifier(t *testing.T) {
	for _, modes := range []Modes{
		{},
		{Name: "nas", MAC: "aa:bb:cc:dd:ee:01"},
		{Name: "nas", IP: "192.168.8.13"},
		{MAC: "aa:bb:cc:dd:ee:01", IP: "192.168.8.13"},
		{Name: "nas", MAC: "aa:bb:cc:dd:ee:01", IP: "192.168.8.13"},
	} {
		if _, err := findTarget(deviceState(), modes); err == nil {
			t.Errorf("findTarget(%+v) succeeded, want an error", modes)
		}
	}
}

func TestFindTargetNotFound(t *testing.T) {
	_, err := findTarget(deviceState(), Modes{Name: "ghost"})
	if err == nil {
		t.Fatal("findTarget succeeded for a missing host")
	}
	if !strings.Contains(err.Error(), "ghost") {
		t.Errorf("error %q does not name the target", err)
	}
}

func TestFindTargetRejectsBadMAC(t *testing.T) {
	if _, err := findTarget(deviceState(), Modes{MAC: "not-a-mac"}); err == nil {
		t.Error("findTarget accepted an invalid MAC")
	}
}

// Multiple matches are refused without --force, because deleting the wrong
// reservation is not recoverable from inside the tool.
func TestFindTargetMultipleMatches(t *testing.T) {
	device := []types.Reservation{
		{Name: "a", MAC: "aa:bb:cc:dd:ee:01", IP: "192.168.8.13"},
		{Name: "b", MAC: "aa:bb:cc:dd:ee:02", IP: "192.168.8.13"},
	}

	_, err := findTarget(device, Modes{IP: "192.168.8.13"})
	if err == nil {
		t.Fatal("findTarget accepted an ambiguous match without --force")
	}
	// The error must list what it found, so the operator can pick.
	if !strings.Contains(err.Error(), "aa:bb:cc:dd:ee:01") || !strings.Contains(err.Error(), "--force") {
		t.Errorf("error does not list the matches and the remedy: %q", err)
	}

	got, err := findTarget(device, Modes{IP: "192.168.8.13", Force: true})
	if err != nil {
		t.Fatalf("findTarget with --force: %v", err)
	}
	if len(got) != 2 {
		t.Errorf("got %d matches with --force, want 2", len(got))
	}
}

func TestDescribeTarget(t *testing.T) {
	tests := []struct {
		modes Modes
		want  string
	}{
		{Modes{Name: "nas"}, "hostname"},
		{Modes{MAC: "aa:bb:cc:dd:ee:01"}, "MAC"},
		{Modes{IP: "192.168.8.13"}, "address"},
	}
	for _, tt := range tests {
		if got := describeTarget(tt.modes); !strings.Contains(got, tt.want) {
			t.Errorf("describeTarget(%+v) = %q, want it to mention %q", tt.modes, got, tt.want)
		}
	}
}

func TestParseFragment(t *testing.T) {
	const fragment = `host mydevice {
    hardware ethernet aa:bb:cc:dd:ee:ff;
    fixed-address 192.168.8.50;
}`
	got, err := parseFragment(strings.NewReader(fragment))
	if err != nil {
		t.Fatalf("parseFragment error: %v", err)
	}
	if got.Name != "mydevice" || got.IP != "192.168.8.50" {
		t.Errorf("got %+v", got)
	}
}

// A fragment must contain exactly one declaration: --add adds one host.
func TestParseFragmentRejectsMultiple(t *testing.T) {
	const fragment = `host a {
    hardware ethernet aa:bb:cc:dd:ee:01;
    fixed-address 192.168.8.50;
}
host b {
    hardware ethernet aa:bb:cc:dd:ee:02;
    fixed-address 192.168.8.51;
}`
	if _, err := parseFragment(strings.NewReader(fragment)); err == nil {
		t.Error("parseFragment accepted two declarations")
	}
}

func TestParseFragmentRejectsEmpty(t *testing.T) {
	for _, input := range []string{"", "# nothing here\n"} {
		if _, err := parseFragment(strings.NewReader(input)); err == nil {
			t.Errorf("parseFragment(%q) accepted an empty fragment", input)
		}
	}
}

func TestParseFragmentRejectsMalformed(t *testing.T) {
	const fragment = "host bad {\n hardware ethernet nope;\n fixed-address 192.168.8.50;\n}"
	if _, err := parseFragment(strings.NewReader(fragment)); err == nil {
		t.Error("parseFragment accepted a bad MAC")
	}
}

func TestCheckAddConflicts(t *testing.T) {
	device := deviceState()

	tests := []struct {
		name string
		res  types.Reservation
		want string
	}{
		{"ip taken", types.Reservation{Name: "new", MAC: "aa:bb:cc:dd:ee:09", IP: "192.168.8.13"}, "already reserved"},
		{"mac taken", types.Reservation{Name: "new", MAC: "aa:bb:cc:dd:ee:01", IP: "192.168.8.99"}, "already reserved"},
		{"name taken", types.Reservation{Name: "nas", MAC: "aa:bb:cc:dd:ee:09", IP: "192.168.8.99"}, "already used"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			res := tt.res
			err := checkAddConflicts(&res, device)
			if err == nil {
				t.Fatalf("no conflict reported, want one mentioning %q", tt.want)
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Errorf("error %q does not mention %q", err, tt.want)
			}
		})
	}
}

func TestCheckAddConflictsClean(t *testing.T) {
	res := types.Reservation{Name: "camera", MAC: "aa:bb:cc:dd:ee:09", IP: "192.168.8.99"}
	if err := checkAddConflicts(&res, deviceState()); err != nil {
		t.Errorf("checkAddConflicts = %v, want nil", err)
	}
}

// Re-adding the identical entry is not a conflict: --add should be idempotent for
// an unchanged host.
func TestCheckAddConflictsIdenticalEntry(t *testing.T) {
	res := types.Reservation{Name: "nas", MAC: "aa:bb:cc:dd:ee:01", IP: "192.168.8.13"}
	if err := checkAddConflicts(&res, deviceState()); err != nil {
		t.Errorf("re-adding an identical entry reported a conflict: %v", err)
	}
}

func TestConfirmAccepts(t *testing.T) {
	for _, answer := range []string{"y\n", "Y\n"} {
		var out bytes.Buffer
		if err := confirm(strings.NewReader(answer), &out); err != nil {
			t.Errorf("confirm(%q) = %v, want nil", answer, err)
		}
		if !strings.Contains(out.String(), "Proceed?") {
			t.Errorf("confirm did not prompt: %q", out.String())
		}
	}
}

func TestConfirmRejects(t *testing.T) {
	for _, answer := range []string{"n\n", "no\n", "\n", "anything\n", ""} {
		var out bytes.Buffer
		if err := confirm(strings.NewReader(answer), &out); err == nil {
			t.Errorf("confirm(%q) = nil, want an abort", answer)
		}
	}
}
