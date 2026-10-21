package main

import (
	"fmt"
	"os"
	"time"

	"github.com/spf13/cobra"

	"github.com/gherlein/gogl/utilities/internal/profile"
)

func newProfileCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "profile",
		Short: "Capture a whole network as JSON, and apply one back",
		Long: `Capture a whole network as JSON, and apply one back.

A profile is not a router image. It carries what defines a network -- the LAN and pool,
reservations, DNS names and domain, wireless identity and radio tuning -- and omits
everything identifying a particular unit: the router's own MAC, serial, uptime and lease
state. That omission is what makes it usable on a second router.

For a byte-exact restore of one device, sysupgrade -b over SSH is the right tool and
always will be.`,

		// Runnable + Args so an unknown subcommand here is a usage error (exit 2) rather
		// than cobra's silent help-with-exit-0 for a non-runnable parent.
		Args: wrapArgsError(unknownSubcommandArgs),
		RunE: showHelp,
	}
	cmd.AddCommand(newProfileExportCommand(), newProfileImportCommand())
	return cmd
}

func newProfileExportCommand() *cobra.Command {
	var withKeys bool
	cmd := &cobra.Command{
		Use:   "export",
		Short: "Write a profile to stdout",
		Args:  wrapArgsError(cobra.NoArgs),
		RunE: func(cmd *cobra.Command, _ []string) error {
			client, err := connect()
			if err != nil {
				return err
			}
			defer client.Close()

			p, err := profile.Capture(cmd.Context(), client, profile.CaptureOptions{
				WithKeys: withKeys,
				Host:     opts.flags.Host,
				Captured: time.Now().Format(time.RFC3339),
			}, cmd.ErrOrStderr())
			if err != nil {
				return explain(err)
			}
			if !withKeys && len(p.Wireless) > 0 {
				fmt.Fprintln(cmd.ErrOrStderr(),
					"note: WiFi passphrases omitted. Applying this profile leaves the target's\n"+
						"      existing passphrases alone. Use --with-keys to include them.")
			}
			return p.Write(cmd.OutOrStdout())
		},
	}
	cmd.Flags().BoolVar(&withKeys, "with-keys", false,
		"include WiFi passphrases in cleartext")
	return cmd
}

func newProfileImportCommand() *cobra.Command {
	var wireless, dryRun, force bool
	cmd := &cobra.Command{
		Use:   "import [file]",
		Short: "Apply a profile from a file, or stdin",
		Long: `Apply a profile from a file, or from stdin when no file is given.

Applied in a fixed order, each step where it is because doing it later fails: the domain
first, since reservation writes are refused without one; the network second, since
reservations must be inside the subnet; then reservations and names; then wireless.

If the profile's subnet differs from the router's, the run stops after the network step
and prints how to resume. The router changes address mid-write, so nothing after that
point is reachable from the same session.`,
		Args: wrapArgsError(cobra.MaximumNArgs(1)),
		RunE: func(cmd *cobra.Command, args []string) error {
			input := cmd.InOrStdin()
			if len(args) == 1 && args[0] != "-" {
				f, err := os.Open(args[0])
				if err != nil {
					return fmt.Errorf("open %s: %w", args[0], err)
				}
				defer f.Close()
				input = f
			}

			p, err := profile.ReadProfile(input)
			if err != nil {
				return err
			}

			client, err := connect()
			if err != nil {
				return err
			}
			defer client.Close()

			return explain(profile.Apply(cmd.Context(), client, p, profile.ApplyModes{
				DryRun: dryRun, Force: force, Wireless: wireless,
			}, cmd.ErrOrStderr()))
		},
	}
	f := cmd.Flags()
	f.BoolVar(&wireless, "wireless", false,
		"apply the wireless sections too; needs a wired session")
	f.BoolVar(&dryRun, "dry-run", false, "show what would change without changing it")
	f.BoolVar(&force, "force", false, "allow a subnet move while reservations exist")
	return cmd
}
