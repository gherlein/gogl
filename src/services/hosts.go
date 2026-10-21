package services

import (
	"context"
	"fmt"

	"github.com/gherlein/gogl/src/transport"
	"github.com/gherlein/gogl/src/types"
)

// CONFIRMED against a GL-SFT1200 on firmware 4.3.28, 2026-07-28.
//
// dns.get_host and dns.set_host read and write the router's hosts(5) file as a
// single string. dnsmasq answers from it, verified by writing an entry and
// resolving both the bare name and an arbitrary FQDN against the router.
//
// This is the only way gogl can create a DNS record. A static bind cannot: its
// name is a label. See types.Reservation.
const (
	hostsGroup   = "dns"
	hostsGetHost = "get_host"
	hostsSetHost = "set_host"
)

type hostContent struct {
	Content string `json:"content"`
}

type hostsService struct {
	transport transport.Transport
}

// NewHostsService returns the service that manages DNS names.
func NewHostsService(t transport.Transport) HostsService {
	return &hostsService{transport: t}
}

// Get reads and parses the router's host file.
func (s *hostsService) Get(ctx context.Context) (*types.HostFile, error) {
	var raw hostContent
	if err := s.transport.Call(ctx, hostsGroup, hostsGetHost, nil, &raw); err != nil {
		return nil, fmt.Errorf("gogl: read host file: %w", err)
	}
	return types.ParseHostFile(raw.Content), nil
}

// Put writes the host file back.
//
// Everything outside gogl's managed block is preserved: the file also carries the
// loopback and IPv6 boilerplate the firmware ships, and overwriting that would
// break local resolution on the router itself.
func (s *hostsService) Put(ctx context.Context, f *types.HostFile) error {
	content := f.Render()

	// Check before writing. The firmware answers -32602 Invalid params without
	// saying which character it disliked, which is a miserable thing to debug from
	// the far side of an RPC.
	if err := types.ValidateContent(content); err != nil {
		return err
	}

	args := hostContent{Content: content}
	if err := s.transport.Call(ctx, hostsGroup, hostsSetHost, args, nil); err != nil {
		return fmt.Errorf("gogl: write host file: %w", err)
	}
	return nil
}

// Domain returns the configured DNS domain, or the empty string if gogl has never
// set one on this router.
func (s *hostsService) Domain(ctx context.Context) (string, error) {
	f, err := s.Get(ctx)
	if err != nil {
		return "", err
	}
	return f.Domain, nil
}

// SetDomain configures the DNS domain and requalifies every managed entry.
//
// The domain lives in the managed block's marker line, because the firmware
// exposes no setting for a dnsmasq domain and /ubus is not routed on this model.
// Storing it in the file keeps it on the device rather than in a config file
// beside whichever machine happened to run the tool.
func (s *hostsService) SetDomain(ctx context.Context, domain string) error {
	if err := types.ValidateName(domain); err != nil {
		return fmt.Errorf("domain %q: %w", domain, err)
	}

	f, err := s.Get(ctx)
	if err != nil {
		return err
	}

	// Rebuild each entry under the new domain, so existing names do not keep a
	// stale suffix.
	previous := f.Entries
	f.Domain = domain
	f.Clear()
	for _, e := range previous {
		if len(e.Names) == 0 {
			continue
		}
		if err := f.Set(e.Names[0], e.IP); err != nil {
			return fmt.Errorf("requalifying %q: %w", e.Names[0], err)
		}
	}

	return s.Put(ctx, f)
}

// List returns the managed host entries.
func (s *hostsService) List(ctx context.Context) ([]types.HostEntry, error) {
	f, err := s.Get(ctx)
	if err != nil {
		return nil, err
	}
	return f.Entries, nil
}

// Set points name at ip, creating or replacing the entry.
func (s *hostsService) Set(ctx context.Context, name, ip string) error {
	f, err := s.Get(ctx)
	if err != nil {
		return err
	}
	if f.Domain == "" {
		return types.ErrDomainNotSet
	}
	if err := f.Set(name, ip); err != nil {
		return err
	}
	return s.Put(ctx, f)
}

// Remove drops the entry for name. Reports ErrNotFound if there was none.
func (s *hostsService) Remove(ctx context.Context, name string) error {
	f, err := s.Get(ctx)
	if err != nil {
		return err
	}
	if !f.Remove(name) {
		return fmt.Errorf("%w: no host entry named %s", types.ErrNotFound, name)
	}
	return s.Put(ctx, f)
}

// Clear removes every managed entry, leaving the domain and the unmanaged parts of
// the file intact.
//
// An already-empty block is a no-op rather than a write. "Make sure there is
// nothing there" is a reasonable request, and answering it by pushing a file to the
// device is how clearing an empty router came to fail.
func (s *hostsService) Clear(ctx context.Context) error {
	f, err := s.Get(ctx)
	if err != nil {
		return err
	}
	if len(f.Entries) == 0 {
		return nil
	}
	f.Clear()
	return s.Put(ctx, f)
}
