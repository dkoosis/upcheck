package upcheck

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

const (
	// prevSuffix names the copy of the binary an update replaces. It sits
	// beside the new one rather than in the cache directory so that somebody
	// whose new build is broken finds the old one where they are already
	// looking, and can run it or rename it back without being told how.
	prevSuffix = ".prev"

	// lockSuffix names the update lock inside the cache directory.
	lockSuffix = ".update.lock"
)

// The two ways an update refuses before it changes anything.
var (
	// ErrLocked reports another update already running against this binary.
	ErrLocked = errors.New("upcheck: an update is already running")
	// ErrNoTarget reports that upcheck could not work out which file `go
	// install` would replace.
	ErrNoTarget = errors.New("upcheck: could not locate the installed binary")
)

// UpdateResult is what an update did, for a caller to print.
type UpdateResult struct {
	// Binary is the file `go install` wrote.
	Binary string
	// Previous is the copy of the binary that was replaced.
	Previous string
	// Version is what the newly installed binary said when it was asked, with
	// surrounding whitespace trimmed. Empty when the new binary could not be
	// run — which is a reported failure, not a silent one.
	Version string
}

// Update installs the published build over the running one.
//
// The order is what makes it safe to interrupt. The lock is taken first, so a
// second update refuses rather than races. The binary is copied to <bin>.prev
// second, so from that moment on there is a working copy on disk no matter what
// happens next. `go install` runs third, under a bounded context. Only then is
// the new binary run, and what it says about itself is the result — because an
// install that reports success and produces a binary that cannot start is a
// failure the caller needs to hear about while the old one is still beside it.
//
// A failure at any step leaves either the old binary or its .prev copy in place,
// and names in the error which one to reach for.
func (c *Checker) Update(ctx context.Context) (UpdateResult, error) {
	var res UpdateResult

	target, err := c.installTarget(ctx)
	if err != nil {
		return res, err
	}
	res.Binary = target
	res.Previous = target + prevSuffix

	unlock, err := c.lock()
	if err != nil {
		return res, err
	}
	defer unlock()

	if err := copyBinary(target, res.Previous); err != nil {
		return res, fmt.Errorf("upcheck: could not keep a copy of the current binary: %w", err)
	}

	installCtx, cancel := context.WithTimeout(ctx, c.cfg.InstallTimeout)
	defer cancel()
	cmd := exec.CommandContext(installCtx, c.goCmd, "install", c.InstallSpec())
	// Out of any module directory, for the same reason Fetch is: inside one,
	// the toolchain would resolve against that module's requirements instead of
	// installing the latest.
	cmd.Dir = os.TempDir()
	cmd.WaitDelay = waitDelay
	if out, err := cmd.CombinedOutput(); err != nil {
		return res, fmt.Errorf("upcheck: %s failed: %w; the previous binary is at %s\n%s",
			c.InstallCommand(), err, res.Previous, strings.TrimSpace(string(out)))
	}

	version, err := c.runVersion(ctx, target)
	if err != nil {
		return res, fmt.Errorf("upcheck: %s was installed but would not run: %w; the previous binary is at %s",
			target, err, res.Previous)
	}
	res.Version = version
	return res, nil
}

// runVersion asks the freshly installed binary what it is. It is a fresh exec
// on purpose: the running process was linked against the old build and can only
// report that one, so anything short of running the new file would be reporting
// the version upcheck hoped for rather than the one on disk.
func (c *Checker) runVersion(ctx context.Context, target string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, c.cfg.FetchTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, target, c.cfg.VersionArgs...)
	cmd.Dir = os.TempDir()
	cmd.WaitDelay = waitDelay
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// installTarget is the file `go install` will write.
//
// It asks the toolchain rather than guessing, because the toolchain is the only
// thing that knows: GOBIN wins when it is set, GOPATH/bin is the fallback, and
// both can come from a config file rather than the environment this process
// happens to have. os.Executable is the last resort, for a binary somebody
// copied somewhere else — and it is a resort rather than the first answer,
// because it names where the old binary is running from, which is not
// necessarily where the new one will land.
func (c *Checker) installTarget(ctx context.Context) (string, error) {
	dir := c.goBinDir(ctx)
	if dir != "" {
		if p := filepath.Join(dir, c.cfg.Binary); isFile(p) {
			return p, nil
		}
	}
	exe, err := os.Executable()
	if err != nil {
		if dir == "" {
			return "", fmt.Errorf("%w: no GOBIN, no GOPATH, and no path to this process", ErrNoTarget)
		}
		// The directory is known and the binary is not in it yet: an install
		// there is still the right move, and the copy step below will find
		// nothing to copy, which it treats as "nothing to preserve".
		return filepath.Join(dir, c.cfg.Binary), nil
	}
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}
	return exe, nil
}

// goBinDir asks `go env` where an install lands. An empty answer means the
// toolchain could not be run or had nothing to say; every caller treats that as
// "fall back", so no error is returned.
func (c *Checker) goBinDir(ctx context.Context) string {
	ctx, cancel := context.WithTimeout(ctx, c.cfg.FetchTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, c.goCmd, "env", "GOBIN", "GOPATH")
	cmd.Dir = os.TempDir()
	cmd.WaitDelay = waitDelay
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	lines := strings.Split(strings.ReplaceAll(string(out), "\r\n", "\n"), "\n")
	if len(lines) > 0 && strings.TrimSpace(lines[0]) != "" {
		return strings.TrimSpace(lines[0])
	}
	if len(lines) > 1 {
		// GOPATH is a list; `go install` writes to the first entry's bin.
		gopath := strings.TrimSpace(lines[1])
		if gopath == "" {
			return ""
		}
		first, _, _ := strings.Cut(gopath, string(os.PathListSeparator))
		if first == "" {
			return ""
		}
		return filepath.Join(first, "bin")
	}
	return ""
}

// isFile reports whether p exists and is a regular file.
func isFile(p string) bool {
	info, err := os.Stat(p)
	return err == nil && info.Mode().IsRegular()
}

// copyBinary writes src to dst through a temporary file in the same directory,
// then renames — so an interrupted copy never leaves a truncated .prev that
// looks like a working fallback. A missing src is not an error: there is
// nothing to preserve, and refusing the install over it would be refusing a
// first install.
func copyBinary(src, dst string) error {
	in, err := os.Open(src) //nolint:gosec // the path is the binary this process was told to update
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	defer func() { _ = in.Close() }()

	info, err := in.Stat()
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(dst), filepath.Base(dst)+".*")
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(tmp.Name()) }()
	if _, err := io.Copy(tmp, in); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Chmod(info.Mode().Perm()); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), dst)
}

// lockPath is the update lock for this binary, inside the cache directory
// rather than beside the binary: the bin directory can be one a user may not
// write to for anything but the install itself, and a lock is not the place to
// discover that.
func (c *Checker) lockPath() string {
	return filepath.Join(c.cfg.CacheDir, c.cfg.Binary+lockSuffix)
}

// lock takes the update lock, or refuses and names who holds it.
//
// O_EXCL is the whole mechanism: on every filesystem worth updating a binary
// on, exactly one of two racing creates succeeds. The loser reads the file and
// puts what it says into the error, because "an update is already running" with
// no way to find out whose is a message that sends somebody hunting.
//
// A stale lock — a holder killed before it could clean up — is left for a
// person. Breaking it automatically would mean choosing a timeout after which
// two concurrent `go install` runs are allowed, and there is no such moment;
// the error names the file so that deleting it is one obvious command.
func (c *Checker) lock() (func(), error) {
	if err := os.MkdirAll(c.cfg.CacheDir, 0o755); err != nil {
		return nil, fmt.Errorf("upcheck: %w", err)
	}
	p := c.lockPath()
	f, err := os.OpenFile(p, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if err != nil {
		if !errors.Is(err, os.ErrExist) {
			return nil, fmt.Errorf("upcheck: could not take the update lock %s: %w", p, err)
		}
		return nil, fmt.Errorf("%w: %s (remove %s if that process is gone)", ErrLocked, holder(p), p)
	}
	host, _ := os.Hostname()
	// A lock that could not be described is still a lock: the file exists, so
	// the second update still refuses. Only the sentence naming the holder is
	// lost, which is why nothing here fails on a write error.
	_, _ = fmt.Fprintf(f, "pid %d on %s since %s\n", os.Getpid(), host, time.Now().Format(time.RFC3339))
	_ = f.Close()
	return func() { _ = os.Remove(p) }, nil
}

// holder reads the lock's description of whoever wrote it, for the error the
// second update returns. An unreadable lock still refuses the update; it just
// cannot say more than that.
func holder(p string) string {
	data, err := os.ReadFile(p) //nolint:gosec // the path is this package's own lock file
	if err != nil || len(data) == 0 {
		return "held by an unnamed process"
	}
	return "held by " + strings.TrimSpace(string(data))
}
