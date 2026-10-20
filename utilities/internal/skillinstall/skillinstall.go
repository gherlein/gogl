// Package skillinstall writes gogl's embedded skill into the canonical, agent-neutral
// skills store, crash-safely and without touching any other entry.
//
// It implements the producer side of the skill self-installation contract: resolve the
// store from XDG, stage the skill beside the store on the same filesystem, and rename
// it into place so a crash can never leave a partial skill directory behind. An
// identical skill already present is a no-op; a differing one is refused unless the
// caller forces a replace.
package skillinstall

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
)

const skillFileName = "SKILL.md"

const (
	storeDirName  = "skills"
	dataSubpath   = ".local/share"
	storeDirMode  = 0o755
	skillFileMode = 0o644

	// stagePrefix names every staging and set-aside directory so a crash leaves
	// findable leftovers and a later run can sweep them. The skill name is embedded so
	// one tool's leftovers never collide with another's beside a shared store.
	stagePrefix = ".install-skill-"
	asideSuffix = ".old"
)

// ErrConflict means a skill directory of the same name already exists in the store and
// differs from the carried skill, and no replace was forced. It is distinct from a
// failure so a caller can tell "already present but different" -- a state it may choose
// to overwrite -- from a store it could not write at all.
var ErrConflict = errors.New("skill already present and differs")

// DefaultStore returns $XDG_DATA_HOME/skills, falling back to ~/.local/share/skills
// when XDG_DATA_HOME is unset, empty, or relative (the XDG spec requires an absolute
// path, so a relative one is ignored rather than honored).
func DefaultStore() (string, error) {
	dataHome := os.Getenv("XDG_DATA_HOME")
	if dataHome == "" || !filepath.IsAbs(dataHome) {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("resolve home directory: %w", err)
		}
		dataHome = filepath.Join(home, dataSubpath)
	}
	return filepath.Join(dataHome, storeDirName), nil
}

// SkillMarkdown returns the exact, unmodified bytes of the carried skill's SKILL.md,
// read from src under the skill's own directory. It is the one artifact print-skill
// emits: a caller writes these bytes to stdout verbatim. It touches nothing on the
// filesystem and needs no configuration.
func SkillMarkdown(src fs.FS, name string) ([]byte, error) {
	data, err := fs.ReadFile(src, path.Join(name, skillFileName))
	if err != nil {
		return nil, fmt.Errorf("read embedded %s/%s: %w", name, skillFileName, err)
	}
	return data, nil
}

// Install writes the skill named name, carried in src under a top-level directory of
// that name, into the default store. It prints a one-line result to stdout on success
// or no-op; it returns ErrConflict when a differing skill of that name exists and force
// is false, and a plain error on any real failure. The store is always left in a
// conforming state: a failed write never leaves a partial skill directory behind.
func Install(src fs.FS, name string, force bool, stdout io.Writer) error {
	store, err := DefaultStore()
	if err != nil {
		return err
	}
	// A producer may create the store. Doing it first also lets EvalSymlinks resolve
	// it, since the store itself may be a symlink into another filesystem.
	if err := os.MkdirAll(store, storeDirMode); err != nil {
		return fmt.Errorf("create store %s: %w", store, err)
	}
	resolved, err := filepath.EvalSymlinks(store)
	if err != nil {
		return fmt.Errorf("resolve store %s: %w", store, err)
	}
	// Stage in the parent of the *resolved* store so the final rename stays on one
	// filesystem even when the store is a symlink elsewhere, and never inside the
	// store, which is reserved for finished skill directories.
	parent := filepath.Dir(resolved)

	// Clear leftovers a previous crash may have orphaned, so they cannot accumulate.
	sweepStages(parent, name)

	sub, err := fs.Sub(src, name)
	if err != nil {
		return fmt.Errorf("locate embedded skill %q: %w", name, err)
	}

	target := filepath.Join(resolved, name)
	_, lerr := os.Lstat(target)
	switch {
	case lerr == nil:
		same, err := equivalent(sub, target)
		if err != nil {
			return err
		}
		if same {
			fmt.Fprintf(stdout, "skill %q already up to date in %s\n", name, resolved)
			return nil
		}
		if !force {
			return fmt.Errorf("%w: %q exists in %s; pass --force to replace", ErrConflict, name, resolved)
		}
	case errors.Is(lerr, fs.ErrNotExist):
		// Fresh install; handled below.
	default:
		return fmt.Errorf("inspect %s: %w", target, lerr)
	}

	// Build the whole skill in staging first, so nothing incomplete is ever moved in.
	stage, err := os.MkdirTemp(parent, stagePrefix+name+".")
	if err != nil {
		return fmt.Errorf("create staging directory beside %s: %w", resolved, err)
	}
	defer os.RemoveAll(stage) // A no-op once stage has been renamed into place.

	if err := copyTree(sub, stage); err != nil {
		return fmt.Errorf("stage skill %q: %w", name, err)
	}

	// Fresh install: a single rename is the whole write; a crash before it leaves the
	// store untouched.
	if errors.Is(lerr, fs.ErrNotExist) {
		if err := os.Rename(stage, target); err != nil {
			return fmt.Errorf("install skill into %s: %w", target, err)
		}
		fmt.Fprintf(stdout, "installed skill %q to %s\n", name, target)
		return nil
	}

	// Forced replace: move the old directory aside, move the new one in, then delete
	// the old. The store briefly has no directory at the name -- unavoidable portably
	// -- but never a partial one, and the old copy is restored if the move in fails.
	aside := stage + asideSuffix
	if err := os.Rename(target, aside); err != nil {
		return fmt.Errorf("set aside existing %s: %w", target, err)
	}
	if err := os.Rename(stage, target); err != nil {
		if rerr := os.Rename(aside, target); rerr != nil {
			return fmt.Errorf("install skill into %s: %w (restore of previous copy failed: %v)", target, err, rerr)
		}
		return fmt.Errorf("install skill into %s: %w", target, err)
	}
	if err := os.RemoveAll(aside); err != nil {
		// The new skill is already in place; a leftover aside carries the reserved
		// prefix and is swept next run, so this is a warning, not a failure.
		fmt.Fprintf(stdout, "warning: could not remove previous copy %s: %v\n", aside, err)
	}
	fmt.Fprintf(stdout, "replaced skill %q in %s\n", name, target)
	return nil
}

// sweepStages removes any staging or set-aside directory for this skill left in parent
// by an earlier crash. It is best effort: a leftover it cannot remove does not block a
// fresh install, which stages under a new unique name regardless.
func sweepStages(parent, name string) {
	entries, err := os.ReadDir(parent)
	if err != nil {
		return
	}
	prefix := stagePrefix + name + "."
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), prefix) {
			_ = os.RemoveAll(filepath.Join(parent, entry.Name()))
		}
	}
}

// copyTree writes every file and directory of src into dst, which must already exist,
// choosing the written permissions itself because an embedded FS reports fixed,
// read-only metadata. Written files are user-readable and not world-writable.
func copyTree(src fs.FS, dst string) error {
	return fs.WalkDir(src, ".", func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		target := filepath.Join(dst, path)
		if entry.IsDir() {
			return os.MkdirAll(target, storeDirMode)
		}
		data, err := fs.ReadFile(src, path)
		if err != nil {
			return err
		}
		return os.WriteFile(target, data, skillFileMode)
	})
}

// node is one entry in a skill directory for equivalence comparison: a directory, or a
// file with its bytes.
type node struct {
	isDir bool
	data  []byte
}

// equivalent reports whether the on-disk directory dir holds exactly the carried skill:
// the same set of relative paths, with identical bytes for every file. Permissions and
// timestamps are not compared. Anything that is not a readable directory, or that
// differs by one path or one byte, is non-equivalent -- which, without --force, is
// refused rather than silently overwritten.
func equivalent(src fs.FS, dir string) (bool, error) {
	want, err := collect(src)
	if err != nil {
		return false, fmt.Errorf("read embedded skill: %w", err)
	}
	info, err := os.Stat(dir)
	if err != nil || !info.IsDir() {
		return false, nil
	}
	got, err := collect(os.DirFS(dir))
	if err != nil {
		return false, nil
	}
	if len(want) != len(got) {
		return false, nil
	}
	for path, wantNode := range want {
		gotNode, ok := got[path]
		if !ok || gotNode.isDir != wantNode.isDir {
			return false, nil
		}
		if !wantNode.isDir && !bytes.Equal(wantNode.data, gotNode.data) {
			return false, nil
		}
	}
	return true, nil
}

// collect reads an fs.FS into a map keyed by relative path. The root itself is skipped
// so two trees compare by their contents, not by the differing names of their roots.
func collect(src fs.FS) (map[string]node, error) {
	out := map[string]node{}
	err := fs.WalkDir(src, ".", func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if path == "." {
			return nil
		}
		if entry.IsDir() {
			out[path] = node{isDir: true}
			return nil
		}
		data, err := fs.ReadFile(src, path)
		if err != nil {
			return err
		}
		out[path] = node{data: data}
		return nil
	})
	return out, err
}
