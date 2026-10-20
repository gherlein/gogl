package main

import (
	"github.com/spf13/cobra"

	"github.com/emergingrobotics/gogl/skill"
	"github.com/emergingrobotics/gogl/utilities/internal/skillinstall"
)

// newInstallSkillCommand builds the fixed `install-skill` subcommand required by the
// skill self-installation contract: it writes the one skill gogl carries into the
// agent-neutral store, needing no config and reaching no router.
func newInstallSkillCommand() *cobra.Command {
	var force bool
	cmd := &cobra.Command{
		Use:   "install-skill",
		Short: "Install gogl's embedded agent skill into the canonical skills store",
		Long: `Install gogl's embedded agent skill into the canonical skills store.

gogl carries, inside this binary, the one skill that teaches an AI agent how to drive
it. This writes that skill into the agent-neutral store at $XDG_DATA_HOME/skills/
(default ~/.local/share/skills/), as a single directory named for the skill. A fan-out
tool such as sm then mirrors it into each agent's own skills directory.

It needs no configuration and reaches no router: a freshly obtained binary can install
its skill before it is set up for anything else.

A differing skill of the same name is left untouched and the command is refused unless
-f/--force is given; an already-identical skill is a successful no-op.

Exit status:
  0  installed, replaced, or already up to date
  3  refused: a different skill of that name exists and --force was not given
  1  failure: the store could not be resolved or written`,

		Args:          wrapArgsError(cobra.NoArgs),
		SilenceErrors: true,
		SilenceUsage:  true,

		// install-skill must not load the config file or resolve a router. Overriding
		// the root's PersistentPreRunE with a no-op bypasses that path, so a never-
		// configured binary -- or one whose config file is malformed -- can still
		// install its skill.
		PersistentPreRunE: func(*cobra.Command, []string) error { return nil },

		RunE: func(cmd *cobra.Command, _ []string) error {
			return skillinstall.Install(skill.FS, skill.Name, force, cmd.OutOrStdout())
		},
	}
	cmd.Flags().BoolVarP(&force, "force", "f", false, "replace an existing skill of the same name that differs")
	return cmd
}
