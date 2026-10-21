package clients

import (
	"net"
	"sort"
	"strings"
	"time"

	"github.com/gherlein/gogl/src/ipmath"
	"github.com/gherlein/gogl/src/types"
)

// Entry is one row of goglmac output. JSON tags define the -j contract.
//
// The field set is narrower than gofimac's because the router reports less than a
// UniFi controller does. A field is added only when the firmware is observed
// returning it; nothing is invented for parity.
type Entry struct {
	MAC          string `json:"mac"`
	IP           string `json:"ip,omitempty"`
	Name         string `json:"hostname"`
	Manufacturer string `json:"manufacturer"`
	IsWired      bool   `json:"is_wired"`
	Online       bool   `json:"online"`

	// Since renders how long the client has been online, when the firmware's
	// online_time can be understood. Empty when it cannot; see types.Client.SinceOnline.
	Since    string `json:"since,omitempty"`
	Reserved bool   `json:"reserved,omitempty"`
	Blocked  bool   `json:"blocked,omitempty"`
	RXBytes  uint64 `json:"rx_bytes,omitempty"`
	TXBytes  uint64 `json:"tx_bytes,omitempty"`
	Band     string `json:"band,omitempty"`
}

// Filter selects which clients to report.
type Filter func(types.Client) bool

func FilterAll(types.Client) bool     { return true }
func FilterWired(c types.Client) bool { return c.IsWired() }
func FilterWiFi(c types.Client) bool  { return !c.IsWired() }

// FilterOnline keeps only clients the router reports as connected.
//
// This is the default, and the reason is a real observation: a router that had been
// renumbered from 192.168.2.0/24 to 192.168.8.0/24 still listed a client at
// 192.168.2.138. The list carries history, and presenting history as current state is
// the misleading case.
func FilterOnline(c types.Client) bool { return c.Online }

// and composes two filters, keeping a client only when both accept it.
func and(a, b Filter) Filter {
	return func(c types.Client) bool { return a(c) && b(c) }
}

// BuildEntries joins clients with the OUI database and the reservation table,
// sorted numerically by address with address-less clients last.
//
// The manufacturer always comes from our own OUI lookup, never from any value the
// router reports: on an OpenWrt 18.06 base the router's own table is likely to be
// years stale.
func BuildEntries(clients []types.Client, reservations []types.Reservation, db OUIDatabase, keep Filter) []Entry {
	return buildEntriesAt(clients, reservations, db, keep, time.Now())
}

// buildEntriesAt takes the clock explicitly so the online-duration rendering can be
// tested without depending on when the test runs.
func buildEntriesAt(clients []types.Client, reservations []types.Reservation, db OUIDatabase, keep Filter, now time.Time) []Entry {
	reserved := make(map[string]bool, len(reservations))
	for _, r := range reservations {
		reserved[strings.ToLower(r.MAC)] = true
	}

	entries := make([]Entry, 0, len(clients))
	for i := range clients {
		c := clients[i]
		if !keep(c) {
			continue
		}
		mac := strings.ToLower(c.MAC)
		entries = append(entries, Entry{
			MAC:          mac,
			IP:           c.IP,
			Name:         c.Hostname(),
			Manufacturer: db.Lookup(c.MAC),
			IsWired:      c.IsWired(),
			Online:       c.Online,
			Since:        sinceOnline(c, now),
			Reserved:     reserved[mac],
			Blocked:      c.Blocked,
			RXBytes:      c.RXBytes.Uint64(),
			TXBytes:      c.TXBytes.Uint64(),
			Band:         c.Band(),
		})
	}

	sort.SliceStable(entries, func(i, j int) bool {
		left, right := net.ParseIP(entries[i].IP), net.ParseIP(entries[j].IP)
		// Clients without an address sort last: they are the least useful rows and
		// would otherwise all collide at position zero.
		switch {
		case left == nil && right == nil:
			return entries[i].MAC < entries[j].MAC
		case left == nil:
			return false
		case right == nil:
			return true
		}
		return ipmath.ToUint32(left) < ipmath.ToUint32(right)
	})

	return entries
}

// sinceOnline renders how long a client has been connected, or nothing when the
// firmware's value cannot be understood.
func sinceOnline(c types.Client, now time.Time) string {
	rendered, ok := c.SinceOnline(now)
	if !ok {
		return ""
	}
	return rendered
}
