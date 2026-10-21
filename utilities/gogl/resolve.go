package main

import (
	"fmt"

	"github.com/spf13/cobra"

	gogl "github.com/gherlein/gogl/src"
	"github.com/gherlein/gogl/utilities/internal/netcfg"
)

// resolveDevice settles which radio a command acts on.
//
// --device wins when given, because it is the escape hatch for what --band cannot
// settle: two radios reporting the same band, or a device gogl's band table does not
// recognise. Otherwise --band is resolved against what the radios report, never against
// a static radio0/radio1 map.
func resolveDevice(cmd *cobra.Command, client *gogl.Client, t *bandTarget) (string, error) {
	if t.device != "" {
		return t.device, nil
	}
	if t.band == "" {
		return "", fmt.Errorf("%w: pass --band 2.4|5|6, or --device to name a radio", errUsage)
	}

	device, _, err := netcfg.ResolveBand(cmd.Context(), client.Wireless(), t.band, t.guest)
	if err != nil {
		return "", err
	}
	return device, nil
}

// resolveInterface settles which wireless interface a command acts on, and which radio
// carries it.
//
// --iface wins for the same reason --device does. Note that resolution needs the band
// *and* the guest flag: a radio has both a main and a guest interface, and they are
// separate objects to the firmware.
func resolveInterface(cmd *cobra.Command, client *gogl.Client, t *bandTarget) (device, iface string, err error) {
	if t.iface != "" {
		return t.device, t.iface, nil
	}
	if t.band == "" {
		return "", "", fmt.Errorf("%w: pass --band 2.4|5|6, or --iface to name an interface", errUsage)
	}
	return netcfg.ResolveBand(cmd.Context(), client.Wireless(), t.band, t.guest)
}
