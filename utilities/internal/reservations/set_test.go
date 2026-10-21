package reservations

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/gherlein/gogl/src/types"
)

// fakeWriter records mutations so a test can assert on what was attempted.
type fakeWriter struct {
	created []types.Reservation
	updated []types.Reservation
	deleted []string
	failOn  string
}

func (f *fakeWriter) Create(_ context.Context, r *types.Reservation) (*types.Reservation, error) {
	if f.failOn == r.MAC {
		return nil, errors.New("injected create failure")
	}
	f.created = append(f.created, *r)
	return r, nil
}

func (f *fakeWriter) Update(_ context.Context, r *types.Reservation) (*types.Reservation, error) {
	if f.failOn == r.MAC {
		return nil, errors.New("injected update failure")
	}
	f.updated = append(f.updated, *r)
	return r, nil
}

func (f *fakeWriter) Delete(_ context.Context, mac string) error {
	if f.failOn == mac {
		return errors.New("injected delete failure")
	}
	f.deleted = append(f.deleted, mac)
	return nil
}

func testPlan() Plan {
	return Plan{
		Create: []types.Reservation{{Name: "camera", MAC: "aa:bb:cc:dd:ee:04", IP: "192.168.8.15"}},
		Update: []types.Reservation{{Name: "printer", MAC: "aa:bb:cc:dd:ee:02", IP: "192.168.8.14"}},
		Skip:   []types.Reservation{{Name: "nas", MAC: "aa:bb:cc:dd:ee:01", IP: "192.168.8.13"}},
		Prune:  []types.Reservation{{Name: "gone", MAC: "aa:bb:cc:dd:ee:03", IP: "192.168.8.30"}},
	}
}

func TestApplyPlan(t *testing.T) {
	w := &fakeWriter{}
	var log bytes.Buffer

	summary, err := applyPlan(context.Background(), &log, w, testPlan(), false)
	if err != nil {
		t.Fatalf("applyPlan error: %v", err)
	}

	if len(w.created) != 1 || w.created[0].Name != "camera" {
		t.Errorf("created = %v", w.created)
	}
	if len(w.updated) != 1 || w.updated[0].IP != "192.168.8.14" {
		t.Errorf("updated = %v", w.updated)
	}
	// Without --prune, extra device reservations are counted, never deleted.
	if len(w.deleted) != 0 {
		t.Errorf("deleted = %v without --prune, want none", w.deleted)
	}
	if summary.Created != 1 || summary.Updated != 1 || summary.Skipped != 1 || summary.Pruned != 0 {
		t.Errorf("summary = %+v", summary)
	}
	if summary.HasError() {
		t.Error("summary reports an error")
	}
	// The operator must be told about the extra entry rather than left guessing.
	if !strings.Contains(log.String(), "--prune") {
		t.Errorf("log does not mention --prune for the extra entry:\n%s", log.String())
	}
}

func TestApplyPlanWithPrune(t *testing.T) {
	w := &fakeWriter{}
	var log bytes.Buffer

	summary, err := applyPlan(context.Background(), &log, w, testPlan(), true)
	if err != nil {
		t.Fatalf("applyPlan error: %v", err)
	}
	if len(w.deleted) != 1 || w.deleted[0] != "aa:bb:cc:dd:ee:03" {
		t.Errorf("deleted = %v, want the pruned MAC", w.deleted)
	}
	if summary.Pruned != 1 {
		t.Errorf("Pruned = %d, want 1", summary.Pruned)
	}
}

// One failing entry must not abort the rest, and must still make the run fail
// overall.
func TestApplyPlanContinuesPastFailure(t *testing.T) {
	plan := Plan{Create: []types.Reservation{
		{Name: "a", MAC: "aa:bb:cc:dd:ee:01", IP: "192.168.8.11"},
		{Name: "b", MAC: "aa:bb:cc:dd:ee:02", IP: "192.168.8.12"},
		{Name: "c", MAC: "aa:bb:cc:dd:ee:03", IP: "192.168.8.13"},
	}}
	w := &fakeWriter{failOn: "aa:bb:cc:dd:ee:02"}
	var log bytes.Buffer

	summary, err := applyPlan(context.Background(), &log, w, plan, false)
	if err != nil {
		t.Fatalf("applyPlan returned a fatal error: %v", err)
	}
	if len(w.created) != 2 {
		t.Errorf("created %d, want 2: the loop stopped at the failure", len(w.created))
	}
	if summary.Errors != 1 {
		t.Errorf("Errors = %d, want 1", summary.Errors)
	}
	if !summary.HasError() {
		t.Error("HasError() = false despite a failure")
	}
	if !strings.Contains(log.String(), "aa:bb:cc:dd:ee:02") {
		t.Errorf("log does not name the failed entry:\n%s", log.String())
	}
}

func TestApplyPlanPruneFailureIsCounted(t *testing.T) {
	w := &fakeWriter{failOn: "aa:bb:cc:dd:ee:03"}
	var log bytes.Buffer

	summary, err := applyPlan(context.Background(), &log, w, testPlan(), true)
	if err != nil {
		t.Fatalf("applyPlan error: %v", err)
	}
	if summary.Errors != 1 || summary.Pruned != 0 {
		t.Errorf("summary = %+v, want one error and no prunes", summary)
	}
}

func TestSummaryHasError(t *testing.T) {
	if (Summary{}).HasError() {
		t.Error("empty summary reports an error")
	}
	if !(Summary{Errors: 1}).HasError() {
		t.Error("summary with errors does not report one")
	}
}

// The summary must report file-side and device-side counts separately, because
// summing them would be meaningless.
func TestSummaryString(t *testing.T) {
	s := Summary{Created: 12, Updated: 3, Skipped: 22, Pruned: 1, Errors: 0}
	got := s.String()
	if !strings.Contains(got, "37 host declarations") {
		t.Errorf("String() = %q, want it to total the 37 file entries", got)
	}
	if !strings.Contains(got, "1 pruned") {
		t.Errorf("String() = %q, want the prune count reported separately", got)
	}
}

// A malformed file must be rejected before any device contact, so a bad file
// never half-writes a router.
func TestValidateFileRejectsBadName(t *testing.T) {
	const input = `host bad_name {
    hardware ethernet aa:bb:cc:dd:ee:01;
    fixed-address 192.168.8.10;
}
`
	_, errs := validateFile(strings.NewReader(input))
	if len(errs) == 0 {
		t.Fatal("accepted an invalid file")
	}
}

func TestValidateFileRejectsDuplicates(t *testing.T) {
	const input = `host a {
    hardware ethernet aa:bb:cc:dd:ee:01;
    fixed-address 192.168.8.10;
}
host b {
    hardware ethernet aa:bb:cc:dd:ee:01;
    fixed-address 192.168.8.11;
}
`
	_, errs := validateFile(strings.NewReader(input))
	if len(errs) == 0 {
		t.Fatal("accepted a duplicate MAC")
	}
	if !strings.Contains(errsText(errs), "already used") {
		t.Errorf("errors do not report the duplicate: %v", errs)
	}
}

func TestValidateFileAccepts(t *testing.T) {
	got, errs := validateFile(strings.NewReader(wellFormed))
	if len(errs) != 0 {
		t.Fatalf("rejected a valid file: %v", errs)
	}
	if len(got) != 2 {
		t.Errorf("parsed %d reservations, want 2", len(got))
	}
}

func TestValidateFileEmpty(t *testing.T) {
	got, errs := validateFile(strings.NewReader("# nothing\n"))
	if len(errs) != 0 {
		t.Errorf("errors: %v", errs)
	}
	if len(got) != 0 {
		t.Errorf("parsed %d reservations, want 0", len(got))
	}
}

func TestPrintPlan(t *testing.T) {
	var buf bytes.Buffer
	if err := printPlan(&buf, testPlan(), false); err != nil {
		t.Fatalf("printPlan error: %v", err)
	}
	out := buf.String()
	for _, want := range []string{"create", "update", "skip", "extra", "camera", "printer", "nas", "gone"} {
		if !strings.Contains(out, want) {
			t.Errorf("plan output missing %q:\n%s", want, out)
		}
	}
}

func TestPrintPlanWithPrune(t *testing.T) {
	var buf bytes.Buffer
	if err := printPlan(&buf, testPlan(), true); err != nil {
		t.Fatalf("printPlan error: %v", err)
	}
	if !strings.Contains(buf.String(), "prune") {
		t.Errorf("plan output does not mark the entry for pruning:\n%s", buf.String())
	}
}
