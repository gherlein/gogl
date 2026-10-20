// Package skill carries gogl's own agent skill inside the binary.
//
// The skill directory lives one level below this file so the //go:embed directive can
// reach it: a directive is resolved relative to its package directory and cannot use
// "..", and the main command lives under utilities/gogl where it could not embed a
// top-level skill/ at all. Embedding from here, the directory containing the skill,
// keeps the skill directory itself free of any stray .go file -- which matters because
// the installed copy is compared byte-for-byte against this one.
package skill

import "embed"

// Name is the carried skill's name. It equals the embedded directory name and the
// frontmatter name, as the skill-directory contract requires.
const Name = "using-gogl"

// FS holds the whole skill directory. The all: prefix is required so bundled dotfiles,
// which a plain embed would skip, travel with it.
//
//go:embed all:using-gogl
var FS embed.FS
