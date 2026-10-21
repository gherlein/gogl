package reservations

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	gogl "github.com/gherlein/gogl/src"
	"github.com/gherlein/gogl/src/types"
	"github.com/gherlein/gogl/utilities/internal/conn"
)

// parseFragment reads exactly one host declaration.
func parseFragment(r io.Reader) (*types.Reservation, error) {
	declarations, errs := ParseHosts(r)
	if len(errs) > 0 {
		return nil, errs[0]
	}
	switch len(declarations) {
	case 0:
		return nil, errors.New("no host declaration found")
	case 1:
		return &declarations[0].Reservation, nil
	default:
		return nil, fmt.Errorf("expected one host declaration, found %d", len(declarations))
	}
}

// checkAddConflicts reports the first collision between res and the device.
func checkAddConflicts(res *types.Reservation, device []types.Reservation) error {
	for _, existing := range device {
		sameMAC := strings.EqualFold(existing.MAC, res.MAC)

		if sameMAC && existing.IP != res.IP {
			return fmt.Errorf("MAC %s is already reserved for %s", res.MAC, existing.IP)
		}
		if existing.IP == res.IP && !sameMAC {
			return fmt.Errorf("address %s is already reserved for %s", res.IP, existing.MAC)
		}
		if existing.Name == res.Name && !sameMAC {
			return fmt.Errorf("hostname %q is already used by %s", res.Name, existing.MAC)
		}
	}
	return nil
}

func Add(ctx context.Context, client *gogl.Client, fragment string, modes Modes) error {
	input := io.Reader(os.Stdin)
	if fragment != "" {
		input = strings.NewReader(fragment)
	}

	res, err := parseFragment(input)
	if err != nil {
		return err
	}

	network, err := client.Network().Get(ctx)
	if err != nil {
		return err
	}
	warnings, errs := validateAgainstDevice([]types.Reservation{*res}, network)
	for _, w := range warnings {
		fmt.Fprintln(os.Stderr, "warning:", w)
	}
	if len(errs) > 0 {
		return errs[0]
	}

	device, err := client.Reservations().List(ctx)
	if err != nil {
		return err
	}

	if !modes.Force {
		if err := checkAddConflicts(res, device); err != nil {
			return fmt.Errorf("%w (use --force to override)", err)
		}
	}

	// The name is a second write to a second table, so the domain has to exist
	// before either happens. Checking here rather than letting Create refuse keeps
	// the remedy in the message.
	hosts, err := client.Hosts().Get(ctx)
	if err != nil {
		return err
	}
	if hosts.Domain == "" {
		return fmt.Errorf("%w\n  set it first:  goglps --domain <domain>", types.ErrDomainNotSet)
	}

	if modes.DryRun {
		fmt.Printf("Would add: %s %s %s\n", res.Name, res.MAC, res.IP)
		fmt.Printf("Would add DNS name: %s -> %s\n", hosts.FQDN(res.Name), res.IP)
		return nil
	}

	// An existing MAC is updated in place rather than failing, so --add never
	// requires a delete first. Note that moving a MAC to a different address is
	// still a conflict above unless --force was given: reaching here with a
	// changed address means the operator asked for it explicitly.
	existing := false
	for _, r := range device {
		if strings.EqualFold(r.MAC, res.MAC) {
			existing = true
			break
		}
	}

	verb := "Created"
	if existing {
		if _, err := client.Reservations().Update(ctx, res); err != nil {
			return err
		}
		verb = "Updated"
	} else if _, err := client.Reservations().Create(ctx, res); err != nil {
		return err
	}

	// The bind is written; the name is what makes it findable. A failure here
	// leaves an address with no name, which goglps reports as drift on the next
	// run, so it is worth saying which half succeeded.
	if err := hosts.Set(res.Name, res.IP); err != nil {
		return fmt.Errorf("%s the reservation, but the DNS name is invalid: %w", strings.ToLower(verb), err)
	}
	if err := client.Hosts().Put(ctx, hosts); err != nil {
		return fmt.Errorf("%s the reservation, but writing the DNS name failed: %w", strings.ToLower(verb), err)
	}

	fmt.Printf("%s: %s %s %s\n  DNS name %s -> %s\n",
		verb, res.Name, res.MAC, res.IP, hosts.FQDN(res.Name), res.IP)
	return nil
}

// findTarget resolves exactly one identifier to the matching reservations.
func findTarget(device []types.Reservation, modes Modes) ([]types.Reservation, error) {
	identifiers := 0
	for _, id := range []string{modes.Name, modes.MAC, modes.IP} {
		if id != "" {
			identifiers++
		}
	}
	if identifiers != 1 {
		return nil, errors.New("exactly one of --name, --mac, or --ip is required")
	}

	var matches []types.Reservation
	switch {
	case modes.Name != "":
		for _, r := range device {
			if r.Name == modes.Name {
				matches = append(matches, r)
			}
		}
	case modes.MAC != "":
		normalized, err := types.NormalizeMAC(modes.MAC)
		if err != nil {
			return nil, err
		}
		for _, r := range device {
			if strings.EqualFold(r.MAC, normalized) {
				matches = append(matches, r)
			}
		}
	case modes.IP != "":
		for _, r := range device {
			if r.IP == modes.IP {
				matches = append(matches, r)
			}
		}
	}

	if len(matches) == 0 {
		return nil, fmt.Errorf("no reservation matches %s", describeTarget(modes))
	}
	// Refuse an ambiguous delete: removing the wrong reservation is not recoverable
	// from inside this tool.
	if len(matches) > 1 && !modes.Force {
		var b strings.Builder
		fmt.Fprintf(&b, "%d reservations match %s:\n", len(matches), describeTarget(modes))
		for _, r := range matches {
			fmt.Fprintf(&b, "  %s %s %s\n", r.Name, r.MAC, r.IP)
		}
		b.WriteString("pass --force to delete all of them")
		return nil, errors.New(b.String())
	}

	return matches, nil
}

func describeTarget(modes Modes) string {
	switch {
	case modes.Name != "":
		return fmt.Sprintf("hostname %q", modes.Name)
	case modes.MAC != "":
		return fmt.Sprintf("MAC %s", modes.MAC)
	default:
		return fmt.Sprintf("address %s", modes.IP)
	}
}

func Del(ctx context.Context, client *gogl.Client, modes Modes) error {
	device, err := client.Reservations().List(ctx)
	if err != nil {
		return err
	}

	targets, err := findTarget(device, modes)
	if err != nil {
		return err
	}

	hosts, err := client.Hosts().Get(ctx)
	if err != nil {
		return err
	}

	for _, r := range targets {
		fmt.Fprintf(os.Stderr, "Will delete: %s %s %s\n", r.Name, r.MAC, r.IP)
		if _, ok := hosts.Lookup(r.Name); ok {
			fmt.Fprintf(os.Stderr, "Will delete DNS name: %s\n", hosts.FQDN(r.Name))
		}
	}
	if modes.DryRun {
		return nil
	}

	// Prompt only when a human is watching. In a pipeline, proceed: the caller has
	// already committed by invoking the tool non-interactively.
	if conn.IsTerminal(os.Stdout) && !modes.Force {
		if err := confirm(os.Stdin, os.Stderr); err != nil {
			return err
		}
	}

	// Names first, in one file write, then the binds. If the second step fails, a
	// leftover bind is an address with no name; the reverse would be a name still
	// resolving to an address nothing holds, which is the worse of the two because
	// it keeps answering.
	names := 0
	for _, r := range targets {
		if hosts.Remove(r.Name) {
			names++
		}
	}
	if names > 0 {
		if err := client.Hosts().Put(ctx, hosts); err != nil {
			return fmt.Errorf("removing DNS name(s): %w", err)
		}
	}

	failures := 0
	for _, r := range targets {
		if err := client.Reservations().Delete(ctx, r.MAC); err != nil {
			fmt.Fprintf(os.Stderr, "error: delete %s: %v\n", r.MAC, err)
			failures++
			continue
		}
		fmt.Printf("Deleted: %s %s %s\n  Removed the DHCP reservation and its DNS name\n",
			r.Name, r.MAC, r.IP)
	}
	if failures > 0 {
		return fmt.Errorf("%d deletion(s) failed", failures)
	}
	return nil
}

// confirm asks for a yes before a destructive operation.
//
// Delegates to conn so goglps and goglnet cannot disagree about what counts as
// consent.
func confirm(in io.Reader, out io.Writer) error {
	return conn.Confirm(in, out, "Proceed? [y/N] ")
}

// SetDomain configures the DNS domain, which every reservation write requires.
//
// The domain is stored on the router, inside gogl's block in the host file: the
// firmware exposes no dnsmasq domain setting and /ubus is not routed on this model,
// so there is nowhere else to keep it that travels with the device.
func SetDomain(ctx context.Context, client *gogl.Client, domain string) error {
	hosts := client.Hosts()

	previous, err := hosts.Domain(ctx)
	if err != nil {
		return err
	}

	if err := hosts.SetDomain(ctx, domain); err != nil {
		return err
	}

	switch {
	case previous == "":
		fmt.Printf("DNS domain set to %q\n", domain)
	case previous != domain:
		// Requalifying rewrites every managed name, which is worth saying out loud.
		fmt.Printf("DNS domain changed from %q to %q; existing host entries requalified\n",
			previous, domain)
	default:
		fmt.Printf("DNS domain already %q\n", domain)
	}
	return nil
}

// Clear deletes every reservation and every managed DNS name.
//
// Both, because they are one intent split across two tables. Clearing only the
// binds would leave the names behind, still resolving, pointing at addresses the
// router no longer reserves -- and since clearing is what unblocks a renumber,
// those names would then be answering for a subnet that no longer exists. That is
// precisely the stranded state the reservation guard exists to prevent.
//
// The domain survives: it is configuration, not content, and having to re-set it
// after every clear would be a papercut with no purpose.
//
// This is the operation that can discard a whole network's addressing, so it
// confirms with a human and prints what is about to go.
func Clear(ctx context.Context, client *gogl.Client, modes Modes) error {
	existing, err := client.Reservations().List(ctx)
	if err != nil {
		return err
	}
	names, err := client.Hosts().List(ctx)
	if err != nil {
		return err
	}
	if len(existing) == 0 && len(names) == 0 {
		fmt.Fprintln(os.Stderr, "nothing to clear")
		return nil
	}

	for _, r := range existing {
		fmt.Fprintf(os.Stderr, "Will delete reservation: %s %s\n", r.MAC, r.IP)
	}
	for _, h := range names {
		fmt.Fprintf(os.Stderr, "Will delete DNS name: %s %s\n", h.IP, strings.Join(h.Names, " "))
	}
	if modes.DryRun {
		fmt.Fprintf(os.Stderr, "%d reservation(s) and %d DNS name(s) would be deleted\n",
			len(existing), len(names))
		return nil
	}

	if conn.IsTerminal(os.Stdout) && !modes.Force {
		fmt.Fprintf(os.Stderr, "This deletes ALL %d reservations and %d DNS names. ",
			len(existing), len(names))
		if err := confirm(os.Stdin, os.Stderr); err != nil {
			return err
		}
	}

	// Names first. If the second call fails, a leftover bind is an address with no
	// name, which goglps reports as drift; the reverse is a name pointing at an
	// address nothing holds, which resolves and is wrong.
	if err := client.Hosts().Clear(ctx); err != nil {
		return err
	}
	if err := client.Reservations().DeleteAll(ctx); err != nil {
		return err
	}
	fmt.Printf("Deleted %d reservations and %d DNS names\n", len(existing), len(names))
	return nil
}
