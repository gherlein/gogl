package netcfg

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/gherlein/gogl/src/types"
)

func testReport() *Report {
	return &Report{
		Model:          "gl-sft1200",
		Firmware:       "4.3.28",
		LANIP:          "192.168.8.1",
		Netmask:        "255.255.255.0",
		Subnet:         "192.168.8.0/24",
		DHCPEnabled:    true,
		DHCPStart:      "192.168.8.100",
		DHCPStop:       "192.168.8.249",
		PoolSize:       150,
		DHCPLease:      types.LeaseTime(12 * time.Hour),
		Interface:      "lan",
		DNS:            []string{"192.168.8.1"},
		ReservedCount:  38,
		AvailableCount: 65,
	}
}

func TestFormatText(t *testing.T) {
	var buf bytes.Buffer
	if err := FormatText(&buf, testReport()); err != nil {
		t.Fatalf("FormatText error: %v", err)
	}
	out := buf.String()

	for _, want := range []string{
		"gl-sft1200", "4.3.28", "192.168.8.0/24",
		"192.168.8.100 - 192.168.8.249", "150", "12h", "lan", "38", "65",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q:\n%s", want, out)
		}
	}
}

// A disabled DHCP server must read as disabled rather than showing a stale pool.
func TestFormatTextDHCPDisabled(t *testing.T) {
	r := testReport()
	r.DHCPEnabled = false

	var buf bytes.Buffer
	if err := FormatText(&buf, r); err != nil {
		t.Fatalf("FormatText error: %v", err)
	}
	if !strings.Contains(buf.String(), disabledMarker) {
		t.Errorf("output does not mark DHCP disabled:\n%s", buf.String())
	}
}

func TestFormatTextEmptyFieldsShowDash(t *testing.T) {
	r := testReport()
	r.Model, r.Firmware, r.Interface, r.DNS = "", "", "", nil

	var buf bytes.Buffer
	if err := FormatText(&buf, r); err != nil {
		t.Fatalf("FormatText error: %v", err)
	}
	if !strings.Contains(buf.String(), emptyMarker) {
		t.Errorf("empty fields not marked with %q:\n%s", emptyMarker, buf.String())
	}
}

func TestFormatJSONIsAnObject(t *testing.T) {
	var buf bytes.Buffer
	if err := FormatJSON(&buf, testReport()); err != nil {
		t.Fatalf("FormatJSON error: %v", err)
	}

	// gofinet emits an array because a UDM Pro has many networks. goglnet emits
	// an object because the travel router has one LAN. Consumers of both must
	// handle the difference, so this is worth pinning.
	trimmed := strings.TrimSpace(buf.String())
	if !strings.HasPrefix(trimmed, "{") {
		t.Errorf("JSON output is not an object: %s", trimmed)
	}

	var decoded map[string]any
	if err := json.Unmarshal(buf.Bytes(), &decoded); err != nil {
		t.Fatalf("output is not valid JSON: %v", err)
	}
	if decoded["model"] != "gl-sft1200" {
		t.Errorf("model = %v, want gl-sft1200", decoded["model"])
	}
	if decoded["dhcp_lease"] != "12h" {
		t.Errorf("dhcp_lease = %v, want the compact duration string 12h", decoded["dhcp_lease"])
	}
	if decoded["reserved_count"] != float64(38) {
		t.Errorf("reserved_count = %v, want 38", decoded["reserved_count"])
	}
}

// Counts must survive even at zero, since "0 available" is meaningful and
// omitting it would read as "unknown".
func TestFormatJSONKeepsZeroCounts(t *testing.T) {
	r := testReport()
	r.ReservedCount, r.AvailableCount = 0, 0

	var buf bytes.Buffer
	if err := FormatJSON(&buf, r); err != nil {
		t.Fatalf("FormatJSON error: %v", err)
	}

	var decoded map[string]any
	if err := json.Unmarshal(buf.Bytes(), &decoded); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	for _, key := range []string{"reserved_count", "available_count"} {
		if _, present := decoded[key]; !present {
			t.Errorf("field %q should be present even at zero", key)
		}
	}
}
