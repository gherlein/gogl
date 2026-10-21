package main

import (
	"github.com/spf13/cobra"

	"github.com/gherlein/gogl/skill"
	"github.com/gherlein/gogl/utilities/internal/skillinstall"
)

// newPrintSkillCommand builds the fixed `print-skill` subcommand required by the skill
// self-installation contract: it writes the carried skill's SKILL.md verbatim to
// stdout so an agent can read it straight into context without installing anything.
func newPrintSkillCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "print-skill",
		Short: "Print gogl's embedded agent skill (SKILL.md) to stdout",
		Long: `Print gogl's embedded agent skill to standard output.

Writes the exact bytes of the carried skill's SKILL.md -- frontmatter and body -- and
nothing else, so an agent can read the instructions directly into its context without
installing anything. The content comes from inside this binary, not the filesystem, so
the output is identical whether or not the skill has ever been installed, and nothing
on disk is created or changed.

This emits the SKILL.md instructions only. To place the whole skill directory, bundled
files and all, in the canonical store, use "gogl install-skill" instead.

Exit status:
  0  the skill was written to stdout
  1  failure: the embedded skill could not be read (unreachable in a correct build)`,

		Args:          wrapArgsError(cobra.NoArgs),
		SilenceErrors: true,
		SilenceUsage:  true,

		// Like install-skill, print-skill reaches no router and loads no config;
		// override the root's PersistentPreRunE so an unconfigured binary still works.
		PersistentPreRunE: func(*cobra.Command, []string) error { return nil },

		RunE: func(cmd *cobra.Command, _ []string) error {
			// Read the whole skill first, then write once: a read failure must leave
			// stdout empty rather than emit a partial, corrupted SKILL.md.
			data, err := skillinstall.SkillMarkdown(skill.FS, skill.Name)
			if err != nil {
				return err
			}
			_, err = cmd.OutOrStdout().Write(data)
			return err
		},
	}
	return cmd
}
