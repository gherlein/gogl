package profile

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	gogl "github.com/gherlein/gogl/src"
	"github.com/gherlein/gogl/src/services"
	"github.com/gherlein/gogl/src/types"
)

// ApplyModes are the flags governing a load.
type ApplyModes struct {
	// DryRun reports what would change without changing it.
	DryRun bool

	// Force allows a subnet move while reservations exist.
	Force bool

	// Wireless applies the wireless sections, which need a wired session.
	Wireless bool
}

// Apply writes a profile to a router.
//
// The order is not arbitrary. Every step below exists because doing it later fails,
// and each ordering rule was established against hardware rather than reasoned about:
//
//  1. Domain first. Reservation writes are refused without one.
//  2. Network second, and only if it differs. Reservations must be inside the subnet,
//     so the subnet has to be right before they are written. This step drops the
//     session when the address moves, which is why it can end the run.
//  3. Reservations, then names. Both per the same reasoning as goglps --set.
//  4. Wireless last. It is the step most likely to be refused -- it needs a wired
//     session -- and the least likely to be wanted, so failing here should not
//     prevent the addressing from having been applied.
//
// A subnet move is the case that cannot complete in one run: the router changes
// address mid-write, so Apply stops there and says how to resume. Pretending
// otherwise would mean reporting success for a run that wrote a third of the profile.
func Apply(ctx context.Context, client *gogl.Client, p *Profile, modes ApplyModes, log io.Writer) error {
	current, err := client.Network().Get(ctx)
	if err != nil {
		return err
	}

	warnOnModelMismatch(ctx, client, p, log)

	moving := p.Network.IP != current.LANIP || p.Network.Netmask != current.Netmask
	poolChanging := p.Network.DHCPStart != current.DHCPStart || p.Network.DHCPStop != current.DHCPStop

	// 1. Domain.
	if p.Domain != "" {
		existing, err := client.Hosts().Domain(ctx)
		if err != nil {
			return err
		}
		if existing != p.Domain {
			fmt.Fprintf(log, "domain: %q -> %q\n", existing, p.Domain)
			if !modes.DryRun {
				if err := client.Hosts().SetDomain(ctx, p.Domain); err != nil {
					return fmt.Errorf("setting the domain: %w", err)
				}
			}
		}
	} else if len(p.Reservations) > 0 {
		return fmt.Errorf("%w: the profile has reservations but no domain, so they could not be named",
			types.ErrDomainNotSet)
	}

	// 2. Network.
	if moving || poolChanging {
		if err := applyNetwork(ctx, client, p, current, moving, modes, log); err != nil {
			return err
		}
		if moving {
			// The write either dropped the session or is about to. Everything after
			// this needs a connection to the new address.
			fmt.Fprintf(log, "\nthe router has moved to %s. The rest of the profile was not applied.\n",
				p.Network.IP)
			fmt.Fprintf(log, "resume with:  goglcfg -H %s --set <file>\n", p.Network.IP)
			return nil
		}
	}

	// 3. Reservations, then names.
	if err := applyReservations(ctx, client, p, modes, log); err != nil {
		return err
	}
	if err := applyHosts(ctx, client, p, modes, log); err != nil {
		return err
	}

	// 4. Wireless.
	if modes.Wireless {
		if err := applyWireless(ctx, client, p, modes, log); err != nil {
			return err
		}
	} else if len(p.Wireless) > 0 || len(p.Radios) > 0 {
		fmt.Fprintln(log, "wireless: skipped; pass --wireless to apply it")
	}

	if modes.DryRun {
		fmt.Fprintln(log, "\ndry run: nothing was changed")
	}
	return nil
}

// warnOnModelMismatch says so when a profile came from a different model.
//
// Not an error. Addresses and names are portable across models; wireless is where the
// trouble is, since interface names, radio names, channel lists and hardware modes are
// all per-device and per-regulatory-domain.
func warnOnModelMismatch(ctx context.Context, client *gogl.Client, p *Profile, log io.Writer) {
	if p.Source.Model == "" {
		return
	}
	info, err := client.System().Info(ctx)
	if err != nil || info.Model == p.Source.Model {
		return
	}
	fmt.Fprintf(log, "warning: profile is from %q, this router is %q\n", p.Source.Model, info.Model)
	fmt.Fprintln(log, "  addresses and names are portable; wireless interface and radio names")
	fmt.Fprintln(log, "  may not exist on this model, and will be reported and skipped")
}

func applyNetwork(ctx context.Context, client *gogl.Client, p *Profile,
	current *types.Network, moving bool, modes ApplyModes, log io.Writer) error {

	target := p.Network.AsNetwork()
	if moving {
		fmt.Fprintf(log, "network: %s/%s -> %s/%s\n",
			current.LANIP, current.Netmask, target.LANIP, target.Netmask)
	}
	if target.DHCPStart != current.DHCPStart || target.DHCPStop != current.DHCPStop {
		fmt.Fprintf(log, "pool: %s-%s -> %s-%s\n",
			current.DHCPStart, current.DHCPStop, target.DHCPStart, target.DHCPStop)
	}

	mode := services.WriteGuarded
	if modes.Force {
		mode = services.WriteForced
	}
	if modes.DryRun {
		// Run the validation the write would run, so a dry run cannot approve a
		// network the device would reject.
		return target.ValidateForWrite()
	}

	if err := client.Network().Set(ctx, target, mode); err != nil {
		if moving && isConnectionLost(err) {
			// Losing the connection here is what success looks like.
			return nil
		}
		return fmt.Errorf("writing the network: %w", err)
	}
	return nil
}

// applyReservations writes the profile's bindings, leaving alone anything already
// correct and reporting per-entry failures without abandoning the rest.
func applyReservations(ctx context.Context, client *gogl.Client, p *Profile,
	modes ApplyModes, log io.Writer) error {

	if len(p.Reservations) == 0 {
		return nil
	}

	device, err := client.Reservations().List(ctx)
	if err != nil {
		return err
	}
	byMAC := make(map[string]types.Reservation, len(device))
	for _, r := range device {
		byMAC[strings.ToLower(r.MAC)] = r
	}

	var failures int
	for i := range p.Reservations {
		want := p.Reservations[i]
		existing, present := byMAC[strings.ToLower(want.MAC)]

		switch {
		case present && existing.IP == want.IP:
			continue
		case present:
			fmt.Fprintf(log, "reservation: %s %s -> %s\n", want.MAC, existing.IP, want.IP)
			if modes.DryRun {
				continue
			}
			if _, err := client.Reservations().Update(ctx, &want); err != nil {
				fmt.Fprintf(log, "  error: %v\n", err)
				failures++
			}
		default:
			fmt.Fprintf(log, "reservation: %s %s (new)\n", want.MAC, want.IP)
			if modes.DryRun {
				continue
			}
			if _, err := client.Reservations().Create(ctx, &want); err != nil {
				fmt.Fprintf(log, "  error: %v\n", err)
				failures++
			}
		}
	}
	if failures > 0 {
		return fmt.Errorf("%d reservation(s) failed", failures)
	}
	return nil
}

// applyHosts writes the DNS names in a single host-file write.
func applyHosts(ctx context.Context, client *gogl.Client, p *Profile,
	modes ApplyModes, log io.Writer) error {

	if len(p.Hosts) == 0 {
		return nil
	}

	file, err := client.Hosts().Get(ctx)
	if err != nil {
		return err
	}

	changed := false
	for _, entry := range p.Hosts {
		if len(entry.Names) == 0 {
			continue
		}
		// The first name is the bare one; Set regenerates the qualified form under
		// whatever domain this router now has, which may differ from the source's.
		name := entry.Names[0]
		if have, ok := file.Lookup(name); ok && have == entry.IP {
			continue
		}
		fmt.Fprintf(log, "dns: %s -> %s\n", file.FQDN(name), entry.IP)
		if modes.DryRun {
			continue
		}
		if err := file.Set(name, entry.IP); err != nil {
			fmt.Fprintf(log, "  error: %v\n", err)
			continue
		}
		changed = true
	}

	if !changed || modes.DryRun {
		return nil
	}
	return client.Hosts().Put(ctx, file)
}

// applyWireless writes identity and tuning, skipping interfaces this router does not
// have rather than failing the run.
func applyWireless(ctx context.Context, client *gogl.Client, p *Profile,
	modes ApplyModes, log io.Writer) error {

	wireless := client.Wireless()

	for _, want := range p.Wireless {
		current, err := wireless.Get(ctx, want.Name)
		if err != nil {
			if errors.Is(err, types.ErrNotFound) {
				fmt.Fprintf(log, "wireless: no interface %q on this router; skipped\n", want.Name)
				continue
			}
			return err
		}

		changes := types.InterfaceChanges{}
		if want.SSID != "" && want.SSID != current.SSID {
			changes.SSID = &want.SSID
			fmt.Fprintf(log, "wireless %s: SSID %q -> %q\n", want.Name, current.SSID, want.SSID)
		}
		// An absent key means "leave it alone", which is why omitting keys from a
		// profile is safe rather than destructive.
		if want.Key != "" && want.Key != current.Key {
			changes.Key = &want.Key
			fmt.Fprintf(log, "wireless %s: passphrase changes\n", want.Name)
		}
		if want.Encryption != "" && want.Encryption != current.Encryption {
			changes.Encryption = &want.Encryption
			fmt.Fprintf(log, "wireless %s: encryption %s -> %s\n",
				want.Name, current.Encryption, want.Encryption)
		}
		if want.Hidden != current.Hidden {
			changes.Hidden = &want.Hidden
			fmt.Fprintf(log, "wireless %s: hidden %t -> %t\n", want.Name, current.Hidden, want.Hidden)
		}
		if want.Enabled != current.Enabled {
			changes.Enabled = &want.Enabled
			fmt.Fprintf(log, "wireless %s: enabled %t -> %t\n", want.Name, current.Enabled, want.Enabled)
		}

		if changes.Empty() || modes.DryRun {
			continue
		}
		if err := wireless.SetInterface(ctx, want.Name, changes); err != nil {
			return fmt.Errorf("wireless %s: %w", want.Name, err)
		}
	}

	for _, want := range p.Radios {
		current, err := wireless.Radio(ctx, want.Device)
		if err != nil {
			if errors.Is(err, types.ErrNotFound) {
				fmt.Fprintf(log, "wireless: no radio %q on this router; skipped\n", want.Device)
				continue
			}
			return err
		}

		changes := types.RadioChanges{}
		if want.Channel != current.Channel {
			changes.Channel = &want.Channel
			fmt.Fprintf(log, "radio %s: channel %d -> %d\n", want.Device, current.Channel, want.Channel)
		}
		if want.HTMode != "" && want.HTMode != current.HTMode {
			changes.HTMode = &want.HTMode
			fmt.Fprintf(log, "radio %s: bandwidth %s -> %s\n", want.Device, current.HTMode, want.HTMode)
		}
		if want.HWMode != "" && want.HWMode != current.HWMode {
			changes.HWMode = &want.HWMode
			fmt.Fprintf(log, "radio %s: hardware mode %s -> %s\n", want.Device, current.HWMode, want.HWMode)
		}
		if want.TXPower != "" && want.TXPower != current.TXPower {
			changes.TXPower = &want.TXPower
			fmt.Fprintf(log, "radio %s: power %s -> %s\n", want.Device, current.TXPower, want.TXPower)
		}

		if changes.Empty() || modes.DryRun {
			continue
		}
		if err := wireless.SetRadio(ctx, want.Device, changes); err != nil {
			return fmt.Errorf("radio %s: %w", want.Device, err)
		}
	}
	return nil
}

// isConnectionLost reports whether err looks like the router going away mid-call,
// which is the normal consequence of a successful re-address.
func isConnectionLost(err error) bool {
	if err == nil {
		return false
	}
	for _, marker := range []string{
		"connection reset", "connection refused", "EOF",
		"broken pipe", "no route to host", "network is unreachable",
		"context deadline exceeded", "timeout",
	} {
		if strings.Contains(err.Error(), marker) {
			return true
		}
	}
	return false
}
