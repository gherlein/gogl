# gogl Implementation Plan — Part 3: Phases 5 and 6

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

> ## Status: executed, and superseded in part
>
> This plan was written before any of it ran against hardware, and several of its premises
> turned out to be wrong. It is kept as the record of how the project was built, not as a description
> of what it now does. **For current behavior read [`../README.md`](../README.md) and
> [`DESIGN.md`](DESIGN.md); for the verified API surface read
> [`../GL_INET_4X_API_DOCUMENTATION.md`](../GL_INET_4X_API_DOCUMENTATION.md).**
>
> What changed, and where the plan is now wrong:
>
> 1. **A static bind does not create a DNS record.** The plan assumed the reservation's name
>    became a resolvable name. On firmware 4.3.28 it is a label and nothing more. DNS names come
>    from the router's host file via `dns.get_host` / `dns.set_host`, which this plan never
>    mentions. Anything below that treats a reservation as carrying a name is wrong.
> 2. **Reservations are no longer the only write.** `NetworkService.Set` writes the LAN address
>    and DHCP pool, and `HostsService` writes names and the DNS domain. Two ordering guards
>    replace the old "read-only everything else" rule.
> 3. **Endpoint names were guessed.** `dhcp.*` does not exist on this device; the real groups are
>    `lan.*` and `dns.*`. Every group and method constant below should be read as provisional.
> 4. **Wireless was out of scope and now is not.** `gogl wifi set` writes SSIDs, passphrases,
>    encryption, hidden and enabled state, and `gogl radio set` writes channel, bandwidth,
>    hardware mode and transmit power -- guarded so that no wireless write happens over a
>    wireless session.
> 5. **Field shapes taken from the vendored API description were wrong more than once.**
>    `dns.set_host` rejects `(`, `)` and `=` anywhere in the file, which broke every host-file
>    write; `wifi.get_config` sends `htmodes` as an object rather than an array, which broke
>    every wireless read. Both were typed from the description rather than a capture. Fixtures
>    are now verbatim captures, with tests asserting they decode through the library's own types.
> 6. **The four utilities became one binary.** Everything below names `goglps`, `goglmac`,
>    `goglnet` or `goglcfg`. Those binaries no longer exist: the CLI is a single `gogl` with a
>    `gogl <area> <action>` tree, and their logic moved to importable packages under
>    `utilities/internal/`. Read every utility name below as the area that replaced it --
>    `gogl lan reservations`, `gogl clients`, `gogl lan`/`wifi`/`radio`, and `gogl profile`.
>    See [`DESIGN-V2.md`](DESIGN-V2.md).
>
> The plan's *method* held up: mock first, transport second, one transparent retry, no hardware
> in the test suite. It was the API assumptions that did not survive contact.

---
Continuation of [`plan-part2.md`](plan-part2.md). Task numbering resumes at 15.
**Global Constraints from [`plan.md`](plan.md) apply to every task here.**

**Prerequisite:** Phase 0 must be complete. Every group and method constant used below comes
from `GL_INET_4X_API_DOCUMENTATION.md`. Where this plan writes `mock.NetworkGroup` or
`mock.MethodGetConfig`, substitute the real captured names — the constants are declared in
one place (`src/mock/handlers.go`) precisely so that Phase 0's findings change one file.

---

## Phase 5: Services

Four small interfaces. Three are read-only; only `ReservationService` writes.

*As built there are five: `HostsService` was added once hardware showed that a static bind
creates no DNS record, and `NetworkService` gained `Set`. See the status note above.*

### Task 15: SystemService

**Files:**
- Create: `src/services/services.go`
- Create: `src/services/system.go`
- Test: `src/services/system_test.go`
- Create: `src/types/system.go`
- Modify: `src/client.go`

**Interfaces:**
- Consumes: `transport.Transport` (Task 12), `mock` (Tasks 9, 10).
- Produces: `types.SystemInfo{Model, Firmware, MAC string; Uptime int64}`; `services.SystemService` interface with `Info(ctx) (*types.SystemInfo, error)`; `services.NewSystemService(transport.Transport) SystemService`; `(*gogl.Client).System() services.SystemService`. Task 20 calls `Info`.

Done first because it is the simplest read path, so it proves the service pattern before the
harder ones use it.

- [ ] **Step 1: Write the type**

`src/types/system.go`:

```go
package types

// SystemInfo identifies the device. Read-only.
//
// JSON tags come from the Phase 0 fixture for the system group; adjust them to
// match the recorded payload rather than renaming the fields.
type SystemInfo struct {
	Model    string `json:"model"`
	Firmware string `json:"firmware_version"`
	MAC      string `json:"mac,omitempty"`
	Uptime   int64  `json:"uptime,omitempty"`
}
```

- [ ] **Step 2: Write the failing test**

`src/services/system_test.go`:

```go
package services_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/gherlein/gogl/src/mock"
	"github.com/gherlein/gogl/src/services"
	"github.com/gherlein/gogl/src/transport"
)

// newTransport wires a Transport to a mock server. Shared by every service test
// in this package.
func newTransport(t *testing.T, s *mock.Server) transport.Transport {
	t.Helper()
	tr := transport.New(transport.Config{
		URL: s.URL(), Username: "root", Password: "secret",
		KeepaliveInterval: -1,
	})
	t.Cleanup(func() {
		if err := tr.Close(); err != nil {
			t.Errorf("Close: %v", err)
		}
	})
	return tr
}

func TestSystemInfo(t *testing.T) {
	s := mock.NewServer(t, mock.Options{Password: "secret"})
	s.LoadFixture(mock.SystemGroup, mock.MethodGetStatus, json.RawMessage(
		`{"model":"gl-sft1200","firmware_version":"4.3.28","mac":"aa:bb:cc:dd:ee:00","uptime":86400}`))

	got, err := services.NewSystemService(newTransport(t, s)).Info(context.Background())
	if err != nil {
		t.Fatalf("Info error: %v", err)
	}
	if got.Model != "gl-sft1200" {
		t.Errorf("Model = %q, want gl-sft1200", got.Model)
	}
	if got.Firmware != "4.3.28" {
		t.Errorf("Firmware = %q, want 4.3.28", got.Firmware)
	}
	if got.Uptime != 86400 {
		t.Errorf("Uptime = %d, want 86400", got.Uptime)
	}
}

func TestSystemInfoPropagatesError(t *testing.T) {
	s := mock.NewServer(t, mock.Options{Password: "secret"})
	s.LoadFixture(mock.SystemGroup, mock.MethodGetStatus, json.RawMessage(`{}`))
	s.FailNext(mock.SystemGroup, mock.MethodGetStatus, -32001, "injected")

	_, err := services.NewSystemService(newTransport(t, s)).Info(context.Background())
	if err == nil {
		t.Fatal("Info succeeded, want error")
	}
	var rpcErr *transport.Error
	if !errors.As(err, &rpcErr) {
		t.Errorf("error is %T, want *transport.Error", err)
	}
}

func TestSystemInfoHonoursContext(t *testing.T) {
	s := mock.NewServer(t, mock.Options{Password: "secret"})
	s.LoadFixture(mock.SystemGroup, mock.MethodGetStatus, json.RawMessage(`{}`))

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if _, err := services.NewSystemService(newTransport(t, s)).Info(ctx); err == nil {
		t.Error("Info with a cancelled context succeeded")
	}
}
```

- [ ] **Step 3: Run it to verify it fails**

Run: `go test ./src/services/ -v`
Expected: FAIL, `undefined: services.NewSystemService`.

- [ ] **Step 4: Write the interfaces and the implementation**

`src/services/services.go`:

```go
// Package services implements the typed operations gogl offers against a
// GL.iNet router. Interfaces are small and single-purpose so each is
// independently mockable.
//
// Three of the four are read-only. Only ReservationService writes: network
// configuration is set in the GL.iNet admin panel, not here.
//
// No method takes a site parameter. GL.iNet routers have no equivalent of a
// UniFi site.
package services

import (
	"context"

	"github.com/gherlein/gogl/src/types"
)

// NetworkService reads the router's LAN and DHCP configuration. Read-only.
type NetworkService interface {
	Get(ctx context.Context) (*types.Network, error)
}

// ReservationService manages static DHCP leases. Each lease carries the DNS name
// for its address, so creating a reservation also creates the name and deleting
// one also removes it.
type ReservationService interface {
	List(ctx context.Context) ([]types.Reservation, error)
	GetByMAC(ctx context.Context, mac string) (*types.Reservation, error)
	GetByIP(ctx context.Context, ip string) ([]types.Reservation, error)
	GetByName(ctx context.Context, name string) (*types.Reservation, error)
	Create(ctx context.Context, r *types.Reservation) (*types.Reservation, error)
	Update(ctx context.Context, r *types.Reservation) (*types.Reservation, error)
	Delete(ctx context.Context, mac string) error
}

// ClientService reads stations known to the router. Read-only.
type ClientService interface {
	List(ctx context.Context) ([]types.Client, error)
}

// SystemService reads device identity. Read-only.
type SystemService interface {
	Info(ctx context.Context) (*types.SystemInfo, error)
}
```

`src/services/system.go`:

```go
package services

import (
	"context"
	"fmt"

	"github.com/gherlein/gogl/src/transport"
	"github.com/gherlein/gogl/src/types"
)

// Group and method names captured in Phase 0. Declared here rather than inline
// so that a firmware naming change is a one-line edit.
const (
	systemGroup     = "system"
	systemGetStatus = "get_status"
)

type systemService struct {
	transport transport.Transport
}

func NewSystemService(t transport.Transport) SystemService {
	return &systemService{transport: t}
}

func (s *systemService) Info(ctx context.Context) (*types.SystemInfo, error) {
	var info types.SystemInfo
	if err := s.transport.Call(ctx, systemGroup, systemGetStatus, nil, &info); err != nil {
		return nil, fmt.Errorf("gogl: read system info: %w", err)
	}
	return &info, nil
}
```

- [ ] **Step 5: Wire the accessor onto Client**

Add to `src/client.go`:

```go
// System reads device identity. Read-only.
func (c *Client) System() services.SystemService {
	return services.NewSystemService(c.transport)
}
```

Add the `services` import.

- [ ] **Step 6: Run the tests**

Run: `go test ./src/... -v -race`
Expected: PASS.

- [ ] **Step 7: Commit**

```bash
git add src/types/system.go src/services/services.go src/services/system.go src/services/system_test.go src/client.go
git commit -m "feat(services): add SystemService"
```

### Task 16: NetworkService

**Files:**
- Create: `src/services/network.go`
- Test: `src/services/network_test.go`
- Modify: `src/client.go`

**Interfaces:**
- Consumes: `types.Network` (Task 7), `transport.Transport` (Task 12).
- Produces: `services.NewNetworkService(transport.Transport) NetworkService`; `(*gogl.Client).Network() services.NetworkService`. Tasks 20 and 26 call `Get`.

- [ ] **Step 1: Write the failing test**

`src/services/network_test.go`:

```go
package services_test

import (
	"context"
	"encoding/json"
	"net"
	"testing"
	"time"

	"github.com/gherlein/gogl/src/mock"
	"github.com/gherlein/gogl/src/services"
	"github.com/gherlein/gogl/src/types"
)

const networkFixture = `{
  "lan_ip": "192.168.8.1",
  "netmask": "255.255.255.0",
  "dhcp_enabled": true,
  "dhcp_start": "192.168.8.100",
  "dhcp_stop": "192.168.8.249",
  "dhcp_lease": "12h",
  "domain": "lan",
  "dns": ["192.168.8.1"]
}`

func TestNetworkGet(t *testing.T) {
	s := mock.NewServer(t, mock.Options{Password: "secret"})
	s.LoadFixture(mock.NetworkGroup, mock.MethodGetConfig, json.RawMessage(networkFixture))

	got, err := services.NewNetworkService(newTransport(t, s)).Get(context.Background())
	if err != nil {
		t.Fatalf("Get error: %v", err)
	}

	if got.LANIP != "192.168.8.1" {
		t.Errorf("LANIP = %q, want 192.168.8.1", got.LANIP)
	}
	if got.DHCPLease != types.LeaseTime(12*time.Hour) {
		t.Errorf("DHCPLease = %v, want 12h", time.Duration(got.DHCPLease))
	}
	if len(got.DNS) != 1 || got.DNS[0] != "192.168.8.1" {
		t.Errorf("DNS = %v, want [192.168.8.1]", got.DNS)
	}
}

// The service must return a Network whose arithmetic works, since goglps relies
// on Contains to reject out-of-subnet reservations.
func TestNetworkGetSubnetArithmetic(t *testing.T) {
	s := mock.NewServer(t, mock.Options{Password: "secret"})
	s.LoadFixture(mock.NetworkGroup, mock.MethodGetConfig, json.RawMessage(networkFixture))

	got, err := services.NewNetworkService(newTransport(t, s)).Get(context.Background())
	if err != nil {
		t.Fatalf("Get error: %v", err)
	}

	inside, err := got.Contains(net.ParseIP("192.168.8.10"))
	if err != nil || !inside {
		t.Errorf("Contains(192.168.8.10) = %v, %v; want true, nil", inside, err)
	}
	outside, err := got.Contains(net.ParseIP("192.168.4.10"))
	if err != nil || outside {
		t.Errorf("Contains(192.168.4.10) = %v, %v; want false, nil", outside, err)
	}
	pooled, err := got.InDHCPPool(net.ParseIP("192.168.8.150"))
	if err != nil || !pooled {
		t.Errorf("InDHCPPool(192.168.8.150) = %v, %v; want true, nil", pooled, err)
	}
}

func TestNetworkGetPropagatesError(t *testing.T) {
	s := mock.NewServer(t, mock.Options{Password: "secret"})
	s.LoadFixture(mock.NetworkGroup, mock.MethodGetConfig, json.RawMessage(`{}`))
	s.FailNext(mock.NetworkGroup, mock.MethodGetConfig, -32001, "injected")

	if _, err := services.NewNetworkService(newTransport(t, s)).Get(context.Background()); err == nil {
		t.Error("Get succeeded, want error")
	}
}
```

- [ ] **Step 2: Run it to verify it fails**

Run: `go test ./src/services/ -run TestNetwork -v`
Expected: FAIL, `undefined: services.NewNetworkService`.

- [ ] **Step 3: Write the implementation**

`src/services/network.go`:

```go
package services

import (
	"context"
	"fmt"

	"github.com/gherlein/gogl/src/transport"
	"github.com/gherlein/gogl/src/types"
)

const (
	networkGroup     = "lan"
	networkGetConfig = "get_config"
)

type networkService struct {
	transport transport.Transport
}

func NewNetworkService(t transport.Transport) NetworkService {
	return &networkService{transport: t}
}

// Get reads the LAN and DHCP configuration. There is no corresponding Set: gogl
// never writes network configuration, so that a bulk reservation import can
// never leave the router unreachable.
func (s *networkService) Get(ctx context.Context) (*types.Network, error) {
	var network types.Network
	if err := s.transport.Call(ctx, networkGroup, networkGetConfig, nil, &network); err != nil {
		return nil, fmt.Errorf("gogl: read network config: %w", err)
	}
	return &network, nil
}
```

- [ ] **Step 4: Wire the accessor**

Add to `src/client.go`:

```go
// Network reads the router's LAN and DHCP configuration. Read-only: set network
// configuration in the GL.iNet admin panel.
func (c *Client) Network() services.NetworkService {
	return services.NewNetworkService(c.transport)
}
```

- [ ] **Step 5: Run the tests, then commit**

Run: `go test ./src/... -v -race`
Expected: PASS.

```bash
git add src/services/network.go src/services/network_test.go src/client.go
git commit -m "feat(services): add read-only NetworkService"
```

### Task 17: ReservationService

**Files:**
- Create: `src/services/reservation.go`
- Test: `src/services/reservation_test.go`
- Modify: `src/client.go`

**Interfaces:**
- Consumes: `types.Reservation`, `types.NormalizeMAC` (Task 6), `transport.Transport` (Task 12).
- Produces: `services.NewReservationService(transport.Transport) ReservationService`; `(*gogl.Client).Reservations() services.ReservationService`. Tasks 22, 25, 26, 27 all use it.

The only writing service in the module. GL.iNet's reservation API replaces the whole table
in one `set_config` call rather than offering per-entry endpoints, so every mutation is
read-modify-write. That is why `Create`, `Update` and `Delete` all funnel through one
`replace` helper.

If Phase 0 found that writes need a separate apply or commit call, add it at the end of
`replace` and nowhere else.

- [ ] **Step 1: Write the failing test**

`src/services/reservation_test.go`:

```go
package services_test

import (
	"context"
	"errors"
	"testing"

	"github.com/gherlein/gogl/src/mock"
	"github.com/gherlein/gogl/src/services"
	"github.com/gherlein/gogl/src/types"
)

func seeded(t *testing.T) (*mock.Server, services.ReservationService) {
	t.Helper()
	s := mock.NewServer(t, mock.Options{Password: "secret"})
	s.SetReservations([]types.Reservation{
		{Name: "nas", MAC: "aa:bb:cc:dd:ee:01", IP: "192.168.8.13", Enabled: true},
		{Name: "printer", MAC: "aa:bb:cc:dd:ee:02", IP: "192.168.8.14", Enabled: true},
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

func TestReservationGetByMAC(t *testing.T) {
	_, svc := seeded(t)
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

func TestReservationGetByName(t *testing.T) {
	_, svc := seeded(t)
	got, err := svc.GetByName(context.Background(), "printer")
	if err != nil {
		t.Fatalf("GetByName error: %v", err)
	}
	if got.IP != "192.168.8.14" {
		t.Errorf("IP = %q, want 192.168.8.14", got.IP)
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
// untouched.
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

func TestReservationUpdateNotFound(t *testing.T) {
	_, svc := seeded(t)
	_, err := svc.Update(context.Background(), &types.Reservation{
		Name: "ghost", MAC: "aa:bb:cc:dd:ee:99", IP: "192.168.8.50",
	})
	if !errors.Is(err, types.ErrNotFound) {
		t.Errorf("error = %v, want ErrNotFound", err)
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

// An empty device must list cleanly rather than erroring, since a factory router
// has no reservations.
func TestReservationListEmpty(t *testing.T) {
	s := mock.NewServer(t, mock.Options{Password: "secret"})
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
```

- [ ] **Step 2: Add the two sentinels this needs to types**

Add to `src/types/errors.go`:

```go
	// ErrNotFound means no matching record exists on the device.
	ErrNotFound = errors.New("gogl: not found")

	// ErrConflict means the write would collide with an existing record.
	ErrConflict = errors.New("gogl: conflict")
```

Then change `src/errors.go` to re-export these two from `types` instead of declaring them,
matching how Task 6 handled `ErrInvalidName`.

- [ ] **Step 3: Run the test to verify it fails**

Run: `go test ./src/services/ -run TestReservation -v`
Expected: FAIL, `undefined: services.NewReservationService`.

- [ ] **Step 4: Write the implementation**

`src/services/reservation.go`:

```go
package services

import (
	"context"
	"fmt"
	"strings"

	"github.com/gherlein/gogl/src/transport"
	"github.com/gherlein/gogl/src/types"
)

const (
	reservationGroup     = "dhcp"
	reservationGetConfig = "get_config"
	reservationSetConfig = "set_config"
)

// reservationTable is the wire shape of the reservation list. The firmware wraps
// the array in an object; the field name comes from the Phase 0 fixture.
type reservationTable struct {
	Res []types.Reservation `json:"res"`
}

type reservationService struct {
	transport transport.Transport
}

func NewReservationService(t transport.Transport) ReservationService {
	return &reservationService{transport: t}
}

func (s *reservationService) List(ctx context.Context) ([]types.Reservation, error) {
	var table reservationTable
	if err := s.transport.Call(ctx, reservationGroup, reservationGetConfig, nil, &table); err != nil {
		return nil, fmt.Errorf("gogl: list reservations: %w", err)
	}
	return table.Res, nil
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

// GetByIP returns every reservation holding ip. More than one indicates
// inconsistent device state rather than normal operation, so the caller decides
// whether to tolerate it.
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
	// Validate first, and before any read, so a bad name cannot reach the device
	// and cannot corrupt dnsmasq's config.
	if err := r.Validate(); err != nil {
		return nil, err
	}

	all, err := s.List(ctx)
	if err != nil {
		return nil, err
	}
	for _, existing := range all {
		if strings.EqualFold(existing.MAC, r.MAC) {
			return nil, fmt.Errorf("%w: MAC %s is already reserved for %s", types.ErrConflict, r.MAC, existing.IP)
		}
	}

	r.Enabled = true
	if err := s.replace(ctx, append(all, *r)); err != nil {
		return nil, err
	}
	return r, nil
}

func (s *reservationService) Update(ctx context.Context, r *types.Reservation) (*types.Reservation, error) {
	if err := r.Validate(); err != nil {
		return nil, err
	}

	all, err := s.List(ctx)
	if err != nil {
		return nil, err
	}

	found := false
	for i := range all {
		if strings.EqualFold(all[i].MAC, r.MAC) {
			r.Enabled = true
			all[i] = *r
			found = true
			break
		}
	}
	if !found {
		return nil, fmt.Errorf("%w: no reservation for MAC %s", types.ErrNotFound, r.MAC)
	}

	if err := s.replace(ctx, all); err != nil {
		return nil, err
	}
	return r, nil
}

// Delete removes the reservation for mac, and with it the DNS name. There is no
// way to remove one and keep the other: on this device they are one record.
func (s *reservationService) Delete(ctx context.Context, mac string) error {
	normalized, err := types.NormalizeMAC(mac)
	if err != nil {
		return err
	}

	all, err := s.List(ctx)
	if err != nil {
		return err
	}

	kept := make([]types.Reservation, 0, len(all))
	for _, r := range all {
		if !strings.EqualFold(r.MAC, normalized) {
			kept = append(kept, r)
		}
	}
	if len(kept) == len(all) {
		return fmt.Errorf("%w: no reservation for MAC %s", types.ErrNotFound, normalized)
	}
	return s.replace(ctx, kept)
}

// replace writes the whole reservation table. The firmware offers no per-entry
// endpoint, so every mutation is read-modify-write through here.
//
// If Phase 0 found that writes require a separate apply or commit call, add it
// here and nowhere else.
func (s *reservationService) replace(ctx context.Context, res []types.Reservation) error {
	if res == nil {
		res = []types.Reservation{}
	}
	args := reservationTable{Res: res}
	if err := s.transport.Call(ctx, reservationGroup, reservationSetConfig, args, nil); err != nil {
		return fmt.Errorf("gogl: write reservations: %w", err)
	}
	return nil
}
```

- [ ] **Step 5: Wire the accessor**

Add to `src/client.go`:

```go
// Reservations manages static DHCP leases. This is the only thing gogl writes.
// Each lease carries the DNS name for its address, so creating a reservation
// also creates the name and deleting one also removes it.
func (c *Client) Reservations() services.ReservationService {
	return services.NewReservationService(c.transport)
}
```

- [ ] **Step 6: Run the tests**

Run: `go test ./src/... -v -race`
Expected: PASS, all fourteen reservation tests.
`TestReservationCreateRejectionDoesNotWrite` is the important one: it proves validation
happens before the device is touched.

- [ ] **Step 7: Commit**

```bash
git add src/services/reservation.go src/services/reservation_test.go src/types/errors.go src/errors.go src/client.go
git commit -m "feat(services): add ReservationService, the only writing service"
```

### Task 18: ClientService

**Files:**
- Create: `src/types/client.go`
- Create: `src/services/client.go`
- Test: `src/services/client_test.go`
- Modify: `src/client.go`

**Interfaces:**
- Consumes: `transport.Transport` (Task 12).
- Produces: `types.Client`; `services.NewClientService(transport.Transport) ClientService`; `(*gogl.Client).Clients() services.ClientService`. Task 22 calls `List`.

- [ ] **Step 1: Write the type**

`src/types/client.go`:

```go
package types

// Client is a station currently known to the router.
//
// Field presence varies with firmware, so optional fields are omitempty or
// pointers. Nothing here is invented to match gofi's richer UniFi client
// record: a field is added only when a Phase 0 fixture shows the firmware
// returning it.
type Client struct {
	MAC     string `json:"mac"`
	IP      string `json:"ip,omitempty"`
	Name    string `json:"name,omitempty"`
	Online  bool   `json:"online"`
	IsWired bool   `json:"is_wired"`
	RXBytes uint64 `json:"rx_bytes,omitempty"`
	TXBytes uint64 `json:"tx_bytes,omitempty"`
	Signal  *int   `json:"signal,omitempty"`
	Band    string `json:"band,omitempty"`
}

// Hostname returns the name to display for c, or "unknown" when the router
// reports none. Utilities use this rather than reimplementing the fallback.
func (c *Client) Hostname() string {
	if c.Name != "" {
		return c.Name
	}
	return "unknown"
}
```

- [ ] **Step 2: Write the failing test**

`src/services/client_test.go`:

```go
package services_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/gherlein/gogl/src/mock"
	"github.com/gherlein/gogl/src/services"
)

const clientsFixture = `{"clients":[
  {"mac":"aa:bb:cc:dd:ee:01","ip":"192.168.8.13","name":"nas","online":true,"is_wired":true,"rx_bytes":100,"tx_bytes":200},
  {"mac":"aa:bb:cc:dd:ee:02","ip":"192.168.8.14","name":"phone","online":true,"is_wired":false,"band":"5g"},
  {"mac":"aa:bb:cc:dd:ee:03","online":false,"is_wired":false}
]}`

func TestClientList(t *testing.T) {
	s := mock.NewServer(t, mock.Options{Password: "secret"})
	s.LoadFixture(mock.ClientGroup, mock.MethodGetList, json.RawMessage(clientsFixture))

	got, err := services.NewClientService(newTransport(t, s)).List(context.Background())
	if err != nil {
		t.Fatalf("List error: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("List returned %d, want 3", len(got))
	}
	if !got[0].IsWired {
		t.Error("first client should be wired")
	}
	if got[1].Band != "5g" {
		t.Errorf("second client Band = %q, want 5g", got[1].Band)
	}
	// A client with no reported name falls back rather than showing empty.
	if got[2].Hostname() != "unknown" {
		t.Errorf("third client Hostname() = %q, want unknown", got[2].Hostname())
	}
	if got[2].IP != "" {
		t.Errorf("third client IP = %q, want empty", got[2].IP)
	}
}

func TestClientListEmpty(t *testing.T) {
	s := mock.NewServer(t, mock.Options{Password: "secret"})
	s.LoadFixture(mock.ClientGroup, mock.MethodGetList, json.RawMessage(`{"clients":[]}`))

	got, err := services.NewClientService(newTransport(t, s)).List(context.Background())
	if err != nil {
		t.Fatalf("List error: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("List returned %d entries, want 0", len(got))
	}
}

func TestClientListPropagatesError(t *testing.T) {
	s := mock.NewServer(t, mock.Options{Password: "secret"})
	s.LoadFixture(mock.ClientGroup, mock.MethodGetList, json.RawMessage(`{}`))
	s.FailNext(mock.ClientGroup, mock.MethodGetList, -32001, "injected")

	if _, err := services.NewClientService(newTransport(t, s)).List(context.Background()); err == nil {
		t.Error("List succeeded, want error")
	}
}
```

- [ ] **Step 3: Run it to verify it fails**

Run: `go test ./src/services/ -run TestClient -v`
Expected: FAIL, `undefined: services.NewClientService`.

- [ ] **Step 4: Write the implementation**

`src/services/client.go`:

```go
package services

import (
	"context"
	"fmt"

	"github.com/gherlein/gogl/src/transport"
	"github.com/gherlein/gogl/src/types"
)

const (
	clientGroup   = "clients"
	clientGetList = "get_list"
)

type clientList struct {
	Clients []types.Client `json:"clients"`
}

type clientService struct {
	transport transport.Transport
}

func NewClientService(t transport.Transport) ClientService {
	return &clientService{transport: t}
}

func (s *clientService) List(ctx context.Context) ([]types.Client, error) {
	var list clientList
	if err := s.transport.Call(ctx, clientGroup, clientGetList, nil, &list); err != nil {
		return nil, fmt.Errorf("gogl: list clients: %w", err)
	}
	return list.Clients, nil
}
```

- [ ] **Step 5: Wire the accessor, test, and commit**

Add to `src/client.go`:

```go
// Clients reads stations known to the router. Read-only.
func (c *Client) Clients() services.ClientService {
	return services.NewClientService(c.transport)
}
```

Run: `go test ./src/... -v -race && make coverage`
Expected: PASS, 100% coverage of everything written so far.

```bash
git add src/types/client.go src/services/client.go src/services/client_test.go src/client.go
git commit -m "feat(services): add ClientService"
```

---

## Phase 6: goglnet

The first utility, and read-only, so it exercises the whole stack without risking a write.

### Task 19: Shared CLI connection helper

**Files:**
- Create: `utilities/internal/conn/conn.go`
- Test: `utilities/internal/conn/conn_test.go`

**Interfaces:**
- Consumes: `gogl.New`, `gogl.Config` (Task 13).
- Produces: `conn.Flags` with `Register(*flag.FlagSet)`, `Validate() error`, `Connect() (*gogl.Client, error)`. Tasks 20, 22, 25 all embed it, which is how the three utilities keep identical connection ergonomics.

- [ ] **Step 1: Write the failing test**

`utilities/internal/conn/conn_test.go`:

```go
package conn

import (
	"flag"
	"strings"
	"testing"
)

func parse(t *testing.T, args ...string) *Flags {
	t.Helper()
	f := &Flags{}
	fs := flag.NewFlagSet("test", flag.ContinueOnError)
	fs.SetOutput(strings.NewReader(""))
	f.Register(fs)
	if err := fs.Parse(args); err != nil {
		t.Fatalf("Parse(%v): %v", args, err)
	}
	return f
}

func TestDefaults(t *testing.T) {
	f := parse(t)
	if f.Port != DefaultPort {
		t.Errorf("Port = %d, want %d", f.Port, DefaultPort)
	}
	if f.HTTPS {
		t.Error("HTTPS should default to false")
	}
	if f.Secure {
		t.Error("Secure should default to false")
	}
}

func TestValidateRequiresHost(t *testing.T) {
	f := parse(t)
	f.Password = "secret"
	err := f.Validate()
	if err == nil || !strings.Contains(err.Error(), "host") {
		t.Errorf("Validate() = %v, want an error mentioning host", err)
	}
}

func TestValidateRequiresPassword(t *testing.T) {
	f := parse(t, "-H", "192.168.8.1")
	err := f.Validate()
	if err == nil || !strings.Contains(err.Error(), "GL_PASSWORD") {
		t.Errorf("Validate() = %v, want an error naming GL_PASSWORD", err)
	}
}

// -k without --https is an error rather than a silent no-op, because a user who
// passes it is expressing an intent the transport cannot honour over HTTP.
func TestValidateRejectsSecureWithoutHTTPS(t *testing.T) {
	f := parse(t, "-H", "192.168.8.1", "-k")
	f.Password = "secret"
	err := f.Validate()
	if err == nil || !strings.Contains(err.Error(), "--https") {
		t.Errorf("Validate() = %v, want an error mentioning --https", err)
	}
}

func TestValidateAcceptsSecureWithHTTPS(t *testing.T) {
	f := parse(t, "-H", "192.168.8.1", "-k", "--https", "-p", "443")
	f.Password = "secret"
	if err := f.Validate(); err != nil {
		t.Errorf("Validate() = %v, want nil", err)
	}
}

func TestValidateWarnsOnHTTPSWithPort80(t *testing.T) {
	f := parse(t, "-H", "192.168.8.1", "--https")
	f.Password = "secret"
	if err := f.Validate(); err != nil {
		t.Fatalf("Validate() = %v, want nil", err)
	}
	if len(f.Warnings()) == 0 {
		t.Error("expected a warning for --https on port 80")
	}
}

// The library must be secure at its zero value while the CLI accepts
// self-signed certificates, so the flag inverts on the way through.
func TestConnectInvertsTLSVerification(t *testing.T) {
	f := parse(t, "-H", "192.168.8.1", "--https", "-p", "443")
	f.Password = "secret"
	if got := f.clientConfig().InsecureSkipVerify; !got {
		t.Error("InsecureSkipVerify = false without -k; CLI should default to accepting self-signed")
	}

	f2 := parse(t, "-H", "192.168.8.1", "--https", "-p", "443", "-k")
	f2.Password = "secret"
	if got := f2.clientConfig().InsecureSkipVerify; got {
		t.Error("InsecureSkipVerify = true with -k; -k should enforce verification")
	}
}
```

- [ ] **Step 2: Run it to verify it fails**

Run: `go test ./utilities/internal/conn/ -v`
Expected: FAIL, `undefined: Flags`.

- [ ] **Step 3: Write the implementation**

`utilities/internal/conn/conn.go`:

```go
// Package conn holds the connection flags and client construction shared by
// goglps, goglnet and goglmac, so the three utilities present identical
// ergonomics.
package conn

import (
	"errors"
	"fmt"
	"os"

	gogl "github.com/gherlein/gogl/src"
)

// DefaultPort is 80, not 443, because that is what GL.iNet firmware serves.
const DefaultPort = 80

// Environment variables. GL_PASSWORD is required; the others are conveniences.
const (
	EnvPassword = "GL_PASSWORD"
	EnvUsername = "GL_USERNAME"
	EnvHost     = "GL_ROUTER_IP"
)

// flagSet is the subset of flag.FlagSet that Register needs.
type flagSet interface {
	StringVar(p *string, name, value, usage string)
	IntVar(p *int, name string, value int, usage string)
	BoolVar(p *bool, name string, value bool, usage string)
}

// Flags holds the connection options common to every utility.
type Flags struct {
	Host   string
	Port   int
	HTTPS  bool
	Secure bool

	// Password is read from the environment rather than a flag, so it never
	// appears in a process listing or a shell history.
	Password string
	Username string

	warnings []string
}

func (f *Flags) Register(fs flagSet) {
	fs.StringVar(&f.Host, "host", os.Getenv(EnvHost), "router host address")
	fs.StringVar(&f.Host, "H", os.Getenv(EnvHost), "router host address (shorthand)")
	fs.IntVar(&f.Port, "port", DefaultPort, "router port")
	fs.IntVar(&f.Port, "p", DefaultPort, "router port (shorthand)")
	fs.BoolVar(&f.HTTPS, "https", false, "use HTTPS instead of HTTP")
	fs.BoolVar(&f.Secure, "secure", false, "under --https, enforce TLS certificate verification")
	fs.BoolVar(&f.Secure, "k", false, "under --https, enforce TLS certificate verification (shorthand)")
}

// Validate checks the flags and fills in the environment-supplied values.
func (f *Flags) Validate() error {
	if f.Password == "" {
		f.Password = os.Getenv(EnvPassword)
	}
	if f.Username == "" {
		f.Username = os.Getenv(EnvUsername)
	}

	if f.Host == "" {
		return fmt.Errorf("no router host: pass -H or set %s", EnvHost)
	}
	if f.Password == "" {
		return fmt.Errorf("no router password: set %s", EnvPassword)
	}

	// -k expresses an intent the transport cannot honour over plain HTTP, so
	// silently ignoring it would be misleading.
	if f.Secure && !f.HTTPS {
		return errors.New("-k/--secure requires --https; there is no certificate to verify over HTTP")
	}
	if f.HTTPS && f.Port == DefaultPort {
		f.warnings = append(f.warnings,
			fmt.Sprintf("--https with port %d is probably wrong; GL.iNet serves HTTPS on 443", DefaultPort))
	}
	return nil
}

// Warnings returns non-fatal advisories accumulated by Validate. Callers print
// these to stderr.
func (f *Flags) Warnings() []string { return f.warnings }

// clientConfig maps CLI flags onto library config, inverting the TLS sense. The
// library is secure at its zero value; the CLI accepts self-signed certificates
// unless -k is given, because a router with a self-signed certificate is the
// normal case and an unusable CLI is worse than a warned-about one.
func (f *Flags) clientConfig() gogl.Config {
	return gogl.Config{
		Host:               f.Host,
		Port:               f.Port,
		HTTPS:              f.HTTPS,
		Username:           f.Username,
		Password:           f.Password,
		InsecureSkipVerify: !f.Secure,
	}
}

// Connect validates and builds a client.
func (f *Flags) Connect() (*gogl.Client, error) {
	if err := f.Validate(); err != nil {
		return nil, err
	}
	for _, w := range f.warnings {
		fmt.Fprintln(os.Stderr, "warning:", w)
	}
	return gogl.New(f.clientConfig())
}
```

- [ ] **Step 4: Run the tests, then commit**

Run: `go test ./utilities/... -v -race`
Expected: PASS, all eight tests.

```bash
git add utilities/internal/conn/
git commit -m "feat(utilities): add shared connection flags"
```

### Task 20: goglnet

**Files:**
- Create: `utilities/goglnet/main.go`
- Create: `utilities/goglnet/operations.go`
- Create: `utilities/goglnet/format.go`
- Test: `utilities/goglnet/format_test.go`
- Test: `utilities/goglnet/operations_test.go`

**Interfaces:**
- Consumes: `conn.Flags` (Task 19), `NetworkService` (Task 16), `SystemService` (Task 15), `ReservationService` (Task 17).
- Produces: the `goglnet` binary and `Report` struct. Nothing consumes these; it is a leaf.

Counterpart to `gofinet`. Emits a single JSON **object**, not an array, because the travel
router has one LAN where a UDM Pro has many networks.

- [ ] **Step 1: Write the failing format test**

`utilities/goglnet/format_test.go`:

```go
package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/gherlein/gogl/src/types"
)

func testReport() *Report {
	return &Report{
		Model:          "gl-sft1200",
		Firmware:       "4.3.28",
		LANIP:          "192.168.8.1",
		Netmask:        "255.255.255.0",
		Subnet:         "192.168.8.0/24",
		DHCPEnabled:    true,
		DHCPStart:      "192.168.8.100",
		DHCPStop:       "192.168.8.249",
		PoolSize:       150,
		DHCPLease:      types.LeaseTime(12 * time.Hour),
		Domain:         "lan",
		DNS:            []string{"192.168.8.1"},
		ReservedCount:  38,
		AvailableCount: 65,
	}
}

func TestFormatText(t *testing.T) {
	var buf bytes.Buffer
	if err := formatText(&buf, testReport()); err != nil {
		t.Fatalf("formatText error: %v", err)
	}
	out := buf.String()

	for _, want := range []string{
		"gl-sft1200", "4.3.28", "192.168.8.1/24",
		"192.168.8.100 - 192.168.8.249", "150", "12h", "lan", "38", "65",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q:\n%s", want, out)
		}
	}
}

// A disabled DHCP server must read as disabled rather than showing a stale pool.
func TestFormatTextDHCPDisabled(t *testing.T) {
	r := testReport()
	r.DHCPEnabled = false

	var buf bytes.Buffer
	if err := formatText(&buf, r); err != nil {
		t.Fatalf("formatText error: %v", err)
	}
	if !strings.Contains(buf.String(), "(disabled)") {
		t.Errorf("output does not mark DHCP disabled:\n%s", buf.String())
	}
}

func TestFormatJSONIsAnObject(t *testing.T) {
	var buf bytes.Buffer
	if err := formatJSON(&buf, testReport()); err != nil {
		t.Fatalf("formatJSON error: %v", err)
	}

	// gofinet emits an array because a UDM Pro has many networks. goglnet emits
	// an object because the travel router has one LAN.
	trimmed := strings.TrimSpace(buf.String())
	if !strings.HasPrefix(trimmed, "{") {
		t.Errorf("JSON output is not an object: %s", trimmed)
	}

	var decoded map[string]any
	if err := json.Unmarshal(buf.Bytes(), &decoded); err != nil {
		t.Fatalf("output is not valid JSON: %v", err)
	}
	if decoded["model"] != "gl-sft1200" {
		t.Errorf("model = %v, want gl-sft1200", decoded["model"])
	}
	if decoded["dhcp_lease"] != "12h" {
		t.Errorf("dhcp_lease = %v, want 12h", decoded["dhcp_lease"])
	}
}
```

- [ ] **Step 2: Write the failing operations test**

`utilities/goglnet/operations_test.go`:

```go
package main

import (
	"context"
	"testing"
	"time"

	"github.com/gherlein/gogl/src/types"
)

// stubs let buildReport be tested without a client, which keeps this test fast
// and independent of the mock server.
type stubNetwork struct{ n *types.Network }

func (s stubNetwork) Get(context.Context) (*types.Network, error) { return s.n, nil }

type stubSystem struct{ i *types.SystemInfo }

func (s stubSystem) Info(context.Context) (*types.SystemInfo, error) { return s.i, nil }

type stubReservations struct{ r []types.Reservation }

func (s stubReservations) List(context.Context) ([]types.Reservation, error) { return s.r, nil }

func TestBuildReport(t *testing.T) {
	network := &types.Network{
		LANIP: "192.168.8.1", Netmask: "255.255.255.0",
		DHCPEnabled: true, DHCPStart: "192.168.8.100", DHCPStop: "192.168.8.249",
		DHCPLease: types.LeaseTime(12 * time.Hour), Domain: "lan",
		DNS: []string{"192.168.8.1"},
	}
	reservations := []types.Reservation{
		{Name: "nas", MAC: "aa:bb:cc:dd:ee:01", IP: "192.168.8.13"},
		{Name: "printer", MAC: "aa:bb:cc:dd:ee:02", IP: "192.168.8.14"},
	}

	got, err := buildReport(context.Background(),
		stubNetwork{network},
		stubSystem{&types.SystemInfo{Model: "gl-sft1200", Firmware: "4.3.28"}},
		stubReservations{reservations},
	)
	if err != nil {
		t.Fatalf("buildReport error: %v", err)
	}

	if got.Subnet != "192.168.8.0/24" {
		t.Errorf("Subnet = %q, want 192.168.8.0/24", got.Subnet)
	}
	if got.PoolSize != 150 {
		t.Errorf("PoolSize = %d, want 150", got.PoolSize)
	}
	if got.ReservedCount != 2 {
		t.Errorf("ReservedCount = %d, want 2", got.ReservedCount)
	}
	// 254 usable, minus 150 pooled, minus 2 reserved, minus the router itself.
	if got.AvailableCount != 101 {
		t.Errorf("AvailableCount = %d, want 101", got.AvailableCount)
	}
}

// A reservation inside the pool must not be double-counted against available.
func TestBuildReportDoesNotDoubleCountPooledReservations(t *testing.T) {
	network := &types.Network{
		LANIP: "192.168.8.1", Netmask: "255.255.255.0",
		DHCPEnabled: true, DHCPStart: "192.168.8.100", DHCPStop: "192.168.8.249",
	}
	got, err := buildReport(context.Background(),
		stubNetwork{network},
		stubSystem{&types.SystemInfo{}},
		stubReservations{[]types.Reservation{{Name: "x", MAC: "aa:bb:cc:dd:ee:01", IP: "192.168.8.150"}}},
	)
	if err != nil {
		t.Fatalf("buildReport error: %v", err)
	}
	// 254 - 150 pooled - 1 router = 103. The reservation is already inside the
	// pool, so subtracting it again would understate what is free.
	if got.AvailableCount != 103 {
		t.Errorf("AvailableCount = %d, want 103", got.AvailableCount)
	}
}
```

- [ ] **Step 3: Run both to verify they fail**

Run: `go test ./utilities/goglnet/ -v`
Expected: FAIL, `undefined: Report`.

- [ ] **Step 4: Write operations.go**

`utilities/goglnet/operations.go`:

```go
package main

import (
	"context"
	"net"

	"github.com/gherlein/gogl/src/types"
)

// Report is the flattened view goglnet prints. JSON tags define the -j output
// contract.
type Report struct {
	Model    string `json:"model,omitempty"`
	Firmware string `json:"firmware,omitempty"`

	LANIP   string `json:"lan_ip"`
	Netmask string `json:"netmask"`
	Subnet  string `json:"subnet"`

	DHCPEnabled bool            `json:"dhcp_enabled"`
	DHCPStart   string          `json:"dhcp_start,omitempty"`
	DHCPStop    string          `json:"dhcp_stop,omitempty"`
	PoolSize    int             `json:"pool_size,omitempty"`
	DHCPLease   types.LeaseTime `json:"dhcp_lease,omitempty"`

	Domain string   `json:"domain,omitempty"`
	DNS    []string `json:"dns,omitempty"`

	ReservedCount  int `json:"reserved_count"`
	AvailableCount int `json:"available_count"`
}

// The three narrow interfaces below exist so buildReport can be tested with
// stubs instead of a live client.
type networkGetter interface {
	Get(context.Context) (*types.Network, error)
}

type systemInfoer interface {
	Info(context.Context) (*types.SystemInfo, error)
}

type reservationLister interface {
	List(context.Context) ([]types.Reservation, error)
}

func buildReport(ctx context.Context, nets networkGetter, sys systemInfoer, res reservationLister) (*Report, error) {
	network, err := nets.Get(ctx)
	if err != nil {
		return nil, err
	}
	reservations, err := res.List(ctx)
	if err != nil {
		return nil, err
	}

	report := &Report{
		LANIP:         network.LANIP,
		Netmask:       network.Netmask,
		DHCPEnabled:   network.DHCPEnabled,
		DHCPStart:     network.DHCPStart,
		DHCPStop:      network.DHCPStop,
		PoolSize:      network.PoolSize(),
		DHCPLease:     network.DHCPLease,
		Domain:        network.Domain,
		DNS:           network.DNS,
		ReservedCount: len(reservations),
	}

	// System info is a convenience: a router that will not report its model is
	// still worth reporting the network of.
	if info, err := sys.Info(ctx); err == nil {
		report.Model, report.Firmware = info.Model, info.Firmware
	}

	if subnet, err := network.Subnet(); err == nil {
		report.Subnet = subnet.String()
	}

	report.AvailableCount = countAvailable(network, reservations)
	return report, nil
}

// countAvailable returns host addresses that are neither pooled, nor reserved,
// nor the router's own address. Reservations already inside the pool are not
// subtracted twice, which would understate what is free.
func countAvailable(network *types.Network, reservations []types.Reservation) int {
	available := network.UsableHosts()
	if available == 0 {
		return 0
	}

	available -= network.PoolSize()

	// The router's own address is never assignable.
	routerIP := net.ParseIP(network.LANIP)
	if routerIP != nil {
		if pooled, err := network.InDHCPPool(routerIP); err == nil && !pooled {
			available--
		}
	}

	for _, r := range reservations {
		ip := net.ParseIP(r.IP)
		if ip == nil {
			continue
		}
		inside, err := network.Contains(ip)
		if err != nil || !inside {
			continue
		}
		if pooled, err := network.InDHCPPool(ip); err == nil && pooled {
			continue
		}
		if routerIP != nil && ip.Equal(routerIP) {
			continue
		}
		available--
	}

	if available < 0 {
		return 0
	}
	return available
}
```

- [ ] **Step 5: Write format.go**

`utilities/goglnet/format.go`:

```go
package main

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"text/tabwriter"
)

const disabledMarker = "(disabled)"

func formatText(w io.Writer, r *Report) error {
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)

	row := func(label, value string) {
		if value == "" {
			value = "-"
		}
		fmt.Fprintf(tw, "%s\t%s\n", label, value)
	}

	row("MODEL", r.Model)
	row("FIRMWARE", r.Firmware)
	row("LAN", fmt.Sprintf("%s  (%s)", r.Subnet, r.Netmask))

	if r.DHCPEnabled {
		row("DHCP", "enabled")
		row("POOL", fmt.Sprintf("%s - %s  (%d addresses)", r.DHCPStart, r.DHCPStop, r.PoolSize))
		row("LEASE", r.DHCPLease.String())
	} else {
		row("DHCP", "disabled")
		row("POOL", disabledMarker)
		row("LEASE", disabledMarker)
	}

	row("DOMAIN", r.Domain)
	row("DNS", strings.Join(r.DNS, ","))
	row("RESERVED", fmt.Sprintf("%d", r.ReservedCount))
	row("AVAILABLE", fmt.Sprintf("%d", r.AvailableCount))

	return tw.Flush()
}

func formatJSON(w io.Writer, r *Report) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(r)
}
```

- [ ] **Step 6: Write main.go**

`utilities/goglnet/main.go`:

```go
// Command goglnet reports a GL.iNet router's LAN address, DHCP pool, and DNS
// settings. Read-only: it never writes to the router.
//
// It is the companion to goglps in the way gofinet is the companion to gofips.
// The pool boundaries define which addresses the router hands out dynamically;
// everything else in the subnet is available for a static reservation.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"

	"github.com/gherlein/gogl/utilities/internal/conn"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "goglnet:", err)
		os.Exit(1)
	}
}

func run() error {
	var flags conn.Flags
	fs := flag.NewFlagSet("goglnet", flag.ExitOnError)
	flags.Register(fs)

	asJSON := fs.Bool("json", false, "output JSON instead of text")
	fs.BoolVar(asJSON, "j", false, "output JSON instead of text (shorthand)")

	if err := fs.Parse(os.Args[1:]); err != nil {
		return err
	}

	client, err := flags.Connect()
	if err != nil {
		return err
	}
	defer client.Close()

	ctx := context.Background()
	report, err := buildReport(ctx, client.Network(), client.System(), client.Reservations())
	if err != nil {
		return err
	}

	if *asJSON {
		return formatJSON(os.Stdout, report)
	}
	return formatText(os.Stdout, report)
}
```

- [ ] **Step 7: Run the tests and build**

Run: `go test ./utilities/... -v -race && make build`
Expected: PASS, and `bin/goglnet` exists.

- [ ] **Step 8: Verify against a real router**

```bash
export GL_ROUTER_IP=192.168.8.1
export GL_PASSWORD='<your router password>'
./bin/goglnet
./bin/goglnet -j
```

Expected: the LAN address, pool, and reservation count matching what the GL.iNet admin panel
shows under LAN. A mismatch means a fixture's JSON tags do not match the firmware — fix the
tags, and add the corrected payload to `src/mock/fixtures/`.

- [ ] **Step 9: Commit**

```bash
git add utilities/goglnet/
git commit -m "feat(goglnet): add read-only LAN and DHCP report"
```

---

## Remaining Tasks

Tasks 21 through 30 continue in [`plan-part4.md`](plan-part4.md).
