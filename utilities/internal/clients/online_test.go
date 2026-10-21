package clients

import (
	"bytes"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gherlein/gogl/src/types"
)

// The online default exists because of a real observation: after the LAN was renumbered
// from 192.168.2.0/24 to 192.168.8.0/24, the router still listed a station at
// 192.168.2.138. The list carries history, and showing history as current state is the
// misleading case.

func mixedClients() []types.Client {
	return []types.Client{
		{MAC: "10:51:07:1f:8d:1c", IP: "192.168.8.10", Name: "europa", Iface: types.IfaceCable, Online: true},
		{MAC: "02:f0:6b:61:70:ff", IP: "192.168.2.138", Name: "iPad", Iface: types.Iface5GHz, Online: false},
		{MAC: "6e:1d:47:db:54:54", IP: "192.168.8.135", Name: "iPhone", Iface: types.Iface5GHz, Online: true},
	}
}

func TestFilterOnline(t *testing.T) {
	kept := 0
	for _, c := range mixedClients() {
		if FilterOnline(c) {
			kept++
		}
	}
	if kept != 2 {
		t.Errorf("FilterOnline kept %d of 3, want 2", kept)
	}
}

// The stale entry from the old subnet is exactly what the default must hide.
func TestBuildEntriesWithOnlineFilterDropsTheStaleEntry(t *testing.T) {
	entries := BuildEntries(mixedClients(), nil, OUIDatabase{}, and(FilterAll, FilterOnline))

	for _, e := range entries {
		if e.IP == "192.168.2.138" {
			t.Errorf("the offline station from the old subnet survived: %+v", e)
		}
	}
	if len(entries) != 2 {
		t.Errorf("got %d entries, want 2", len(entries))
	}
}

func TestAndComposesFilters(t *testing.T) {
	// Wireless and online: the iPad is wireless but offline, so only the iPhone.
	keep := and(FilterWiFi, FilterOnline)
	entries := BuildEntries(mixedClients(), nil, OUIDatabase{}, keep)

	if len(entries) != 1 || entries[0].Name != "iPhone" {
		t.Errorf("entries = %+v, want just the iPhone", entries)
	}
}

// ---------------------------------------------------------------------------
// online_time, whose format the firmware does not document
// ---------------------------------------------------------------------------

func TestSinceOnlineReadings(t *testing.T) {
	now := time.Date(2026, 7, 30, 12, 0, 0, 0, time.UTC)

	// Computed rather than hardcoded: the first draft of this test used a magic constant
	// that was 240 hours out, and the failure looked like a bug in the code.
	anHourAgo := strconv.FormatInt(now.Add(-time.Hour).Unix(), 10)

	tests := []struct {
		name  string
		value string
		want  string
		ok    bool
	}{
		{"unix timestamp an hour ago", anHourAgo, "1h0m0s", true},
		{"elapsed seconds", "3600", "1h0m0s", true},
		{"a formatted date passes through", "2026-07-30 11:00:00", "2026-07-30 11:00:00", true},
		{"empty is unknown", "", "", false},
		{"zero is unknown", "0", "", false},
		{"negative is unknown", "-5", "", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := types.Client{OnlineTime: types.FlexString(tt.value)}
			got, ok := c.SinceOnline(now)
			if ok != tt.ok {
				t.Fatalf("ok = %t, want %t (got %q)", ok, tt.ok, got)
			}
			if ok && got != tt.want {
				t.Errorf("SinceOnline = %q, want %q", got, tt.want)
			}
		})
	}
}

// A value that cannot be understood must not be rendered as a guess. An earlier field in
// this project -- a lease "expires" holding seconds remaining -- was rendered with
// time.Unix and would have printed 1970 dates.
func TestSinceOnlineDoesNotInventPrecision(t *testing.T) {
	now := time.Date(2026, 7, 30, 12, 0, 0, 0, time.UTC)
	c := types.Client{OnlineTime: types.FlexString("not a time at all")}

	got, ok := c.SinceOnline(now)
	if !ok {
		t.Fatal("an unparseable value was discarded rather than passed through")
	}
	if strings.Contains(got, "1970") || strings.Contains(got, "h0m0s") {
		t.Errorf("an unparseable value was rendered as a duration: %q", got)
	}
}

func TestFormatTextShowsSinceAndOnlineColumns(t *testing.T) {
	entries := []Entry{
		{MAC: "aa:bb:cc:dd:ee:01", IP: "192.168.8.10", Name: "europa",
			Manufacturer: "Intel", Online: true, Since: "3h0m0s"},
		{MAC: "02:f0:6b:61:70:ff", IP: "192.168.2.138", Name: "iPad",
			Manufacturer: "randomized", Online: false},
	}

	var buf bytes.Buffer
	if err := FormatText(&buf, entries, FormatOptions{ShowOnline: true}); err != nil {
		t.Fatalf("FormatText: %v", err)
	}
	got := buf.String()

	for _, want := range []string{"MAC", "SINCE", "ONLINE", "3h0m0s", "yes", "no"} {
		if !strings.Contains(got, want) {
			t.Errorf("output missing %q:\n%s", want, got)
		}
	}
}

// Without --all the caller has already filtered to online, so the column is redundant.
func TestFormatTextOmitsOnlineColumnByDefault(t *testing.T) {
	entries := []Entry{{MAC: "aa:bb:cc:dd:ee:01", IP: "192.168.8.10", Online: true}}

	var buf bytes.Buffer
	if err := FormatText(&buf, entries, FormatOptions{}); err != nil {
		t.Fatalf("FormatText: %v", err)
	}
	if strings.Contains(buf.String(), "ONLINE") {
		t.Errorf("the ONLINE column appeared without --all:\n%s", buf.String())
	}
}
