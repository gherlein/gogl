package clients

import (
	"context"
	"errors"
	"io"

	gogl "github.com/gherlein/gogl/src"
	"github.com/gherlein/gogl/src/types"
	"github.com/gherlein/gogl/utilities/internal/conn"
)

// Options controls what List reports.
type Options struct {
	// Keep selects which clients appear. Nil means all of them.
	Keep Filter

	// ShowReserved marks which clients hold a reservation, at the cost of one extra
	// round trip against a small SoC.
	ShowReserved bool

	// IncludeOffline reports clients the router remembers but does not currently see.
	//
	// Off by default. The client list carries history: a router renumbered from
	// 192.168.2.0/24 to 192.168.8.0/24 was still listing a station at 192.168.2.138,
	// and showing that alongside live clients with no distinction is worse than not
	// showing it at all.
	IncludeOffline bool

	// JSON selects machine-readable output.
	JSON bool
}

// List reports the router's connected clients.
//
// This is the composition the CLI layer needs: load the OUI database, read the
// clients, optionally read reservations, and format. It lives here rather than in the
// command layer so that the ordering below stays covered by this package's tests.
func List(ctx context.Context, client *gogl.Client, out io.Writer, opts Options) error {
	keep := opts.Keep
	if keep == nil {
		keep = FilterAll
	}
	if !opts.IncludeOffline {
		keep = and(keep, FilterOnline)
	}

	// Load the OUI database before touching the device. A stale-cache warning belongs
	// before any device output, and a hard OUI failure should not leave a session open.
	cacheDir, err := conn.OUICacheDir()
	if err != nil {
		return err
	}
	db, err := LoadOUI(cacheDir, nil)
	if err != nil {
		return err
	}

	stations, err := client.Clients().List(ctx)
	if err != nil {
		return err
	}

	var reservations []types.Reservation
	if opts.ShowReserved {
		reservations, err = client.Reservations().List(ctx)
		if err != nil {
			return err
		}
	}

	entries := BuildEntries(stations, reservations, db, keep)

	if opts.JSON {
		return FormatJSON(out, entries)
	}
	return FormatText(out, entries, FormatOptions{
		ShowReserved: opts.ShowReserved,
		// Only worth a column when an offline row can actually appear.
		ShowOnline: opts.IncludeOffline,
	})
}

// FilterFor resolves the mutually exclusive band selectors into a Filter.
//
// Returns an error rather than silently preferring one, because "--wifi --wired" has no
// sensible reading and guessing would quietly report the wrong set.
func FilterFor(wifi, wired bool) (Filter, error) {
	if wifi && wired {
		return nil, errors.New("--wifi and --wired are mutually exclusive")
	}
	switch {
	case wifi:
		return FilterWiFi, nil
	case wired:
		return FilterWired, nil
	default:
		return FilterAll, nil
	}
}
