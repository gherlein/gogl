package main

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/gherlein/gogl/src/types"
	"github.com/gherlein/gogl/utilities/internal/conn"
	"github.com/gherlein/gogl/utilities/internal/netcfg"
)

// bandTarget resolves --band and --guest into the device and interface names the
// firmware needs. --device and --iface override it for the cases resolution cannot
// settle: two radios reporting one band, or an interface gogl does not model.
type bandTarget struct {
	band   string
	guest  bool
	device string
	iface  string
}

func (t *bandTarget) register(cmd *cobra.Command, wantIface bool) {
	f := cmd.Flags()
	f.StringVar(&t.band, "band", "", "which radio: 2.4, 5 or 6")
	f.StringVar(&t.device, "device", "", "radio device name, instead of --band")
	if wantIface {
		f.BoolVar(&t.guest, "guest", false, "the guest interface on that radio")
		f.StringVar(&t.iface, "iface", "", "wireless interface name, instead of --band")
	}
}

func newRadioCommand() *cobra.Command {
	radio := &cobra.Command{
		Use:   "radio",
		Short: "Radio tuning: channel, width, hardware mode, transmit power",
		Long: `Radio tuning.

These are the fields the firmware scopes by radio rather than by SSID, so they affect
every network on that radio. Retuning drops the radio's clients, which is why it carries
the same wired-session guard as an SSID change.`,

		// Runnable + Args so an unknown subcommand here is a usage error (exit 2) rather
		// than cobra's silent help-with-exit-0 for a non-runnable parent.
		Args: wrapArgsError(unknownSubcommandArgs),
		RunE: showHelp,
	}
	radio.AddCommand(newRadioListCommand(), newRadioShowCommand(), newRadioSetCommand())
	return radio
}

func newRadioListCommand() *cobra.Command {
	var showKey bool
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List every radio with the values it accepts",
		Args:  wrapArgsError(cobra.NoArgs),
		RunE: func(cmd *cobra.Command, _ []string) error {
			client, err := connect()
			if err != nil {
				return err
			}
			defer client.Close()

			radios, err := client.Wireless().Radios(cmd.Context())
			if err != nil {
				return explain(err)
			}
			out := cmd.OutOrStdout()
			if asJSON() {
				return writeJSON(out, radios)
			}
			return netcfg.FormatWireless(out, radios, showKey)
		},
	}
	cmd.Flags().BoolVar(&showKey, "show-key", false, "print WiFi passphrases instead of masking them")
	return cmd
}

func newRadioShowCommand() *cobra.Command {
	var target bandTarget
	cmd := &cobra.Command{
		Use:   "show",
		Short: "Report one radio",
		Args:  wrapArgsError(cobra.NoArgs),
		RunE: func(cmd *cobra.Command, _ []string) error {
			client, err := connect()
			if err != nil {
				return err
			}
			defer client.Close()

			device, err := resolveDevice(cmd, client, &target)
			if err != nil {
				return err
			}
			radio, err := client.Wireless().Radio(cmd.Context(), device)
			if err != nil {
				return explain(err)
			}
			out := cmd.OutOrStdout()
			if asJSON() {
				return writeJSON(out, radio)
			}
			return netcfg.FormatWireless(out, []types.WirelessRadio{*radio}, false)
		},
	}
	target.register(cmd, false)
	return cmd
}

func newRadioSetCommand() *cobra.Command {
	var (
		target               bandTarget
		channel              int
		width, hwmode, power string
		dryRun, yes          bool
	)
	cmd := &cobra.Command{
		Use:   "set",
		Short: "Tune a radio",
		Long: `Tune a radio.

Refused over a wireless session: applying it would drop the session with no address to
reconnect at. --yes waives the prompt, never that guard.

Values are validated against what the radio advertises, so a bad channel is refused with
the available ones named rather than answered by a bare firmware error.`,
		Example: `  gogl radio set --band 5 --channel 149
  gogl radio set --band 2.4 --channel 0        # 0 means auto
  gogl radio set --device radio1 --width 80 --power Low`,
		Args: wrapArgsError(cobra.NoArgs),
		RunE: func(cmd *cobra.Command, _ []string) error {
			changes := types.RadioChanges{}
			f := cmd.Flags()
			if f.Changed("channel") {
				changes.Channel = &channel
			}
			if f.Changed("width") {
				changes.HTMode = &width
			}
			if f.Changed("hwmode") {
				changes.HWMode = &hwmode
			}
			if f.Changed("power") {
				changes.TXPower = &power
			}
			if changes.Empty() {
				return fmt.Errorf("%w: nothing to set; see `gogl radio set --help`", errUsage)
			}

			client, err := connect()
			if err != nil {
				return err
			}
			defer client.Close()

			device, err := resolveDevice(cmd, client, &target)
			if err != nil {
				return err
			}
			return explain(netcfg.SetWireless(cmd.Context(), client,
				"", types.InterfaceChanges{}, device, changes,
				netcfg.WirelessModes{DryRun: dryRun, Yes: yes}))
		},
	}
	target.register(cmd, false)
	f := cmd.Flags()
	f.IntVar(&channel, "channel", 0, "channel, or 0 for automatic")
	f.StringVar(&width, "width", "", "channel width in MHz: 20, 40, 80, or auto")
	f.StringVar(&hwmode, "hwmode", "", "hardware mode, e.g. 11a/n/ac")
	f.StringVar(&power, "power", "", "transmit power: Max, High, Medium or Low")
	f.BoolVar(&dryRun, "dry-run", false, "show the change and any refusal without writing")
	f.BoolVar(&yes, "yes", false, "skip the confirmation prompt")
	return cmd
}

func newWiFiCommand() *cobra.Command {
	wifi := &cobra.Command{
		Use:   "wifi",
		Short: "Wireless identity: SSID, passphrase, encryption, hidden, enabled",
		Long: `Wireless identity.

These are the fields the firmware scopes by interface rather than by radio, so a guest
SSID and a main SSID on the same radio are set independently.`,

		// Runnable + Args so an unknown subcommand here is a usage error (exit 2) rather
		// than cobra's silent help-with-exit-0 for a non-runnable parent.
		Args: wrapArgsError(unknownSubcommandArgs),
		RunE: showHelp,
	}
	wifi.AddCommand(newWiFiListCommand(), newWiFiShowCommand(), newWiFiSetCommand())
	return wifi
}

func newWiFiListCommand() *cobra.Command {
	var showKey bool
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List every wireless interface",
		Args:  wrapArgsError(cobra.NoArgs),
		RunE: func(cmd *cobra.Command, _ []string) error {
			client, err := connect()
			if err != nil {
				return err
			}
			defer client.Close()

			radios, err := client.Wireless().Radios(cmd.Context())
			if err != nil {
				return explain(err)
			}
			out := cmd.OutOrStdout()
			if asJSON() {
				ifaces, err := client.Wireless().Interfaces(cmd.Context())
				if err != nil {
					return explain(err)
				}
				return writeJSON(out, ifaces)
			}
			return netcfg.FormatWireless(out, radios, showKey)
		},
	}
	cmd.Flags().BoolVar(&showKey, "show-key", false, "print WiFi passphrases instead of masking them")
	return cmd
}

func newWiFiShowCommand() *cobra.Command {
	var target bandTarget
	cmd := &cobra.Command{
		Use:   "show",
		Short: "Report one wireless interface",
		Args:  wrapArgsError(cobra.NoArgs),
		RunE: func(cmd *cobra.Command, _ []string) error {
			client, err := connect()
			if err != nil {
				return err
			}
			defer client.Close()

			_, iface, err := resolveInterface(cmd, client, &target)
			if err != nil {
				return err
			}
			got, err := client.Wireless().Get(cmd.Context(), iface)
			if err != nil {
				return explain(err)
			}
			out := cmd.OutOrStdout()
			if asJSON() {
				return writeJSON(out, got)
			}
			fmt.Fprintf(out, "%s\nSSID        %s\nENCRYPTION  %s\nHIDDEN      %t\nENABLED     %t\nKEY         %s\n",
				got.Describe(), got.SSID, got.Encryption, got.Hidden, got.Enabled, got.MaskedKey())
			return nil
		},
	}
	target.register(cmd, true)
	return cmd
}

func newWiFiSetCommand() *cobra.Command {
	var (
		target            bandTarget
		ssid, encryption  string
		hidden, enabled   bool
		passphrase        bool
		passphraseCommand string
		dryRun, yes       bool
	)
	cmd := &cobra.Command{
		Use:   "set",
		Short: "Write a wireless interface's identity",
		Long: `Write a wireless interface's identity.

Only the fields named are sent: setting a passphrase leaves the SSID, encryption and
enabled state exactly as they were.

Refused over a wireless session, since applying it would drop the session with no address
to reconnect at. --yes waives the prompt, never that guard.

--passphrase takes no value. A secret on the command line is visible through ps and
recorded in shell history, which is the reason the router password has no flag either.`,
		Example: `  gogl wifi set --band 5 --ssid lab-5g
  gogl wifi set --band 5 --passphrase                    # prompts, echo off
  gogl wifi set --band 2.4 --guest --enabled=true --hidden=false`,
		Args: wrapArgsError(cobra.NoArgs),
		RunE: func(cmd *cobra.Command, _ []string) error {
			f := cmd.Flags()
			changes := types.InterfaceChanges{}
			if f.Changed("ssid") {
				changes.SSID = &ssid
			}
			if f.Changed("encryption") {
				changes.Encryption = &encryption
			}
			if f.Changed("hidden") {
				changes.Hidden = &hidden
			}
			if f.Changed("enabled") {
				changes.Enabled = &enabled
			}
			if passphrase || passphraseCommand != "" {
				secret, err := conn.ReadSecret("new WiFi passphrase: ", passphraseCommand)
				if err != nil {
					return err
				}
				changes.Key = &secret
			}
			if changes.Empty() {
				return fmt.Errorf("%w: nothing to set; see `gogl wifi set --help`", errUsage)
			}

			client, err := connect()
			if err != nil {
				return err
			}
			defer client.Close()

			_, iface, err := resolveInterface(cmd, client, &target)
			if err != nil {
				return err
			}
			return explain(netcfg.SetWireless(cmd.Context(), client,
				iface, changes, "", types.RadioChanges{},
				netcfg.WirelessModes{DryRun: dryRun, Yes: yes}))
		},
	}
	target.register(cmd, true)
	f := cmd.Flags()
	f.StringVar(&ssid, "ssid", "", "new SSID, up to 32 characters")
	f.StringVar(&encryption, "encryption", "", "encryption mode, e.g. psk2 or sae")
	f.BoolVar(&hidden, "hidden", false, "whether the SSID is hidden: true or false")
	f.BoolVar(&enabled, "enabled", true, "whether the interface is up: true or false")
	f.BoolVar(&passphrase, "passphrase", false, "prompt for a new passphrase, 8 to 63 characters")
	f.StringVar(&passphraseCommand, "passphrase-command", "",
		"read the new passphrase from this command's first line")
	f.BoolVar(&dryRun, "dry-run", false, "show the change and any refusal without writing")
	f.BoolVar(&yes, "yes", false, "skip the confirmation prompt")
	return cmd
}
