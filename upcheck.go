// Package upcheck tells a Go program whether a newer build of itself has been
// published, and installs that build on request.
//
// It exists because every tool in a fleet installed with `go install
// <module>/cmd/<bin>@latest` has the same two problems and no way to notice
// either one: the binary on disk has no idea a newer tag exists, and the person
// running it has no reason to think about it.
//
// The design is shaped by one constraint, which is that a currency check must
// never be part of what a caller waited for. Asking the module proxy what is
// published costs a subprocess and a network round trip — more than a
// command-line tool's whole latency budget on a good connection, and unbounded
// on a bad one. So the question is split across time. [Checker.Notice] reads
// what the last check left behind in a stamp file, which costs one file read.
// [Checker.Check] starts a detached child to do the resolving when the stamp is
// missing or old, and returns without waiting for it. The consequence is worth
// naming: the first run after an install says nothing, because nothing has been
// checked yet, and that is correct rather than unfortunate — a binary installed
// a moment ago is current.
//
// [Checker.Update] is the other half: it runs `go install` for the caller,
// keeping the binary it replaces beside the new one as <bin>.prev.
//
// The surface is one type and four methods on it. [New] builds a [Checker]
// from a [Config] naming the module and the binary. Then: Check starts a
// background resolution when one is due, ReadStamp returns what the last one
// found, Notice turns that into the line to print, and Update installs the
// newer build.
//
// The whole package depends on nothing outside the standard library. A program
// that reaches for a self-updater is often the only thing standing between a
// user and a broken install; adding a dependency graph to it would be a poor
// trade.
package upcheck

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"
)

// Defaults for the three durations in [Config]. Each is a policy rather than a
// tuning knob, so each is documented where the policy lives.
const (
	// DefaultStaleAfter is how long a stamp is believed before a fresh check is
	// worth starting. A day: the notice is advice about a tool the caller is
	// already running successfully, so learning it late costs nothing, while
	// resolving on every call would spend a subprocess and a network round trip
	// to be told the same thing.
	DefaultStaleAfter = 24 * time.Hour

	// DefaultFetchTimeout bounds the detached child that resolves the latest
	// version. Nobody is waiting for it, so the bound exists to stop a hung
	// resolution from leaving a process behind for the rest of the session
	// rather than to protect anyone's latency — which is why it is generous
	// enough for a cold `go list` that has to reach a version control host
	// instead of a warm module cache.
	DefaultFetchTimeout = 60 * time.Second

	// DefaultInstallTimeout bounds `go install`. Unlike the fetch, somebody is
	// waiting for this one, but they asked for it: a compile of a whole module
	// and its dependencies is minutes on a cold cache, and killing it at the
	// one-minute mark would turn a slow success into a failure.
	DefaultInstallTimeout = 10 * time.Minute
)

// Config describes the program upcheck is answering about. Everything but
// Module and Binary has a default worth taking.
type Config struct {
	// Module is the module path `go install` resolves, such as
	// "github.com/dkoosis/mnemd". Required.
	Module string

	// Binary is the name of the installed command — the last element of
	// <Module>/cmd/<Binary>, and the name of the file on disk. Required.
	Binary string

	// CommandPath is the package path the install builds, relative to Module.
	// Defaults to "cmd/" + Binary, which is where every repo in this fleet puts
	// it. A module whose command is at its root sets this to "." and gets an
	// install command naming the module itself.
	CommandPath string

	// CacheDir is where the stamp and the update lock live. Defaults to
	// <os.UserCacheDir>/<Binary>. A program that already keeps a cache
	// directory should pass it, so that one directory holds everything it
	// derives.
	CacheDir string

	// SilenceEnv names the environment variable that turns the whole mechanism
	// off: no stamp read, no notice, no child. Defaults to
	// <BINARY>_NO_UPDATE_CHECK, uppercased with non-alphanumerics folded to
	// underscores. For CI, for a packaged build somebody else keeps current,
	// and for anyone who finds the notice unwelcome.
	SilenceEnv string

	// RefreshArgs is what the detached child is run with, and it must be a
	// command that resolves the latest version and writes the stamp — the child
	// is a copy of the caller's own binary. Defaults to
	// {"version", "--refresh"}.
	RefreshArgs []string

	// VersionArgs is what [Checker.Update] runs the newly installed binary with
	// to report the version it just installed. Defaults to {"--version"}.
	VersionArgs []string

	// StaleAfter, FetchTimeout and InstallTimeout override the Default
	// constants above. Zero means the default.
	StaleAfter     time.Duration
	FetchTimeout   time.Duration
	InstallTimeout time.Duration
}

// Checker answers the currency questions for one program. Build one at startup
// and keep it; it holds no state beyond its configuration, and every method on
// it is safe to call from more than one goroutine.
type Checker struct {
	cfg Config

	// goCmd is the toolchain upcheck shells out to. A field rather than a
	// constant so the tests can put a stub in front of the real one; nothing
	// outside this package writes it.
	goCmd string

	// buildOf names the running build. A field for one reason: a test binary
	// carries no module version by construction, so a package that could not
	// put an installed build in front of this code could never exercise the
	// notice at all. Production always reads Current.
	buildOf func() Build

	// spawn starts the detached refresh. A field for the same reason buildOf
	// is: Check's one decision — is a check worth starting — is worth testing
	// without starting processes.
	spawn func(context.Context)
}

// ErrConfig reports a Config that names no program to check.
var ErrConfig = errors.New("upcheck: config")

// New validates cfg, fills in its defaults, and returns the Checker.
//
// It returns an error rather than panicking on a bad config because the two
// required fields are usually constants in the calling program, and a caller
// who wires them wrong deserves to find out at startup with a sentence rather
// than in a stack trace.
func New(cfg Config) (*Checker, error) {
	if cfg.Module == "" {
		return nil, fmt.Errorf("%w: Module is required", ErrConfig)
	}
	if cfg.Binary == "" {
		return nil, fmt.Errorf("%w: Binary is required", ErrConfig)
	}
	if strings.ContainsAny(cfg.Binary, `/\`) {
		return nil, fmt.Errorf("%w: Binary %q is a file name, not a path", ErrConfig, cfg.Binary)
	}
	if cfg.CommandPath == "" {
		cfg.CommandPath = "cmd/" + cfg.Binary
	}
	if cfg.SilenceEnv == "" {
		cfg.SilenceEnv = defaultSilenceEnv(cfg.Binary)
	}
	if cfg.CacheDir == "" {
		dir, err := os.UserCacheDir()
		if err != nil {
			return nil, fmt.Errorf("%w: no CacheDir and no user cache directory: %w", ErrConfig, err)
		}
		cfg.CacheDir = filepath.Join(dir, cfg.Binary)
	}
	if len(cfg.RefreshArgs) == 0 {
		cfg.RefreshArgs = []string{"version", "--refresh"}
	}
	if len(cfg.VersionArgs) == 0 {
		cfg.VersionArgs = []string{"--version"}
	}
	if cfg.StaleAfter == 0 {
		cfg.StaleAfter = DefaultStaleAfter
	}
	if cfg.FetchTimeout == 0 {
		cfg.FetchTimeout = DefaultFetchTimeout
	}
	if cfg.InstallTimeout == 0 {
		cfg.InstallTimeout = DefaultInstallTimeout
	}
	c := &Checker{cfg: cfg, goCmd: "go", buildOf: Current}
	c.spawn = c.spawnRefresh
	return c, nil
}

// defaultSilenceEnv turns a binary name into an environment variable name:
// upper case, with everything that cannot appear in one folded to an
// underscore, so that a command called "go-tool" gets GO_TOOL_NO_UPDATE_CHECK
// rather than a variable no shell can set.
func defaultSilenceEnv(binary string) string {
	var b strings.Builder
	for _, r := range binary {
		switch {
		case r >= 'a' && r <= 'z':
			b.WriteRune(r - 'a' + 'A')
		case (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9'):
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	return b.String() + "_NO_UPDATE_CHECK"
}

// Module is the module path this Checker answers about.
func (c *Checker) Module() string { return c.cfg.Module }

// Binary is the name of the installed command.
func (c *Checker) Binary() string { return c.cfg.Binary }

// InstallSpec is the argument `go install` is given: the command's package path
// at @latest.
func (c *Checker) InstallSpec() string {
	pkg := c.cfg.Module
	if c.cfg.CommandPath != "." {
		pkg = path.Join(pkg, c.cfg.CommandPath)
	}
	return pkg + "@latest"
}

// InstallCommand is what a caller who is behind should run by hand. It ships
// inside the notice on purpose: a caller told it is behind and not told the
// cure has been handed a chore instead of an answer.
func (c *Checker) InstallCommand() string {
	return "go install " + c.InstallSpec()
}

// SilenceEnv names the environment variable that turns the mechanism off.
func (c *Checker) SilenceEnv() string { return c.cfg.SilenceEnv }

// Silenced reports whether the caller has turned the mechanism off.
func (c *Checker) Silenced() bool { return os.Getenv(c.cfg.SilenceEnv) != "" }

// CacheDir is the directory holding the stamp and the update lock.
func (c *Checker) CacheDir() string { return c.cfg.CacheDir }
