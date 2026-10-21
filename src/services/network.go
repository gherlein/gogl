package services

import (
	"context"
	"fmt"

	"github.com/gherlein/gogl/src/transport"
	"github.com/gherlein/gogl/src/types"
)

// CONFIRMED against a GL-SFT1200 on firmware 4.3.28, 2026-07-28.
//
// The group is "lan" but the method is get_config_list, not get_config -- the
// latter does not exist and returns -32601, which is what blocked this service
// until the endpoint was captured.
const (
	networkGroup        = "lan"
	networkGetConfig    = "get_config_list"
	networkSetConfig    = "set_config"
	networkDHCPGroup    = "network"
	networkGetDHCPLease = "get_dhcp_leases"
)

// interfaceList is the wire shape of lan.get_config_list: both the LAN and the
// guest network come back in one call.
type interfaceList struct {
	Interfaces []types.Network `json:"interfaces"`
}

// leaseList is the wire shape of network.get_dhcp_leases.
//
// The published documentation calls this field "entries"; the device actually
// sends "leases". The device wins.
type leaseList struct {
	Leases []types.DHCPLease `json:"leases"`
}

type networkService struct {
	transport transport.Transport

	// reservations is consulted before a write, to enforce the ordering rule that
	// the LAN cannot be renumbered underneath live reservations.
	reservations ReservationService
}

// NewNetworkService returns the LAN and DHCP configuration service.
func NewNetworkService(t transport.Transport) NetworkService {
	return &networkService{transport: t, reservations: NewReservationService(t)}
}

// Get reads the main LAN's address and DHCP configuration.
func (s *networkService) Get(ctx context.Context) (*types.Network, error) {
	all, err := s.List(ctx)
	if err != nil {
		return nil, err
	}
	for i := range all {
		if all[i].Interface == types.InterfaceLAN {
			return &all[i], nil
		}
	}
	return nil, fmt.Errorf("%w: router reported no %q interface", types.ErrNotFound, types.InterfaceLAN)
}

// List returns every interface the router reports, main LAN and guest alike.
// Guest is useful for address planning even though gogl never writes it.
func (s *networkService) List(ctx context.Context) ([]types.Network, error) {
	var list interfaceList
	if err := s.transport.Call(ctx, networkGroup, networkGetConfig, nil, &list); err != nil {
		return nil, fmt.Errorf("gogl: read network config: %w", err)
	}
	return list.Interfaces, nil
}

// Leases returns the dynamic DHCP leases the router is currently holding.
//
// These are not reservations: a lease expires, a reservation is a permanent
// MAC-to-IP binding. Leases are how you discover what is worth reserving, and
// their hostnames are what the router's DNS actually answers.
func (s *networkService) Leases(ctx context.Context) ([]types.DHCPLease, error) {
	var list leaseList
	if err := s.transport.Call(ctx, networkDHCPGroup, networkGetDHCPLease, nil, &list); err != nil {
		return nil, fmt.Errorf("gogl: read DHCP leases: %w", err)
	}
	return list.Leases, nil
}

// WriteMode says whether a network write may proceed past the reservations guard.
//
// Named rather than a bare bool so the call site reads as its own documentation:
// Set(ctx, n, WriteForced) says what it does, Set(ctx, n, true) does not.
type WriteMode int

const (
	// WriteGuarded refuses a subnet change while reservations exist.
	WriteGuarded WriteMode = iota

	// WriteForced proceeds anyway, accepting that the firmware will rewrite every
	// reservation into the new subnet without announcing it.
	WriteForced
)

// Set writes an interface's address and DHCP pool via lan.set_config.
//
// Refused while any reservation exists, with ErrReservationsExist.
//
// The original reason for that was wrong, and is recorded here because the correction
// matters more than the rule. It was written expecting the firmware to leave static
// binds alone, stranding all of them outside the new subnet. OBSERVED 2026-07-29 on a
// GL-SFT1200 running 4.3.28: the firmware silently renumbers every bind into the new
// subnet, preserving host parts. 192.168.2.10 became 192.168.8.10 across 27
// reservations, with no prompt and no report.
//
// The guard is kept for two reasons that survive the correction. Rewriting every
// reservation is a large change to happen as a side effect of an address flag, and
// nothing in the API announces it. And the behavior is only known for a same-size
// subnet: a narrower netmask, where a host part no longer fits, is untested, as is a
// move that lands reservations inside the new DHCP pool -- which is what happened to
// 20 of those 27.
//
// This changes the address the router is managed at, so the session making the call
// will not survive it. That is inherent, not a defect: expect to reconnect.
func (s *networkService) Set(ctx context.Context, n *types.Network, mode WriteMode) error {
	if err := n.ValidateForWrite(); err != nil {
		return fmt.Errorf("gogl: %w", err)
	}

	moving, err := s.movesSubnet(ctx, n)
	if err != nil {
		return err
	}

	// The guard exists because the subnet moves. A pool-only change moves nothing:
	// the router keeps its address, the session survives, and the firmware has no
	// reason to touch a single reservation. Refusing it would be guarding against
	// something that cannot happen.
	if moving && mode == WriteGuarded {
		existing, err := s.reservations.List(ctx)
		if err != nil {
			return err
		}
		if len(existing) > 0 {
			return fmt.Errorf("%w: %d reservation(s) present; clear them, or force the write",
				types.ErrReservationsExist, len(existing))
		}
	}

	args := map[string]any{
		"interface": n.Interface,
		"ip":        n.LANIP,
		"netmask":   n.Netmask,
		"start":     n.DHCPStart,
		"end":       n.DHCPStop,
	}
	if err := s.transport.Call(ctx, networkGroup, networkSetConfig, args, nil); err != nil {
		return fmt.Errorf("gogl: write network config: %w", err)
	}
	return nil
}

// movesSubnet reports whether n changes the interface's address or netmask.
//
// This is what decides whether the reservations guard applies. A pool-only change
// leaves the subnet alone, so no reservation can be renumbered by it and the session
// survives; guarding it would be guarding against something that cannot happen.
//
// An interface that is not in the current list is treated as a move: when the current
// state cannot be established, the safe assumption is the one that keeps the guard.
func (s *networkService) movesSubnet(ctx context.Context, n *types.Network) (bool, error) {
	interfaces, err := s.List(ctx)
	if err != nil {
		return false, err
	}
	for _, current := range interfaces {
		if current.Interface != n.Interface {
			continue
		}
		return current.LANIP != n.LANIP || current.Netmask != n.Netmask, nil
	}
	return true, nil
}
