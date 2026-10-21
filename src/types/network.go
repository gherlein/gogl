package types

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"

	"github.com/gherlein/gogl/src/ipmath"
)

// Interface names lan.get_config_list reports. The firmware returns both in a
// single call.
const (
	InterfaceLAN   = "lan"
	InterfaceGuest = "guest"
)

// IntBool is a boolean the firmware encodes as 0 or 1.
//
// gofi's FlexBool was deliberately not ported on the grounds that GL.iNet's JSON
// is well-typed. A captured payload disproved that for exactly one field:
// lan.get_config_list reports "enable" as a number.
type IntBool bool

func (b *IntBool) UnmarshalJSON(data []byte) error {
	var asBool bool
	if err := json.Unmarshal(data, &asBool); err == nil {
		*b = IntBool(asBool)
		return nil
	}

	var asNumber int
	if err := json.Unmarshal(data, &asNumber); err == nil {
		*b = asNumber != 0
		return nil
	}

	var asString string
	if err := json.Unmarshal(data, &asString); err == nil {
		switch asString {
		case "1", "true", "yes", "on":
			*b = true
			return nil
		case "0", "false", "no", "off", "":
			*b = false
			return nil
		}
	}
	return fmt.Errorf("types: cannot read %s as a boolean", data)
}

func (b IntBool) MarshalJSON() ([]byte, error) {
	return json.Marshal(bool(b))
}

// Network is one interface's address and DHCP configuration, as returned by
// lan.get_config_list.
//
// Read to report state and to validate that reservations fall inside the subnet.
// Written by NetworkService.Set, which is refused while reservations exist and
// which drops the calling session, since the router moves.
//
// Field names mirror the firmware verbatim. Note there is no domain field: no
// endpoint exposes the dnsmasq suffix, so gogl keeps its own in the host file.
type Network struct {
	// Interface is "lan" or "guest".
	Interface string `json:"interface"`

	LANIP   string `json:"ip"`
	Netmask string `json:"netmask"`

	DHCPEnabled IntBool   `json:"enable"`
	DHCPStart   string    `json:"start"`
	DHCPStop    string    `json:"end"`
	DHCPLease   LeaseTime `json:"leasetime"`

	// Gateway is advertised to clients when non-empty.
	Gateway string `json:"gateway,omitempty"`

	DNS []string `json:"dns,omitempty"`
}

// Subnet returns the interface's network in CIDR form.
func (n *Network) Subnet() (*net.IPNet, error) {
	return ipmath.SubnetFrom(n.LANIP, n.Netmask)
}

// Contains reports whether ip falls inside the subnet.
func (n *Network) Contains(ip net.IP) (bool, error) {
	subnet, err := n.Subnet()
	if err != nil {
		return false, err
	}
	return subnet.Contains(ip), nil
}

// InDHCPPool reports whether ip falls inside the dynamic pool.
//
// Informational only: dnsmasq honors a static lease inside the dynamic range and
// excludes that address from dynamic allocation, so an address here is untidy
// rather than broken. It would be a genuine conflict under ISC dhcpd, which is
// where the contrary intuition comes from.
func (n *Network) InDHCPPool(ip net.IP) (bool, error) {
	if !n.DHCPEnabled {
		return false, nil
	}
	start, stop := net.ParseIP(n.DHCPStart), net.ParseIP(n.DHCPStop)
	if start == nil || stop == nil {
		return false, nil
	}
	return ipmath.InRange(ip, start, stop), nil
}

// PoolSize returns the count of addresses in the dynamic pool, or 0 when DHCP is
// disabled or the boundaries are unparseable.
func (n *Network) PoolSize() int {
	if !n.DHCPEnabled {
		return 0
	}
	start, stop := net.ParseIP(n.DHCPStart), net.ParseIP(n.DHCPStop)
	if start == nil || stop == nil {
		return 0
	}
	first, last := ipmath.ToUint32(start), ipmath.ToUint32(stop)
	if last < first {
		return 0
	}
	return int(last-first) + 1
}

// UsableHosts returns the assignable host count of the subnet.
func (n *Network) UsableHosts() int {
	subnet, err := n.Subnet()
	if err != nil {
		return 0
	}
	return ipmath.UsableHosts(subnet)
}

// IsGuest reports whether this is the guest network rather than the main LAN.
func (n *Network) IsGuest() bool { return n.Interface == InterfaceGuest }

// DHCPLease represents a currently-held dynamic lease, from
// network.get_dhcp_leases.
//
// Distinct from a Reservation: a lease is what the router handed out and will
// expire, a reservation is a permanent MAC-to-IP binding.
//
// Leases are also where the router's DNS names come from. The Hostname here is
// what the client announced, and it is what actually resolves -- a reservation's
// name does not.
type DHCPLease struct {
	MAC      string `json:"mac"`
	IP       string `json:"ip"`
	Hostname string `json:"hostname,omitempty"`

	// Expires is seconds remaining on the lease.
	Expires int64 `json:"expires,omitempty"`
}

// ValidateForWrite checks that n describes a network the firmware can actually
// serve, before anything is written.
//
// It lives on the type rather than in the service so that a preview can run the
// identical check: a dry run that approves what the real write would reject is
// worse than no dry run at all.
//
// The firmware accepts a pool outside its own subnet and responds by handing out
// nothing, with no error to explain why, so this is the only place the mistake
// gets caught.
func (n *Network) ValidateForWrite() error {
	if n.Interface == "" {
		return errors.New("gogl: no interface named")
	}

	subnet, err := n.Subnet()
	if err != nil {
		return fmt.Errorf("refusing to write an unusable network: %w", err)
	}

	start, stop := net.ParseIP(n.DHCPStart), net.ParseIP(n.DHCPStop)
	if start == nil || stop == nil {
		return fmt.Errorf("%w: pool bounds %q-%q", ErrInvalidIP, n.DHCPStart, n.DHCPStop)
	}
	if !subnet.Contains(start) || !subnet.Contains(stop) {
		return fmt.Errorf("%w: pool %s-%s falls outside %s",
			ErrOutsideSubnet, n.DHCPStart, n.DHCPStop, subnet)
	}
	if ipmath.ToUint32(stop) < ipmath.ToUint32(start) {
		return fmt.Errorf("gogl: pool end %s precedes start %s", n.DHCPStop, n.DHCPStart)
	}
	return nil
}
