package reservations

import (
	"context"
	"fmt"
	"io"
	"net"
	"strings"

	"github.com/gherlein/gogl/src/types"
)

// The narrow interfaces below let every operation be tested with stubs rather
// than a live client.
type reservationLister interface {
	List(context.Context) ([]types.Reservation, error)
}

type networkGetter interface {
	Get(context.Context) (*types.Network, error)
}

// Plan is the diff between a host file and the device, partitioned by action.
//
// Create, Update and Skip partition the file's declarations. Prune counts
// reservations that exist only on the device, so summing all four is meaningless:
// they count different things.
type Plan struct {
	Create []types.Reservation
	Update []types.Reservation
	Skip   []types.Reservation
	Prune  []types.Reservation

	// NameSet and NameRemove are the DNS side of the same intent.
	//
	// They are separate from the reservation actions because the two live in
	// unrelated tables on the device and can disagree: a bind can exist with no
	// name, or a name with no bind. Either is drift, and reporting it needs the
	// two lists kept apart rather than folded into one row per MAC.
	NameSet    []types.Reservation
	NameRemove []string
}

// Empty reports whether the plan would change nothing at all.
func (p Plan) Empty() bool {
	return len(p.Create) == 0 && len(p.Update) == 0 &&
		len(p.NameSet) == 0 && len(p.NameRemove) == 0 && len(p.Prune) == 0
}

// Get exports the device's reservations in ISC DHCP format.
func Get(ctx context.Context, w io.Writer, res reservationLister, nets networkGetter, date string) error {
	reservations, err := res.List(ctx)
	if err != nil {
		return err
	}

	header := Header{Date: date}
	// The network is a convenience in the header; a router that will not report it
	// is still worth exporting reservations from.
	if network, err := nets.Get(ctx); err == nil {
		header.Network = network
		header.Host = network.LANIP
	}

	return FormatHosts(w, reservations, header)
}

// planNames diffs the wanted names against the router's host file.
//
// This is a second, independent diff because a static bind creates no DNS record
// on this firmware: the name on a bind is a label. The record lives in the host
// file, so a host declaration is two writes to two tables, and the tables can
// disagree. planNames is what notices.
//
// Names are keyed by name rather than by MAC, since the host file has no MAC
// column -- it joins to a reservation by address and nothing else.
func planNames(file []types.Reservation, hosts *types.HostFile, prune []types.Reservation) (set []types.Reservation, remove []string) {
	for i := range file {
		want := file[i]
		if want.Name == "" {
			continue
		}
		if have, ok := hosts.Lookup(want.Name); !ok || have != want.IP {
			set = append(set, want)
		}
	}

	// A pruned reservation takes its name with it, or the name is left resolving
	// to an address the router no longer reserves.
	for _, gone := range prune {
		if gone.Name == "" {
			continue
		}
		if _, ok := hosts.Lookup(gone.Name); ok {
			remove = append(remove, gone.Name)
		}
	}
	return set, remove
}

// planChanges diffs a host file against the device, keyed by MAC.
//
// MAC is the identity because it is the only thing a client cannot change about
// itself, and it is what dnsmasq keys the lease on.
func planChanges(file, device []types.Reservation) Plan {
	byMAC := make(map[string]types.Reservation, len(device))
	for _, r := range device {
		byMAC[strings.ToLower(r.MAC)] = r
	}

	inFile := make(map[string]bool, len(file))
	var plan Plan

	for _, want := range file {
		key := strings.ToLower(want.MAC)
		inFile[key] = true

		existing, present := byMAC[key]
		switch {
		case !present:
			plan.Create = append(plan.Create, want)
		case existing.IP == want.IP && existing.Name == want.Name:
			plan.Skip = append(plan.Skip, want)
		default:
			plan.Update = append(plan.Update, want)
		}
	}

	for _, existing := range device {
		if !inFile[strings.ToLower(existing.MAC)] {
			plan.Prune = append(plan.Prune, existing)
		}
	}

	return plan
}

// validateAgainstDevice checks a host file against the router's actual LAN,
// returning warnings that do not block and errors that do.
func validateAgainstDevice(file []types.Reservation, network *types.Network) (warnings []string, errs []error) {
	subnet, err := network.Subnet()
	if err != nil {
		return nil, []error{fmt.Errorf("router LAN configuration is unusable: %w", err)}
	}
	routerIP := net.ParseIP(network.LANIP)

	var outside []types.Reservation
	for _, r := range file {
		ip := net.ParseIP(r.IP)
		if ip == nil {
			errs = append(errs, fmt.Errorf("host %q: %q is not a valid address", r.Name, r.IP))
			continue
		}

		if !subnet.Contains(ip) {
			outside = append(outside, r)
			continue
		}
		if routerIP != nil && ip.Equal(routerIP) {
			errs = append(errs, fmt.Errorf("host %q: %s is the router's own address", r.Name, r.IP))
			continue
		}

		// A pooled address is untidy, not broken: dnsmasq honors a static lease
		// inside the dynamic range and excludes it from allocation. It would be a
		// genuine conflict under ISC dhcpd, which is where the contrary intuition
		// comes from, so rejecting it would fail valid UniFi dumps over a hazard
		// that does not exist here.
		if pooled, err := network.InDHCPPool(ip); err == nil && pooled {
			warnings = append(warnings, fmt.Sprintf(
				"host %q at %s is inside the DHCP pool (%s-%s); dnsmasq honors it, but it is tidier outside",
				r.Name, r.IP, network.DHCPStart, network.DHCPStop))
		}
	}

	if len(outside) > 0 {
		errs = append(errs, subnetMismatch(outside, len(file), subnet, network))
	}
	return warnings, errs
}

// subnetMismatch builds the report for addresses outside the router's LAN.
//
// Both remedies are named because both are the operator's choice: gogl never
// changes the router's LAN address, and never silently renumbers a file.
func subnetMismatch(outside []types.Reservation, total int, subnet *net.IPNet, network *types.Network) error {
	fileSubnet, suggested := "unknown", "unknown"
	if ip := net.ParseIP(outside[0].IP).To4(); ip != nil {
		fileSubnet = fmt.Sprintf("%d.%d.%d.0/24", ip[0], ip[1], ip[2])
		suggested = fmt.Sprintf("%d.%d.%d.1/24", ip[0], ip[1], ip[2])
	}

	ones, _ := subnet.Mask.Size()

	var b strings.Builder
	fmt.Fprint(&b, "subnet mismatch\n")
	fmt.Fprintf(&b, "  host file:   %s (%d of %d entries)\n", fileSubnet, len(outside), total)
	fmt.Fprintf(&b, "  router LAN:  %s/%d\n", network.LANIP, ones)
	fmt.Fprint(&b, "\nResolve by either:\n")
	fmt.Fprintf(&b, "  - Setting the router's LAN address to %s in the GL.iNet admin panel\n", suggested)
	fmt.Fprint(&b, "    (LAN -> Router IP Address), then re-running. Your management session will\n")
	fmt.Fprint(&b, "    drop and you will need to reconnect at the new address.\n")
	fmt.Fprintf(&b, "  - Renumbering the host file into %s before re-running.\n", subnet.String())
	return fmt.Errorf("%s", b.String())
}

// findDuplicates reports repeated names, MACs, or addresses within a file. Each is
// fatal: a file that reserves one address twice does not describe a network.
func findDuplicates(declarations []Declaration) []error {
	var errs []error
	names := make(map[string]int, len(declarations))
	macs := make(map[string]int, len(declarations))
	ips := make(map[string]int, len(declarations))

	for _, d := range declarations {
		r := d.Reservation

		if first, seen := names[r.Name]; seen {
			errs = append(errs, fmt.Errorf("line %d: hostname %q already used at line %d", d.Line, r.Name, first))
		} else {
			names[r.Name] = d.Line
		}

		key := strings.ToLower(r.MAC)
		if first, seen := macs[key]; seen {
			errs = append(errs, fmt.Errorf("line %d: MAC %s already used at line %d", d.Line, r.MAC, first))
		} else {
			macs[key] = d.Line
		}

		if first, seen := ips[r.IP]; seen {
			errs = append(errs, fmt.Errorf("line %d: address %s already used at line %d", d.Line, r.IP, first))
		} else {
			ips[r.IP] = d.Line
		}
	}
	return errs
}

// reservationsOf strips line numbers, for handing a parsed file to the planner.
func reservationsOf(declarations []Declaration) []types.Reservation {
	out := make([]types.Reservation, len(declarations))
	for i, d := range declarations {
		out[i] = d.Reservation
	}
	return out
}
