package main

import (
	"fmt"
	"io"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/gherlein/gogl/src/types"
	"github.com/gherlein/gogl/utilities/internal/netcfg"
	"github.com/gherlein/gogl/utilities/internal/reservations"
)

func newLANCommand() *cobra.Command {
	lan := &cobra.Command{
		Use:   "lan",
		Short: "The LAN address, DHCP pool, reservations and DNS names",

		// Runnable + Args so an unknown subcommand here is a usage error (exit 2) rather
		// than cobra's silent help-with-exit-0 for a non-runnable parent.
		Args: wrapArgsError(unknownSubcommandArgs),
		RunE: showHelp,
	}
	lan.AddCommand(
		newLANShowCommand(),
		newLANSetCommand(),
		newLANLeasesCommand(),
		newReservationsCommand(),
		newDNSCommand(),
	)
	return lan
}

func newLANShowCommand() *cobra.Command {
	var showKey bool
	cmd := &cobra.Command{
		Use:   "show",
		Short: "Report the LAN address, DHCP pool, reservation counts and radios",
		Args:  wrapArgsError(cobra.NoArgs),
		RunE: func(cmd *cobra.Command, _ []string) error {
			client, err := connect()
			if err != nil {
				return err
			}
			defer client.Close()

			return explain(netcfg.Show(cmd.Context(), client, cmd.OutOrStdout(), cmd.ErrOrStderr(),
				netcfg.ShowOptions{JSON: asJSON(), ShowKey: showKey}))
		},
	}
	cmd.Flags().BoolVar(&showKey, "show-key", false, "print WiFi passphrases instead of masking them")
	return cmd
}

func newLANSetCommand() *cobra.Command {
	var (
		ip, mask, poolStart, poolEnd string
		guest, force, dryRun         bool
	)
	cmd := &cobra.Command{
		Use:   "set",
		Short: "Write the LAN address or the DHCP pool",
		Long: `Write the LAN address or the DHCP pool.

Changing only the pool is never refused and never drops the session: the router keeps
its address, so no reservation moves. The address and netmask are read from the device.

Moving the subnet requires --ip, --mask, --pool-start and --pool-end together, because a
pool from the old subnet cannot be valid in a new one. It drops the session, and it is
refused while reservations exist unless --force: the firmware silently rewrites every
reservation into the new subnet, which is usually what you want but is unannounced.`,
		Args: wrapArgsError(cobra.NoArgs),
		RunE: func(cmd *cobra.Command, _ []string) error {
			moving := ip != "" || mask != ""
			pooling := poolStart != "" || poolEnd != ""

			switch {
			case moving && (ip == "" || mask == "" || poolStart == "" || poolEnd == ""):
				return fmt.Errorf("%w: moving the LAN address requires --ip, --mask, "+
					"--pool-start and --pool-end together", errUsage)
			case !moving && pooling && (poolStart == "" || poolEnd == ""):
				return fmt.Errorf("%w: --pool-start and --pool-end are required together", errUsage)
			case !moving && !pooling:
				return fmt.Errorf("%w: nothing to set; see `gogl lan set --help`", errUsage)
			}

			iface := types.InterfaceLAN
			if guest {
				iface = types.InterfaceGuest
			}

			client, err := connect()
			if err != nil {
				return err
			}
			defer client.Close()

			return explain(netcfg.SetNetwork(cmd.Context(), client, &types.Network{
				Interface: iface,
				LANIP:     ip,
				Netmask:   mask,
				DHCPStart: poolStart,
				DHCPStop:  poolEnd,
			}, netcfg.NetworkModes{DryRun: dryRun, Force: force}))
		},
	}
	f := cmd.Flags()
	f.StringVar(&ip, "ip", "", "new LAN address, e.g. 192.168.8.1")
	f.StringVar(&mask, "mask", "", "new netmask")
	f.StringVar(&poolStart, "pool-start", "", "new DHCP pool start")
	f.StringVar(&poolEnd, "pool-end", "", "new DHCP pool end")
	f.BoolVar(&guest, "guest", false, "write the guest interface instead of the main LAN")
	f.BoolVar(&force, "force", false, "allow a subnet move while reservations exist")
	f.BoolVar(&dryRun, "dry-run", false, "show the change and any refusal without writing")
	return cmd
}

func newLANLeasesCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "leases",
		Short: "List the dynamic DHCP leases the router currently holds",
		Long: `List the dynamic DHCP leases the router currently holds.

Leases are not reservations: a lease expires. This is how you discover what is worth
reserving.`,
		Args: wrapArgsError(cobra.NoArgs),
		RunE: func(cmd *cobra.Command, _ []string) error {
			client, err := connect()
			if err != nil {
				return err
			}
			defer client.Close()

			leases, err := client.Network().Leases(cmd.Context())
			if err != nil {
				return explain(err)
			}
			out := cmd.OutOrStdout()
			if asJSON() {
				return writeJSON(out, leases)
			}
			return formatLeases(out, leases)
		},
	}
}

func formatLeases(w io.Writer, leases []types.DHCPLease) error {
	if len(leases) == 0 {
		_, err := fmt.Fprintln(w, "no active leases")
		return err
	}

	tw := tabwriter.NewWriter(w, 0, 8, 2, ' ', 0)
	fmt.Fprintln(tw, "IP\tMAC\tHOSTNAME\tEXPIRES")
	for _, l := range leases {
		// Expires is seconds remaining, not a timestamp. Rendered as a duration
		// because that is what the firmware reports; converting to an absolute time
		// would invent precision the reading does not have.
		expires := "-"
		if l.Expires > 0 {
			expires = (time.Duration(l.Expires) * time.Second).String()
		}
		name := l.Hostname
		if name == "" {
			name = "-"
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", l.IP, l.MAC, name, expires)
	}
	return tw.Flush()
}

func newReservationsCommand() *cobra.Command {
	res := &cobra.Command{
		Use:     "reservations",
		Aliases: []string{"res"},
		Short:   "Static MAC-to-IP bindings, in ISC DHCP host-declaration format",
		Long: `Static MAC-to-IP bindings.

Each host declaration is two writes: a static bind for the address and a host-file entry
for the name, because the firmware stores them separately and joins them for nobody.
These commands keep the two in step.`,

		// Runnable + Args so an unknown subcommand here is a usage error (exit 2) rather
		// than cobra's silent help-with-exit-0 for a non-runnable parent.
		Args: wrapArgsError(unknownSubcommandArgs),
		RunE: showHelp,
	}
	res.AddCommand(
		newReservationsListCommand(),
		newReservationsExportCommand(),
		newReservationsImportCommand(),
		newReservationsAddCommand(),
		newReservationsRemoveCommand(),
		newReservationsClearCommand(),
	)
	return res
}

func newReservationsListCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List the reservations on the router",
		Args:  wrapArgsError(cobra.NoArgs),
		RunE: func(cmd *cobra.Command, _ []string) error {
			client, err := connect()
			if err != nil {
				return err
			}
			defer client.Close()

			if asJSON() {
				list, err := client.Reservations().List(cmd.Context())
				if err != nil {
					return explain(err)
				}
				return writeJSON(cmd.OutOrStdout(), list)
			}
			return explain(reservations.Get(cmd.Context(), cmd.OutOrStdout(),
				client.Reservations(), client.Network(), time.Now().Format("2006-01-02")))
		},
	}
}

func newReservationsExportCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "export",
		Short: "Write every reservation to stdout in ISC DHCP format",
		Long: `Write every reservation to stdout in ISC DHCP host-declaration format.

The format is kept on its own merits, not for compatibility: it diffs, it reviews, it
lives in git, and it is how a UniFi dump produced by gofips gets in.`,
		Args: wrapArgsError(cobra.NoArgs),
		RunE: func(cmd *cobra.Command, _ []string) error {
			client, err := connect()
			if err != nil {
				return err
			}
			defer client.Close()

			return explain(reservations.Get(cmd.Context(), cmd.OutOrStdout(),
				client.Reservations(), client.Network(), time.Now().Format("2006-01-02")))
		},
	}
}

func newReservationsImportCommand() *cobra.Command {
	var prune, dryRun, force bool
	cmd := &cobra.Command{
		Use:   "import [file]",
		Short: "Import host declarations from a file, or stdin",
		Long: `Import host declarations from a file, or from stdin when no file is given.

Idempotent: a second run reports everything skipped and leaves the host file
byte-identical. Requires a configured domain, since a reservation with no name is an
address nothing can find.`,
		Args: wrapArgsError(cobra.MaximumNArgs(1)),
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := connect()
			if err != nil {
				return err
			}
			defer client.Close()

			var path string
			if len(args) == 1 && args[0] != "-" {
				path = args[0]
			}
			return explain(reservations.Set(cmd.Context(), client, path, reservations.Modes{
				Prune: prune, DryRun: dryRun, Force: force,
			}))
		},
	}
	f := cmd.Flags()
	f.BoolVar(&prune, "prune", false,
		"delete reservations and names on the router but absent from the file")
	f.BoolVar(&dryRun, "dry-run", false, "show what would change without changing it")
	f.BoolVar(&force, "force", false, "proceed past conflicts")
	return cmd
}

func newReservationsAddCommand() *cobra.Command {
	var name, mac, ip string
	var force, dryRun bool
	cmd := &cobra.Command{
		Use:   "add [declaration]",
		Short: "Add one host, by flags or as an ISC DHCP declaration fragment",
		Example: `  gogl lan reservations add --name nas --mac aa:bb:cc:dd:ee:01 --ip 192.168.8.13
  gogl lan reservations add 'host nas { hardware ethernet aa:bb:cc:dd:ee:01; fixed-address 192.168.8.13; }'`,
		Args: wrapArgsError(cobra.MaximumNArgs(1)),
		RunE: func(cmd *cobra.Command, args []string) error {
			fragment, err := addFragment(args, name, mac, ip)
			if err != nil {
				return err
			}

			client, err := connect()
			if err != nil {
				return err
			}
			defer client.Close()

			return explain(reservations.Add(cmd.Context(), client, fragment,
				reservations.Modes{Force: force, DryRun: dryRun}))
		},
	}
	f := cmd.Flags()
	f.StringVar(&name, "name", "", "hostname")
	f.StringVar(&mac, "mac", "", "MAC address")
	f.StringVar(&ip, "ip", "", "IPv4 address")
	f.BoolVar(&force, "force", false, "proceed past conflicts")
	f.BoolVar(&dryRun, "dry-run", false, "show what would change without changing it")
	return cmd
}

// addFragment turns either form of `add` into the declaration the reservations package
// parses, so that both paths share one parser and one set of validation rules.
func addFragment(args []string, name, mac, ip string) (string, error) {
	byFlags := name != "" || mac != "" || ip != ""

	switch {
	case len(args) == 1 && byFlags:
		return "", fmt.Errorf("%w: give either a declaration or --name/--mac/--ip, not both", errUsage)
	case len(args) == 1:
		return args[0], nil
	case byFlags:
		if name == "" || mac == "" || ip == "" {
			return "", fmt.Errorf("%w: --name, --mac and --ip are required together", errUsage)
		}
		return fmt.Sprintf("host %s { hardware ethernet %s; fixed-address %s; }", name, mac, ip), nil
	default:
		// No arguments at all means stdin, which is how a fragment gets piped in.
		return "", nil
	}
}

func newReservationsRemoveCommand() *cobra.Command {
	var name, mac, ip string
	var force, dryRun bool
	cmd := &cobra.Command{
		Use:     "rm",
		Aliases: []string{"remove"},
		Short:   "Remove one host, identified by --name, --mac or --ip",
		Long: `Remove one host and its DNS name.

The name goes first, then the binding: a leftover binding is an address with no name,
which the next import repairs, while a leftover name keeps resolving to an address
nothing holds.`,
		Args: wrapArgsError(cobra.NoArgs),
		RunE: func(cmd *cobra.Command, _ []string) error {
			client, err := connect()
			if err != nil {
				return err
			}
			defer client.Close()

			return explain(reservations.Del(cmd.Context(), client, reservations.Modes{
				Name: name, MAC: mac, IP: ip, Force: force, DryRun: dryRun,
			}))
		},
	}
	f := cmd.Flags()
	f.StringVar(&name, "name", "", "hostname to remove")
	f.StringVar(&mac, "mac", "", "MAC address to remove")
	f.StringVar(&ip, "ip", "", "IPv4 address to remove")
	f.BoolVar(&force, "force", false, "skip the confirmation prompt")
	f.BoolVar(&dryRun, "dry-run", false, "show what would change without changing it")
	return cmd
}

func newReservationsClearCommand() *cobra.Command {
	var force, dryRun bool
	cmd := &cobra.Command{
		Use:   "clear",
		Short: "Remove every reservation and every managed DNS name",
		Long: `Remove every reservation and every managed DNS name.

Both, because they are one intent stored in two tables. The DNS domain survives: it is
configuration rather than content.

This is also the precondition for moving the LAN subnet without --force.`,
		Args: wrapArgsError(cobra.NoArgs),
		RunE: func(cmd *cobra.Command, _ []string) error {
			client, err := connect()
			if err != nil {
				return err
			}
			defer client.Close()

			return explain(reservations.Clear(cmd.Context(), client, reservations.Modes{
				Force: force, DryRun: dryRun,
			}))
		},
	}
	cmd.Flags().BoolVar(&force, "force", false, "skip the confirmation prompt")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "list what would go without removing it")
	return cmd
}
