package services_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/gherlein/gogl/src/mock"
	"github.com/gherlein/gogl/src/services"
	"github.com/gherlein/gogl/src/types"
)

// seeded starts a mock with a configured domain, because reservation writes are
// gated on one. The gate itself is covered in guards_test.go.
func seeded(t *testing.T) (*mock.Server, services.ReservationService) {
	t.Helper()
	s := mock.NewServer(t, mock.Options{Password: "secret"})
	withDomain(t, s, "lab.example")
	s.SetReservations([]types.Reservation{
		{Name: "nas", MAC: "aa:bb:cc:dd:ee:01", IP: "192.168.8.13"},
		{Name: "printer", MAC: "aa:bb:cc:dd:ee:02", IP: "192.168.8.14"},
	})
	return s, services.NewReservationService(newTransport(t, s))
}

func TestReservationList(t *testing.T) {
	_, svc := seeded(t)
	got, err := svc.List(context.Background())
	if err != nil {
		t.Fatalf("List error: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("List returned %d, want 2", len(got))
	}
	if got[0].Name != "nas" {
		t.Errorf("first name = %q, want nas", got[0].Name)
	}
}

// An empty device must list cleanly rather than erroring: a factory router has no
// reservations.
func TestReservationListEmpty(t *testing.T) {
	s := mock.NewServer(t, mock.Options{Password: "secret"})
	withDomain(t, s, "lab.example")
	s.SetReservations(nil)
	svc := services.NewReservationService(newTransport(t, s))

	got, err := svc.List(context.Background())
	if err != nil {
		t.Fatalf("List error: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("List returned %d entries for an empty device", len(got))
	}
}

func TestReservationListPropagatesError(t *testing.T) {
	s := mock.NewServer(t, mock.Options{Password: "secret"})
	withDomain(t, s, "lab.example")
	s.SetReservations(nil)
	s.FailNext(mock.ReservationGroup, mock.MethodGetStaticBinds, mock.CodeNotFound, "injected")
	svc := services.NewReservationService(newTransport(t, s))

	if _, err := svc.List(context.Background()); err == nil {
		t.Error("List succeeded, want error")
	}
}

func TestReservationGetByMAC(t *testing.T) {
	_, svc := seeded(t)
	// Case-insensitive on input: the caller should not have to normalize first.
	got, err := svc.GetByMAC(context.Background(), "AA:BB:CC:DD:EE:01")
	if err != nil {
		t.Fatalf("GetByMAC error: %v", err)
	}
	if got.Name != "nas" {
		t.Errorf("Name = %q, want nas", got.Name)
	}
}

func TestReservationGetByMACNotFound(t *testing.T) {
	_, svc := seeded(t)
	_, err := svc.GetByMAC(context.Background(), "aa:bb:cc:dd:ee:99")
	if !errors.Is(err, types.ErrNotFound) {
		t.Errorf("error = %v, want ErrNotFound", err)
	}
}

func TestReservationGetByMACRejectsBadMAC(t *testing.T) {
	_, svc := seeded(t)
	_, err := svc.GetByMAC(context.Background(), "not-a-mac")
	if !errors.Is(err, types.ErrInvalidMAC) {
		t.Errorf("error = %v, want ErrInvalidMAC", err)
	}
}

func TestReservationGetByName(t *testing.T) {
	_, svc := seeded(t)
	got, err := svc.GetByName(context.Background(), "printer")
	if err != nil {
		t.Fatalf("GetByName error: %v", err)
	}
	if got.IP != "192.168.8.14" {
		t.Errorf("IP = %q, want 192.168.8.14", got.IP)
	}

	if _, err := svc.GetByName(context.Background(), "ghost"); !errors.Is(err, types.ErrNotFound) {
		t.Errorf("error = %v, want ErrNotFound", err)
	}
}

// GetByIP returns a slice because inconsistent device state can hold the same
// address twice; callers decide whether to tolerate it.
func TestReservationGetByIP(t *testing.T) {
	_, svc := seeded(t)
	got, err := svc.GetByIP(context.Background(), "192.168.8.13")
	if err != nil {
		t.Fatalf("GetByIP error: %v", err)
	}
	if len(got) != 1 || got[0].Name != "nas" {
		t.Errorf("GetByIP = %v, want one entry named nas", got)
	}

	none, err := svc.GetByIP(context.Background(), "192.168.8.99")
	if err != nil {
		t.Fatalf("GetByIP error: %v", err)
	}
	if len(none) != 0 {
		t.Errorf("GetByIP for an unreserved address = %v, want empty", none)
	}
}

func TestReservationGetByIPFindsDuplicates(t *testing.T) {
	s := mock.NewServer(t, mock.Options{Password: "secret"})
	withDomain(t, s, "lab.example")
	s.SetReservations([]types.Reservation{
		{Name: "a", MAC: "aa:bb:cc:dd:ee:01", IP: "192.168.8.13"},
		{Name: "b", MAC: "aa:bb:cc:dd:ee:02", IP: "192.168.8.13"},
	})
	svc := services.NewReservationService(newTransport(t, s))

	got, err := svc.GetByIP(context.Background(), "192.168.8.13")
	if err != nil {
		t.Fatalf("GetByIP error: %v", err)
	}
	if len(got) != 2 {
		t.Errorf("GetByIP returned %d, want 2 for inconsistent device state", len(got))
	}
}

func TestReservationCreate(t *testing.T) {
	s, svc := seeded(t)
	created, err := svc.Create(context.Background(), &types.Reservation{
		Name: "camera", MAC: "AA:BB:CC:DD:EE:03", IP: "192.168.8.15",
	})
	if err != nil {
		t.Fatalf("Create error: %v", err)
	}
	// Create normalizes through Validate, so the stored MAC is lowercase.
	if created.MAC != "aa:bb:cc:dd:ee:03" {
		t.Errorf("created MAC = %q, want lowercase", created.MAC)
	}

	stored := s.Reservations()
	if len(stored) != 3 {
		t.Fatalf("device holds %d reservations, want 3", len(stored))
	}
}

func TestReservationCreateRejectsInvalidName(t *testing.T) {
	_, svc := seeded(t)
	_, err := svc.Create(context.Background(), &types.Reservation{
		Name: "bad_name", MAC: "aa:bb:cc:dd:ee:03", IP: "192.168.8.15",
	})
	if !errors.Is(err, types.ErrInvalidName) {
		t.Errorf("error = %v, want ErrInvalidName", err)
	}
}

// Validation must happen before the write, so a rejected name leaves the device
// untouched. This is the test that matters most in this file: a quote reaching
// dnsmasq's config breaks DHCP and DNS for the whole router.
func TestReservationCreateRejectionDoesNotWrite(t *testing.T) {
	s, svc := seeded(t)
	_, _ = svc.Create(context.Background(), &types.Reservation{
		Name: `bad"name`, MAC: "aa:bb:cc:dd:ee:03", IP: "192.168.8.15",
	})
	if got := len(s.Reservations()); got != 2 {
		t.Errorf("device holds %d reservations after a rejected create, want 2", got)
	}
}

func TestReservationCreateConflictsOnExistingMAC(t *testing.T) {
	_, svc := seeded(t)
	_, err := svc.Create(context.Background(), &types.Reservation{
		Name: "other", MAC: "aa:bb:cc:dd:ee:01", IP: "192.168.8.99",
	})
	if !errors.Is(err, types.ErrConflict) {
		t.Errorf("error = %v, want ErrConflict", err)
	}
}

func TestReservationCreateOnEmptyDevice(t *testing.T) {
	s := mock.NewServer(t, mock.Options{Password: "secret"})
	withDomain(t, s, "lab.example")
	s.SetReservations(nil)
	svc := services.NewReservationService(newTransport(t, s))

	if _, err := svc.Create(context.Background(), &types.Reservation{
		Name: "first", MAC: "aa:bb:cc:dd:ee:01", IP: "192.168.8.10",
	}); err != nil {
		t.Fatalf("Create on an empty device: %v", err)
	}
	if got := s.Reservations(); len(got) != 1 {
		t.Errorf("device holds %d reservations, want 1", len(got))
	}
}

func TestReservationUpdate(t *testing.T) {
	s, svc := seeded(t)
	if _, err := svc.Update(context.Background(), &types.Reservation{
		Name: "nas", MAC: "aa:bb:cc:dd:ee:01", IP: "192.168.8.20",
	}); err != nil {
		t.Fatalf("Update error: %v", err)
	}

	for _, r := range s.Reservations() {
		if r.MAC == "aa:bb:cc:dd:ee:01" {
			if r.IP != "192.168.8.20" {
				t.Errorf("IP = %q, want 192.168.8.20", r.IP)
			}
			return
		}
	}
	t.Error("reservation disappeared after Update")
}

// Updating must not disturb the other entries.
func TestReservationUpdatePreservesOthers(t *testing.T) {
	s, svc := seeded(t)
	if _, err := svc.Update(context.Background(), &types.Reservation{
		Name: "nas", MAC: "aa:bb:cc:dd:ee:01", IP: "192.168.8.20",
	}); err != nil {
		t.Fatalf("Update error: %v", err)
	}

	stored := s.Reservations()
	if len(stored) != 2 {
		t.Fatalf("device holds %d reservations, want 2", len(stored))
	}
	found := false
	for _, r := range stored {
		if r.MAC == "aa:bb:cc:dd:ee:02" && r.IP == "192.168.8.14" && r.Name == "printer" {
			found = true
		}
	}
	if !found {
		t.Errorf("Update disturbed an unrelated reservation: %v", stored)
	}
}

func TestReservationUpdateNotFound(t *testing.T) {
	_, svc := seeded(t)
	_, err := svc.Update(context.Background(), &types.Reservation{
		Name: "ghost", MAC: "aa:bb:cc:dd:ee:99", IP: "192.168.8.50",
	})
	if !errors.Is(err, types.ErrNotFound) {
		t.Errorf("error = %v, want ErrNotFound", err)
	}
}

func TestReservationUpdateRejectsInvalidName(t *testing.T) {
	_, svc := seeded(t)
	_, err := svc.Update(context.Background(), &types.Reservation{
		Name: "bad name", MAC: "aa:bb:cc:dd:ee:01", IP: "192.168.8.13",
	})
	if !errors.Is(err, types.ErrInvalidName) {
		t.Errorf("error = %v, want ErrInvalidName", err)
	}
}

func TestReservationDelete(t *testing.T) {
	s, svc := seeded(t)
	if err := svc.Delete(context.Background(), "AA:BB:CC:DD:EE:01"); err != nil {
		t.Fatalf("Delete error: %v", err)
	}

	stored := s.Reservations()
	if len(stored) != 1 {
		t.Fatalf("device holds %d reservations, want 1", len(stored))
	}
	if stored[0].MAC != "aa:bb:cc:dd:ee:02" {
		t.Errorf("wrong reservation deleted; remaining MAC = %q", stored[0].MAC)
	}
}

func TestReservationDeleteNotFound(t *testing.T) {
	_, svc := seeded(t)
	if err := svc.Delete(context.Background(), "aa:bb:cc:dd:ee:99"); !errors.Is(err, types.ErrNotFound) {
		t.Errorf("error = %v, want ErrNotFound", err)
	}
}

func TestReservationDeleteRejectsBadMAC(t *testing.T) {
	_, svc := seeded(t)
	if err := svc.Delete(context.Background(), "nope"); !errors.Is(err, types.ErrInvalidMAC) {
		t.Errorf("error = %v, want ErrInvalidMAC", err)
	}
}

func TestReservationDeleteLast(t *testing.T) {
	s := mock.NewServer(t, mock.Options{Password: "secret"})
	withDomain(t, s, "lab.example")
	s.SetReservations([]types.Reservation{{Name: "only", MAC: "aa:bb:cc:dd:ee:01", IP: "192.168.8.10"}})
	svc := services.NewReservationService(newTransport(t, s))

	if err := svc.Delete(context.Background(), "aa:bb:cc:dd:ee:01"); err != nil {
		t.Fatalf("Delete error: %v", err)
	}
	if got := s.Reservations(); len(got) != 0 {
		t.Errorf("device holds %d reservations, want 0", len(got))
	}
}

func TestReservationWritePropagatesError(t *testing.T) {
	s, svc := seeded(t)
	// Deliberately not CodeAccessDenied: that code means "session stale" and the
	// transport is supposed to swallow it by re-logging in and retrying. See
	// TestReservationWriteRetriesOnStaleSession.
	s.FailNext(mock.ReservationGroup, mock.MethodAddStaticBind, mock.CodeNotFound, "injected")

	_, err := svc.Create(context.Background(), &types.Reservation{
		Name: "camera", MAC: "aa:bb:cc:dd:ee:03", IP: "192.168.8.15",
	})
	if err == nil {
		t.Error("Create succeeded despite an injected write failure")
	}
}

// A write rejected for a stale session must succeed on the transport's single
// transparent retry, so the service layer never sees a session problem. This is
// the behavior that made an earlier version of the test above fail.
func TestReservationWriteRetriesOnStaleSession(t *testing.T) {
	s, svc := seeded(t)
	s.FailNext(mock.ReservationGroup, mock.MethodAddStaticBind, mock.CodeAccessDenied, "session gone")

	if _, err := svc.Create(context.Background(), &types.Reservation{
		Name: "camera", MAC: "aa:bb:cc:dd:ee:03", IP: "192.168.8.15",
	}); err != nil {
		t.Fatalf("Create did not recover from a stale session: %v", err)
	}
	if got := len(s.Reservations()); got != 3 {
		t.Errorf("device holds %d reservations, want 3 after the retry", got)
	}
	if got := s.LoginCount(); got != 2 {
		t.Errorf("LoginCount() = %d, want 2 (one initial, one renewal)", got)
	}
}

// A round trip through the service must preserve every field, or a read-modify-
// write cycle would quietly drop data.
func TestReservationRoundTripPreservesFields(t *testing.T) {
	_, svc := seeded(t)
	if _, err := svc.Create(context.Background(), &types.Reservation{
		Name: "camera", MAC: "aa:bb:cc:dd:ee:03", IP: "192.168.8.15",
	}); err != nil {
		t.Fatalf("Create error: %v", err)
	}

	got, err := svc.GetByName(context.Background(), "camera")
	if err != nil {
		t.Fatalf("GetByName error: %v", err)
	}
	if got.MAC != "aa:bb:cc:dd:ee:03" || got.IP != "192.168.8.15" {
		t.Errorf("round trip lost data: %+v", got)
	}
}

func TestReservationTableJSONShape(t *testing.T) {
	// Guard against the wrapper field name drifting: the services and the mock
	// must agree, or every reservation test would pass while real writes silently
	// went nowhere.
	server := mock.NewServer(t, mock.Options{Password: "secret"})
	withDomain(t, server, "lab.example")
	server.SetReservations([]types.Reservation{{Name: "a", MAC: "aa:bb:cc:dd:ee:01", IP: "192.168.8.10"}})

	tr := newTransport(t, server)
	var raw map[string]json.RawMessage
	if err := tr.Call(context.Background(), mock.ReservationGroup, mock.MethodGetStaticBinds, nil, &raw); err != nil {
		t.Fatalf("Call error: %v", err)
	}
	if _, ok := raw["static_bind_list"]; !ok {
		t.Errorf("reservation payload has no \"static_bind_list\" field: %v", raw)
	}
}
