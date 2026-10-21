package profile

import (
	"context"
	"encoding/json"
	"fmt"
	"io"

	gogl "github.com/gherlein/gogl/src"
	"github.com/gherlein/gogl/src/types"
)

// ProfileVersion is the schema version written into every profile.
//
// Read on load and refused if unrecognized. A profile is something an operator will
// commit and come back to months later, so a file from a future version has to fail
// loudly rather than be half-understood.
const ProfileVersion = 1

// Profile is a router's reproducible configuration.
//
// Deliberately not a router image. It carries what defines a *network* -- addresses,
// names, and what devices associate to -- and omits everything identifying a
// particular unit: MAC addresses of the router itself, serial numbers, uptime, lease
// state. Those are what make a full config dump unusable on a second router, which is
// the case this exists to serve.
//
// Every field here comes from an endpoint verified against hardware. That is the
// reason the file is this small: 110 getters exist, 23 are verified, and a profile
// built on the rest would be guesswork.
type Profile struct {
	Version int `json:"gogl_profile_version"`

	// Source records where the profile came from, for the operator's benefit and to
	// warn on a model mismatch at load time. Nothing here is applied.
	Source Source `json:"source"`

	// Network is the LAN address and DHCP pool.
	Network *ProfileNetwork `json:"network,omitempty"`

	// Domain is the DNS suffix. Applied first, since reservation writes require it.
	Domain string `json:"domain,omitempty"`

	// Reservations are MAC-to-IP bindings.
	Reservations []types.Reservation `json:"reservations,omitempty"`

	// Hosts are the DNS names, kept separate from Reservations because the firmware
	// stores them separately and the two can legitimately disagree.
	Hosts []types.HostEntry `json:"hosts,omitempty"`

	// Wireless is per-interface identity. Passphrases are omitted unless the capture
	// explicitly asked for them; see Capture.
	Wireless []ProfileInterface `json:"wireless,omitempty"`

	// Radios is per-radio tuning.
	Radios []ProfileRadio `json:"radios,omitempty"`
}

// Source identifies the router a profile was captured from.
type Source struct {
	Model    string `json:"model,omitempty"`
	Firmware string `json:"firmware,omitempty"`
	Host     string `json:"host,omitempty"`
	Captured string `json:"captured,omitempty"`
}

// ProfileNetwork is the writable subset of a network. Lease time and DNS servers are
// excluded because no verified endpoint sets them.
type ProfileNetwork struct {
	Interface string `json:"interface"`
	IP        string `json:"ip"`
	Netmask   string `json:"netmask"`
	DHCPStart string `json:"dhcp_start"`
	DHCPStop  string `json:"dhcp_end"`
}

// ProfileInterface is one wireless interface's identity.
type ProfileInterface struct {
	Name string `json:"name"`
	SSID string `json:"ssid"`

	// Key is the passphrase, present only when captured with keys included. An absent
	// key is not an empty key: on load it is simply not written, which leaves whatever
	// the target router already has. That relies on wifi.set_config's partial-update
	// behavior, verified on hardware.
	Key string `json:"key,omitempty"`

	Encryption string `json:"encryption,omitempty"`
	Hidden     bool   `json:"hidden"`
	Enabled    bool   `json:"enabled"`

	// Band and Guest are recorded to make the file readable and to let a load match
	// interfaces across models where the names differ. Neither is written.
	Band  string `json:"band,omitempty"`
	Guest bool   `json:"guest,omitempty"`
}

// ProfileRadio is one radio's tuning.
type ProfileRadio struct {
	Device  string `json:"device"`
	Channel int    `json:"channel"`
	HTMode  string `json:"htmode,omitempty"`
	HWMode  string `json:"hwmode,omitempty"`
	TXPower string `json:"txpower,omitempty"`

	// Band is recorded for readability and cross-model matching. Not written.
	Band string `json:"band,omitempty"`
}

// CaptureOptions controls what a capture includes.
type CaptureOptions struct {
	// WithKeys includes WiFi passphrases in cleartext.
	//
	// Off by default, and worth the inconvenience: a profile is a file people commit.
	// Without keys, a load leaves the target's existing passphrases alone rather than
	// clearing them, so an omitted key is safe as well as private.
	WithKeys bool

	// Host and Captured are recorded in Source. Passed in rather than discovered so
	// that a capture is reproducible and testable.
	Host     string
	Captured string
}

// Capture reads a profile from a router.
//
// Failures on optional sections are not fatal. A router that will not report wireless
// is still worth capturing addresses from, and a profile missing a section says so by
// omitting it rather than by failing whole.
func Capture(ctx context.Context, client *gogl.Client, opts CaptureOptions, warn io.Writer) (*Profile, error) {
	p := &Profile{
		Version: ProfileVersion,
		Source:  Source{Host: opts.Host, Captured: opts.Captured},
	}

	if info, err := client.System().Info(ctx); err == nil {
		p.Source.Model = info.Model
		p.Source.Firmware = info.Firmware
	} else {
		fmt.Fprintln(warn, "warning: could not read model and firmware:", err)
	}

	// The network is the one section a profile is useless without: every reservation
	// is meaningless unless the subnet is known.
	network, err := client.Network().Get(ctx)
	if err != nil {
		return nil, fmt.Errorf("reading the network: %w", err)
	}
	p.Network = &ProfileNetwork{
		Interface: network.Interface,
		IP:        network.LANIP,
		Netmask:   network.Netmask,
		DHCPStart: network.DHCPStart,
		DHCPStop:  network.DHCPStop,
	}

	if hosts, err := client.Hosts().Get(ctx); err == nil {
		p.Domain = hosts.Domain
		p.Hosts = hosts.Entries
	} else {
		fmt.Fprintln(warn, "warning: could not read DNS names:", err)
	}

	if reservations, err := client.Reservations().List(ctx); err == nil {
		p.Reservations = reservations
	} else {
		fmt.Fprintln(warn, "warning: could not read reservations:", err)
	}

	radios, err := client.Wireless().Radios(ctx)
	if err != nil {
		fmt.Fprintln(warn, "warning: could not read wireless:", err)
		return p, nil
	}
	for _, r := range radios {
		p.Radios = append(p.Radios, ProfileRadio{
			Device: r.Device, Band: r.Band, Channel: r.Channel,
			HTMode: r.HTMode, HWMode: r.HWMode, TXPower: r.TXPower,
		})
		for _, f := range r.Ifaces {
			entry := ProfileInterface{
				Name: f.Name, SSID: f.SSID, Encryption: f.Encryption,
				Hidden: f.Hidden, Enabled: f.Enabled, Band: f.Band, Guest: f.Guest,
			}
			if opts.WithKeys {
				entry.Key = f.Key
			}
			p.Wireless = append(p.Wireless, entry)
		}
	}
	return p, nil
}

// Write encodes the profile as indented JSON.
func (p *Profile) Write(w io.Writer) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(p)
}

// ReadProfile decodes a profile and checks it is one this build understands.
func ReadProfile(r io.Reader) (*Profile, error) {
	var p Profile
	dec := json.NewDecoder(r)

	// Unknown fields are an error rather than a shrug: a profile written by a newer
	// gogl may depend on a section this build would silently drop, and silently
	// dropping part of a network is worse than refusing the file.
	dec.DisallowUnknownFields()
	if err := dec.Decode(&p); err != nil {
		return nil, fmt.Errorf("reading profile: %w", err)
	}

	if p.Version == 0 {
		return nil, fmt.Errorf("%w: no gogl_profile_version; is this a gogl profile?",
			types.ErrInvalidInput)
	}
	if p.Version != ProfileVersion {
		return nil, fmt.Errorf("%w: profile version %d, this build understands %d",
			types.ErrInvalidInput, p.Version, ProfileVersion)
	}
	if p.Network == nil {
		return nil, fmt.Errorf("%w: profile has no network section", types.ErrInvalidInput)
	}
	return &p, nil
}

// AsNetwork converts the profile's network into the library type.
func (n *ProfileNetwork) AsNetwork() *types.Network {
	return &types.Network{
		Interface: n.Interface,
		LANIP:     n.IP,
		Netmask:   n.Netmask,
		DHCPStart: n.DHCPStart,
		DHCPStop:  n.DHCPStop,
	}
}
