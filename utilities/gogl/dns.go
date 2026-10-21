package main

import (
	"fmt"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/gherlein/gogl/utilities/internal/reservations"
)

func newDNSCommand() *cobra.Command {
	dns := &cobra.Command{
		Use:   "dns",
		Short: "The DNS domain and the names the router resolves",
		Long: `The DNS domain and the names the router resolves.

A reservation does not create a DNS record: on this firmware its name is a label. Names
live in the router's host file, which dnsmasq answers from, and that is what these
commands write.

The domain is stored inside gogl's block in that file, because the firmware exposes no
dnsmasq domain setting.`,

		// Runnable + Args so an unknown subcommand here is a usage error (exit 2) rather
		// than cobra's silent help-with-exit-0 for a non-runnable parent.
		Args: wrapArgsError(unknownSubcommandArgs),
		RunE: showHelp,
	}
	dns.AddCommand(
		newDNSShowCommand(),
		newDNSSetCommand(),
		newDNSAddCommand(),
		newDNSRemoveCommand(),
		newDNSClearCommand(),
	)
	return dns
}

func newDNSShowCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "show",
		Short: "Report the domain and every managed DNS name",
		Args:  wrapArgsError(cobra.NoArgs),
		RunE: func(cmd *cobra.Command, _ []string) error {
			client, err := connect()
			if err != nil {
				return err
			}
			defer client.Close()

			file, err := client.Hosts().Get(cmd.Context())
			if err != nil {
				return explain(err)
			}
			out := cmd.OutOrStdout()
			if asJSON() {
				return writeJSON(out, map[string]any{
					"domain":  file.Domain,
					"entries": file.Entries,
				})
			}

			if file.Domain == "" {
				fmt.Fprintln(out, "DOMAIN     (not set; reservation writes are refused until it is)")
			} else {
				fmt.Fprintf(out, "DOMAIN     %s\n", file.Domain)
			}
			if len(file.Entries) == 0 {
				fmt.Fprintln(out, "no managed DNS names")
				return nil
			}

			fmt.Fprintln(out)
			tw := tabwriter.NewWriter(out, 0, 8, 2, ' ', 0)
			fmt.Fprintln(tw, "ADDRESS\tNAMES")
			for _, e := range file.Entries {
				fmt.Fprintf(tw, "%s\t%s\n", e.IP, joinNames(e.Names))
			}
			return tw.Flush()
		},
	}
}

func joinNames(names []string) string {
	out := ""
	for i, n := range names {
		if i > 0 {
			out += "  "
		}
		out += n
	}
	return out
}

func newDNSSetCommand() *cobra.Command {
	var domain string
	cmd := &cobra.Command{
		Use:   "set",
		Short: "Set the DNS domain",
		Long: `Set the DNS domain.

Required before any reservation write: a reservation with no name is an address nothing
can find, and nothing in the router's UI marks it as incomplete.

Changing an existing domain requalifies every managed name, so resolution does not split
between two suffixes.`,
		Args: wrapArgsError(cobra.NoArgs),
		RunE: func(cmd *cobra.Command, _ []string) error {
			if domain == "" {
				return fmt.Errorf("%w: --domain is required", errUsage)
			}
			client, err := connect()
			if err != nil {
				return err
			}
			defer client.Close()

			return explain(reservations.SetDomain(cmd.Context(), client, domain))
		},
	}
	cmd.Flags().StringVar(&domain, "domain", "", "the DNS suffix, e.g. lab.example")
	return cmd
}

func newDNSAddCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "add <name> <ip>",
		Short: "Point a name at an address",
		Long: `Point a name at an address.

The entry carries both the bare name and its fully-qualified form, so either resolves. A
name already in use is replaced: two answers for one name is not a state worth keeping.`,
		Args: wrapArgsError(cobra.ExactArgs(2)),
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := connect()
			if err != nil {
				return err
			}
			defer client.Close()

			if err := client.Hosts().Set(cmd.Context(), args[0], args[1]); err != nil {
				return explain(err)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "DNS name set: %s -> %s\n", args[0], args[1])
			return nil
		},
	}
}

func newDNSRemoveCommand() *cobra.Command {
	return &cobra.Command{
		Use:     "rm <name>",
		Aliases: []string{"remove"},
		Short:   "Remove a DNS name, in either its bare or qualified form",
		Args:    wrapArgsError(cobra.ExactArgs(1)),
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := connect()
			if err != nil {
				return err
			}
			defer client.Close()

			if err := client.Hosts().Remove(cmd.Context(), args[0]); err != nil {
				return explain(err)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "DNS name removed: %s\n", args[0])
			return nil
		},
	}
}

func newDNSClearCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "clear",
		Short: "Remove every managed DNS name, keeping the domain",
		Long: `Remove every managed DNS name.

The domain survives, and so does everything outside gogl's block: the file also carries
the loopback and IPv6 entries the router resolves its own name from.

This leaves reservations in place, so goglps reports the result as drift. To clear both,
use ` + "`gogl lan reservations clear`" + `.`,
		Args: wrapArgsError(cobra.NoArgs),
		RunE: func(cmd *cobra.Command, _ []string) error {
			client, err := connect()
			if err != nil {
				return err
			}
			defer client.Close()

			if err := client.Hosts().Clear(cmd.Context()); err != nil {
				return explain(err)
			}
			fmt.Fprintln(cmd.OutOrStdout(), "every managed DNS name removed; the domain is unchanged")
			return nil
		},
	}
}
