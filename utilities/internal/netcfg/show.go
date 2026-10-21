package netcfg

import (
	"context"
	"fmt"
	"io"

	gogl "github.com/gherlein/gogl/src"
)

// ShowOptions controls what Show reports.
type ShowOptions struct {
	// JSON selects machine-readable output. Covers the wired network only: the
	// wireless section is a human-facing table, and a caller wanting wireless as data
	// should read it from the services layer.
	JSON bool

	// ShowKey prints WiFi passphrases instead of masking them.
	ShowKey bool
}

// Show reports the wired network and every radio.
//
// Wireless appears alongside the wired network rather than behind a flag, because
// "what is this network" includes what devices associate to.
//
// A router that will not report wireless still gets its LAN reported: the wireless
// read is a warning rather than a failure. That matters for models where the group is
// absent, and it is why this composition is tested here rather than assembled in the
// command layer.
func Show(ctx context.Context, client *gogl.Client, out, warn io.Writer, opts ShowOptions) error {
	report, err := BuildReport(ctx, client.Network(), client.System(), client.Reservations())
	if err != nil {
		return err
	}

	if opts.JSON {
		return FormatJSON(out, report)
	}
	if err := FormatText(out, report); err != nil {
		return err
	}

	radios, err := client.Wireless().Radios(ctx)
	if err != nil {
		fmt.Fprintln(warn, "warning: could not read wireless config:", err)
		return nil
	}
	fmt.Fprintln(out)
	return FormatWireless(out, radios, opts.ShowKey)
}
