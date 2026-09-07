package upcheck

import "strings"

// The semantic-version subset upcheck needs, implemented here rather than taken
// from golang.org/x/mod/semver.
//
// The package's one hard rule is zero dependencies outside the standard library
// (see the package comment), and this is the only thing that rule costs. What
// is needed is small and completely specified: validate the vMAJOR.MINOR.PATCH
// shape the Go toolchain emits, and order two of them. Go module versions are
// always canonical — the toolchain produces them, not a human — so the
// tolerances a general parser needs are absent here.
//
// The rules implemented are semver 2.0.0's, restricted to Go's "v" prefix:
//
//   - a version is v, three dot-separated numeric identifiers, an optional
//     "-prerelease" and an optional "+build";
//   - numeric identifiers carry no leading zeros;
//   - build metadata is ignored entirely when ordering, which is what makes
//     the toolchain's "+dirty" suffix invisible to a comparison;
//   - a prerelease sorts before the release it belongs to;
//   - prerelease identifiers are compared field by field, numerics
//     numerically and below non-numerics, and a shorter run of equal fields
//     sorts first.

// isValidSemver reports whether v is a version this package can compare.
func isValidSemver(v string) bool {
	_, ok := parseSemver(v)
	return ok
}

// semverParts is a version taken apart. Build metadata is dropped at the parse,
// since nothing here ever looks at it again.
type semverParts struct {
	num [3]uint64
	pre string // without the leading "-"; empty means a release
}

// parseSemver splits v into its parts, reporting whether it is well formed.
func parseSemver(v string) (semverParts, bool) {
	var p semverParts
	rest, ok := strings.CutPrefix(v, "v")
	if !ok {
		return p, false
	}
	// Build metadata first: it may contain characters the prerelease grammar
	// forbids, and it never affects anything below.
	if i := strings.IndexByte(rest, '+'); i >= 0 {
		build := rest[i+1:]
		if !validDotSeparated(build, false) {
			return p, false
		}
		rest = rest[:i]
	}
	if i := strings.IndexByte(rest, '-'); i >= 0 {
		p.pre = rest[i+1:]
		if !validDotSeparated(p.pre, true) {
			return p, false
		}
		rest = rest[:i]
	}
	for i := range 3 {
		field := rest
		if i < 2 {
			dot := strings.IndexByte(rest, '.')
			if dot < 0 {
				return p, false
			}
			field, rest = rest[:dot], rest[dot+1:]
		} else if strings.IndexByte(rest, '.') >= 0 {
			return p, false
		}
		n, ok := parseNumericID(field)
		if !ok {
			return p, false
		}
		p.num[i] = n
	}
	return p, true
}

// validDotSeparated checks a prerelease or build-metadata string: dot-separated
// identifiers of alphanumerics and hyphens, none of them empty. numericRule
// applies semver's ban on leading zeros, which binds in a prerelease and not in
// build metadata.
func validDotSeparated(s string, numericRule bool) bool {
	if s == "" {
		return false
	}
	for id := range strings.SplitSeq(s, ".") {
		if id == "" {
			return false
		}
		numeric := true
		for i := range len(id) {
			ch := id[i]
			switch {
			case ch >= '0' && ch <= '9':
			case (ch >= 'a' && ch <= 'z') || (ch >= 'A' && ch <= 'Z') || ch == '-':
				numeric = false
			default:
				return false
			}
		}
		if numericRule && numeric {
			if _, ok := parseNumericID(id); !ok {
				return false
			}
		}
	}
	return true
}

// parseNumericID reads a semver numeric identifier: digits, no leading zero
// unless the whole field is "0".
func parseNumericID(s string) (uint64, bool) {
	if s == "" || (len(s) > 1 && s[0] == '0') {
		return 0, false
	}
	var n uint64
	for i := range len(s) {
		ch := s[i]
		if ch < '0' || ch > '9' {
			return 0, false
		}
		// A version number long enough to overflow is not a version anyone
		// published; refusing it is more honest than wrapping.
		if n > (1<<63)/10 {
			return 0, false
		}
		n = n*10 + uint64(ch-'0')
	}
	return n, true
}

// compareSemver orders two versions: -1, 0 or +1. An invalid version sorts
// below every valid one, and two invalid ones are equal, so a caller that has
// already validated its inputs never sees the fallback and one that has not
// gets a total order rather than a panic.
func compareSemver(a, b string) int {
	pa, oka := parseSemver(a)
	pb, okb := parseSemver(b)
	switch {
	case !oka && !okb:
		return 0
	case !oka:
		return -1
	case !okb:
		return 1
	}
	for i := range 3 {
		if pa.num[i] != pb.num[i] {
			if pa.num[i] < pb.num[i] {
				return -1
			}
			return 1
		}
	}
	return comparePrerelease(pa.pre, pb.pre)
}

// comparePrerelease orders the prerelease sections of two versions whose
// numbers are equal. A release outranks any prerelease of itself.
func comparePrerelease(a, b string) int {
	switch {
	case a == b:
		return 0
	case a == "":
		return 1
	case b == "":
		return -1
	}
	fa, fb := strings.Split(a, "."), strings.Split(b, ".")
	for i := range min(len(fa), len(fb)) {
		if c := compareIdentifier(fa[i], fb[i]); c != 0 {
			return c
		}
	}
	switch {
	case len(fa) < len(fb):
		return -1
	case len(fa) > len(fb):
		return 1
	}
	return 0
}

// compareIdentifier orders one prerelease field against another: numeric fields
// numerically, and below every non-numeric field.
func compareIdentifier(a, b string) int {
	na, oka := parseNumericID(a)
	nb, okb := parseNumericID(b)
	switch {
	case oka && okb:
		switch {
		case na < nb:
			return -1
		case na > nb:
			return 1
		}
		return 0
	case oka:
		return -1
	case okb:
		return 1
	}
	return strings.Compare(a, b)
}
