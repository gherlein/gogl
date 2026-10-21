package gogl_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"strconv"
	"strings"
	"testing"

	gogl "github.com/gherlein/gogl/src"
	"github.com/gherlein/gogl/src/mock"
)

// clientFor points a Client at a mock server by splitting the mock's URL back
// into host and port.
func clientFor(t *testing.T, s *mock.Server) *gogl.Client {
	t.Helper()
	u, err := url.Parse(s.URL())
	if err != nil {
		t.Fatalf("parse mock URL: %v", err)
	}
	port, err := strconv.Atoi(u.Port())
	if err != nil {
		t.Fatalf("parse port %q: %v", u.Port(), err)
	}

	c, err := gogl.New(gogl.Config{
		Host:              u.Hostname(),
		Port:              port,
		Password:          s.Password(),
		KeepaliveInterval: -1,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() {
		if err := c.Close(); err != nil {
			t.Errorf("Close: %v", err)
		}
	})
	return c
}

func TestNewRequiresHost(t *testing.T) {
	_, err := gogl.New(gogl.Config{Password: "secret"})
	if err == nil {
		t.Fatal("New without Host succeeded")
	}
	if !strings.Contains(err.Error(), "host") {
		t.Errorf("error %q does not mention the missing host", err)
	}
}

func TestNewRequiresPassword(t *testing.T) {
	_, err := gogl.New(gogl.Config{Host: "192.168.8.1"})
	if err == nil {
		t.Fatal("New without Password succeeded")
	}
	if !strings.Contains(err.Error(), "password") {
		t.Errorf("error %q does not mention the missing password", err)
	}
}

// New must not contact the router, so constructing a client is cheap and cannot
// fail on a network error.
func TestNewDoesNotContactRouter(t *testing.T) {
	// 192.0.2.0/24 is reserved for documentation and never routable.
	c, err := gogl.New(gogl.Config{Host: "192.0.2.1", Password: "secret"})
	if err != nil {
		t.Fatalf("New against an unreachable host failed: %v", err)
	}
	if err := c.Close(); err != nil {
		t.Errorf("Close: %v", err)
	}
}

// Port 80 and HTTP, because that is what GL.iNet firmware serves. A habit
// carried over from the UDM Pro's 443 would fail here.
func TestConfigDefaultsToHTTPPort80(t *testing.T) {
	c, err := gogl.New(gogl.Config{Host: "192.168.8.1", Password: "secret"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer c.Close()

	if got := c.Endpoint(); got != "http://192.168.8.1:80/rpc" {
		t.Errorf("Endpoint() = %q, want http://192.168.8.1:80/rpc", got)
	}
}

func TestConfigHTTPS(t *testing.T) {
	c, err := gogl.New(gogl.Config{Host: "192.168.8.1", Port: 443, HTTPS: true, Password: "secret"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer c.Close()

	if got := c.Endpoint(); got != "https://192.168.8.1:443/rpc" {
		t.Errorf("Endpoint() = %q, want https://192.168.8.1:443/rpc", got)
	}
}

// Username defaults to root, which is the only account standard firmware has.
func TestConfigDefaultUsername(t *testing.T) {
	s := mock.NewServer(t, mock.Options{Password: "secret"})
	s.LoadFixture("lan", "get_config", json.RawMessage(`{}`))
	c := clientFor(t, s)

	if err := c.Call(context.Background(), "lan", "get_config", nil, nil); err != nil {
		t.Errorf("Call with the default username: %v", err)
	}
}

func TestCallEscapeHatch(t *testing.T) {
	s := mock.NewServer(t, mock.Options{Password: "secret"})
	s.LoadFixture("anygroup", "anymethod", json.RawMessage(`{"value":42}`))
	c := clientFor(t, s)

	var out struct {
		Value int `json:"value"`
	}
	if err := c.Call(context.Background(), "anygroup", "anymethod", nil, &out); err != nil {
		t.Fatalf("Call: %v", err)
	}
	if out.Value != 42 {
		t.Errorf("Value = %d, want 42", out.Value)
	}
}

// The root package must translate the transport's error type into its own, so
// callers never need to import transport.
func TestCallReturnsRPCError(t *testing.T) {
	s := mock.NewServer(t, mock.Options{Password: "secret"})
	c := clientFor(t, s)

	err := c.Call(context.Background(), "nope", "nope", nil, nil)
	if err == nil {
		t.Fatal("Call on an unknown group succeeded")
	}

	var rpcErr *gogl.RPCError
	if !errors.As(err, &rpcErr) {
		t.Fatalf("error is %T, want *gogl.RPCError", err)
	}
	if rpcErr.Group != "nope" || rpcErr.Method != "nope" {
		t.Errorf("error lost its call site: %+v", rpcErr)
	}
	if !errors.Is(err, gogl.ErrNotFound) {
		t.Errorf("errors.Is(err, ErrNotFound) = false; RPCError.Unwrap should map code %d", rpcErr.Code)
	}
}

func TestRPCErrorMessage(t *testing.T) {
	err := &gogl.RPCError{Code: -32000, Message: "Access denied", Group: "lan", Method: "get_config"}
	const want = "gogl: rpc error -32000 on lan.get_config: Access denied"
	if got := err.Error(); got != want {
		t.Errorf("Error() = %q, want %q", got, want)
	}
}

func TestRPCErrorUnwrap(t *testing.T) {
	tests := []struct {
		name string
		code int
		want error
	}{
		{"access denied maps to session expired", gogl.CodeAccessDenied, gogl.ErrSessionExpired},
		{"not found", gogl.CodeNotFound, gogl.ErrNotFound},
		{"unknown code unwraps to nil", -1, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := &gogl.RPCError{Code: tt.code}
			if got := err.Unwrap(); !errors.Is(got, tt.want) {
				t.Errorf("Unwrap() = %v, want %v", got, tt.want)
			}
		})
	}
}

// The sentinels re-exported here must be the same values the leaf packages
// return, or errors.Is would silently fail across package boundaries.
func TestSentinelsAreShared(t *testing.T) {
	s := mock.NewServer(t, mock.Options{Password: "secret"})
	s.SetReservations(nil)
	c := clientFor(t, s)

	_, err := c.Reservations().GetByMAC(context.Background(), "aa:bb:cc:dd:ee:99")
	if !errors.Is(err, gogl.ErrNotFound) {
		t.Errorf("errors.Is(err, gogl.ErrNotFound) = false for %v", err)
	}
}

func TestServiceAccessors(t *testing.T) {
	s := mock.NewServer(t, mock.Options{Password: "secret"})
	c := clientFor(t, s)

	if c.Network() == nil {
		t.Error("Network() returned nil")
	}
	if c.Reservations() == nil {
		t.Error("Reservations() returned nil")
	}
	if c.Clients() == nil {
		t.Error("Clients() returned nil")
	}
	if c.System() == nil {
		t.Error("System() returned nil")
	}
}

func TestCloseIsIdempotent(t *testing.T) {
	c, err := gogl.New(gogl.Config{Host: "192.0.2.1", Password: "secret"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := c.Close(); err != nil {
		t.Fatalf("first Close: %v", err)
	}
	if err := c.Close(); err != nil {
		t.Errorf("second Close: %v", err)
	}
}
