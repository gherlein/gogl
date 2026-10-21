package services

import (
	"context"
	"fmt"
	"strings"

	"github.com/gherlein/gogl/src/transport"
	"github.com/gherlein/gogl/src/types"
)

// CONFIRMED against a GL-SFT1200 on firmware 4.3.28, 2026-07-28.
//
// GL.iNet calls a reservation a "static bind". The firmware offers real per-entry
// operations, so no read-modify-write of a whole table is needed -- an earlier
// design assumed one and would have clobbered concurrent edits.
const (
	reservationGroup  = "lan"
	reservationList   = "get_static_bind_list"
	reservationAdd    = "add_static_bind"
	reservationSet    = "set_static_bind"
	reservationRemove = "remove_static_bind"

	// removeModeSingle deletes the entry matching the supplied MAC.
	removeModeSingle = 0

	// removeModeAll discards every binding in one call. Reachable only through
	// DeleteAll, never as an argument to Delete: this is the operation that can
	// erase a whole network's addressing, so it should be impossible to trigger by
	// passing the wrong value.
	removeModeAll = 1
)

// bindList is the wire shape of lan.get_static_bind_list.
type bindList struct {
	StaticBindList []types.Reservation `json:"static_bind_list"`
}

type reservationService struct {
	transport transport.Transport

	// hosts is consulted before a write, to enforce the ordering rule that a
	// reservation is not written until a DNS domain exists.
	hosts HostsService
}

// NewReservationService returns the reservation service.
func NewReservationService(t transport.Transport) ReservationService {
	return &reservationService{transport: t, hosts: NewHostsService(t)}
}

// requireDomain refuses a write until a DNS domain has been configured.
//
// A reservation creates no DNS record by itself, so writing one before the domain
// exists yields an address with no name and nothing to indicate that was
// unintentional. Establishing the domain first makes the pairing deliberate.
func (s *reservationService) requireDomain(ctx context.Context) error {
	domain, err := s.hosts.Domain(ctx)
	if err != nil {
		return err
	}
	if domain == "" {
		return fmt.Errorf("%w: set one first, then write reservations", types.ErrDomainNotSet)
	}
	return nil
}

func (s *reservationService) List(ctx context.Context) ([]types.Reservation, error) {
	var list bindList
	if err := s.transport.Call(ctx, reservationGroup, reservationList, nil, &list); err != nil {
		return nil, fmt.Errorf("gogl: list reservations: %w", err)
	}
	return list.StaticBindList, nil
}

func (s *reservationService) GetByMAC(ctx context.Context, mac string) (*types.Reservation, error) {
	normalized, err := types.NormalizeMAC(mac)
	if err != nil {
		return nil, err
	}

	all, err := s.List(ctx)
	if err != nil {
		return nil, err
	}
	for i := range all {
		if strings.EqualFold(all[i].MAC, normalized) {
			return &all[i], nil
		}
	}
	return nil, fmt.Errorf("%w: no reservation for MAC %s", types.ErrNotFound, normalized)
}

func (s *reservationService) GetByName(ctx context.Context, name string) (*types.Reservation, error) {
	all, err := s.List(ctx)
	if err != nil {
		return nil, err
	}
	for i := range all {
		if all[i].Name == name {
			return &all[i], nil
		}
	}
	return nil, fmt.Errorf("%w: no reservation named %s", types.ErrNotFound, name)
}

func (s *reservationService) GetByIP(ctx context.Context, ip string) ([]types.Reservation, error) {
	all, err := s.List(ctx)
	if err != nil {
		return nil, err
	}
	var matches []types.Reservation
	for _, r := range all {
		if r.IP == ip {
			matches = append(matches, r)
		}
	}
	return matches, nil
}

func (s *reservationService) Create(ctx context.Context, r *types.Reservation) (*types.Reservation, error) {
	// Validate first, and before any read, so that a name which could corrupt
	// dnsmasq's configuration never reaches the device.
	if err := r.Validate(); err != nil {
		return nil, err
	}
	if err := s.requireDomain(ctx); err != nil {
		return nil, err
	}

	all, err := s.List(ctx)
	if err != nil {
		return nil, err
	}
	for _, existing := range all {
		if strings.EqualFold(existing.MAC, r.MAC) {
			return nil, fmt.Errorf("%w: MAC %s is already reserved for %s",
				types.ErrConflict, r.MAC, existing.IP)
		}
	}

	args := map[string]any{"mac": r.MAC, "ip": r.IP, "name": r.Name}
	if err := s.transport.Call(ctx, reservationGroup, reservationAdd, args, nil); err != nil {
		return nil, fmt.Errorf("gogl: add reservation: %w", err)
	}
	return r, nil
}

func (s *reservationService) Update(ctx context.Context, r *types.Reservation) (*types.Reservation, error) {
	if err := r.Validate(); err != nil {
		return nil, err
	}
	if err := s.requireDomain(ctx); err != nil {
		return nil, err
	}

	// The firmware's set_static_bind silently does nothing for an unknown MAC, so
	// the existence check has to happen here for Update to report ErrNotFound.
	if _, err := s.GetByMAC(ctx, r.MAC); err != nil {
		return nil, err
	}

	args := map[string]any{"mac": r.MAC, "ip": r.IP, "name": r.Name}
	if err := s.transport.Call(ctx, reservationGroup, reservationSet, args, nil); err != nil {
		return nil, fmt.Errorf("gogl: update reservation: %w", err)
	}
	return r, nil
}

// Delete removes the reservation for mac.
func (s *reservationService) Delete(ctx context.Context, mac string) error {
	normalized, err := types.NormalizeMAC(mac)
	if err != nil {
		return err
	}

	// Same reason as Update: confirm it exists so a missing entry is ErrNotFound
	// rather than a silent success.
	if _, err := s.GetByMAC(ctx, normalized); err != nil {
		return err
	}

	args := map[string]any{"mode": removeModeSingle, "mac": normalized}
	if err := s.transport.Call(ctx, reservationGroup, reservationRemove, args, nil); err != nil {
		return fmt.Errorf("gogl: remove reservation: %w", err)
	}
	return nil
}

// DeleteAll removes every reservation in one call, using the firmware's mode 1.
//
// Delete refuses a missing MAC so that a typo is an error rather than a silent
// no-op; DeleteAll on an empty table is simply a no-op, because "make sure there
// are none" is a reasonable thing to ask for.
func (s *reservationService) DeleteAll(ctx context.Context) error {
	args := map[string]any{"mode": removeModeAll}
	if err := s.transport.Call(ctx, reservationGroup, reservationRemove, args, nil); err != nil {
		return fmt.Errorf("gogl: remove all reservations: %w", err)
	}
	return nil
}
