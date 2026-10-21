package netcfg

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"strings"

	gogl "github.com/gherlein/gogl/src"

	"github.com/gherlein/gogl/src/services"
	"github.com/gherlein/gogl/src/types"
)

// NetworkModes are the flags governing a LAN write.
type NetworkModes struct {
	// DryRun reports what would change, including any refusal, without writing.
	DryRun bool

	// Force allows a subnet move while reservations exist. The firmware renumbers
	// them; see NetworkService.Set for why that is permitted but not the default.
	Force bool
}

// Report is the flattened view goglnet prints. JSON tags define the -j output
// contract.
type Report struct {
	Model    string `json:"model,omitempty"`
	Firmware string `json:"firmware,omitempty"`

	LANIP   string `json:"lan_ip"`
	Netmask string `json:"netmask"`
	Subnet  string `json:"subnet,omitempty"`

	DHCPEnabled bool            `json:"dhcp_enabled"`
	DHCPStart   string          `json:"dhcp_start,omitempty"`
	DHCPStop    string          `json:"dhcp_stop,omitempty"`
	PoolSize    int             `json:"pool_size,omitempty"`
	DHCPLease   types.LeaseTime `json:"dhcp_lease,omitempty"`

	Interface string   `json:"interface,omitempty"`
	Gateway   string   `json:"gateway,omitempty"`
	DNS       []string `json:"dns,omitempty"`

	ReservedCount  int `json:"reserved_count"`
	AvailableCount int `json:"available_count"`

	// InPool are reservations whose address falls inside the DHCP pool.
	//
	// Reported because it is invisible otherwise and arises by accident: a LAN
	// renumber moved 20 of 27 reservations into the pool on a real device, silently,
	// because the firmware rewrites host parts without considering the pool. dnsmasq
	// honors them and excludes them from allocation, so nothing is broken -- but they
	// are missing from AvailableCount, and an operator counting free addresses is
	// otherwise left to work out why the numbers do not add up.
	InPool []types.Reservation `json:"in_pool,omitempty"`
}

// The three narrow interfaces below exist so BuildReport can be tested with stubs
// instead of a live client.
type networkGetter interface {
	Get(context.Context) (*types.Network, error)
}

type systemInfoer interface {
	Info(context.Context) (*types.SystemInfo, error)
}

type reservationLister interface {
	List(context.Context) ([]types.Reservation, error)
}

func BuildReport(ctx context.Context, nets networkGetter, sys systemInfoer, res reservationLister) (*Report, error) {
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
		DHCPEnabled:   bool(network.DHCPEnabled),
		DHCPStart:     network.DHCPStart,
		DHCPStop:      network.DHCPStop,
		PoolSize:      network.PoolSize(),
		DHCPLease:     network.DHCPLease,
		Interface:     network.Interface,
		Gateway:       network.Gateway,
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
	report.InPool = ReservationsInPool(network, reservations)
	return report, nil
}

// countAvailable returns host addresses that are neither pooled, nor reserved,
// nor the router's own address.
//
// Reservations already inside the pool are not subtracted twice, which would
// understate what is actually free.
func countAvailable(network *types.Network, reservations []types.Reservation) int {
	available := network.UsableHosts()
	if available == 0 {
		return 0
	}

	available -= network.PoolSize()

	// The router's own address is never assignable, but only subtract it if the
	// pool has not already accounted for it.
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

// SetNetwork writes a new address and pool to the router.
//
// Two things make this unlike every other write in gogl. It is refused while
// reservations exist -- not because they would be stranded, which is what that guard
// was written for and is wrong, but because the firmware rewrites every one of them
// silently; see NetworkService.Set. And it moves the router to a different address, so the
// call cannot report success in the usual way: the connection dies as the change
// takes effect. dryRun runs every check and prints the same summary, but stops
// short of the write.
func SetNetwork(ctx context.Context, client *gogl.Client, n *types.Network, modes NetworkModes) error {
	current, err := client.Network().Get(ctx)
	if err != nil {
		return err
	}

	// Fill in whatever was not asked for. A pool-only change is the common case and
	// should not require restating an address that is not moving -- but the firmware
	// takes all four fields in one call, so they have to come from somewhere.
	if n.LANIP == "" {
		n.LANIP = current.LANIP
	}
	if n.Netmask == "" {
		n.Netmask = current.Netmask
	}
	if n.DHCPStart == "" {
		n.DHCPStart = current.DHCPStart
	}
	if n.DHCPStop == "" {
		n.DHCPStop = current.DHCPStop
	}

	// The same validation the write performs, so a dry run cannot approve what
	// the real call would reject.
	if err := n.ValidateForWrite(); err != nil {
		return err
	}
	subnet, err := n.Subnet()
	if err != nil {
		return err
	}

	moving := n.LANIP != current.LANIP || n.Netmask != current.Netmask

	existing, err := client.Reservations().List(ctx)
	if err != nil {
		return err
	}

	// Check the blocking condition before announcing the change. Printing "the
	// router will move" and then refusing reads as a failed write rather than a
	// refused one.
	if moving && len(existing) > 0 && !modes.Force {
		return fmt.Errorf("%w: %d reservation(s) present\n"+
			"  the firmware would silently rewrite all of them into %s, keeping host parts\n"+
			"  that is usually what you want, but it is unannounced, and untested for a\n"+
			"  netmask change or a move that lands addresses inside the new DHCP pool\n"+
			"  clear them first:      goglps --clear\n"+
			"  or accept the rewrite: goglnet --force ...",
			types.ErrReservationsExist, len(existing), subnet)
	}

	if moving {
		fmt.Fprintf(os.Stderr, "interface %s: %s/%s -> %s (%s)\n",
			n.Interface, current.LANIP, current.Netmask, n.LANIP, subnet)
	} else {
		fmt.Fprintf(os.Stderr, "interface %s: staying at %s (%s)\n",
			n.Interface, current.LANIP, subnet)
	}
	if n.DHCPStart != current.DHCPStart || n.DHCPStop != current.DHCPStop {
		fmt.Fprintf(os.Stderr, "pool: %s-%s -> %s-%s\n",
			current.DHCPStart, current.DHCPStop, n.DHCPStart, n.DHCPStop)
	}

	// Reservations the new pool will enclose. dnsmasq honors a static bind inside the
	// dynamic range and excludes that address from allocation, so this works -- but it
	// happened silently to 20 of 27 reservations on a real renumber, and an operator
	// counting free addresses deserves to be told.
	if enclosed := ReservationsInPool(n, existing); len(enclosed) > 0 {
		fmt.Fprintf(os.Stderr,
			"warning: %d reservation(s) will fall inside the new pool %s-%s\n",
			len(enclosed), n.DHCPStart, n.DHCPStop)
		for _, r := range enclosed {
			fmt.Fprintf(os.Stderr, "  %s %s\n", r.IP, r.Name)
		}
		fmt.Fprintln(os.Stderr,
			"  dnsmasq honors these and excludes them from dynamic allocation, so they work;")
		fmt.Fprintln(os.Stderr,
			"  they are simply not counted as available.")
	}

	if moving && len(existing) > 0 {
		fmt.Fprintf(os.Stderr,
			"the firmware will rewrite %d reservation(s) into %s, keeping host parts.\n",
			len(existing), subnet)
	}
	if modes.DryRun {
		fmt.Fprintln(os.Stderr, "dry run: nothing was changed")
		return nil
	}

	mode := services.WriteGuarded
	if modes.Force {
		mode = services.WriteForced
	}

	if !moving {
		// No address change, so no dropped session and nothing to warn about.
		if err := client.Network().Set(ctx, n, mode); err != nil {
			return err
		}
		fmt.Printf("pool set: %s-%s on %s\n", n.DHCPStart, n.DHCPStop, n.Interface)
		return nil
	}

	fmt.Fprintln(os.Stderr,
		"the router will move to the new address; this session will drop")

	if err := client.Network().Set(ctx, n, mode); err != nil {
		// A dropped connection here is the expected outcome of success, not a
		// failure, and saying so saves the operator a diagnosis.
		if errors.Is(err, context.DeadlineExceeded) || isConnectionLost(err) {
			fmt.Fprintln(os.Stderr,
				"connection lost, which is expected: the router has moved.")
			fmt.Fprintf(os.Stderr, "reconnect with -H %s\n", n.LANIP)
			return nil
		}
		return err
	}

	fmt.Printf("network set: %s on %s, pool %s-%s\n",
		n.LANIP, n.Interface, n.DHCPStart, n.DHCPStop)
	fmt.Fprintf(os.Stderr, "reconnect with -H %s\n", n.LANIP)
	return nil
}

// ReservationsInPool returns the reservations whose address falls inside n's pool.
func ReservationsInPool(n *types.Network, reservations []types.Reservation) []types.Reservation {
	var enclosed []types.Reservation
	for _, r := range reservations {
		ip := net.ParseIP(r.IP)
		if ip == nil {
			continue
		}
		if pooled, err := n.InDHCPPool(ip); err == nil && pooled {
			enclosed = append(enclosed, r)
		}
	}
	return enclosed
}

// isConnectionLost reports whether err looks like the router going away mid-call,
// which is the normal consequence of a successful re-address.
func isConnectionLost(err error) bool {
	if err == nil {
		return false
	}
	text := err.Error()
	for _, sign := range []string{
		"connection refused", "connection reset", "EOF",
		"no route to host", "network is unreachable", "Client.Timeout",
	} {
		if strings.Contains(text, sign) {
			return true
		}
	}
	return false
}
