package upcheck

import (
	"context"
	"errors"
	"testing"
	"time"
)

// answerLatest is a toolchain stub that resolves cleanly. The version it names
// is newer than currentVersion, so a stamp written from it puts the running
// build behind.
const answerLatest = `case "$1" in
  list) printf '{"Version":"` + newerVersion + `"}' ;;
  *) exit 1 ;;
esac`

func TestFetchReadsTheToolchainsAnswer(t *testing.T) {
	c := newChecker(t)
	c.SetGoCmdForTest(t, stubGo(t, answerLatest))

	got, err := c.Fetch(t.Context())
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if got != newerVersion {
		t.Errorf("Fetch = %q, want %q", got, newerVersion)
	}
}

func TestFetchFailures(t *testing.T) {
	cases := []struct {
		name string
		stub string
		want error
	}{
		{
			name: "the toolchain could not answer",
			stub: `exit 1`,
			want: ErrResolveFailed,
		},
		{
			name: "the answer was not json",
			stub: `printf 'go: not a module'`,
			want: ErrResolveFailed,
		},
		{
			name: "the answer was not a version",
			stub: `printf '{"Version":"latest"}'`,
			want: ErrNotAVersion,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := newChecker(t)
			c.SetGoCmdForTest(t, stubGo(t, tc.stub))
			if _, err := c.Fetch(t.Context()); !errors.Is(err, tc.want) {
				t.Errorf("Fetch error = %v, want %v", err, tc.want)
			}
		})
	}
}

func TestFetchIsBounded(t *testing.T) {
	c := newChecker(t)
	// A toolchain that never answers. Without the bound this test would hang
	// rather than fail, which is the failure the bound exists to prevent.
	c.SetGoCmdForTest(t, stubGo(t, `sleep 30`))
	ctx, cancel := context.WithTimeout(t.Context(), 200*time.Millisecond)
	defer cancel()

	start := time.Now()
	if _, err := c.Fetch(ctx); err == nil {
		t.Fatal("Fetch waited out a hung toolchain and reported success")
	}
	if elapsed := time.Since(start); elapsed > 10*time.Second {
		t.Errorf("Fetch took %v; the context bound did not end the wait", elapsed)
	}
}

func TestRefreshRecordsWhatItLearned(t *testing.T) {
	c := newChecker(t)
	c.SetGoCmdForTest(t, stubGo(t, answerLatest))

	if _, err := c.Refresh(t.Context()); err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	s, ok := c.ReadStamp()
	if !ok {
		t.Fatal("Refresh recorded no stamp")
	}
	if s.Latest != newerVersion {
		t.Errorf("stamped %q, want %q", s.Latest, newerVersion)
	}
	if s.Checked.IsZero() {
		t.Error("the stamp records no time of checking")
	}
}

func TestRefreshRecordsAFailedAttemptWithoutErasingTheAnswer(t *testing.T) {
	// This is the runaway the stamp-on-failure rule prevents: a machine that
	// cannot resolve must still leave a stamp behind, or every later call finds
	// nothing, forks another check, and fails again.
	c := newChecker(t)
	seed(t, c, newerVersion, time.Now().Add(-48*time.Hour))
	c.SetGoCmdForTest(t, stubGo(t, `exit 1`))

	if _, err := c.Refresh(t.Context()); !errors.Is(err, ErrResolveFailed) {
		t.Fatalf("Refresh error = %v, want %v", err, ErrResolveFailed)
	}
	s, ok := c.ReadStamp()
	if !ok {
		t.Fatal("a failed refresh left no record that it had tried")
	}
	if s.Latest != newerVersion {
		t.Errorf("a failed refresh erased the last good answer: %q", s.Latest)
	}
	if c.Stale(s, time.Now()) {
		t.Error("a failed refresh left the stamp stale, so the next call forks again")
	}
}

func TestCheckStartsAResolutionOnlyWhenItIsWorthIt(t *testing.T) {
	cases := []struct {
		name  string
		setup func(t *testing.T, c *Checker)
		want  int
	}{
		{
			name:  "nothing has been checked yet",
			setup: func(*testing.T, *Checker) {},
			want:  1,
		},
		{
			name:  "the stamp is old",
			setup: func(t *testing.T, c *Checker) { seed(t, c, currentVersion, time.Now().Add(-48*time.Hour)) },
			want:  1,
		},
		{
			name:  "the stamp is fresh",
			setup: func(t *testing.T, c *Checker) { seed(t, c, currentVersion, time.Now()) },
			want:  0,
		},
		{
			name: "this is a checkout build",
			setup: func(t *testing.T, c *Checker) {
				c.SetBuildForTest(t, Build{Version: currentVersion, Revision: "abc123"})
			},
			want: 0,
		},
		{
			name:  "the caller silenced it",
			setup: func(t *testing.T, c *Checker) { t.Setenv(c.SilenceEnv(), "1") },
			want:  0,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := newChecker(t)
			spawns := c.CountSpawnsForTest(t)
			tc.setup(t, c)
			c.Check(t.Context())
			if *spawns != tc.want {
				t.Errorf("Check started %d resolutions, want %d", *spawns, tc.want)
			}
		})
	}
}

func TestCheckDoesNotWaitForTheChild(t *testing.T) {
	// The rule this guards is the whole shape of the package: a currency check
	// may not be part of what the caller waited for. The child here is this
	// test binary run with a filter that matches no test, so it starts, exits
	// and is nobody's business.
	c, err := New(Config{
		Module:      testModule,
		Binary:      testBinary,
		CacheDir:    t.TempDir(),
		RefreshArgs: []string{"-test.run=NoSuchTestExists"},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Setenv(c.SilenceEnv(), "")
	c.SetBuildForTest(t, Build{Version: currentVersion})

	start := time.Now()
	c.Check(t.Context())
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Errorf("Check took %v; it waited for the child it started", elapsed)
	}
}
