package upcheck

import (
	"runtime/debug"
	"strings"
)

// devel is what the toolchain records for a build made from a checkout rather
// than resolved from a module version.
const devel = "(devel)"

// Build is the identity of the running binary, as the toolchain stamped it at
// link time. Reading it costs nothing and never touches the network.
type Build struct {
	Version  string // module version: a tag, a pseudo-version, or "(devel)"
	Revision string // vcs.revision, stamped only on a build from a checkout
	Time     string // vcs.time, likewise
	Modified bool   // vcs.modified: the checkout had uncommitted changes
}

// Current reads the identity the toolchain embedded at link time.
//
// The two ways a fleet binary gets built put their identity in different
// places, and only one of them is checkable. `go install <module>/cmd/<bin>
// @latest` records the resolved module version in Main.Version and stamps no
// VCS settings at all, because it builds from the module cache rather than from
// a checkout. A build made inside a checkout stamps vcs.revision, vcs.time and
// vcs.modified, and puts either "(devel)" or a synthesised pseudo-version in
// Main.Version depending on the toolchain.
//
// [Build.Installed] is what reads the difference; see the note there, because
// the obvious test for it is wrong.
func Current() Build {
	bi, ok := debug.ReadBuildInfo()
	if !ok {
		return Build{}
	}
	b := Build{Version: bi.Main.Version}
	for _, s := range bi.Settings {
		switch s.Key {
		case "vcs.revision":
			b.Revision = s.Value
		case "vcs.time":
			b.Time = s.Value
		case "vcs.modified":
			b.Modified = s.Value == "true"
		}
	}
	return b
}

// Installed reports whether this build came from `go install <module>@<version>`
// — the only shape upcheck may say anything about, because it is the only one
// `go install ...@latest` would replace.
//
// The absence of a VCS stamp is what decides it, and that is not the obvious
// test. A build made from a checkout does not reliably say "(devel)": since Go
// 1.24 the toolchain synthesises a pseudo-version for it out of the commit and
// its date, suffixed "+dirty" when the tree is modified — a string this
// package's validator accepts and its comparison orders, so a version test
// alone calls a developer's own working build installed and tells them to
// install over it. Measured 2026-09-06: a plain `go build` binary reported
// v0.0.0-20260906000929-155985eecb4d+dirty.
//
// vcs.revision separates them cleanly. The toolchain stamps it only when it
// builds from a source tree under version control, which `go install
// <module>@<version>` never does — it builds from the module cache.
func (b Build) Installed() bool {
	if b.Revision != "" {
		return false
	}
	return b.Version != "" && b.Version != devel && isValidSemver(b.Version)
}

// String names the build the way a `version` verb prints it: the module version
// first because that is what an install would match, then the commit and its
// date when the build carries them.
func (b Build) String() string {
	v := b.Version
	if v == "" {
		v = "unknown"
	}
	parts := []string{v}
	if b.Revision != "" {
		rev := b.Revision
		if len(rev) > 12 {
			rev = rev[:12]
		}
		// Only when the version has not already said it. Since Go 1.24 the
		// synthesised version for a checkout build carries its own "+dirty",
		// and printing the word twice on one line reads as two separate facts.
		if b.Modified && !strings.HasSuffix(b.Version, "+dirty") {
			rev += "+dirty"
		}
		parts = append(parts, rev)
	}
	if b.Time != "" {
		parts = append(parts, b.Time)
	}
	return strings.Join(parts, "  ")
}

// Behind reports whether the version the stamp records is newer than this
// build.
//
// Newer, not merely different. Somebody who deliberately installed an older tag
// is behind and should hear it; somebody running a build made between two
// fetches is ahead, and telling them to reinstall would send them backwards.
// The comparison orders pseudo-versions by their embedded timestamp, so this is
// right while a repo carries no tags and stays right once it does.
func (b Build) Behind(s Stamp) bool {
	if !b.Installed() || !isValidSemver(s.Latest) {
		return false
	}
	return compareSemver(s.Latest, b.Version) > 0
}
