package netcfg

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"text/tabwriter"

	gogl "github.com/gherlein/gogl/src"
	"github.com/gherlein/gogl/src/services"
	"github.com/gherlein/gogl/src/types"
	"github.com/gherlein/gogl/utilities/internal/conn"
)

// WirelessModes are the flags governing a wireless write.
type WirelessModes struct {
	// DryRun reports what would change, including any refusal, without writing.
	DryRun bool

	// Yes skips the confirmation prompt. It never waives the wired-session guard:
	// consent is not the same as reachability.
	Yes bool
}

// FormatWireless reports every radio and the SSIDs on it.
//
// The passphrase is masked unless showKey: a key printed to a terminal is a key in a
// scrollback buffer, and in whatever the operator pastes into a bug report.
func FormatWireless(w io.Writer, radios []types.WirelessRadio, showKey bool) error {
	if len(radios) == 0 {
		_, err := fmt.Fprintln(w, "no wireless radios reported")
		return err
	}

	tw := tabwriter.NewWriter(w, 0, 8, 2, ' ', 0)
	fmt.Fprintln(tw, "BAND\tINTERFACE\tSSID\tENCRYPTION\tSTATE\tKEY")

	for _, r := range radios {
		for _, f := range r.Ifaces {
			state := "enabled"
			if !f.Enabled {
				state = "disabled"
			}
			if f.Hidden {
				state += ", hidden"
			}
			if f.Guest {
				state += ", guest"
			}

			key := f.MaskedKey()
			if showKey {
				key = f.Key
			}
			fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\n",
				r.Band, f.Name, f.SSID, f.Encryption, state, key)
		}
	}
	if err := tw.Flush(); err != nil {
		return err
	}

	// The tuning, and what each radio says it supports. Printed because the
	// --set-channel and --set-htmode flags are unusable without it: the valid values
	// differ per radio and per regulatory domain, and guessing gets a bare error.
	for _, r := range radios {
		fmt.Fprintf(w, "\n%s radio %s: channel %s, %s, %s, power %s\n",
			r.Band, r.Device, channelName(r.Channel), r.HTMode, r.HWMode, r.TXPower)

		if len(r.Channels) > 0 {
			fmt.Fprintf(w, "  channels:    %s\n", describeChannels(r))
		}
		if options := r.HTModeOptions(); len(options) > 0 {
			fmt.Fprintf(w, "  bandwidths:  %s\n", strings.Join(options, ", "))
		}
		if len(r.HWModes) > 0 {
			fmt.Fprintf(w, "  hw modes:    %s\n", strings.Join(r.HWModes, ", "))
		}
		if len(r.Encryptions) > 0 {
			fmt.Fprintf(w, "  encryptions: %s\n", strings.Join(r.Encryptions, ", "))
		}
		fmt.Fprintf(w, "  power:       %s\n", strings.Join(types.TXPowerLevels, ", "))
	}
	return nil
}

// describeChannels lists the selectable channels, marking the DFS ones. The mark
// matters: a DFS channel is a channel the radio can be forced off with every client
// on it, which is worth knowing before choosing one.
func describeChannels(r types.WirelessRadio) string {
	parts := make([]string, 0, len(r.Channels))
	for _, c := range r.Channels {
		if c.DFS {
			parts = append(parts, fmt.Sprintf("%d (DFS)", c.Channel))
			continue
		}
		parts = append(parts, strconv.Itoa(c.Channel))
	}
	return strings.Join(parts, ", ") + ", or 0 for auto"
}

// SetWireless applies interface-scoped and radio-scoped changes.
//
// Two gates on both, because these are the writes in gogl with no remote recovery.
// Changing an SSID or passphrase drops every client on that radio; retuning a
// channel or bandwidth does the same for at least a re-association, and longer on a
// DFS channel. If the caller is one of those clients there is no new address to
// reconnect at.
func SetWireless(ctx context.Context, client *gogl.Client,
	iface string, ifaceChanges types.InterfaceChanges,
	device string, radioChanges types.RadioChanges, modes WirelessModes) error {

	wireless := client.Wireless()

	// Resolve and validate everything before announcing anything, so a typo reads as
	// a typo rather than as a half-finished change.
	var current *types.WirelessInterface
	if !ifaceChanges.Empty() {
		var err error
		if current, err = wireless.Get(ctx, iface); err != nil {
			return err
		}
	}
	var radio *types.WirelessRadio
	if !radioChanges.Empty() {
		var err error
		if radio, err = wireless.Radio(ctx, device); err != nil {
			return err
		}
		if err := radioChanges.Validate(radio); err != nil {
			return err
		}
	}

	if err := reportSessionPath(ctx, wireless); err != nil {
		return err
	}

	planned := describeInterfaceChanges(current, ifaceChanges)
	planned = append(planned, describeRadioChanges(radio, radioChanges)...)
	if len(planned) == 0 {
		fmt.Println("nothing to change; every requested value is already set")
		return nil
	}

	for _, line := range planned {
		fmt.Fprintln(os.Stderr, line)
	}
	if radio != nil && radioChanges.Channel != nil && radio.IsDFS(*radioChanges.Channel) {
		fmt.Fprintf(os.Stderr,
			"warning: channel %d is a DFS channel. The radio must vacate it if it detects\n"+
				"radar, taking every client with it for the minutes it spends re-scanning.\n",
			*radioChanges.Channel)
	}
	fmt.Fprintln(os.Stderr, "clients on the affected radio will be disconnected.")

	if modes.DryRun {
		fmt.Fprintln(os.Stderr, "dry run: nothing was changed")
		return nil
	}
	if err := confirmWireless(modes); err != nil {
		return err
	}

	// Interface first, then radio. A failed radio retune leaves a working network
	// under the new name; a failed interface write after a successful retune leaves
	// the radio moved for no reason.
	if !ifaceChanges.Empty() {
		if err := wireless.SetInterface(ctx, iface, ifaceChanges); err != nil {
			return err
		}
		fmt.Printf("wireless interface %s updated\n", iface)
	}
	if !radioChanges.Empty() {
		if err := wireless.SetRadio(ctx, device, radioChanges); err != nil {
			return err
		}
		fmt.Printf("radio %s updated\n", device)
	}

	fmt.Fprintln(os.Stderr, "reconnect your wireless clients.")
	return nil
}

// reportSessionPath states how this session reaches the router, and refuses if that
// path is a radio.
func reportSessionPath(ctx context.Context, wireless services.WirelessService) error {
	path, err := wireless.SessionInterface(ctx)
	if err != nil {
		return fmt.Errorf("%w: cannot determine how this session reaches the router: %w",
			types.ErrWirelessSession, err)
	}
	switch path {
	case "":
		fmt.Fprintln(os.Stderr, "session is arriving from off-LAN, so no radio here carries it")
	case "cable":
		fmt.Fprintln(os.Stderr, "session is on ethernet")
	default:
		return fmt.Errorf("%w: this session is on %s\n"+
			"  changing wireless would drop it, with no address to reconnect at\n"+
			"  connect over ethernet and try again",
			types.ErrWirelessSession, path)
	}
	return nil
}

// confirmWireless gates a wireless write on a human, unless --yes.
//
// Unlike --del, a non-terminal stdout does not imply consent. There the worst case
// is a lost reservation; here it is a device you have to walk to.
func confirmWireless(modes WirelessModes) error {
	if modes.Yes {
		return nil
	}
	if !conn.IsTerminal(os.Stdout) {
		return errors.New("refusing to change wireless without confirmation: pass --yes")
	}
	return conn.Confirm(os.Stdin, os.Stderr, "Apply these wireless changes? [y/N] ")
}

// describeInterfaceChanges renders the before and after for each field that would
// actually change, dropping the ones already at the requested value.
//
// The passphrase is described by length rather than shown: this goes to a terminal.
func describeInterfaceChanges(current *types.WirelessInterface, c types.InterfaceChanges) []string {
	if current == nil {
		return nil
	}
	var out []string
	label := current.Describe()

	if c.SSID != nil && *c.SSID != current.SSID {
		out = append(out, fmt.Sprintf("%s: SSID %q -> %q", label, current.SSID, *c.SSID))
	}
	if c.Key != nil && *c.Key != current.Key {
		out = append(out, fmt.Sprintf("%s: passphrase changes (%d characters -> %d)",
			label, len(current.Key), len(*c.Key)))
	}
	if c.Encryption != nil && *c.Encryption != current.Encryption {
		out = append(out, fmt.Sprintf("%s: encryption %s -> %s",
			label, current.Encryption, *c.Encryption))
	}
	if c.Hidden != nil && *c.Hidden != current.Hidden {
		out = append(out, fmt.Sprintf("%s: hidden %t -> %t", label, current.Hidden, *c.Hidden))
	}
	if c.Enabled != nil && *c.Enabled != current.Enabled {
		out = append(out, fmt.Sprintf("%s: enabled %t -> %t", label, current.Enabled, *c.Enabled))
	}
	return out
}

// describeRadioChanges does the same for radio tuning.
func describeRadioChanges(radio *types.WirelessRadio, c types.RadioChanges) []string {
	if radio == nil {
		return nil
	}
	var out []string
	label := fmt.Sprintf("%s radio (%s)", radio.Band, radio.Device)

	if c.Channel != nil && *c.Channel != radio.Channel {
		out = append(out, fmt.Sprintf("%s: channel %s -> %s",
			label, channelName(radio.Channel), channelName(*c.Channel)))
	}
	if c.HTMode != nil && *c.HTMode != radio.HTMode {
		out = append(out, fmt.Sprintf("%s: bandwidth %s -> %s", label, radio.HTMode, *c.HTMode))
	}
	if c.HWMode != nil && *c.HWMode != radio.HWMode {
		out = append(out, fmt.Sprintf("%s: hardware mode %s -> %s", label, radio.HWMode, *c.HWMode))
	}
	if c.TXPower != nil && *c.TXPower != radio.TXPower {
		out = append(out, fmt.Sprintf("%s: transmit power %s -> %s", label, radio.TXPower, *c.TXPower))
	}
	return out
}

func channelName(channel int) string {
	if channel == types.AutoChannel {
		return "auto"
	}
	return strconv.Itoa(channel)
}

// SetSSID changes one interface's SSID. Retained as the narrow entry point the
// SSID tests exercise.
//
// Two gates, both because this is the one write in gogl with no remote recovery.
// Changing an SSID drops every client on that radio; if the caller is one of them,
// the network it was using stops existing under that name and there is no new
// address to reconnect at. Recovery means ethernet or the reset pin.
func SetSSID(ctx context.Context, client *gogl.Client, iface, ssid string, modes WirelessModes) error {
	return SetWireless(ctx, client, iface, types.InterfaceChanges{SSID: &ssid},
		"", types.RadioChanges{}, modes)
}
