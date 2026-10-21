package mock

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/gherlein/gogl/src/types"
)

// Every fixture the mock serves must decode through the types the library uses.
//
// This is the check that was missing. The wireless fixture was hand-written from the
// vendored API description, the types were written from the same description, and the
// two agreed with each other while both disagreed with the device. Asserting the
// fixture parses proves only self-consistency -- but combined with the fixture being
// a verbatim capture, it proves the types match the hardware.
func TestFactoryWirelessDecodesThroughTheRealTypes(t *testing.T) {
	var cfg struct {
		DFSSupport bool                  `json:"dfs_support"`
		Res        []types.WirelessRadio `json:"res"`
	}
	if err := json.Unmarshal([]byte(FactoryWireless), &cfg); err != nil {
		t.Fatalf("FactoryWireless does not decode: %v", err)
	}

	if len(cfg.Res) != 2 {
		t.Fatalf("got %d radios, want 2", len(cfg.Res))
	}

	for _, r := range cfg.Res {
		if r.Device == "" || r.Band == "" {
			t.Errorf("radio has no device or band: %+v", r)
		}
		if len(r.Channels) == 0 {
			t.Errorf("radio %s lists no channels", r.Device)
		}
		if len(r.HWModes) == 0 {
			t.Errorf("radio %s lists no hardware modes", r.Device)
		}
		// The bug: htmodes decoded to nothing while the read appeared to succeed.
		if len(r.HTModes.MaxWidthMHz) == 0 {
			t.Errorf("radio %s decoded no htmodes; the field shape is wrong", r.Device)
		}
		if len(r.HTModeOptions()) == 0 {
			t.Errorf("radio %s offers no settable bandwidth", r.Device)
		}
		if len(r.Ifaces) == 0 {
			t.Errorf("radio %s has no interfaces", r.Device)
		}
	}

	// The named constants must match what the fixture actually contains, or tests
	// asserting against them are asserting against nothing.
	found := false
	for _, r := range cfg.Res {
		for _, f := range r.Ifaces {
			if f.Name == Factory2GIface {
				found = true
				if f.SSID != FactorySSID {
					t.Errorf("FactorySSID is %q but the fixture has %q", FactorySSID, f.SSID)
				}
				if f.Key != FactoryKey {
					t.Errorf("FactoryKey is %q but the fixture has %q", FactoryKey, f.Key)
				}
			}
		}
	}
	if !found {
		t.Errorf("Factory2GIface %q is not in the fixture", Factory2GIface)
	}
}

func TestDFSWirelessDecodesAndHasADFSChannel(t *testing.T) {
	var cfg struct {
		Res []types.WirelessRadio `json:"res"`
	}
	if err := json.Unmarshal([]byte(DFSWireless), &cfg); err != nil {
		t.Fatalf("DFSWireless does not decode: %v", err)
	}

	// The whole point of this fixture: the shipped unit reports no DFS channel, so
	// without it the DFS warning has nothing to fire on.
	any := false
	for _, r := range cfg.Res {
		for _, c := range r.Channels {
			if c.DFS {
				any = true
				if !r.IsDFS(c.Channel) {
					t.Errorf("IsDFS(%d) is false on a channel marked dfs", c.Channel)
				}
			}
		}
	}
	if !any {
		t.Error("DFSWireless contains no DFS channel")
	}
}

// The host file the mock ships must be writable by the firmware's own rules, or every
// test starts from a state the device would refuse.
func TestFactoryHostFileIsWritable(t *testing.T) {
	if err := types.ValidateContent(FactoryHostFile); err != nil {
		t.Errorf("FactoryHostFile would be rejected by the firmware: %v", err)
	}
	// It must also be the factory state: no gogl block, so "domain not set" is what a
	// fresh mock reports.
	if strings.Contains(FactoryHostFile, types.BeginMarker) {
		t.Error("FactoryHostFile carries a gogl block; it should be the factory state")
	}
	if got := types.ParseHostFile(FactoryHostFile).Domain; got != "" {
		t.Errorf("FactoryHostFile reports domain %q, want none", got)
	}
}

func TestHostFileWithProducesWritableContent(t *testing.T) {
	content := HostFileWith("lab.example", "192.168.8.13 nas nas.lab.example")
	if err := types.ValidateContent(content); err != nil {
		t.Errorf("HostFileWith produced content the firmware would reject: %v", err)
	}
	f := types.ParseHostFile(content)
	if f.Domain != "lab.example" {
		t.Errorf("domain = %q", f.Domain)
	}
	if len(f.Entries) != 1 {
		t.Errorf("entries = %+v", f.Entries)
	}
}
