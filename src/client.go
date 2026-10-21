package gogl

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/gherlein/gogl/src/services"
	"github.com/gherlein/gogl/src/transport"
)

// Defaults reflect what GL.iNet firmware actually serves: HTTP on port 80, and
// the root account, which is the only one standard firmware has.
const (
	DefaultPort     = 80
	DefaultUsername = "root"
	DefaultTimeout  = 10 * time.Second
)

// Config configures a Client. Zero values are safe: the resulting client
// verifies TLS and uses conservative timeouts.
type Config struct {
	Host string // required
	Port int    // default 80
	// HTTPS selects TLS. Default false, because GL.iNet serves plain HTTP on 80.
	HTTPS bool

	Username string // default "root"
	Password string // required

	// InsecureSkipVerify disables TLS certificate verification. Named for what it
	// does, and false by default, so the library is secure at its zero value even
	// though the CLIs default the other way in order to reach a device with a
	// self-signed certificate. A library must not be insecure by default.
	InsecureSkipVerify bool

	Timeout           time.Duration
	KeepaliveInterval time.Duration
	MaxConcurrent     int
}

// Client is a connection to a GL.iNet router. Safe for concurrent use.
type Client struct {
	cfg       Config
	endpoint  string
	transport transport.Transport
}

// New builds a Client.
//
// It does not contact the router: the first call authenticates lazily, so
// construction is cheap and cannot fail on a network error.
func New(cfg Config) (*Client, error) {
	if cfg.Host == "" {
		return nil, errors.New("gogl: config requires a host")
	}
	if cfg.Password == "" {
		return nil, errors.New("gogl: config requires a password")
	}

	if cfg.Port == 0 {
		cfg.Port = DefaultPort
	}
	if cfg.Username == "" {
		cfg.Username = DefaultUsername
	}
	if cfg.Timeout == 0 {
		cfg.Timeout = DefaultTimeout
	}

	scheme := "http"
	httpClient := &http.Client{Timeout: cfg.Timeout}
	if cfg.HTTPS {
		scheme = "https"
		httpClient.Transport = &http.Transport{
			TLSClientConfig: &tls.Config{InsecureSkipVerify: cfg.InsecureSkipVerify}, //nolint:gosec // documented opt-in for self-signed router certificates
		}
	}
	endpoint := fmt.Sprintf("%s://%s:%d/rpc", scheme, cfg.Host, cfg.Port)

	return &Client{
		cfg:      cfg,
		endpoint: endpoint,
		transport: transport.New(transport.Config{
			URL:               endpoint,
			Username:          cfg.Username,
			Password:          cfg.Password,
			HTTPClient:        httpClient,
			KeepaliveInterval: cfg.KeepaliveInterval,
			MaxConcurrent:     cfg.MaxConcurrent,
		}),
	}, nil
}

// Endpoint returns the JSON-RPC URL this client talks to.
func (c *Client) Endpoint() string { return c.endpoint }

// Network reads and writes the router's LAN and DHCP configuration.
//
// Set is refused while reservations exist, and changing the LAN address will drop
// the session making the call.
func (c *Client) Network() services.NetworkService {
	return services.NewNetworkService(c.transport)
}

// Reservations manages static MAC-to-IP bindings.
//
// A binding creates no DNS record; use Hosts for names. Writes are refused until a
// DNS domain has been configured.
func (c *Client) Reservations() services.ReservationService {
	return services.NewReservationService(c.transport)
}

// Hosts manages DNS names through the router's host file. This is the only way to
// create a DNS record: a reservation does not.
func (c *Client) Hosts() services.HostsService {
	return services.NewHostsService(c.transport)
}

// Wireless reads and writes wireless identity.
//
// SetSSID is refused when this session arrives over WiFi: applying it would drop
// the session with no address to reconnect at.
func (c *Client) Wireless() services.WirelessService {
	return services.NewWirelessService(c.transport, c.cfg.Host)
}

// Clients reads stations known to the router. Read-only.
func (c *Client) Clients() services.ClientService {
	return services.NewClientService(c.transport)
}

// System reads device identity. Read-only.
func (c *Client) System() services.SystemService {
	return services.NewSystemService(c.transport)
}

// Call invokes an arbitrary group and method, bypassing the typed services.
//
// It exists so that endpoints not yet modelled remain reachable, and because API
// discovery is done with it. Prefer a typed service where one exists.
func (c *Client) Call(ctx context.Context, group, method string, args, out any) error {
	err := c.transport.Call(ctx, group, method, args, out)

	// Translate the transport's error into this package's type so callers need
	// not import transport to inspect a failure.
	var tErr *transport.Error
	if errors.As(err, &tErr) {
		return &RPCError{Code: tErr.Code, Message: tErr.Message, Group: tErr.Group, Method: tErr.Method}
	}
	return err
}

// Close stops the keepalive goroutine and releases resources.
func (c *Client) Close() error { return c.transport.Close() }
