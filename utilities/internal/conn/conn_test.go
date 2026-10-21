package conn

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	gogl "github.com/gherlein/gogl/src"
)

// flagsFor builds Flags with a clean environment.
//
// It used to parse argv through Flags.Register, which cobra replaced. The subject of
// these tests was never flag parsing -- it is Validate and Explain -- so the fields are
// set directly now.
func flagsFor(t *testing.T, mutate func(*Flags)) *Flags {
	t.Helper()
	t.Setenv(EnvHost, "")
	t.Setenv(EnvPassword, "")
	t.Setenv(EnvUsername, "")

	f := &Flags{Port: DefaultPort}
	if mutate != nil {
		mutate(f)
	}
	return f
}

func TestDefaults(t *testing.T) {
	f := flagsFor(t, nil)
	if f.Port != DefaultPort {
		t.Errorf("Port = %d, want %d", f.Port, DefaultPort)
	}
	if f.HTTPS {
		t.Error("HTTPS should default to false")
	}
	if f.Secure {
		t.Error("Secure should default to false")
	}
}

func TestValidateRequiresHost(t *testing.T) {
	f := flagsFor(t, nil)
	f.Password = "secret"
	err := f.Validate()
	if err == nil || !strings.Contains(err.Error(), "host") {
		t.Errorf("Validate() = %v, want an error mentioning host", err)
	}
}

func TestValidateRequiresPassword(t *testing.T) {
	f := flagsFor(t, func(f *Flags) { f.Host = "192.168.8.1" })
	err := f.Validate()
	if err == nil || !strings.Contains(err.Error(), EnvPassword) {
		t.Errorf("Validate() = %v, want an error naming %s", err, EnvPassword)
	}
}

func TestValidateReadsEnvironment(t *testing.T) {
	t.Setenv(EnvHost, "10.0.0.1")
	t.Setenv(EnvPassword, "from-env")
	t.Setenv(EnvUsername, "admin")

	f := &Flags{Port: DefaultPort}
	if err := f.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if f.Host != "10.0.0.1" {
		t.Errorf("Host = %q, want 10.0.0.1 from %s", f.Host, EnvHost)
	}
	if f.Password != "from-env" {
		t.Errorf("Password not read from %s", EnvPassword)
	}
	if f.Username != "admin" {
		t.Errorf("Username = %q, want admin from %s", f.Username, EnvUsername)
	}
}

// An explicit host must beat the environment. Validate only fills in what is empty.
func TestFlagBeatsEnvironment(t *testing.T) {
	t.Setenv(EnvHost, "10.0.0.1")

	f := &Flags{Port: DefaultPort, Host: "192.168.8.1", Password: "secret"}
	if err := f.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if f.Host != "192.168.8.1" {
		t.Errorf("Host = %q, want the explicit value to win", f.Host)
	}
}

// -k without --https is an error rather than a silent no-op, because a user who
// passes it is expressing an intent the transport cannot honour over HTTP.
func TestValidateRejectsSecureWithoutHTTPS(t *testing.T) {
	f := flagsFor(t, func(f *Flags) { f.Host = "192.168.8.1"; f.Secure = true })
	f.Password = "secret"
	err := f.Validate()
	if err == nil || !strings.Contains(err.Error(), "--https") {
		t.Errorf("Validate() = %v, want an error mentioning --https", err)
	}
}

func TestValidateAcceptsSecureWithHTTPS(t *testing.T) {
	f := flagsFor(t, func(f *Flags) { f.Host = "192.168.8.1"; f.Secure = true; f.HTTPS = true; f.Port = 443 })
	f.Password = "secret"
	if err := f.Validate(); err != nil {
		t.Errorf("Validate() = %v, want nil", err)
	}
	if len(f.Warnings()) != 0 {
		t.Errorf("unexpected warnings: %v", f.Warnings())
	}
}

func TestValidateWarnsOnHTTPSWithPort80(t *testing.T) {
	f := flagsFor(t, func(f *Flags) { f.Host = "192.168.8.1"; f.HTTPS = true })
	f.Password = "secret"
	if err := f.Validate(); err != nil {
		t.Fatalf("Validate() = %v, want nil", err)
	}
	if len(f.Warnings()) == 0 {
		t.Error("expected a warning for --https on port 80")
	}
}

// The library must be secure at its zero value while the CLI accepts
// self-signed certificates, so the flag inverts on the way through.
func TestClientConfigInvertsTLSVerification(t *testing.T) {
	lenient := flagsFor(t, func(f *Flags) { f.Host = "192.168.8.1"; f.HTTPS = true; f.Port = 443 })
	lenient.Password = "secret"
	if !lenient.ClientConfig().InsecureSkipVerify {
		t.Error("InsecureSkipVerify = false without -k; the CLI should accept self-signed by default")
	}

	strict := flagsFor(t, func(f *Flags) { f.Host = "192.168.8.1"; f.HTTPS = true; f.Port = 443; f.Secure = true })
	strict.Password = "secret"
	if strict.ClientConfig().InsecureSkipVerify {
		t.Error("InsecureSkipVerify = true with -k; -k should enforce verification")
	}
}

func TestClientConfigCarriesFields(t *testing.T) {
	f := flagsFor(t, func(f *Flags) { f.Host = "192.168.8.1"; f.Port = 8080 })
	f.Password = "secret"
	f.Username = "admin"

	cfg := f.ClientConfig()
	if cfg.Host != "192.168.8.1" || cfg.Port != 8080 || cfg.Password != "secret" || cfg.Username != "admin" {
		t.Errorf("ClientConfig() = %+v", cfg)
	}
}

func TestConnectFailsValidation(t *testing.T) {
	f := flagsFor(t, nil)
	if _, err := f.Connect(); err == nil {
		t.Error("Connect succeeded with no host")
	}
}

func TestConnectSucceeds(t *testing.T) {
	f := flagsFor(t, func(f *Flags) { f.Host = "192.0.2.1" })
	f.Password = "secret"

	c, err := f.Connect()
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	if err := c.Close(); err != nil {
		t.Errorf("Close: %v", err)
	}
}

// The OUI database is a re-downloadable cache, so it belongs under XDG_CACHE_HOME.
// It previously sat under XDG_DATA_HOME in a directory named after one of the four
// utilities, which was wrong twice over: wrong base, and a name that outlived the tool.
func TestOUICacheDirHonoursXDG(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", "/tmp/xdg-cache-test")
	got, err := OUICacheDir()
	if err != nil {
		t.Fatalf("OUICacheDir: %v", err)
	}
	if want := filepath.Join("/tmp/xdg-cache-test", "gogl"); got != want {
		t.Errorf("OUICacheDir() = %q, want %q", got, want)
	}
}

func TestOUICacheDirFallsBackToHome(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", "")
	got, err := OUICacheDir()
	if err != nil {
		t.Fatalf("OUICacheDir: %v", err)
	}
	if !strings.HasSuffix(got, filepath.Join(".cache", "gogl")) {
		t.Errorf("OUICacheDir() = %q, want it to end in .cache/gogl", got)
	}
}

// A regular file is not a terminal, so a utility piping output must not prompt.
func TestIsTerminalOnRegularFile(t *testing.T) {
	f, err := os.CreateTemp(t.TempDir(), "not-a-tty")
	if err != nil {
		t.Fatalf("CreateTemp: %v", err)
	}
	defer f.Close()

	if IsTerminal(f) {
		t.Error("IsTerminal(regular file) = true, want false")
	}
}

func TestIsTerminalOnClosedFile(t *testing.T) {
	f, err := os.CreateTemp(t.TempDir(), "closed")
	if err != nil {
		t.Fatalf("CreateTemp: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if IsTerminal(f) {
		t.Error("IsTerminal(closed file) = true, want false")
	}
}

// Setting GL_PASSWD instead of GL_PASSWORD is a real and easy mistake, and the
// resulting failure looks like a wrong password rather than an unset one. The
// error must name the variable the user actually set.
func TestValidateNamesPasswordNearMiss(t *testing.T) {
	for _, name := range passwordNearMisses {
		t.Run(name, func(t *testing.T) {
			f := flagsFor(t, func(f *Flags) { f.Host = "192.168.8.1" })
			t.Setenv(name, "something")

			err := f.Validate()
			if err == nil {
				t.Fatal("Validate succeeded with no GL_PASSWORD")
			}
			if !strings.Contains(err.Error(), name) {
				t.Errorf("error %q does not name the variable that was set", err)
			}
			if !strings.Contains(err.Error(), EnvPassword) {
				t.Errorf("error %q does not name the correct variable", err)
			}
		})
	}
}

// With nothing set at all, the plain message is enough.
func TestValidatePlainMessageWhenNothingSet(t *testing.T) {
	f := flagsFor(t, func(f *Flags) { f.Host = "192.168.8.1" })
	err := f.Validate()
	if err == nil {
		t.Fatal("Validate succeeded with no password")
	}
	if !strings.Contains(err.Error(), EnvPassword) {
		t.Errorf("error %q does not name %s", err, EnvPassword)
	}
}

// An authentication failure must say which credential was used, so a stale or
// mistyped environment variable is visible at a glance.
func TestExplainAnnotatesAuthFailure(t *testing.T) {
	f := flagsFor(t, func(f *Flags) { f.Host = "192.168.8.1" })
	f.Username = "root"
	f.Password = "hunter2!!"

	got := f.Explain(fmt.Errorf("list clients: %w", gogl.ErrUnauthorized))
	if got == nil {
		t.Fatal("Explain returned nil")
	}
	text := got.Error()

	if !strings.Contains(text, `"root"`) {
		t.Errorf("explanation does not name the username:\n%s", text)
	}
	if !strings.Contains(text, "9-character") {
		t.Errorf("explanation does not report the password length:\n%s", text)
	}
	if !strings.Contains(text, EnvPassword) {
		t.Errorf("explanation does not name the source variable:\n%s", text)
	}
	// The password itself must never appear.
	if strings.Contains(text, "hunter2") {
		t.Errorf("explanation leaks the password:\n%s", text)
	}
	// The original error must remain matchable.
	if !errors.Is(got, gogl.ErrUnauthorized) {
		t.Error("Explain broke the error chain")
	}
}

// Anything that is not an auth failure passes through untouched.
func TestExplainLeavesOtherErrorsAlone(t *testing.T) {
	original := errors.New("connection refused")
	if got := f_explain(t, original); got != original {
		t.Errorf("Explain modified a non-auth error: %v", got)
	}
	if got := f_explain(t, nil); got != nil {
		t.Errorf("Explain(nil) = %v, want nil", got)
	}
}

func f_explain(t *testing.T, err error) error {
	t.Helper()
	f := flagsFor(t, func(f *Flags) { f.Host = "192.168.8.1" })
	f.Password = "secret"
	return f.Explain(err)
}

// The username must be resolved before Explain runs, or an auth diagnostic reports
// an empty string instead of the account it actually tried.
func TestValidateResolvesDefaultUsername(t *testing.T) {
	f := flagsFor(t, func(f *Flags) { f.Host = "192.168.8.1" })
	f.Password = "secret"
	if err := f.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if f.Username != gogl.DefaultUsername {
		t.Errorf("Username = %q, want %q", f.Username, gogl.DefaultUsername)
	}

	explained := f.Explain(gogl.ErrUnauthorized).Error()
	if strings.Contains(explained, `username ""`) {
		t.Errorf("diagnostic reports an empty username:\n%s", explained)
	}
	if !strings.Contains(explained, gogl.DefaultUsername) {
		t.Errorf("diagnostic does not name the account tried:\n%s", explained)
	}
}
