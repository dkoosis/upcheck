package upcheck

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

const (
	// testModule and testBinary stand in for a real fleet tool. Nothing in the
	// tests reaches the network, so the module never has to exist.
	testModule = "github.com/dkoosis/example"
	testBinary = "example"

	// The three versions the tests compare. Named rather than inlined so that a
	// test reads as behind/current/ahead instead of as three numbers.
	olderVersion   = "v0.1.0"
	currentVersion = "v0.2.0"
	newerVersion   = "v0.3.0"
)

// newChecker builds a Checker whose cache directory is this test's own, whose
// silence variable is guaranteed unset, and whose build identity is an
// installed one — the shape every currency question is asked about.
//
// It calls t.Setenv, so no test using it may run in parallel. That is the price
// of a mechanism whose off switch is an environment variable, and it is cheap:
// nothing here is slow.
func newChecker(t *testing.T) *Checker {
	t.Helper()
	c, err := New(Config{
		Module:   testModule,
		Binary:   testBinary,
		CacheDir: t.TempDir(),
		// Short, because two tests deliberately wait for a stubbed toolchain to
		// be killed and neither should hold the suite for a minute.
		FetchTimeout:   5 * time.Second,
		InstallTimeout: 5 * time.Second,
		VersionArgs:    []string{"version"},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Setenv(c.SilenceEnv(), "")
	c.SetBuildForTest(t, Build{Version: currentVersion})
	return c
}

// stubGo writes a shell script that answers like the Go toolchain and returns
// its path, for a Checker to run instead of the real one. The body is a case
// statement on $1; every stub in this package is written the same way so that
// what one stub does differently is visible on sight.
func stubGo(t *testing.T, body string) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the toolchain stubs are shell scripts")
	}
	path := filepath.Join(t.TempDir(), "go")
	script := "#!/bin/sh\n" + body + "\n"
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil { //nolint:gosec // a test stub must be executable
		t.Fatalf("writing the toolchain stub: %v", err)
	}
	return path
}

func TestNewRequiresModuleAndBinary(t *testing.T) {
	cases := []struct {
		name string
		cfg  Config
	}{
		{"no module", Config{Binary: testBinary}},
		{"no binary", Config{Module: testModule}},
		{"binary is a path", Config{Module: testModule, Binary: "bin/example"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := New(tc.cfg); err == nil {
				t.Fatal("New accepted a config that names no program to check")
			}
		})
	}
}

func TestNewDefaults(t *testing.T) {
	c, err := New(Config{Module: testModule, Binary: "go-tool", CacheDir: t.TempDir()})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if got, want := c.SilenceEnv(), "GO_TOOL_NO_UPDATE_CHECK"; got != want {
		t.Errorf("SilenceEnv = %q, want %q", got, want)
	}
	if got, want := c.InstallCommand(), "go install "+testModule+"/cmd/go-tool@latest"; got != want {
		t.Errorf("InstallCommand = %q, want %q", got, want)
	}
}

func TestInstallCommandForARootCommand(t *testing.T) {
	c, err := New(Config{
		Module:      testModule,
		Binary:      testBinary,
		CommandPath: ".",
		CacheDir:    t.TempDir(),
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if got, want := c.InstallCommand(), "go install "+testModule+"@latest"; got != want {
		t.Errorf("InstallCommand = %q, want %q", got, want)
	}
}

func TestInstalledRejectsACheckoutBuild(t *testing.T) {
	// The case this test exists for: since Go 1.24 a checkout build carries a
	// synthesised pseudo-version, so the version string alone would call a
	// developer's own working binary installed. The VCS stamp is what separates
	// them.
	checkout := Build{
		Version:  "v0.0.0-20260906000929-155985eecb4d+dirty",
		Revision: "155985eecb4d5b3a",
		Modified: true,
	}
	if checkout.Installed() {
		t.Error("a build stamped with a VCS revision was called installed")
	}
	if (Build{Version: devel}).Installed() {
		t.Error("a (devel) build was called installed")
	}
	if (Build{}).Installed() {
		t.Error("a build with no version at all was called installed")
	}
	if !(Build{Version: currentVersion}).Installed() {
		t.Error("a build resolved from a module version was not called installed")
	}
}

func TestBuildString(t *testing.T) {
	cases := []struct {
		name string
		b    Build
		want string
	}{
		{"installed", Build{Version: currentVersion}, currentVersion},
		{"unknown", Build{}, "unknown"},
		{
			"checkout",
			Build{Version: devel, Revision: "155985eecb4d5b3a", Time: "2026-09-06T00:09:29Z", Modified: true},
			"(devel)  155985eecb4d+dirty  2026-09-06T00:09:29Z",
		},
		{
			// The version already says dirty; saying it twice reads as two
			// separate facts.
			"pseudo-version already dirty",
			Build{Version: "v0.0.0-20260906000929-155985eecb4d+dirty", Revision: "155985eecb4d5b3a", Modified: true},
			"v0.0.0-20260906000929-155985eecb4d+dirty  155985eecb4d",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.b.String(); got != tc.want {
				t.Errorf("String() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestBehind(t *testing.T) {
	installed := Build{Version: currentVersion}
	cases := []struct {
		name string
		b    Build
		s    Stamp
		want bool
	}{
		{"newer published", installed, Stamp{Latest: newerVersion}, true},
		{"same version", installed, Stamp{Latest: currentVersion}, false},
		{"older published", installed, Stamp{Latest: olderVersion}, false},
		{"no version recorded", installed, Stamp{}, false},
		{
			"a checkout build is never behind",
			Build{Version: currentVersion, Revision: "abc123"},
			Stamp{Latest: newerVersion},
			false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.b.Behind(tc.s); got != tc.want {
				t.Errorf("Behind = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestStampRoundTrip(t *testing.T) {
	c := newChecker(t)
	want := Stamp{Latest: newerVersion, Checked: time.Now().Truncate(time.Second)}
	if err := c.WriteStamp(want); err != nil {
		t.Fatalf("WriteStamp: %v", err)
	}
	got, ok := c.ReadStamp()
	if !ok {
		t.Fatal("ReadStamp found nothing after a write")
	}
	if got.Latest != want.Latest || !got.Checked.Equal(want.Checked) {
		t.Errorf("ReadStamp = %+v, want %+v", got, want)
	}
}

func TestReadStampIsSilentOnEveryFailure(t *testing.T) {
	cases := []struct {
		name    string
		content string
		write   bool
	}{
		{name: "no stamp at all"},
		{name: "truncated json", content: `{"latest":"v0.`, write: true},
		{name: "no check recorded", content: `{"latest":"v0.3.0"}`, write: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := newChecker(t)
			if tc.write {
				if err := os.WriteFile(c.StampPath(), []byte(tc.content), 0o600); err != nil {
					t.Fatalf("seeding the stamp: %v", err)
				}
			}
			if _, ok := c.ReadStamp(); ok {
				t.Error("ReadStamp reported a stamp it should have treated as absent")
			}
		})
	}
}

func TestStale(t *testing.T) {
	c := newChecker(t)
	now := time.Now()
	cases := []struct {
		name    string
		checked time.Time
		want    bool
	}{
		{"just checked", now.Add(-time.Minute), false},
		{"a day old", now.Add(-25 * time.Hour), true},
		// A clock that moved backwards would otherwise silence the check
		// forever.
		{"from the future", now.Add(time.Hour), true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := c.Stale(Stamp{Checked: tc.checked}, now); got != tc.want {
				t.Errorf("Stale = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestNoticeNamesTheCure(t *testing.T) {
	c := newChecker(t)
	seed(t, c, newerVersion, time.Now())

	line, ok := c.Notice()
	if !ok {
		t.Fatal("Notice said nothing about a build that is behind")
	}
	for _, want := range []string{newerVersion, currentVersion, c.InstallCommand()} {
		if !strings.Contains(line, want) {
			t.Errorf("the notice %q does not name %q", line, want)
		}
	}
}

func TestNoticeIsSilent(t *testing.T) {
	cases := []struct {
		name  string
		setup func(t *testing.T, c *Checker)
	}{
		{
			name:  "nothing has been checked yet",
			setup: func(*testing.T, *Checker) {},
		},
		{
			name: "the build is current",
			setup: func(t *testing.T, c *Checker) { seed(t, c, currentVersion, time.Now()) },
		},
		{
			name: "the check failed and recorded no version",
			setup: func(t *testing.T, c *Checker) { seed(t, c, "", time.Now()) },
		},
		{
			name: "this is a checkout build",
			setup: func(t *testing.T, c *Checker) {
				seed(t, c, newerVersion, time.Now())
				c.SetBuildForTest(t, Build{Version: currentVersion, Revision: "abc123"})
			},
		},
		{
			name: "the caller silenced it",
			setup: func(t *testing.T, c *Checker) {
				seed(t, c, newerVersion, time.Now())
				t.Setenv(c.SilenceEnv(), "1")
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := newChecker(t)
			tc.setup(t, c)
			if line, ok := c.Notice(); ok {
				t.Errorf("Notice said %q when it should have said nothing", line)
			}
		})
	}
}

// seed writes a stamp directly, standing in for a check that has already run.
func seed(t *testing.T, c *Checker, latest string, checked time.Time) {
	t.Helper()
	if err := c.WriteStamp(Stamp{Latest: latest, Checked: checked}); err != nil {
		t.Fatalf("seeding the stamp: %v", err)
	}
}
