package gogl_test

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	gogl "github.com/gherlein/gogl/src"
	"github.com/gherlein/gogl/src/mock"
	"github.com/gherlein/gogl/src/types"
)

// The full stack under sustained concurrent load across several session
// lifetimes: crypt, two-challenge login, session renewal, keepalive, transparent
// retry, and call dispatch.
//
// This is the test worth keeping. The double-checked locking around
// re-authentication is easy to get subtly wrong, and the failure only appears
// under load, which is exactly where a small SoC punishes it.
func TestStackSurvivesSessionExpiryUnderLoad(t *testing.T) {
	s := mock.NewServer(t, mock.Options{
		Password:   "secret",
		SessionTTL: 60 * time.Millisecond,
	})
	s.LoadFixture(mock.NetworkGroup, mock.MethodGetConfigList,
		[]byte(`{"interfaces":[{"interface":"lan","ip":"192.168.8.1","netmask":"255.255.255.0","enable":1}]}`))

	u, err := url.Parse(s.URL())
	if err != nil {
		t.Fatalf("parse URL: %v", err)
	}
	port, err := strconv.Atoi(u.Port())
	if err != nil {
		t.Fatalf("parse port: %v", err)
	}

	c, err := gogl.New(gogl.Config{
		Host: u.Hostname(), Port: port, Password: "secret",
		KeepaliveInterval: 20 * time.Millisecond,
		MaxConcurrent:     4,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer c.Close()

	deadline := time.Now().Add(400 * time.Millisecond)
	var wg sync.WaitGroup
	errs := make(chan error, 512)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for time.Now().Before(deadline) {
				network, err := c.Network().Get(context.Background())
				if err != nil {
					errs <- err
					return
				}
				if network.LANIP != "192.168.8.1" {
					errs <- fmt.Errorf("lan_ip = %q", network.LANIP)
					return
				}
			}
		}()
	}
	wg.Wait()
	close(errs)

	for err := range errs {
		t.Errorf("call failed during sustained load: %v", err)
	}
}

// The reservation lifecycle end to end through the public API, which is the path
// goglps takes -- including the ordering rule that a domain comes first.
func TestReservationLifecycleThroughClient(t *testing.T) {
	s := mock.NewServer(t, mock.Options{Password: "secret"})
	s.SetReservations(nil)
	c := clientFor(t, s)

	ctx := context.Background()
	reservations := c.Reservations()

	// A factory router has no domain, so the first write must be refused.
	if _, err := reservations.Create(ctx, &types.Reservation{
		Name: "nas", MAC: "aa:bb:cc:dd:ee:01", IP: "192.168.8.13",
	}); !errors.Is(err, gogl.ErrDomainNotSet) {
		t.Fatalf("first Create error = %v, want ErrDomainNotSet", err)
	}

	if err := c.Hosts().SetDomain(ctx, "lab.example"); err != nil {
		t.Fatalf("SetDomain: %v", err)
	}

	created, err := reservations.Create(ctx, &types.Reservation{
		Name: "nas", MAC: "AA:BB:CC:DD:EE:01", IP: "192.168.8.13",
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if created.MAC != "aa:bb:cc:dd:ee:01" {
		t.Errorf("MAC not normalized: %q", created.MAC)
	}

	fetched, err := reservations.GetByName(ctx, "nas")
	if err != nil {
		t.Fatalf("GetByName: %v", err)
	}
	if fetched.IP != "192.168.8.13" {
		t.Errorf("IP = %q, want 192.168.8.13", fetched.IP)
	}

	if _, err := reservations.Update(ctx, &types.Reservation{
		Name: "nas", MAC: "aa:bb:cc:dd:ee:01", IP: "192.168.8.20",
	}); err != nil {
		t.Fatalf("Update: %v", err)
	}

	if err := reservations.Delete(ctx, "aa:bb:cc:dd:ee:01"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if got := s.Reservations(); len(got) != 0 {
		t.Errorf("device holds %d reservations after delete, want 0", len(got))
	}
}

// A DNS name is created through the host file, not the reservation. This is the
// full sequence a caller performs, and it must leave the router's own boilerplate
// untouched.
func TestHostsAndReservationsThroughClient(t *testing.T) {
	s := mock.NewServer(t, mock.Options{Password: "secret"})
	s.SetReservations(nil)
	c := clientFor(t, s)
	ctx := context.Background()

	if err := c.Hosts().SetDomain(ctx, "lab.example"); err != nil {
		t.Fatalf("SetDomain: %v", err)
	}
	if _, err := c.Reservations().Create(ctx, &types.Reservation{
		Name: "nas", MAC: "aa:bb:cc:dd:ee:01", IP: "192.168.8.13",
	}); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := c.Hosts().Set(ctx, "nas", "192.168.8.13"); err != nil {
		t.Fatalf("Hosts.Set: %v", err)
	}

	file, err := c.Hosts().Get(ctx)
	if err != nil {
		t.Fatalf("Hosts.Get: %v", err)
	}
	if ip, ok := file.Lookup("nas"); !ok || ip != "192.168.8.13" {
		t.Errorf("Lookup(nas) = %q, %v", ip, ok)
	}
	if ip, ok := file.Lookup("nas.lab.example"); !ok || ip != "192.168.8.13" {
		t.Errorf("Lookup(FQDN) = %q, %v", ip, ok)
	}

	// The router's own loopback and IPv6 lines must survive every write.
	written := s.HostFile()
	for _, line := range []string{"127.0.0.1 localhost", "ff02::2 ip6-allrouters"} {
		if !strings.Contains(written, line) {
			t.Errorf("host file lost %q:\n%s", line, written)
		}
	}
}
