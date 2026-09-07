package upcheck

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// fakeInstall stands in for a bin directory holding an installed binary. The
// binary is a shell script that prints its version, so a test can tell which
// build is on disk by running it — which is the only honest way to check that
// an update replaced one file and preserved another.
type fakeInstall struct {
	dir    string // the bin directory `go install` writes to
	binary string // the installed binary's path
	prev   string // where the copy of the replaced binary belongs
}

func newFakeInstall(t *testing.T, version string) fakeInstall {
	t.Helper()
	f := fakeInstall{dir: t.TempDir()}
	f.binary = filepath.Join(f.dir, testBinary)
	f.prev = f.binary + prevSuffix
	writeFakeBinary(t, f.binary, version)
	return f
}

// writeFakeBinary writes a script that reports the version it was given.
func writeFakeBinary(t *testing.T, path, version string) {
	t.Helper()
	script := fmt.Sprintf("#!/bin/sh\necho %s %s\n", testBinary, version)
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil { //nolint:gosec // a fake binary must be executable
		t.Fatalf("writing the fake binary: %v", err)
	}
}

// runFake runs one of the fake binaries and returns the version it reports.
// Running it, rather than reading it, is the point: the question these tests
// ask is which build is on disk and starts.
func runFake(t *testing.T, path string) string {
	t.Helper()
	out, err := exec.CommandContext(t.Context(), path).Output()
	if err != nil {
		t.Fatalf("running %s: %v", path, err)
	}
	fields := strings.Fields(string(out))
	if len(fields) == 0 {
		return ""
	}
	return fields[len(fields)-1]
}

// goStub builds a toolchain stub for the update tests: it answers `go env` with
// the fake bin directory, and runs installBody for `go install`.
func goStub(t *testing.T, f fakeInstall, installBody string) string {
	t.Helper()
	return stubGo(t, fmt.Sprintf(`case "$1" in
  env) printf '%s\n\n' ;;
  install) %s ;;
  list) printf '{"Version":"%s"}' ;;
  *) exit 1 ;;
esac`, f.dir, installBody, newerVersion))
}

// installsVersion is an install that succeeds: it writes a new binary reporting
// the version it was given.
func installsVersion(f fakeInstall, version string) string {
	return fmt.Sprintf(`{ echo '#!/bin/sh'; echo 'echo %s %s'; } > '%s' && chmod 755 '%s'`,
		testBinary, version, f.binary, f.binary)
}

func TestUpdateKeepsTheReplacedBinaryBesideTheNewOne(t *testing.T) {
	c := newChecker(t)
	f := newFakeInstall(t, currentVersion)
	c.SetGoCmdForTest(t, goStub(t, f, installsVersion(f, newerVersion)))

	res, err := c.Update(t.Context())
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if res.Binary != f.binary {
		t.Errorf("Update wrote %q, want %q", res.Binary, f.binary)
	}
	if res.Previous != f.prev {
		t.Errorf("the previous binary is at %q, want %q", res.Previous, f.prev)
	}
	if !strings.Contains(res.Version, newerVersion) {
		t.Errorf("Update reported %q, which does not name the version it installed (%s)", res.Version, newerVersion)
	}
	if !isFile(f.prev) {
		t.Fatalf("no copy of the replaced binary at %s", f.prev)
	}
	if got := runFake(t, f.prev); got != currentVersion {
		t.Errorf("the .prev copy is %s, want the build that was replaced (%s)", got, currentVersion)
	}
	if got := runFake(t, f.binary); got != newerVersion {
		t.Errorf("the installed binary is %s, want %s", got, newerVersion)
	}
	// The report has to come from running the new file. Anything short of that
	// would report the version upcheck hoped for rather than the one on disk.
	if !strings.HasPrefix(res.Version, testBinary+" ") {
		t.Errorf("Update reported %q, which is not the new binary's own output", res.Version)
	}
}

func TestUpdateSurvivesAnInstallThatDiesHalfway(t *testing.T) {
	c := newChecker(t)
	f := newFakeInstall(t, currentVersion)
	// The worst case: the install truncates the binary it was replacing and
	// then fails. Whether a real `go install` can do this is beside the point —
	// the .prev copy is what makes it survivable either way.
	c.SetGoCmdForTest(t, goStub(t, f, fmt.Sprintf(`echo '#!/bin/sh -- half a bi' > '%s'; exit 137`, f.binary)))

	if _, err := c.Update(t.Context()); err == nil {
		t.Fatal("Update reported success after the install died")
	}
	if !isFile(f.prev) {
		t.Fatalf("the install died and left no copy at %s", f.prev)
	}
	if got := runFake(t, f.prev); got != currentVersion {
		t.Errorf("the .prev copy is %s, want the build that was running (%s)", got, currentVersion)
	}
	info, err := os.Stat(f.prev)
	if err != nil {
		t.Fatalf("stat %s: %v", f.prev, err)
	}
	if info.Mode().Perm()&0o111 == 0 {
		t.Error("the .prev copy is not executable, so it is not a binary anyone can fall back to")
	}
}

func TestUpdateNamesTheHolderOfTheLock(t *testing.T) {
	c := newChecker(t)
	f := newFakeInstall(t, currentVersion)
	c.SetGoCmdForTest(t, goStub(t, f, installsVersion(f, newerVersion)))

	// A first update, still running: its lock is on disk.
	held := "pid 4242 on someone-elses-laptop since 2026-09-06T12:00:00Z"
	if err := os.MkdirAll(filepath.Dir(c.LockPathForTest()), 0o755); err != nil {
		t.Fatalf("making the cache directory: %v", err)
	}
	if err := os.WriteFile(c.LockPathForTest(), []byte(held+"\n"), 0o600); err != nil {
		t.Fatalf("planting the lock: %v", err)
	}

	_, err := c.Update(t.Context())
	if !errors.Is(err, ErrLocked) {
		t.Fatalf("Update error = %v, want %v", err, ErrLocked)
	}
	if !strings.Contains(err.Error(), "pid 4242") {
		t.Errorf("the refusal %q does not name who holds the lock", err)
	}
	if !strings.Contains(err.Error(), c.LockPathForTest()) {
		t.Errorf("the refusal %q does not name the lock file to remove", err)
	}
	// A refused update changes nothing.
	if isFile(f.prev) {
		t.Error("a refused update still copied the binary")
	}
	if got := runFake(t, f.binary); got != currentVersion {
		t.Errorf("a refused update replaced the binary: it is now %s", got)
	}
}

func TestUpdateReleasesTheLock(t *testing.T) {
	c := newChecker(t)
	f := newFakeInstall(t, currentVersion)
	c.SetGoCmdForTest(t, goStub(t, f, installsVersion(f, newerVersion)))

	if _, err := c.Update(t.Context()); err != nil {
		t.Fatalf("Update: %v", err)
	}
	if isFile(c.LockPathForTest()) {
		t.Fatal("the lock outlived the update that took it; the next one would refuse forever")
	}
	// And so the next update runs.
	if _, err := c.Update(t.Context()); err != nil {
		t.Fatalf("the second Update: %v", err)
	}
}

func TestUpdateReportsANewBinaryThatWillNotRun(t *testing.T) {
	c := newChecker(t)
	f := newFakeInstall(t, currentVersion)
	// An install that succeeds and produces something that cannot start. The
	// caller has to hear about it while the old binary is still beside it.
	c.SetGoCmdForTest(t, goStub(t, f, fmt.Sprintf(`echo 'not a binary' > '%s'; chmod 000 '%s'`, f.binary, f.binary)))

	res, err := c.Update(t.Context())
	if err == nil {
		t.Fatal("Update reported success for a binary that will not run")
	}
	if !strings.Contains(err.Error(), f.prev) {
		t.Errorf("the failure %q does not say where the previous binary is", err)
	}
	if res.Version != "" {
		t.Errorf("Update reported version %q for a binary it could not run", res.Version)
	}
	if !isFile(f.prev) {
		t.Error("no previous binary to fall back to")
	}
}

func TestInstallTargetAsksTheToolchain(t *testing.T) {
	gobin := t.TempDir()
	gopath := t.TempDir()
	if err := os.MkdirAll(filepath.Join(gopath, "bin"), 0o755); err != nil {
		t.Fatalf("making the fake GOPATH bin: %v", err)
	}
	writeFakeBinary(t, filepath.Join(gobin, testBinary), currentVersion)
	writeFakeBinary(t, filepath.Join(gopath, "bin", testBinary), olderVersion)

	cases := []struct {
		name string
		env  string // what `go env GOBIN GOPATH` prints
		want string
	}{
		{
			name: "GOBIN wins when it is set",
			env:  fmt.Sprintf(`printf '%s\n%s\n'`, gobin, gopath),
			want: filepath.Join(gobin, testBinary),
		},
		{
			name: "GOPATH/bin is the fallback",
			env:  fmt.Sprintf(`printf '\n%s\n'`, gopath),
			want: filepath.Join(gopath, "bin", testBinary),
		},
		{
			name: "the first GOPATH entry is the one an install writes to",
			env:  fmt.Sprintf(`printf '\n%s%c/nowhere\n'`, gopath, os.PathListSeparator),
			want: filepath.Join(gopath, "bin", testBinary),
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := newChecker(t)
			c.SetGoCmdForTest(t, stubGo(t, fmt.Sprintf("case \"$1\" in\n  env) %s ;;\n  *) exit 1 ;;\nesac", tc.env)))
			got, err := c.installTarget(t.Context())
			if err != nil {
				t.Fatalf("installTarget: %v", err)
			}
			if got != tc.want {
				t.Errorf("installTarget = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestInstallTargetFallsBackToThisProcess(t *testing.T) {
	// No toolchain to ask, and the binary this process is running is the only
	// thing left to name.
	c := newChecker(t)
	c.SetGoCmdForTest(t, stubGo(t, `exit 1`))

	got, err := c.installTarget(t.Context())
	if err != nil {
		t.Fatalf("installTarget: %v", err)
	}
	exe, err := os.Executable()
	if err != nil {
		t.Skip("this platform cannot name its own executable")
	}
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}
	if got != exe {
		t.Errorf("installTarget = %q, want this process at %q", got, exe)
	}
}

func TestCopyBinaryTreatsAMissingSourceAsNothingToPreserve(t *testing.T) {
	// A first install has no binary to copy, and refusing over that would be
	// refusing the install.
	dir := t.TempDir()
	if err := copyBinary(filepath.Join(dir, "absent"), filepath.Join(dir, "absent.prev")); err != nil {
		t.Errorf("copyBinary over a missing source: %v", err)
	}
	if isFile(filepath.Join(dir, "absent.prev")) {
		t.Error("copyBinary invented a .prev out of a file that was not there")
	}
}

// AC (mn-o9o): two updates of one binary started together — the second exits
// non-zero naming the lock holder.
//
// TestUpdateNamesTheHolderOfTheLock above plants a lock file and watches an
// update refuse it, which is the same code path but not the same claim: it
// proves the refusal, not that two updates racing for the lock cannot both take
// it. This one runs a real second update while a real first one holds the lock.
//
// The overlap is arranged rather than raced. Two goroutines started at the same
// moment may not overlap at all — the first can finish before the second is
// scheduled — and a test that passes because nothing collided is worse than no
// test. So the first update's install blocks until the second has had its turn,
// which makes contention certain instead of likely. The lock itself is taken
// with O_CREATE|O_EXCL, so what happens when two arrive at once is the file
// system's answer, not this test's.
func TestUpdateRefusesASecondUpdateWhileTheFirstIsRunning(t *testing.T) {
	first := newChecker(t)
	f := newFakeInstall(t, currentVersion)

	// The install signals that it is running, then waits to be released — so
	// the first update is holding the lock for as long as the second needs.
	running := filepath.Join(f.dir, "install-running")
	release := filepath.Join(f.dir, "install-may-finish")
	first.SetGoCmdForTest(t, goStub(t, f, fmt.Sprintf(
		`touch '%s'; i=0; while [ ! -f '%s' ] && [ "$i" -lt 60 ]; do sleep 0.05; i=$((i+1)); done; %s`,
		running, release, installsVersion(f, newerVersion))))

	// A second Checker over the same cache directory and the same binary is
	// what a second process running `update` looks like from here.
	second, err := New(Config{
		Module:         testModule,
		Binary:         testBinary,
		CacheDir:       first.CacheDir(),
		InstallTimeout: 5 * time.Second,
		VersionArgs:    []string{"version"},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	second.SetBuildForTest(t, Build{Version: currentVersion})
	second.SetGoCmdForTest(t, goStub(t, f, installsVersion(f, currentVersion)))

	firstDone := make(chan error, 1)
	go func() {
		_, err := first.Update(context.WithoutCancel(t.Context()))
		firstDone <- err
	}()
	waitForFile(t, running)

	_, err = second.Update(t.Context())
	if !errors.Is(err, ErrLocked) {
		t.Fatalf("the second update returned %v, want %v", err, ErrLocked)
	}
	if !strings.Contains(err.Error(), fmt.Sprintf("pid %d", os.Getpid())) {
		t.Errorf("the refusal %q does not name the process holding the lock", err)
	}
	if !strings.Contains(err.Error(), second.LockPathForTest()) {
		t.Errorf("the refusal %q does not name the lock file to remove", err)
	}

	if err := os.WriteFile(release, nil, 0o600); err != nil {
		t.Fatalf("releasing the first install: %v", err)
	}
	if err := <-firstDone; err != nil {
		t.Fatalf("the first update failed: %v", err)
	}
	// The refused update installed nothing: the binary is what the first update
	// put there, not what the second would have.
	if got := runFake(t, f.binary); got != newerVersion {
		t.Errorf("the binary is %s, want the first update's %s", got, newerVersion)
	}
	// And the lock is gone, so a third update is not refused forever.
	if isFile(first.LockPathForTest()) {
		t.Errorf("the lock survived the update that held it: %s", first.LockPathForTest())
	}
}

// waitForFile blocks until path exists, and fails the test rather than hanging
// if it never does.
func waitForFile(t *testing.T, path string) {
	t.Helper()
	for range 200 {
		if isFile(path) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("waiting for %s: it never appeared", path)
}
