package upcheck

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"time"
)

// The two ways the resolution can finish without an answer. Both mean the same
// thing to every caller — the program does not know what is published — and the
// background check swallows both, so they exist to be readable in a hand-run
// refresh rather than to be branched on.
var (
	// ErrResolveFailed reports a toolchain that could not answer: no Go
	// installed, no network, no credentials for a private module.
	ErrResolveFailed = errors.New("upcheck: could not resolve the latest version")
	// ErrNotAVersion reports an answer that is not a version this package can
	// compare against.
	ErrNotAVersion = errors.New("upcheck: the answer was not a version")
)

// waitDelay is how long a command waits, after its context is done and the
// toolchain has been killed, before closing the pipes out from under whatever
// the toolchain left running. Long enough that a process shutting down normally
// finishes writing; short enough that the timeout means something. See the note
// in Fetch.
const waitDelay = time.Second

// Notice is the one line a caller prints when a newer build has been published:
// what is available, what is running, and the command that closes the gap. The
// second return is false when there is nothing to say, which is the common case.
//
// It reads one small file and never the network, so it costs microseconds
// rather than a round trip. What it cannot do is be right about a build nobody
// has checked yet: the first call after an install is silent, and the one after
// that is informed.
//
// Print it on stderr. The notice is about the program, not about what the
// program was asked for, and a caller parsing stdout must not have to filter it
// out.
func (c *Checker) Notice() (string, bool) {
	if c.Silenced() {
		return "", false
	}
	b := c.buildOf()
	// A checkout build has no published version it could be behind, and telling
	// a developer to `go install` over the build they just made would be wrong.
	// They have the repo; they do not need telling.
	if !b.Installed() {
		return "", false
	}
	s, ok := c.ReadStamp()
	if !ok || !b.Behind(s) {
		return "", false
	}
	return fmt.Sprintf("%s: %s is available, running %s; get it with '%s'",
		c.cfg.Binary, s.Latest, b.Version, c.InstallCommand()), true
}

// Check starts a currency check when the stamp is missing or old, and returns
// immediately.
//
// It never resolves anything on the caller's path. The resolving happens in a
// detached copy of the caller's own binary — run with Config.RefreshArgs, which
// must name a command that calls [Checker.Refresh] — and nobody waits for that
// child. A goroutine would not do: the process usually exits long before a
// module resolution finishes. So the fetch moves out of the call altogether,
// and the next run is the one that benefits.
//
// Every failure here is silent. Nothing the caller asked for depends on this
// succeeding, so a machine that cannot spawn a child gets the answer it came
// for and no notice — the same outcome as a machine that has simply not checked
// yet.
func (c *Checker) Check(ctx context.Context) {
	if c.Silenced() {
		return
	}
	if !c.buildOf().Installed() {
		return
	}
	if s, ok := c.ReadStamp(); ok && !c.Stale(s, time.Now()) {
		return
	}
	c.spawn(ctx)
}

// spawnRefresh starts the detached child. It is separate from Check so that
// Check's decision — is a check worth starting at all — can be tested without
// starting processes, and so that the process work is one small function to
// read.
func (c *Checker) spawnRefresh(ctx context.Context) {
	exe, err := os.Executable()
	if err != nil {
		return
	}
	// WithoutCancel, and deliberately: the child is meant to outlive the call
	// that started it, so a context that dies with the verb would kill the one
	// thing the child exists to do. What bounds it is Config.FetchTimeout,
	// inside the child, where the waiting actually happens.
	cmd := exec.CommandContext(context.WithoutCancel(ctx), exe, c.cfg.RefreshArgs...)
	// The child inherits nothing. It has no terminal to write to, no input to
	// read and no answer anybody is waiting for, so all three streams go to the
	// null device rather than into the parent's output.
	cmd.Stdin, cmd.Stdout, cmd.Stderr = nil, nil, nil
	if err := cmd.Start(); err != nil {
		return
	}
	// Release drops the parent's claim on the child, so the parent exits
	// without waiting and without leaving a zombie behind.
	_ = cmd.Process.Release()
}

// Refresh resolves what is published and records it, returning the stamp it
// wrote. It is the whole job of the detached child [Checker.Check] starts, and
// it is also what a hand-run refresh verb should call, so that the background
// work is something a person can run, watch and debug.
//
// A resolution that fails still writes a stamp: the attempt is recorded with
// whatever the last successful check found, so one bad resolution never erases
// a good answer and never leaves the mechanism to fork a child on every call.
// The returned error is the resolution's; the stamp write's error, if the two
// differ, is joined onto it.
func (c *Checker) Refresh(ctx context.Context) (Stamp, error) {
	ctx, cancel := context.WithTimeout(ctx, c.cfg.FetchTimeout)
	defer cancel()

	s, _ := c.ReadStamp()
	s.Checked = time.Now()
	latest, ferr := c.Fetch(ctx)
	if ferr == nil {
		s.Latest = latest
	}
	if werr := c.WriteStamp(s); werr != nil {
		return s, errors.Join(ferr, werr)
	}
	return s, ferr
}

// Fetch asks the Go toolchain what version `go install <spec>` would install
// right now. It blocks on the network; only [Checker.Refresh] and a caller who
// has decided to wait should call it.
//
// `go list -m -json <module>@latest`, rather than a request to
// proxy.golang.org, because the toolchain resolves the way the install will: it
// reads GOPROXY, GOPRIVATE and GONOSUMDB, and for a module those rules keep off
// the public proxy it falls through to the version control host with the
// caller's own credentials. A private module answers 404 forever on the public
// proxy, so a check built on an HTTP request would be silently inert on exactly
// the machines it was built for (measured 2026-09-06).
//
// The cost is a subprocess and a dependency on the toolchain being installed.
// Neither is a real constraint: the only cure this package ever recommends is
// `go install`, so a machine that cannot run `go` could not act on the answer
// anyway.
func (c *Checker) Fetch(ctx context.Context) (string, error) {
	cmd := exec.CommandContext(ctx, c.goCmd, "list", "-m", "-json", c.cfg.Module+"@latest")
	// A module directory would make the toolchain resolve against that module's
	// requirements rather than answer about the latest. Asking from the temp
	// directory keeps the question the one that was asked.
	cmd.Dir = os.TempDir()
	// Without this, cancelling the context does not end the wait. `go list`
	// runs its own children — git, for a module the public proxy will not
	// answer for — and they inherit the pipe Output reads. Killing `go list`
	// leaves that pipe open in the grandchild, and Wait blocks on the copy
	// until the grandchild exits on its own. The timeout would then bound
	// nothing, which is the one job it has here.
	cmd.WaitDelay = waitDelay
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("%w: %w", ErrResolveFailed, err)
	}
	var body struct {
		Version string `json:"Version"`
	}
	if err := json.Unmarshal(out, &body); err != nil {
		return "", fmt.Errorf("%w: %w", ErrResolveFailed, err)
	}
	if !isValidSemver(body.Version) {
		return "", fmt.Errorf("%w: %q", ErrNotAVersion, body.Version)
	}
	return body.Version, nil
}
