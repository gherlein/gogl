// Package services implements the typed operations gogl offers against a
// GL.iNet router. Interfaces are small and single-purpose so each is
// independently mockable.
//
// Three of the four are read-only. Only ReservationService writes: network
// configuration is set in the GL.iNet admin panel, not here. That boundary is
// what keeps a bulk reservation import from ever leaving the router unreachable.
//
// No method takes a site parameter. GL.iNet routers have no equivalent of a
// UniFi site.
package services

import (
	"context"

	"github.com/gherlein/gogl/src/types"
)

// NetworkService reads the router's LAN and DHCP configuration. Read-only.
type NetworkService interface {
	// Get returns the main LAN interface.
	Get(ctx context.Context) (*types.Network, error)

	// List returns every interface the router reports, main LAN and guest alike.
	List(ctx context.Context) ([]types.Network, error)

	// Leases returns the dynamic DHCP leases currently held. These are not
	// reservations: a lease expires, a reservation is a permanent MAC-to-IP
	// binding. Leases are how you discover what is worth reserving.
	Leases(ctx context.Context) ([]types.DHCPLease, error)

	// Set writes an interface's address and DHCP pool.
	//
	// A change that moves the subnet is refused with ErrReservationsExist while any
	// reservation is present, unless mode is WriteForced. The firmware silently
	// renumbers every bind into the new subnet rather than stranding them, which is
	// not what that guard was designed against; see the correction on
	// networkService.Set.
	//
	// A pool-only change is never guarded and never drops the session: the router
	// keeps its address, so no reservation moves.
	//
	// Moving the subnet changes the address the router is managed at, so the calling
	// session will not survive that case.
	Set(ctx context.Context, n *types.Network, mode WriteMode) error
}

// HostsService manages DNS names through the router's hosts file.
//
// This is the only way gogl creates DNS records. A reservation does not: on this
// firmware its name is a label. gogl owns a delimited block in the file and
// preserves everything outside it.
type HostsService interface {
	// Get returns the parsed host file, managed and unmanaged parts alike.
	Get(ctx context.Context) (*types.HostFile, error)

	// Put writes the host file back.
	Put(ctx context.Context, f *types.HostFile) error

	// Domain returns the configured DNS domain, empty if never set.
	Domain(ctx context.Context) (string, error)

	// SetDomain configures the domain and requalifies existing entries.
	SetDomain(ctx context.Context, domain string) error

	// List returns the managed entries.
	List(ctx context.Context) ([]types.HostEntry, error)

	// Set points name at ip. Requires a configured domain.
	Set(ctx context.Context, name, ip string) error

	// Remove drops the entry for name.
	Remove(ctx context.Context, name string) error

	// Clear removes every managed entry.
	Clear(ctx context.Context) error
}

// ReservationService manages static DHCP bindings.
//
// A binding pins a MAC to an IP. It does not create a DNS record -- see
// types.Reservation, and HostsService for names.
type ReservationService interface {
	List(ctx context.Context) ([]types.Reservation, error)

	// GetByMAC returns the reservation for mac, or ErrNotFound.
	GetByMAC(ctx context.Context, mac string) (*types.Reservation, error)

	// GetByIP returns every reservation holding ip. More than one indicates
	// inconsistent device state rather than normal operation, so the caller
	// decides whether to tolerate it.
	GetByIP(ctx context.Context, ip string) ([]types.Reservation, error)

	// GetByName returns the reservation named name, or ErrNotFound.
	GetByName(ctx context.Context, name string) (*types.Reservation, error)

	// Create writes a new reservation. Returns ErrConflict if the MAC is already
	// reserved, and ErrDomainNotSet if no DNS domain has been configured.
	// Validates before touching the device.
	Create(ctx context.Context, r *types.Reservation) (*types.Reservation, error)

	// Update replaces the reservation identified by r.MAC.
	Update(ctx context.Context, r *types.Reservation) (*types.Reservation, error)

	// Delete removes the reservation for mac.
	Delete(ctx context.Context, mac string) error

	// DeleteAll removes every reservation in one call.
	//
	// Deliberately explicit rather than a mode flag on Delete: this is the one
	// operation that can discard a whole network's addressing, and it should be
	// impossible to reach by passing the wrong argument.
	DeleteAll(ctx context.Context) error
}

// WirelessService reads and writes wireless identity: SSID, passphrase, hidden and
// enabled state, per interface.
//
// Writes are refused when the calling session arrives over WiFi, because applying
// one would sever that session with no address to reconnect at. See
// VISION.md's Wireless Writes section.
type WirelessService interface {
	// Radios returns every radio with its interfaces.
	Radios(ctx context.Context) ([]types.WirelessRadio, error)

	// Interfaces returns every wireless interface, flattened across radios.
	Interfaces(ctx context.Context) ([]types.WirelessInterface, error)

	// Get returns the interface named name, or ErrNotFound listing the valid names.
	Get(ctx context.Context, name string) (*types.WirelessInterface, error)

	// Radio returns the radio named device, or ErrNotFound listing the valid names.
	Radio(ctx context.Context, device string) (*types.WirelessRadio, error)

	// SetSSID writes one interface's SSID. A convenience wrapper over SetInterface.
	SetSSID(ctx context.Context, name, ssid string) error

	// SetInterface writes a partial update to one interface: SSID, passphrase,
	// encryption, hidden or enabled. Unset fields are left alone.
	SetInterface(ctx context.Context, name string, changes types.InterfaceChanges) error

	// SetRadio writes a partial update to one radio's tuning: channel, bandwidth,
	// hardware mode or transmit power. Every interface on the radio inherits it.
	SetRadio(ctx context.Context, device string, changes types.RadioChanges) error

	// SessionInterface reports the firmware's name for the link this session
	// arrives over: "cable", "2.4G", "5G", or "" when off-LAN.
	SessionInterface(ctx context.Context) (string, error)
}

// ClientService reads stations known to the router. Read-only.
type ClientService interface {
	List(ctx context.Context) ([]types.Client, error)
}

// SystemService reads device identity. Read-only.
type SystemService interface {
	Info(ctx context.Context) (*types.SystemInfo, error)
}
