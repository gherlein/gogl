package main

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/gherlein/gogl/utilities/internal/clients"
	"github.com/gherlein/gogl/utilities/internal/conn"
)

func newClientsCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "clients",
		Short: "Stations connected to the router, with manufacturer lookup",
		Long: `Stations connected to the router.

Its own area rather than part of lan, because a station arrives over cable, 2.4GHz or
5GHz and the useful view is all of them together. Manufacturer lookup is done from the
IEEE OUI registry independently of the router.`,

		// Runnable + Args so an unknown subcommand here is a usage error (exit 2) rather
		// than cobra's silent help-with-exit-0 for a non-runnable parent.
		Args: wrapArgsError(unknownSubcommandArgs),
		RunE: showHelp,
	}
	cmd.AddCommand(newClientsListCommand(), newClientsVendorCommand())
	return cmd
}

func newClientsListCommand() *cobra.Command {
	var wifi, wired, reserved, all bool
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List connected stations",
		Long: `List connected stations.

Only clients the router currently sees, by default. The client list carries history: a
router renumbered from 192.168.2.0/24 to 192.168.8.0/24 was still listing a station at
192.168.2.138, and presenting that beside live clients with no distinction is worse than
not showing it. Pass --all to include them, which adds an ONLINE column.

SINCE reports how long a client has been connected, where the firmware's value can be
understood -- its format is undocumented and uncaptured, so it is rendered when it makes
sense and left blank when it does not.`,
		Args: wrapArgsError(cobra.NoArgs),
		RunE: func(cmd *cobra.Command, _ []string) error {
			keep, err := clients.FilterFor(wifi, wired)
			if err != nil {
				return fmt.Errorf("%w: %s", errUsage, err)
			}

			client, err := connect()
			if err != nil {
				return err
			}
			defer client.Close()

			return explain(clients.List(cmd.Context(), client, cmd.OutOrStdout(), clients.Options{
				Keep:           keep,
				ShowReserved:   reserved,
				IncludeOffline: all,
				JSON:           asJSON(),
			}))
		},
	}
	f := cmd.Flags()
	f.BoolVarP(&wifi, "wifi", "w", false, "only wireless stations")
	f.BoolVarP(&wired, "wired", "e", false, "only wired stations")
	f.BoolVarP(&reserved, "reserved", "r", false, "mark which stations hold a reservation")
	f.BoolVarP(&all, "all", "a", false,
		"include clients the router remembers but does not currently see")
	return cmd
}

func newClientsVendorCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "vendor <mac>",
		Short: "Look up a MAC address's manufacturer, without contacting the router",
		Long: `Look up a MAC address's manufacturer.

Entirely offline: it reads the cached IEEE OUI registry and never opens a session. Useful
for identifying a device from a MAC someone sent you.`,
		Args: wrapArgsError(cobra.ExactArgs(1)),
		RunE: func(cmd *cobra.Command, args []string) error {
			cacheDir, err := conn.OUICacheDir()
			if err != nil {
				return err
			}
			db, err := clients.LoadOUI(cacheDir, nil)
			if err != nil {
				return err
			}

			out := cmd.OutOrStdout()
			vendor := db.Lookup(args[0])
			if vendor == "" {
				// A locally-administered or randomized address has no registry entry,
				// and saying "unknown" would imply a failed lookup rather than an
				// address that by design identifies nobody.
				fmt.Fprintf(out, "%s  no registered manufacturer (locally administered or randomized)\n",
					strings.ToLower(args[0]))
				return nil
			}
			if asJSON() {
				return writeJSON(out, map[string]string{
					"mac": strings.ToLower(args[0]), "vendor": vendor,
				})
			}
			fmt.Fprintf(out, "%s  %s\n", strings.ToLower(args[0]), vendor)
			return nil
		},
	}
}
