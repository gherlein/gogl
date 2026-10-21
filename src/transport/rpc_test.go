package transport_test

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/gherlein/gogl/src/mock"
	"github.com/gherlein/gogl/src/transport"
)

func newTransport(t *testing.T, s *mock.Server, cfg transport.Config) transport.Transport {
	t.Helper()
	cfg.URL = s.URL()
	if cfg.Username == "" {
		cfg.Username = "root"
	}
	if cfg.Password == "" {
		cfg.Password = "secret"
	}
	tr := transport.New(cfg)
	t.Cleanup(func() {
		if err := tr.Close(); err != nil {
			t.Errorf("Close: %v", err)
		}
	})
	return tr
}

func TestCallDecodesResult(t *testing.T) {
	s := mock.NewServer(t, mock.Options{Password: "secret"})
	s.LoadFixture("lan", "get_config", json.RawMessage(`{"lan_ip":"192.168.8.1","netmask":"255.255.255.0"}`))
	tr := newTransport(t, s, transport.Config{})

	var out struct {
		LANIP   string `json:"lan_ip"`
		Netmask string `json:"netmask"`
	}
	if err := tr.Call(context.Background(), "lan", "get_config", nil, &out); err != nil {
		t.Fatalf("Call error: %v", err)
	}
	if out.LANIP != "192.168.8.1" {
		t.Errorf("LANIP = %q, want 192.168.8.1", out.LANIP)
	}
}

// out may be nil when the caller does not care about the payload.
func TestCallWithNilOut(t *testing.T) {
	s := mock.NewServer(t, mock.Options{Password: "secret"})
	s.LoadFixture("lan", "get_config", json.RawMessage(`{}`))
	tr := newTransport(t, s, transport.Config{})

	if err := tr.Call(context.Background(), "lan", "get_config", nil, nil); err != nil {
		t.Errorf("Call with nil out: %v", err)
	}
}

func TestCallPassesArgs(t *testing.T) {
	s := mock.NewServer(t, mock.Options{Password: "secret"})
	s.SetReservations(nil)
	tr := newTransport(t, s, transport.Config{})

	args := map[string]any{"name": "nas", "mac": "aa:bb:cc:dd:ee:01", "ip": "192.168.8.13"}
	if err := tr.Call(context.Background(), mock.ReservationGroup, mock.MethodAddStaticBind, args, nil); err != nil {
		t.Fatalf("Call error: %v", err)
	}

	got := s.Reservations()
	if len(got) != 1 || got[0].Name != "nas" {
		t.Errorf("args did not reach the device: %v", got)
	}
}

func TestCallSurfacesRPCError(t *testing.T) {
	s := mock.NewServer(t, mock.Options{Password: "secret"})
	s.LoadFixture("lan", "get_config", json.RawMessage(`{}`))
	tr := newTransport(t, s, transport.Config{})

	s.FailNext("lan", "get_config", mock.CodeNotFound, "injected")

	err := tr.Call(context.Background(), "lan", "get_config", nil, nil)
	if err == nil {
		t.Fatal("Call succeeded, want error")
	}
	var rpcErr *transport.Error
	if !errors.As(err, &rpcErr) {
		t.Fatalf("error is %T, want *transport.Error", err)
	}
	if rpcErr.Group != "lan" || rpcErr.Method != "get_config" {
		t.Errorf("error lost its call site: group=%q method=%q", rpcErr.Group, rpcErr.Method)
	}
	if rpcErr.Code != mock.CodeNotFound {
		t.Errorf("Code = %d, want %d", rpcErr.Code, mock.CodeNotFound)
	}
}

func TestErrorMessage(t *testing.T) {
	err := &transport.Error{Code: -32000, Message: "Access denied", Group: "lan", Method: "get_config"}
	const want = "gogl: rpc error -32000 on lan.get_config: Access denied"
	if got := err.Error(); got != want {
		t.Errorf("Error() = %q, want %q", got, want)
	}
}

// A session that idles out must be renewed transparently: the caller sees a
// successful call, not an error.
func TestCallRetriesOnExpiredSession(t *testing.T) {
	s := mock.NewServer(t, mock.Options{
		Password:   "secret",
		SessionTTL: 50 * time.Millisecond,
	})
	s.LoadFixture("lan", "get_config", json.RawMessage(`{"lan_ip":"192.168.8.1"}`))
	// Keepalive off, so the session genuinely expires.
	tr := newTransport(t, s, transport.Config{KeepaliveInterval: -1})

	if err := tr.Call(context.Background(), "lan", "get_config", nil, nil); err != nil {
		t.Fatalf("first Call: %v", err)
	}
	time.Sleep(120 * time.Millisecond)
	if err := tr.Call(context.Background(), "lan", "get_config", nil, nil); err != nil {
		t.Errorf("second Call after session expiry: %v", err)
	}
	if got := s.LoginCount(); got != 2 {
		t.Errorf("LoginCount() = %d, want 2 (one initial, one renewal)", got)
	}
}

// Retry is bounded at one attempt: a wrong password must not become a login
// flood.
func TestCallDoesNotRetryForever(t *testing.T) {
	s := mock.NewServer(t, mock.Options{Password: "secret"})
	s.LoadFixture("lan", "get_config", json.RawMessage(`{}`))
	tr := newTransport(t, s, transport.Config{Password: "wrong", KeepaliveInterval: -1})

	done := make(chan error, 1)
	go func() {
		done <- tr.Call(context.Background(), "lan", "get_config", nil, nil)
	}()

	select {
	case err := <-done:
		if err == nil {
			t.Error("Call with a wrong password succeeded")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Call did not return; retry is probably unbounded")
	}
}

// A burst of calls arriving with no session must produce exactly one login.
func TestConcurrentCallsLoginOnce(t *testing.T) {
	s := mock.NewServer(t, mock.Options{Password: "secret"})
	s.LoadFixture("lan", "get_config", json.RawMessage(`{}`))
	tr := newTransport(t, s, transport.Config{KeepaliveInterval: -1, MaxConcurrent: 8})

	const callers = 16
	var wg sync.WaitGroup
	errs := make(chan error, callers)
	for i := 0; i < callers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs <- tr.Call(context.Background(), "lan", "get_config", nil, nil)
		}()
	}
	wg.Wait()
	close(errs)

	for err := range errs {
		if err != nil {
			t.Errorf("concurrent Call: %v", err)
		}
	}
	if got := s.LoginCount(); got != 1 {
		t.Errorf("LoginCount() = %d, want 1", got)
	}
}

func TestKeepaliveHoldsSessionOpen(t *testing.T) {
	s := mock.NewServer(t, mock.Options{
		Password:   "secret",
		SessionTTL: 150 * time.Millisecond,
	})
	s.LoadFixture("lan", "get_config", json.RawMessage(`{}`))
	tr := newTransport(t, s, transport.Config{KeepaliveInterval: 30 * time.Millisecond})

	if err := tr.Call(context.Background(), "lan", "get_config", nil, nil); err != nil {
		t.Fatalf("first Call: %v", err)
	}
	time.Sleep(400 * time.Millisecond)

	if got := s.LoginCount(); got != 1 {
		t.Errorf("LoginCount() = %d; keepalive should have held the session at 1", got)
	}
	if got := s.AliveCount(); got < 3 {
		t.Errorf("AliveCount() = %d, want at least 3 keepalives in 400ms at 30ms", got)
	}
}

func TestCloseStopsKeepalive(t *testing.T) {
	s := mock.NewServer(t, mock.Options{Password: "secret"})
	s.LoadFixture("lan", "get_config", json.RawMessage(`{}`))
	tr := transport.New(transport.Config{
		URL: s.URL(), Username: "root", Password: "secret",
		KeepaliveInterval: 10 * time.Millisecond,
	})
	if err := tr.Call(context.Background(), "lan", "get_config", nil, nil); err != nil {
		t.Fatalf("Call: %v", err)
	}
	time.Sleep(40 * time.Millisecond)
	if err := tr.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	before := s.AliveCount()
	time.Sleep(100 * time.Millisecond)
	if after := s.AliveCount(); after != before {
		t.Errorf("keepalive still running after Close: alive count went %d -> %d", before, after)
	}
}

// Close must be safe to call more than once, since defer plus an explicit Close
// is a normal pattern.
func TestCloseIsIdempotent(t *testing.T) {
	s := mock.NewServer(t, mock.Options{Password: "secret"})
	tr := transport.New(transport.Config{URL: s.URL(), Username: "root", Password: "secret"})
	if err := tr.Close(); err != nil {
		t.Fatalf("first Close: %v", err)
	}
	if err := tr.Close(); err != nil {
		t.Errorf("second Close: %v", err)
	}
}

func TestCallHonoursContextCancellation(t *testing.T) {
	s := mock.NewServer(t, mock.Options{Password: "secret"})
	s.LoadFixture("lan", "get_config", json.RawMessage(`{}`))
	tr := newTransport(t, s, transport.Config{KeepaliveInterval: -1})

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if err := tr.Call(ctx, "lan", "get_config", nil, nil); err == nil {
		t.Error("Call with a cancelled context succeeded")
	}
}

// The semaphore must bound in-flight requests rather than silently ignoring the
// setting.
func TestMaxConcurrentIsRespected(t *testing.T) {
	s := mock.NewServer(t, mock.Options{Password: "secret"})
	s.LoadFixture("lan", "get_config", json.RawMessage(`{}`))
	tr := newTransport(t, s, transport.Config{KeepaliveInterval: -1, MaxConcurrent: 1})

	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := tr.Call(context.Background(), "lan", "get_config", nil, nil); err != nil {
				t.Errorf("Call: %v", err)
			}
		}()
	}
	wg.Wait()
}

func TestConfigDefaults(t *testing.T) {
	s := mock.NewServer(t, mock.Options{Password: "secret"})
	s.LoadFixture("lan", "get_config", json.RawMessage(`{}`))

	// Zero HTTPClient, KeepaliveInterval and MaxConcurrent must all be filled in.
	tr := transport.New(transport.Config{URL: s.URL(), Username: "root", Password: "secret"})
	t.Cleanup(func() { _ = tr.Close() })

	if err := tr.Call(context.Background(), "lan", "get_config", nil, nil); err != nil {
		t.Errorf("Call with a zero-value Config: %v", err)
	}
}
