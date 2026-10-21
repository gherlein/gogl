package reservations

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"

	gogl "github.com/gherlein/gogl/src"
	"github.com/gherlein/gogl/src/types"
)

// reservationWriter is the mutating subset of ReservationService, so applyPlan can
// be tested without a client.
type reservationWriter interface {
	Create(context.Context, *types.Reservation) (*types.Reservation, error)
	Update(context.Context, *types.Reservation) (*types.Reservation, error)
	Delete(context.Context, string) error
}

// nameWriter is the one method applyNames needs, for the same reason: the whole
// file goes back in a single call.
type nameWriter interface {
	Put(context.Context, *types.HostFile) error
}

// Summary counts what a run did.
//
// File-side and device-side counts are reported separately because they count
// different things: Created, Updated and Skipped partition the file's
// declarations, while Pruned counts device-only entries.
//
// Names are counted separately again, because they are written to a different
// table and their count will legitimately differ from the reservation counts --
// re-running an import after someone edited the host file by hand writes names
// while creating no bindings at all.
type Summary struct {
	Created int
	Updated int
	Skipped int
	Pruned  int

	NamesSet     int
	NamesRemoved int

	Errors int
}

func (s Summary) HasError() bool { return s.Errors > 0 }

func (s Summary) String() string {
	return fmt.Sprintf(
		"%d host declarations: %d created, %d updated, %d skipped; %d pruned; "+
			"%d DNS name(s) written, %d removed; %d errors",
		s.Created+s.Updated+s.Skipped, s.Created, s.Updated, s.Skipped, s.Pruned,
		s.NamesSet, s.NamesRemoved, s.Errors)
}

// validateFile parses and validates a host file with no device contact, so a
// malformed file can never half-write a router.
func validateFile(r io.Reader) ([]types.Reservation, []error) {
	declarations, errs := ParseHosts(r)
	if len(errs) > 0 {
		return nil, errs
	}
	if dupes := findDuplicates(declarations); len(dupes) > 0 {
		return nil, dupes
	}
	return reservationsOf(declarations), nil
}

// Set imports host declarations.
//
// The four phases run in a deliberate order: all file validation before any device
// contact, all device reads before any write, then independent per-entry writes.
// A malformed file therefore never results in a half-written router, and the diff
// is always computed against one consistent snapshot.
func Set(ctx context.Context, client *gogl.Client, path string, modes Modes) error {
	// Phase 1: offline validation.
	input := io.Reader(os.Stdin)
	if path != "" {
		f, err := os.Open(path)
		if err != nil {
			return fmt.Errorf("open %s: %w", path, err)
		}
		defer f.Close()
		input = f
	}

	file, errs := validateFile(input)
	if len(errs) > 0 {
		for _, err := range errs {
			fmt.Fprintln(os.Stderr, "error:", err)
		}
		return fmt.Errorf("%d validation error(s); nothing was written", len(errs))
	}

	// Phase 2: read device state once, so the diff is against one snapshot.
	network, err := client.Network().Get(ctx)
	if err != nil {
		return err
	}
	device, err := client.Reservations().List(ctx)
	if err != nil {
		return err
	}
	// The host file comes back as one string, so it is read once here, mutated in
	// memory, and written once at the end. Writing per entry would be one whole-file
	// round trip per name, each a chance to clobber a concurrent edit.
	hosts, err := client.Hosts().Get(ctx)
	if err != nil {
		return err
	}
	if hosts.Domain == "" {
		return fmt.Errorf("%w\n"+
			"  a reservation without a name is an address nothing can find, and this\n"+
			"  firmware does not derive names from reservations\n"+
			"  set it first:  goglps --domain <domain>",
			types.ErrDomainNotSet)
	}

	// Phase 3: reconcile against the device.
	warnings, errs := validateAgainstDevice(file, network)
	for _, w := range warnings {
		fmt.Fprintln(os.Stderr, "warning:", w)
	}
	if len(errs) > 0 {
		for _, err := range errs {
			fmt.Fprintln(os.Stderr, "error:", err)
		}
		return errors.New("device validation failed; nothing was written")
	}

	plan := planChanges(file, device)
	plan.NameSet, plan.NameRemove = planNames(file, hosts, plan.Prune)

	if modes.DryRun {
		return printPlan(os.Stdout, plan, modes.Prune)
	}

	// Phase 4: write. Reservations per entry, names batched into one file write.
	summary, err := applyPlan(ctx, os.Stderr, client.Reservations(), plan, modes.Prune)
	if err != nil {
		return err
	}
	if err := applyNames(ctx, os.Stderr, client.Hosts(), hosts, plan, modes.Prune, &summary); err != nil {
		return err
	}
	fmt.Fprintln(os.Stderr, summary)
	if summary.HasError() {
		return fmt.Errorf("%d entr(ies) failed", summary.Errors)
	}
	return nil
}

// applyPlan performs the writes.
//
// A per-entry failure is logged and counted, and the loop continues: one bad entry
// must not abandon the rest of a bulk import.
func applyPlan(ctx context.Context, log io.Writer, w reservationWriter, plan Plan, prune bool) (Summary, error) {
	var summary Summary

	for _, r := range plan.Skip {
		fmt.Fprintf(log, "skip    %s %s %s\n", r.Name, r.MAC, r.IP)
		summary.Skipped++
	}

	for i := range plan.Create {
		r := plan.Create[i]
		if _, err := w.Create(ctx, &r); err != nil {
			fmt.Fprintf(log, "error   %s %s %s: %v\n", r.Name, r.MAC, r.IP, err)
			summary.Errors++
			continue
		}
		fmt.Fprintf(log, "create  %s %s %s\n", r.Name, r.MAC, r.IP)
		summary.Created++
	}

	for i := range plan.Update {
		r := plan.Update[i]
		if _, err := w.Update(ctx, &r); err != nil {
			fmt.Fprintf(log, "error   %s %s %s: %v\n", r.Name, r.MAC, r.IP, err)
			summary.Errors++
			continue
		}
		fmt.Fprintf(log, "update  %s %s %s\n", r.Name, r.MAC, r.IP)
		summary.Updated++
	}

	if !prune {
		for _, r := range plan.Prune {
			fmt.Fprintf(log, "extra   %s %s %s (on router, absent from file; use --prune to delete)\n",
				r.Name, r.MAC, r.IP)
		}
		return summary, nil
	}

	for _, r := range plan.Prune {
		if err := w.Delete(ctx, r.MAC); err != nil {
			fmt.Fprintf(log, "error   %s %s: %v\n", r.Name, r.MAC, err)
			summary.Errors++
			continue
		}
		fmt.Fprintf(log, "prune   %s %s %s\n", r.Name, r.MAC, r.IP)
		summary.Pruned++
	}

	return summary, nil
}

// applyNames writes the DNS side of the plan.
//
// Every change is made to the in-memory file and then committed in a single Put,
// because dns.set_host takes the whole file: N names would otherwise be N
// read-modify-write cycles, each racing the last.
//
// A name that will not validate is counted as an error and skipped, matching how
// applyPlan treats a bad reservation -- one bad entry must not abandon a bulk
// import. A failure of the single Put is different: nothing was written, so it is
// returned rather than counted.
func applyNames(ctx context.Context, log io.Writer, h nameWriter, hosts *types.HostFile,
	plan Plan, prune bool, summary *Summary) error {

	changed := false

	for _, r := range plan.NameSet {
		if err := hosts.Set(r.Name, r.IP); err != nil {
			fmt.Fprintf(log, "error   name %s %s: %v\n", r.Name, r.IP, err)
			summary.Errors++
			continue
		}
		fmt.Fprintf(log, "name    %s -> %s\n", hosts.FQDN(r.Name), r.IP)
		summary.NamesSet++
		changed = true
	}

	if prune {
		for _, name := range plan.NameRemove {
			if hosts.Remove(name) {
				fmt.Fprintf(log, "unname  %s\n", name)
				summary.NamesRemoved++
				changed = true
			}
		}
	} else {
		for _, name := range plan.NameRemove {
			fmt.Fprintf(log, "extra   name %s (on router, absent from file; use --prune to delete)\n", name)
		}
	}

	if !changed {
		return nil
	}
	return h.Put(ctx, hosts)
}

func printPlan(w io.Writer, plan Plan, prune bool) error {
	for _, r := range plan.Create {
		fmt.Fprintf(w, "create  %s %s %s\n", r.Name, r.MAC, r.IP)
	}
	for _, r := range plan.Update {
		fmt.Fprintf(w, "update  %s %s %s\n", r.Name, r.MAC, r.IP)
	}
	for _, r := range plan.Skip {
		fmt.Fprintf(w, "skip    %s %s %s\n", r.Name, r.MAC, r.IP)
	}
	for _, r := range plan.Prune {
		action := "extra "
		if prune {
			action = "prune "
		}
		fmt.Fprintf(w, "%s  %s %s %s\n", action, r.Name, r.MAC, r.IP)
	}
	for _, r := range plan.NameSet {
		fmt.Fprintf(w, "name    %s -> %s\n", r.Name, r.IP)
	}
	for _, name := range plan.NameRemove {
		action := "extra "
		if prune {
			action = "unname"
		}
		fmt.Fprintf(w, "%s  name %s\n", action, name)
	}
	_, err := fmt.Fprintf(w,
		"\n%d create, %d update, %d skip, %d device-only; %d name(s) to write, %d to remove\n",
		len(plan.Create), len(plan.Update), len(plan.Skip), len(plan.Prune),
		len(plan.NameSet), len(plan.NameRemove))
	return err
}
