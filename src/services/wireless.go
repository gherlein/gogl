package services

import (
	"context"
	"fmt"
	"net"
	"strings"

	"github.com/gherlein/gogl/src/transport"
	"github.com/gherlein/gogl/src/types"
)

// CONFIRMED against a GL-SFT1200 on firmware 4.3.28.
//
// wifi.get_config returns one entry per radio under "res", each carrying its
// interfaces. wifi.set_config writes one interface at a time and requires
// iface_name alongside any interface-scoped field.
const (
	wirelessGroup     = "wifi"
	wirelessGetConfig = "get_config"
	wirelessSetConfig = "set_config"
)

// The firmware's own name for a wired client, in client.get_list's iface field.
// Anything else is a radio.
const ifaceCable = "cable"

type wirelessConfig struct {
	Res []types.WirelessRadio `json:"res"`
}

type wirelessService struct {
	transport transport.Transport
	clients   ClientService

	// localAddr reports the address this process reaches the router from. Injected
	// so the wireless-session guard can be tested without a network.
	localAddr func(ctx context.Context) (string, error)
}

// NewWirelessService returns the service that reads and writes wireless identity.
func NewWirelessService(t transport.Transport, endpoint string) WirelessService {
	return &wirelessService{
		transport: t,
		clients:   NewClientService(t),
		localAddr: func(ctx context.Context) (string, error) { return localAddrTo(ctx, endpoint) },
	}
}

// Radios returns every radio and the interfaces on it.
func (s *wirelessService) Radios(ctx context.Context) ([]types.WirelessRadio, error) {
	var cfg wirelessConfig
	if err := s.transport.Call(ctx, wirelessGroup, wirelessGetConfig, nil, &cfg); err != nil {
		return nil, fmt.Errorf("gogl: read wireless config: %w", err)
	}

	// The firmware reports the band on the radio, not on the interface. Stamping it
	// down means a caller holding one interface still knows which radio it is on.
	for i := range cfg.Res {
		for j := range cfg.Res[i].Ifaces {
			cfg.Res[i].Ifaces[j].Band = cfg.Res[i].Band
		}
	}
	return cfg.Res, nil
}

// Interfaces returns every wireless interface across all radios, flattened.
func (s *wirelessService) Interfaces(ctx context.Context) ([]types.WirelessInterface, error) {
	radios, err := s.Radios(ctx)
	if err != nil {
		return nil, err
	}
	var out []types.WirelessInterface
	for _, r := range radios {
		out = append(out, r.Ifaces...)
	}
	return out, nil
}

// Get returns the interface named name, or ErrNotFound.
func (s *wirelessService) Get(ctx context.Context, name string) (*types.WirelessInterface, error) {
	ifaces, err := s.Interfaces(ctx)
	if err != nil {
		return nil, err
	}
	for i := range ifaces {
		if ifaces[i].Name == name {
			return &ifaces[i], nil
		}
	}

	// A wrong interface name is the most likely mistake here, so name the valid ones
	// rather than only reporting the miss.
	available := make([]string, 0, len(ifaces))
	for _, f := range ifaces {
		available = append(available, f.Name)
	}
	return nil, fmt.Errorf("%w: no wireless interface named %q (have: %s)",
		types.ErrNotFound, name, strings.Join(available, ", "))
}

// SetSSID writes the SSID of one interface.
//
// Refused with ErrWirelessSession when the calling session arrives over WiFi.
// Applying it would drop every client on that radio, including the caller, and
// unlike a LAN renumber there is no new address to reconnect at: the network the
// session was using stops existing under that name.
func (s *wirelessService) SetSSID(ctx context.Context, name, ssid string) error {
	return s.SetInterface(ctx, name, types.InterfaceChanges{SSID: &ssid})
}

// Radio returns the radio named device, or ErrNotFound listing the valid names.
func (s *wirelessService) Radio(ctx context.Context, device string) (*types.WirelessRadio, error) {
	radios, err := s.Radios(ctx)
	if err != nil {
		return nil, err
	}
	for i := range radios {
		if radios[i].Device == device {
			return &radios[i], nil
		}
	}

	available := make([]string, 0, len(radios))
	for _, r := range radios {
		available = append(available, fmt.Sprintf("%s (%s)", r.Device, r.Band))
	}
	return nil, fmt.Errorf("%w: no radio named %q (have: %s)",
		types.ErrNotFound, device, strings.Join(available, ", "))
}

// radioFor returns the radio carrying the named interface.
func (s *wirelessService) radioFor(ctx context.Context, iface string) (*types.WirelessRadio, error) {
	radios, err := s.Radios(ctx)
	if err != nil {
		return nil, err
	}
	for i := range radios {
		for _, f := range radios[i].Ifaces {
			if f.Name == iface {
				return &radios[i], nil
			}
		}
	}

	var available []string
	for _, r := range radios {
		for _, f := range r.Ifaces {
			available = append(available, f.Name)
		}
	}
	return nil, fmt.Errorf("%w: no wireless interface named %q (have: %s)",
		types.ErrNotFound, iface, strings.Join(available, ", "))
}

// SetInterface writes a partial update to one interface.
//
// Only the fields set in changes are sent, matching wifi.set_config's own
// semantics: an absent field is left alone. Sending the unchanged values back would
// work but would make every write a chance to clobber something a concurrent edit
// had changed.
func (s *wirelessService) SetInterface(ctx context.Context, name string, changes types.InterfaceChanges) error {
	if changes.Empty() {
		return fmt.Errorf("%w: nothing to change on %s", types.ErrInvalidInput, name)
	}

	// Validate against the radio that carries the interface, so an unsupported
	// encryption is reported with the supported ones named. This also confirms the
	// interface exists, before the session guard, so a typo reads as a typo.
	radio, err := s.radioFor(ctx, name)
	if err != nil {
		return err
	}
	if err := changes.Validate(radio); err != nil {
		return err
	}
	if err := s.requireWiredSession(ctx); err != nil {
		return err
	}

	args := map[string]any{"iface_name": name}
	if changes.SSID != nil {
		args["ssid"] = *changes.SSID
	}
	if changes.Key != nil {
		args["key"] = *changes.Key
	}
	if changes.Encryption != nil {
		args["encryption"] = *changes.Encryption
	}
	if changes.Hidden != nil {
		args["hidden"] = *changes.Hidden
	}
	if changes.Enabled != nil {
		args["enabled"] = *changes.Enabled
	}

	if err := s.transport.Call(ctx, wirelessGroup, wirelessSetConfig, args, nil); err != nil {
		return fmt.Errorf("gogl: write wireless interface %s: %w", name, err)
	}
	return nil
}

// SetRadio writes a partial update to one radio's tuning.
//
// Radio-scoped fields go in their own call, keyed by device rather than iface_name,
// because that is how the firmware scopes them: channel and bandwidth belong to the
// radio, and every interface on it inherits the change.
func (s *wirelessService) SetRadio(ctx context.Context, device string, changes types.RadioChanges) error {
	if changes.Empty() {
		return fmt.Errorf("%w: nothing to change on %s", types.ErrInvalidInput, device)
	}

	radio, err := s.Radio(ctx, device)
	if err != nil {
		return err
	}
	if err := changes.Validate(radio); err != nil {
		return err
	}
	// Retuning a radio drops its clients for at least a re-association, and a
	// channel change on a DFS channel for considerably longer. Same guard as an SSID
	// change, for the same reason.
	if err := s.requireWiredSession(ctx); err != nil {
		return err
	}

	args := map[string]any{"device": device}
	if changes.Channel != nil {
		args["channel"] = *changes.Channel
	}
	if changes.HTMode != nil {
		args["htmode"] = *changes.HTMode
	}
	if changes.HWMode != nil {
		args["hwmode"] = *changes.HWMode
	}
	if changes.TXPower != nil {
		args["txpower"] = *changes.TXPower
	}

	if err := s.transport.Call(ctx, wirelessGroup, wirelessSetConfig, args, nil); err != nil {
		return fmt.Errorf("gogl: write radio %s: %w", device, err)
	}
	return nil
}

// SessionInterface reports the firmware's name for the link this session arrives
// over: "cable", "2.4G", "5G", or "" when the session is not on the LAN at all.
//
// Exported because a caller that wants to explain a refusal, or decide before
// attempting a write, needs the same answer the guard uses.
func (s *wirelessService) SessionInterface(ctx context.Context) (string, error) {
	local, err := s.localAddr(ctx)
	if err != nil {
		return "", err
	}

	clients, err := s.clients.List(ctx)
	if err != nil {
		return "", err
	}
	for _, c := range clients {
		if c.IP == local {
			return c.Iface, nil
		}
	}

	// Not in the client list: the session is arriving from off-LAN through a
	// router, so a radio on this device cannot be the path.
	return "", nil
}

// requireWiredSession refuses a wireless write issued over a wireless session.
func (s *wirelessService) requireWiredSession(ctx context.Context) error {
	iface, err := s.SessionInterface(ctx)
	if err != nil {
		// Failing to determine the path is not permission to proceed: the whole
		// point is that the operator cannot recover remotely if this is wrong.
		return fmt.Errorf("%w: cannot determine how this session reaches the router: %w",
			types.ErrWirelessSession, err)
	}

	switch iface {
	case "", ifaceCable:
		return nil
	default:
		return fmt.Errorf("%w: this session is on %s\n"+
			"  changing wireless would drop it, with no address to reconnect at\n"+
			"  connect over ethernet and try again",
			types.ErrWirelessSession, iface)
	}
}

// localAddrTo reports the local address this host uses to reach endpoint.
//
// A UDP dial is used because it selects a route and binds a local address without
// sending anything or requiring the peer to exist. The alternative, inspecting the
// live HTTP connection, is not reachable through http.Client without a custom
// dialer whose only purpose would be this.
func localAddrTo(ctx context.Context, endpoint string) (string, error) {
	host := endpoint
	// Accept a bare host, host:port, or a URL.
	if i := strings.Index(host, "://"); i >= 0 {
		host = host[i+3:]
	}
	if i := strings.IndexAny(host, "/"); i >= 0 {
		host = host[:i]
	}
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	if host == "" {
		return "", fmt.Errorf("no router address to route to")
	}

	var d net.Dialer
	conn, err := d.DialContext(ctx, "udp", net.JoinHostPort(host, "80"))
	if err != nil {
		return "", fmt.Errorf("routing to %s: %w", host, err)
	}
	defer conn.Close()

	addr, ok := conn.LocalAddr().(*net.UDPAddr)
	if !ok {
		return "", fmt.Errorf("unexpected local address type %T", conn.LocalAddr())
	}
	return addr.IP.String(), nil
}

// NewWirelessServiceForTest builds the service with an injected way to report the
// local address the router is reached from.
//
// The production path routes a UDP socket to discover it, which a test cannot
// control: the answer depends on the host's routing table. Injecting it is what
// makes the wireless-session guard testable at all, and that guard is the one piece
// of gogl whose failure cannot be undone remotely.
func NewWirelessServiceForTest(t transport.Transport, localAddr func(context.Context) (string, error)) WirelessService {
	return &wirelessService{
		transport: t,
		clients:   NewClientService(t),
		localAddr: localAddr,
	}
}
