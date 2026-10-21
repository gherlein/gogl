package reservations

import (
	"fmt"
	"io"
	"net"
	"sort"
	"strings"

	"github.com/gherlein/gogl/src/ipmath"
	"github.com/gherlein/gogl/src/types"
)

const indent = "    "

// Header is the commented preamble emitted above the declarations.
type Header struct {
	Host    string
	Network *types.Network
	Date    string
}

// FormatHosts writes reservations as ISC DHCP host declarations, sorted
// numerically by address.
//
// The output follows the same rules gofips uses, so files move between the two
// tools without conversion.
func FormatHosts(w io.Writer, res []types.Reservation, header Header) error {
	if err := writeHeader(w, header); err != nil {
		return err
	}

	if len(res) == 0 {
		// Show the expected format rather than an empty file, entirely commented so
		// the file still re-imports as empty.
		_, err := fmt.Fprintf(w, `#
# No reservations are configured. The expected format is:
#
# host example {
#     hardware ethernet aa:bb:cc:dd:ee:ff;
#     fixed-address %s;
# }
`, exampleAddress(header.Network))
		return err
	}

	sorted := make([]types.Reservation, len(res))
	copy(sorted, res)
	sort.SliceStable(sorted, func(i, j int) bool {
		return ipmath.ToUint32(net.ParseIP(sorted[i].IP)) < ipmath.ToUint32(net.ParseIP(sorted[j].IP))
	})

	for _, r := range sorted {
		name := r.Name
		if name == "" {
			// The format is keyed by hostname and cannot represent its absence, so
			// derive one from the MAC and say so: re-importing this file gives the
			// entry that label for real.
			name = strings.ReplaceAll(strings.ToLower(r.MAC), ":", "-")
			if _, err := fmt.Fprint(w,
				"\n# this reservation has no label on the router; importing this file will assign the one below\n"); err != nil {
				return err
			}
		} else if _, err := fmt.Fprintln(w); err != nil {
			return err
		}

		if _, err := fmt.Fprintf(w, "host %s {\n%shardware ethernet %s;\n%sfixed-address %s;\n}\n",
			name, indent, strings.ToLower(r.MAC), indent, r.IP); err != nil {
			return err
		}
	}
	return nil
}

func writeHeader(w io.Writer, header Header) error {
	if _, err := fmt.Fprintln(w, "# goglps reservations"); err != nil {
		return err
	}
	if header.Host != "" {
		if _, err := fmt.Fprintf(w, "# exported from GL.iNet router at %s\n", header.Host); err != nil {
			return err
		}
	}

	// Recording the subnet and pool makes a file's intended network evident from
	// the file itself, which matters when several are checked into one repo.
	if n := header.Network; n != nil {
		subnet := ""
		if s, err := n.Subnet(); err == nil {
			subnet = s.String()
		}
		pool := "(disabled)"
		if n.DHCPEnabled {
			pool = fmt.Sprintf("%s-%s", n.DHCPStart, n.DHCPStop)
		}
		if _, err := fmt.Fprintf(w, "# lan: %s  pool: %s  lease: %s\n",
			subnet, pool, n.DHCPLease.String()); err != nil {
			return err
		}
	}

	if header.Date != "" {
		if _, err := fmt.Fprintf(w, "# date: %s\n", header.Date); err != nil {
			return err
		}
	}
	return nil
}

// exampleAddress picks a plausible address inside the router's subnet for the
// commented example, so the placeholder is not misleading.
func exampleAddress(n *types.Network) string {
	const fallback = "192.168.8.10"
	if n == nil {
		return fallback
	}
	subnet, err := n.Subnet()
	if err != nil {
		return fallback
	}
	base := subnet.IP.To4()
	if base == nil {
		return fallback
	}
	return fmt.Sprintf("%d.%d.%d.10", base[0], base[1], base[2])
}
