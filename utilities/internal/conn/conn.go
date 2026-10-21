// Package conn holds the connection flags and client construction shared by
// goglps, goglnet and goglmac, so the three utilities present identical
// ergonomics.
package conn

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	gogl "github.com/gherlein/gogl/src"
	"github.com/gherlein/gogl/utilities/internal/config"
)

// DefaultPort is 80, not 443, because that is what GL.iNet firmware serves. A
// habit carried over from the UDM Pro would fail here.
const DefaultPort = 80

// Environment variables. The password comes from the environment rather than a
// flag so it never appears in a process listing or a shell history.
const (
	EnvPassword = "GL_PASSWORD"
	EnvUsername = "GL_USERNAME"
	EnvHost     = "GL_ROUTER_IP"
)

// passwordNearMisses are variable names people reach for instead of GL_PASSWORD.
// Setting one of these and nothing else produces an authentication failure that
// looks like a wrong password rather than an unset one, so it is worth naming.
var passwordNearMisses = []string{
	"GL_PASSWD",
	"GL_PASS",
	"GL_PW",
	"GLINET_PASSWORD",
	"GL_ROUTER_PASSWORD",
	"GL_ADMIN_PASSWORD",
}

// Flags holds the connection options common to every utility.
type Flags struct {
	Host   string
	Port   int
	HTTPS  bool
	Secure bool

	Password string
	Username string

	warnings []string
}

// reorderArgs returns args with all flags (and their values) before all operands.
func reorderArgs(fs *flag.FlagSet, args []string) []string {
	flags := make([]string, 0, len(args))
	operands := make([]string, 0, len(args))

	for i := 0; i < len(args); i++ {
		arg := args[i]

		if arg == "--" {
			operands = append(operands, args[i+1:]...)
			break
		}

		if len(arg) > 1 && strings.HasPrefix(arg, "-") {
			flags = append(flags, arg)
			// A non-boolean flag written as "-name value" consumes the next
			// argument, which must travel with it rather than being mistaken for
			// an operand.
			if !strings.Contains(arg, "=") && takesValue(fs, arg) && i+1 < len(args) {
				i++
				flags = append(flags, args[i])
			}
			continue
		}

		operands = append(operands, arg)
	}

	if len(operands) == 0 {
		return flags
	}
	// The explicit separator is required, not cosmetic: without it flag.Parse would
	// go on to interpret an operand that happens to begin with a dash, and a
	// caller's own "--" would lose its meaning.
	return append(append(flags, "--"), operands...)
}

// takesValue reports whether the flag named by arg consumes a following value.
func takesValue(fs *flag.FlagSet, arg string) bool {
	name := strings.TrimLeft(arg, "-")
	f := fs.Lookup(name)
	if f == nil {
		return false
	}
	boolFlag, ok := f.Value.(interface{ IsBoolFlag() bool })
	return !ok || !boolFlag.IsBoolFlag()
}

// Validate checks the flags and fills in the environment-supplied values.
func (f *Flags) Validate() error {
	// The host is read here as well as in the command layer, so that Validate holds the
	// same contract for all three settings. Register used to seed it from the
	// environment; deleting Register left this as the only inconsistency of the three.
	if f.Host == "" {
		f.Host = os.Getenv(EnvHost)
	}
	if f.Password == "" {
		f.Password = os.Getenv(EnvPassword)
	}
	if f.Username == "" {
		f.Username = os.Getenv(EnvUsername)
	}
	// Resolve the default here rather than leaving it to the library, so that a
	// diagnostic can report the username that was actually used instead of an
	// empty string.
	if f.Username == "" {
		f.Username = gogl.DefaultUsername
	}

	if f.Host == "" {
		return fmt.Errorf("no router host: pass -H or set %s", EnvHost)
	}
	if f.Password == "" {
		// Name the near miss rather than just the correct variable: someone who
		// exported GL_PASSWD is looking straight at it and will not spot the
		// difference from a generic message.
		for _, name := range passwordNearMisses {
			if os.Getenv(name) != "" {
				return fmt.Errorf("no router password: %s is set, but the variable is %s",
					name, EnvPassword)
			}
		}
		return fmt.Errorf("no router password: set %s", EnvPassword)
	}

	// -k expresses an intent the transport cannot honour over plain HTTP, so
	// silently ignoring it would be misleading.
	if f.Secure && !f.HTTPS {
		return errors.New("-k/--secure requires --https; there is no certificate to verify over HTTP")
	}
	if f.HTTPS && f.Port == DefaultPort {
		f.warnings = append(f.warnings,
			fmt.Sprintf("--https with port %d is probably wrong; GL.iNet serves HTTPS on 443", DefaultPort))
	}
	return nil
}

// Warnings returns non-fatal advisories accumulated by Validate.
func (f *Flags) Warnings() []string { return f.warnings }

// Explain annotates an authentication failure with the credential actually used.
//
// A bare "Access denied" is indistinguishable from a wrong password, a stale
// environment variable, and a mistyped variable name. Reporting the username and
// the password's length and source resolves all three at a glance without
// revealing the password itself.
//
// Any other error is returned unchanged.
func (f *Flags) Explain(err error) error {
	if err == nil || !errors.Is(err, gogl.ErrUnauthorized) {
		return err
	}
	return fmt.Errorf("%w\n  tried username %q with a %d-character password from %s\n"+
		"  if that length is not what you expect, %s holds something other than what you set",
		err, f.Username, len(f.Password), EnvPassword, EnvPassword)
}

// ClientConfig maps CLI flags onto library config, inverting the TLS sense.
//
// The library is secure at its zero value; the CLI accepts self-signed
// certificates unless -k is given, because a router with a self-signed
// certificate is the normal case and an unusable CLI is worse than a
// warned-about one.
func (f *Flags) ClientConfig() gogl.Config {
	return gogl.Config{
		Host:               f.Host,
		Port:               f.Port,
		HTTPS:              f.HTTPS,
		Username:           f.Username,
		Password:           f.Password,
		InsecureSkipVerify: !f.Secure,
	}
}

// Connect validates and builds a client, printing any warnings to stderr.
func (f *Flags) Connect() (*gogl.Client, error) {
	if err := f.Validate(); err != nil {
		return nil, err
	}
	for _, w := range f.warnings {
		fmt.Fprintln(os.Stderr, "warning:", w)
	}
	return gogl.New(f.ClientConfig())
}

// OUICacheDir returns the directory for cached IEEE OUI data, honouring
// XDG_DATA_HOME per the XDG Base Directory Specification. No root access needed.
func OUICacheDir() (string, error) {
	return config.CacheDir()
}

// IsTerminal reports whether f is attached to a terminal, so a utility can
// prompt a human but proceed in a pipeline.
//
// Implemented with Stat rather than golang.org/x/term deliberately: x/term
// requires a newer Go than this module targets, and the character-device check is
// sufficient for deciding whether to prompt.
func IsTerminal(f *os.File) bool {
	info, err := f.Stat()
	if err != nil {
		return false
	}
	return info.Mode()&os.ModeCharDevice != 0
}

// Confirm asks for a yes before a destructive operation, reading from in and
// prompting on out.
//
// Shared rather than duplicated per utility: both goglps and goglnet gate writes
// this way, and two copies of a yes/no parser is two chances to disagree about what
// counts as consent. Anything but "y" is a refusal, including EOF -- a closed stdin
// is not agreement.
func Confirm(in io.Reader, out io.Writer, prompt string) error {
	fmt.Fprint(out, prompt)

	var answer string
	if _, err := fmt.Fscanln(in, &answer); err != nil {
		return errors.New("aborted")
	}
	if !strings.EqualFold(strings.TrimSpace(answer), "y") {
		return errors.New("aborted")
	}
	return nil
}
