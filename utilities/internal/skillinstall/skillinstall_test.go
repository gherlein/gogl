package skillinstall

import (
	"bytes"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
	"testing/fstest"
)

const skillName = "using-gogl"

// fakeSkill builds an in-memory stand-in for the embedded skill FS: a top-level
// directory named skillName holding SKILL.md with the given body.
func fakeSkill(body string) fs.FS {
	return fstest.MapFS{
		skillName + "/SKILL.md": &fstest.MapFile{Data: []byte(body)},
	}
}

// storeIn points XDG_DATA_HOME at a fresh temp dir and returns the store path.
func storeIn(t *testing.T) string {
	t.Helper()
	dataHome := t.TempDir()
	t.Setenv("XDG_DATA_HOME", dataHome)
	return filepath.Join(dataHome, storeDirName)
}

func readInstalled(t *testing.T, store string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(store, skillName, "SKILL.md"))
	if err != nil {
		t.Fatalf("reading installed skill: %v", err)
	}
	return string(data)
}

func TestInstallFresh(t *testing.T) {
	store := storeIn(t)
	var out bytes.Buffer
	if err := Install(fakeSkill("hello"), skillName, false, &out); err != nil {
		t.Fatalf("Install: %v", err)
	}
	if got := readInstalled(t, store); got != "hello" {
		t.Fatalf("installed content = %q, want %q", got, "hello")
	}
}

func TestInstallIdempotentNoOp(t *testing.T) {
	store := storeIn(t)
	src := fakeSkill("hello")
	if err := Install(src, skillName, false, &bytes.Buffer{}); err != nil {
		t.Fatalf("first Install: %v", err)
	}
	var out bytes.Buffer
	if err := Install(src, skillName, false, &out); err != nil {
		t.Fatalf("second Install: %v", err)
	}
	if got := readInstalled(t, store); got != "hello" {
		t.Fatalf("content after no-op = %q, want %q", got, "hello")
	}
	assertNoStageLeftovers(t, filepath.Dir(store))
}

func TestInstallConflictRefused(t *testing.T) {
	store := storeIn(t)
	if err := Install(fakeSkill("original"), skillName, false, &bytes.Buffer{}); err != nil {
		t.Fatalf("seed Install: %v", err)
	}
	// A differing skill of the same name must be refused, and the original left intact.
	err := Install(fakeSkill("different"), skillName, false, &bytes.Buffer{})
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("Install error = %v, want ErrConflict", err)
	}
	if got := readInstalled(t, store); got != "original" {
		t.Fatalf("content after refusal = %q, want it unchanged %q", got, "original")
	}
}

func TestInstallForceReplaces(t *testing.T) {
	store := storeIn(t)
	if err := Install(fakeSkill("original"), skillName, false, &bytes.Buffer{}); err != nil {
		t.Fatalf("seed Install: %v", err)
	}
	if err := Install(fakeSkill("replacement"), skillName, true, &bytes.Buffer{}); err != nil {
		t.Fatalf("forced Install: %v", err)
	}
	if got := readInstalled(t, store); got != "replacement" {
		t.Fatalf("content after force = %q, want %q", got, "replacement")
	}
	assertNoStageLeftovers(t, filepath.Dir(store))
}

func TestInstallExtraFileIsNonEquivalent(t *testing.T) {
	store := storeIn(t)
	if err := Install(fakeSkill("hello"), skillName, false, &bytes.Buffer{}); err != nil {
		t.Fatalf("seed Install: %v", err)
	}
	// An installed directory with an extra file is not equivalent, so a plain re-run is
	// refused rather than treated as already-present.
	stray := filepath.Join(store, skillName, "STRAY.md")
	if err := os.WriteFile(stray, []byte("x"), skillFileMode); err != nil {
		t.Fatalf("writing stray file: %v", err)
	}
	if err := Install(fakeSkill("hello"), skillName, false, &bytes.Buffer{}); !errors.Is(err, ErrConflict) {
		t.Fatalf("Install error = %v, want ErrConflict", err)
	}
}

func TestInstallSweepsLeftoverStage(t *testing.T) {
	store := storeIn(t)
	parent := filepath.Dir(store)
	if err := os.MkdirAll(parent, storeDirMode); err != nil {
		t.Fatalf("prep parent: %v", err)
	}
	leftover := filepath.Join(parent, stagePrefix+skillName+".crashed")
	if err := os.MkdirAll(leftover, storeDirMode); err != nil {
		t.Fatalf("prep leftover: %v", err)
	}
	if err := Install(fakeSkill("hello"), skillName, false, &bytes.Buffer{}); err != nil {
		t.Fatalf("Install: %v", err)
	}
	if _, err := os.Stat(leftover); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("leftover stage not swept: stat err = %v", err)
	}
}

func TestDefaultStoreHonorsXDG(t *testing.T) {
	abs := t.TempDir()
	t.Setenv("XDG_DATA_HOME", abs)
	got, err := DefaultStore()
	if err != nil {
		t.Fatalf("DefaultStore: %v", err)
	}
	if want := filepath.Join(abs, storeDirName); got != want {
		t.Fatalf("DefaultStore = %q, want %q", got, want)
	}
}

func TestDefaultStoreIgnoresRelativeXDG(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", "relative/path")
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skipf("no home dir: %v", err)
	}
	got, err := DefaultStore()
	if err != nil {
		t.Fatalf("DefaultStore: %v", err)
	}
	if want := filepath.Join(home, dataSubpath, storeDirName); got != want {
		t.Fatalf("DefaultStore = %q, want %q", got, want)
	}
}

func TestSkillMarkdownReturnsExactBytes(t *testing.T) {
	body := "---\nname: using-gogl\n---\nbody text\n"
	got, err := SkillMarkdown(fakeSkill(body), skillName)
	if err != nil {
		t.Fatalf("SkillMarkdown: %v", err)
	}
	if string(got) != body {
		t.Fatalf("SkillMarkdown = %q, want verbatim %q", got, body)
	}
}

func TestSkillMarkdownMissingIsError(t *testing.T) {
	// A source with no SKILL.md under the skill directory must error, not return
	// empty or partial content.
	empty := fstest.MapFS{skillName + "/other.txt": &fstest.MapFile{Data: []byte("x")}}
	if _, err := SkillMarkdown(empty, skillName); err == nil {
		t.Fatal("SkillMarkdown on a source without SKILL.md returned no error")
	}
}

func assertNoStageLeftovers(t *testing.T, parent string) {
	t.Helper()
	entries, err := os.ReadDir(parent)
	if err != nil {
		t.Fatalf("reading parent %s: %v", parent, err)
	}
	for _, entry := range entries {
		if len(entry.Name()) >= len(stagePrefix) && entry.Name()[:len(stagePrefix)] == stagePrefix {
			t.Fatalf("staging leftover not cleaned: %s", entry.Name())
		}
	}
}
