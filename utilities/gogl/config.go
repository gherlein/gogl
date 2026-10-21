package main

import (
	"fmt"
	"os"
	"path/filepath"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/gherlein/gogl/utilities/internal/config"
)

func newConfigCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "config",
		Short: "gogl's own configuration file",
		Long: `gogl's own configuration file.

This is the only area that acts on your machine rather than on a router. It holds
everything except secrets: a password never appears in the file, coming instead from the
environment, from a command the file names, or from a prompt.`,

		// Runnable + Args so an unknown subcommand here is a usage error (exit 2) rather
		// than cobra's silent help-with-exit-0 for a non-runnable parent.
		Args: wrapArgsError(unknownSubcommandArgs),
		RunE: showHelp,
	}
	cmd.AddCommand(newConfigShowCommand(), newConfigRoutersCommand(), newConfigInitCommand())
	return cmd
}

func newConfigShowCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "show",
		Short: "Report the config file's location and what it resolves to",
		Args:  wrapArgsError(cobra.NoArgs),
		RunE: func(cmd *cobra.Command, _ []string) error {
			out := cmd.OutOrStdout()
			exists := "yes"
			if _, err := os.Stat(opts.file.Path()); err != nil {
				exists = "no (flags and the environment still work)"
			}

			if asJSON() {
				return writeJSON(out, map[string]any{
					"path":    opts.file.Path(),
					"exists":  exists == "yes",
					"output":  opts.output,
					"default": opts.file.Default,
					"routers": opts.file.Names(),
					"host":    opts.flags.Host,
				})
			}

			fmt.Fprintf(out, "PATH       %s\nEXISTS     %s\nOUTPUT     %s\n",
				opts.file.Path(), exists, opts.output)
			if name := wantedRouterName(); name != "" {
				fmt.Fprintf(out, "ROUTER     %s\n", name)
			}
			if opts.flags.Host != "" {
				fmt.Fprintf(out, "HOST       %s:%d\n", opts.flags.Host, opts.flags.Port)
			} else {
				fmt.Fprintf(out, "HOST       (none: pass -H, set %s, or configure a router)\n",
					config.EnvHost)
			}
			return nil
		},
	}
}

func newConfigRoutersCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "routers",
		Short: "List the configured routers",
		Args:  wrapArgsError(cobra.NoArgs),
		RunE: func(cmd *cobra.Command, _ []string) error {
			out := cmd.OutOrStdout()
			names := opts.file.Names()
			if asJSON() {
				return writeJSON(out, names)
			}
			if len(names) == 0 {
				fmt.Fprintf(out, "no routers configured in %s\n", opts.file.Path())
				fmt.Fprintln(out, "run `gogl config init` to write a starting point")
				return nil
			}

			tw := tabwriter.NewWriter(out, 0, 8, 2, ' ', 0)
			fmt.Fprintln(tw, "NAME\tHOST\tDOMAIN\tPASSWORD")
			for _, name := range names {
				r := opts.file.Routers[name]
				marker := name
				if name == opts.file.Default {
					marker += " (default)"
				}
				secret := "environment or prompt"
				if r.PasswordCommand != "" {
					secret = "command"
				}
				domain := r.Domain
				if domain == "" {
					domain = "-"
				}
				fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", marker, r.Host, domain, secret)
			}
			return tw.Flush()
		},
	}
}

func newConfigInitCommand() *cobra.Command {
	var force bool
	cmd := &cobra.Command{
		Use:   "init",
		Short: "Write a starting configuration file",
		Args:  wrapArgsError(cobra.NoArgs),
		RunE: func(cmd *cobra.Command, _ []string) error {
			path := config.Path()
			if _, err := os.Stat(path); err == nil && !force {
				return fmt.Errorf("%w: %s already exists; pass --force to overwrite", errUsage, path)
			}
			if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
				return err
			}

			// 0600: the file holds no secrets by design, but it names the commands that
			// produce them, and those are worth keeping to the owner.
			if err := os.WriteFile(path, []byte(starterConfig), 0o600); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "wrote %s\n", path)
			fmt.Fprintln(cmd.OutOrStdout(), "edit it, then run `gogl config show`")
			return nil
		},
	}
	cmd.Flags().BoolVar(&force, "force", false, "overwrite an existing file")
	return cmd
}

const starterConfig = `# gogl configuration. See ` + "`gogl config show`" + ` for where this was read from.
#
# Secrets never belong here. A password comes from GL_PASSWORD, from the command named
# by password_command, or from a prompt with echo off -- in that order.

# Which router to use when --router is not given. Optional with only one defined.
default = "home"

# Output format for every command: "text" or "json". Overridden by --output.
output = "text"

[routers.home]
host = "192.168.8.1"

# The DNS suffix this router should serve. Applied by ` + "`gogl lan dns set`" + `,
# never implicitly.
# domain = "lab.example"

# A command printing the password on its first line of output. Anything works:
# pass, gpg, a keyring helper, or ` + "`echo`" + ` if you do not care.
# password_command = "pass show routers/home"

# [routers.travel]
# host = "192.168.8.1"
`
