package netcfg

import (
	"context"
	"fmt"
	"strings"

	"github.com/gherlein/gogl/src/services"
	"github.com/gherlein/gogl/src/types"
)

// NormalizeBand accepts the spellings an operator will reach for and returns the band
// as the firmware reports it.
//
// "2.4", "2.4g", "2g", "24" all mean the same radio, and requiring the firmware's exact
// "2G" would be a needless trap.
func NormalizeBand(band string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(band)) {
	case "2", "2.4", "2g", "2.4g", "24", "2.4ghz":
		return types.Band2G, nil
	case "5", "5g", "5ghz":
		return types.Band5G, nil
	case "6", "6g", "6ghz":
		// No captured device has a 6GHz radio, but the resolution below is by reported
		// band rather than by a static map, so one would work if present.
		return "6G", nil
	default:
		return "", fmt.Errorf("%w: band %q; use 2.4, 5 or 6", types.ErrInvalidInput, band)
	}
}

// ResolveBand finds the radio device and interface for a band.
//
// Resolution is by the band each radio reports, never by a static radio0/radio1 map:
// nothing guarantees radio0 is the 2.4GHz radio across the product line, and a model
// with three radios would break a fixed mapping silently.
//
// guest selects the guest interface on that radio rather than the main one.
func ResolveBand(ctx context.Context, w services.WirelessService, band string, guest bool) (device, iface string, err error) {
	wanted, err := NormalizeBand(band)
	if err != nil {
		return "", "", err
	}

	radios, err := w.Radios(ctx)
	if err != nil {
		return "", "", err
	}

	var matches []types.WirelessRadio
	var available []string
	for _, r := range radios {
		available = append(available, r.Band)
		if r.Band == wanted {
			matches = append(matches, r)
		}
	}

	switch len(matches) {
	case 0:
		return "", "", fmt.Errorf("%w: no %s radio on this router (has: %s)",
			types.ErrNotFound, wanted, strings.Join(available, ", "))
	case 1:
	default:
		// Two radios reporting one band is ambiguous, and picking either would
		// eventually write to the wrong one.
		return "", "", fmt.Errorf("%w: %d radios report band %s; name one with --device",
			types.ErrInvalidInput, len(matches), wanted)
	}

	radio := matches[0]
	for _, f := range radio.Ifaces {
		if f.Guest == guest {
			return radio.Device, f.Name, nil
		}
	}

	kind := "main"
	if guest {
		kind = "guest"
	}
	return radio.Device, "", fmt.Errorf("%w: the %s radio has no %s interface",
		types.ErrNotFound, wanted, kind)
}
