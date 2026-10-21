package netcfg

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/gherlein/gogl/src/mock"
	"github.com/gherlein/gogl/src/types"
)

func TestNormalizeBand(t *testing.T) {
	for _, spelling := range []string{"2", "2.4", "2g", "2.4G", " 2.4ghz "} {
		got, err := NormalizeBand(spelling)
		if err != nil || got != types.Band2G {
			t.Errorf("NormalizeBand(%q) = %q, %v; want %q", spelling, got, err, types.Band2G)
		}
	}
	for _, spelling := range []string{"5", "5g", "5GHz"} {
		got, err := NormalizeBand(spelling)
		if err != nil || got != types.Band5G {
			t.Errorf("NormalizeBand(%q) = %q, %v; want %q", spelling, got, err, types.Band5G)
		}
	}
	for _, bad := range []string{"", "2.5", "wifi", "60"} {
		if _, err := NormalizeBand(bad); !errors.Is(err, types.ErrInvalidInput) {
			t.Errorf("NormalizeBand(%q) = %v, want ErrInvalidInput", bad, err)
		}
	}
}

// Resolution reads the band each radio reports rather than assuming radio0 is 2.4GHz.
// A static map would break silently on a model that orders its radios differently.
func TestResolveBand(t *testing.T) {
	_, c := wirelessClient(t, "cable")
	ctx := context.Background()

	device, iface, err := ResolveBand(ctx, c.Wireless(), "2.4", false)
	if err != nil {
		t.Fatalf("ResolveBand 2.4: %v", err)
	}
	if device != mock.Factory2GDevice || iface != mock.Factory2GIface {
		t.Errorf("2.4 resolved to %s/%s, want %s/%s",
			device, iface, mock.Factory2GDevice, mock.Factory2GIface)
	}

	device, iface, err = ResolveBand(ctx, c.Wireless(), "5", false)
	if err != nil {
		t.Fatalf("ResolveBand 5: %v", err)
	}
	if device != mock.Factory5GDevice || iface != mock.Factory5GIface {
		t.Errorf("5 resolved to %s/%s", device, iface)
	}
}

func TestResolveBandGuest(t *testing.T) {
	_, c := wirelessClient(t, "cable")

	device, iface, err := ResolveBand(context.Background(), c.Wireless(), "5", true)
	if err != nil {
		t.Fatalf("ResolveBand guest: %v", err)
	}
	if device != mock.Factory5GDevice {
		t.Errorf("device = %s", device)
	}
	if iface != "guest5g" {
		t.Errorf("guest interface = %q, want guest5g", iface)
	}
}

// A band the router does not have must name what it does have, since the likeliest
// cause is a 6GHz flag on a two-radio device.
func TestResolveBandAbsent(t *testing.T) {
	_, c := wirelessClient(t, "cable")

	_, _, err := ResolveBand(context.Background(), c.Wireless(), "6", false)
	if !errors.Is(err, types.ErrNotFound) {
		t.Fatalf("error = %v, want ErrNotFound", err)
	}
	for _, want := range []string{"2G", "5G"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error does not list %q: %v", want, err)
		}
	}
}

func TestResolveBandRejectsNonsense(t *testing.T) {
	_, c := wirelessClient(t, "cable")

	if _, _, err := ResolveBand(context.Background(), c.Wireless(), "banana", false); err == nil {
		t.Error("a nonsense band resolved")
	}
}
