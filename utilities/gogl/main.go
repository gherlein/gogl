// Command gogl manages GL.iNet 4.x travel routers over their JSON-RPC API.
//
// One binary, `gogl <area> <action>`. It replaces the four utilities goglps, goglnet,
// goglmac and goglcfg, whose logic now lives in importable packages under
// utilities/internal/ and is called from the command tree here.
//
// See docs/DESIGN-V2.md for the command tree and the reasoning behind it.
package main

import (
	"errors"
	"fmt"
	"os"

	"github.com/gherlein/gogl/src/types"
	"github.com/gherlein/gogl/utilities/internal/conn"
	"github.com/gherlein/gogl/utilities/internal/skillinstall"
)

// Exit codes. Distinguishing a refusal from a failure matters for scripting: a guard
// saying "not while reservations exist" is a different situation from a router that
// cannot be reached, and a caller may reasonably retry one and not the other.
const (
	exitOK      = 0
	exitError   = 1
	exitUsage   = 2
	exitRefused = 3
)

func main() {
	root := newRootCommand()
	root.SetVersionTemplate("")
	root.Version = " " // presence enables --version; the template above suppresses cobra's

	// cobra prints its own --version output, which does not carry the revision. Handle
	// the flag before Execute so the build stamp is the one conn produces.
	for _, arg := range os.Args[1:] {
		if arg == "--version" || arg == "-v" {
			conn.PrintVersion(os.Stdout, "gogl")
			os.Exit(exitOK)
		}
		if arg == "--" {
			break
		}
	}

	if err := root.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, "gogl:", err)
		os.Exit(codeFor(err))
	}
}

// codeFor maps an error to an exit code.
//
// The refusal set is every guard gogl enforces before writing: they are the errors a
// script should treat as "the state was wrong", not "the tool failed".
func codeFor(err error) int {
	switch {
	case errors.Is(err, types.ErrDomainNotSet),
		errors.Is(err, types.ErrReservationsExist),
		errors.Is(err, types.ErrWirelessSession),
		errors.Is(err, skillinstall.ErrConflict),
		errors.Is(err, errRefused):
		return exitRefused
	case errors.Is(err, errUsage):
		return exitUsage
	default:
		return exitError
	}
}

// errUsage marks an error caused by how the command was invoked rather than by the
// device's state.
var errUsage = errors.New("usage")
