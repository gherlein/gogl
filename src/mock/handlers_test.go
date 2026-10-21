package mock

import (
	"encoding/json"
	"testing"

	"github.com/gherlein/gogl/src/types"
)

// authenticate performs the full login and returns the sid.
func authenticate(t *testing.T, s *Server) string {
	t.Helper()
	out := loginWith(t, s, s.Password())
	result, ok := out["result"].(map[string]any)
	if !ok {
		t.Fatalf("login failed: %v", out)
	}
	sid, _ := result["sid"].(string)
	if sid == "" {
		t.Fatal("empty sid")
	}
	return sid
}

func callRPC(t *testing.T, s *Server, sid, group, method string, args any) map[string]any {
	t.Helper()
	return post(t, s.URL(), map[string]any{
		"jsonrpc": "2.0", "id": 7, "method": "call",
		"params": []any{sid, group, method, args},
	})
}

func TestCallReturnsLoadedFixture(t *testing.T) {
	s := NewServer(t, Options{Password: "secret"})
	s.LoadFixture("lan", "get_config", json.RawMessage(`{"lan_ip":"192.168.8.1"}`))
	sid := authenticate(t, s)

	out := callRPC(t, s, sid, "lan", "get_config", map[string]any{})
	result, ok := out["result"].(map[string]any)
	if !ok {
		t.Fatalf("no result: %v", out)
	}
	if result["lan_ip"] != "192.168.8.1" {
		t.Errorf("lan_ip = %v, want 192.168.8.1", result["lan_ip"])
	}
}

func TestCallRejectsBadSID(t *testing.T) {
	s := NewServer(t, Options{Password: "secret"})
	s.LoadFixture("lan", "get_config", json.RawMessage(`{}`))
	authenticate(t, s)

	out := callRPC(t, s, "not-the-sid", "lan", "get_config", map[string]any{})
	errObj, ok := out["error"].(map[string]any)
	if !ok {
		t.Fatalf("expected an error, got %v", out)
	}
	if int(errObj["code"].(float64)) != CodeAccessDenied {
		t.Errorf("code = %v, want %d", errObj["code"], CodeAccessDenied)
	}
}

func TestCallUnknownGroupReturnsError(t *testing.T) {
	s := NewServer(t, Options{Password: "secret"})
	sid := authenticate(t, s)

	out := callRPC(t, s, sid, "nosuch", "nomethod", map[string]any{})
	errObj, ok := out["error"].(map[string]any)
	if !ok {
		t.Fatalf("unknown group succeeded: %v", out)
	}
	if int(errObj["code"].(float64)) != CodeNotFound {
		t.Errorf("code = %v, want %d", errObj["code"], CodeNotFound)
	}
}

func TestCallRejectsMalformedParams(t *testing.T) {
	s := NewServer(t, Options{Password: "secret"})
	authenticate(t, s)

	out := post(t, s.URL(), map[string]any{
		"jsonrpc": "2.0", "id": 7, "method": "call",
		"params": map[string]any{"not": "an array"},
	})
	if _, ok := out["error"]; !ok {
		t.Errorf("malformed call params succeeded: %v", out)
	}
}

func TestFailNextIsOneShot(t *testing.T) {
	s := NewServer(t, Options{Password: "secret"})
	s.LoadFixture("lan", "get_config", json.RawMessage(`{"lan_ip":"192.168.8.1"}`))
	sid := authenticate(t, s)

	s.FailNext("lan", "get_config", CodeNotFound, "injected")

	if out := callRPC(t, s, sid, "lan", "get_config", map[string]any{}); out["error"] == nil {
		t.Fatalf("injected failure did not fire: %v", out)
	}
	if out := callRPC(t, s, sid, "lan", "get_config", map[string]any{}); out["error"] != nil {
		t.Errorf("injected failure fired twice: %v", out)
	}
}

func TestReservationRoundTrip(t *testing.T) {
	s := NewServer(t, Options{Password: "secret"})
	s.SetReservations([]types.Reservation{
		{Name: "nas", MAC: "aa:bb:cc:dd:ee:01", IP: "192.168.8.13"},
	})
	sid := authenticate(t, s)

	out := callRPC(t, s, sid, ReservationGroup, MethodGetStaticBinds, map[string]any{})
	result, ok := out["result"].(map[string]any)
	if !ok {
		t.Fatalf("no result: %v", out)
	}
	list, ok := result[reservationsKey].([]any)
	if !ok || len(list) != 1 {
		t.Fatalf("%s = %v, want one entry", reservationsKey, result[reservationsKey])
	}

	// Writing through the API must be visible to Reservations(), so tests can
	// assert on device state rather than only on returned values.
	written := callRPC(t, s, sid, ReservationGroup, MethodAddStaticBind, map[string]any{
		"name": "printer", "mac": "aa:bb:cc:dd:ee:02", "ip": "192.168.8.14",
	})
	if written["error"] != nil {
		t.Fatalf("add_static_bind failed: %v", written)
	}

	got := s.Reservations()
	if len(got) != 2 {
		t.Fatalf("Reservations() returned %d entries, want 2", len(got))
	}
	if got[1].Name != "printer" {
		t.Errorf("second reservation name = %q, want printer", got[1].Name)
	}
}

// The firmware's remove mode 1 clears every binding. gogl must never send it, but
// the mock honors it so that intent is testable.
func TestRemoveAllBindings(t *testing.T) {
	s := NewServer(t, Options{Password: "secret"})
	s.SetReservations([]types.Reservation{
		{Name: "a", MAC: "aa:bb:cc:dd:ee:01", IP: "192.168.8.10"},
		{Name: "b", MAC: "aa:bb:cc:dd:ee:02", IP: "192.168.8.11"},
	})
	sid := authenticate(t, s)

	callRPC(t, s, sid, ReservationGroup, MethodRemoveBind, map[string]any{"mode": removeModeAll})
	if got := s.Reservations(); len(got) != 0 {
		t.Errorf("mode 1 left %d bindings, want 0", len(got))
	}
}

func TestSetReservationsNil(t *testing.T) {
	s := NewServer(t, Options{Password: "secret"})
	s.SetReservations(nil)

	if got := s.Reservations(); len(got) != 0 {
		t.Errorf("Reservations() = %v, want empty", got)
	}
}

func TestReservationsWithNoFixture(t *testing.T) {
	s := NewServer(t, Options{Password: "secret"})
	if got := s.Reservations(); got != nil {
		t.Errorf("Reservations() = %v, want nil when no fixture is loaded", got)
	}
}

func TestBindWriteRequiresArgs(t *testing.T) {
	s := NewServer(t, Options{Password: "secret"})
	sid := authenticate(t, s)

	out := post(t, s.URL(), map[string]any{
		"jsonrpc": "2.0", "id": 7, "method": "call",
		"params": []any{sid, ReservationGroup, MethodAddStaticBind},
	})
	if _, ok := out["error"]; !ok {
		t.Errorf("add_static_bind without args succeeded: %v", out)
	}
}
