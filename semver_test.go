package upcheck

import "testing"

func TestSemverValidity(t *testing.T) {
	t.Parallel()
	valid := []string{
		"v0.0.0",
		"v1.2.3",
		"v0.2.1-0.20260905172110-5559869235a1",
		"v0.0.0-20260906000929-155985eecb4d+dirty",
		"v1.0.0-alpha",
		"v1.0.0-alpha.1",
		"v1.0.0-0.3.7",
		"v1.0.0+build.1",
		"v10.20.30",
	}
	for _, v := range valid {
		if !isValidSemver(v) {
			t.Errorf("isValidSemver(%q) = false, want true", v)
		}
	}
	invalid := []string{
		"",
		"1.2.3",     // the toolchain always writes the v
		"v1.2",      // three fields or nothing
		"v1.2.3.4",  // and no more than three
		"v01.2.3",   // no leading zeros
		"v1.2.3-",   // an empty prerelease is not a prerelease
		"v1.2.3-01", // nor one with a leading zero in a numeric field
		"v1.2.3+",   // an empty build is not build metadata
		"(devel)",   // what a checkout build says when the toolchain declines to guess
		"vlatest",
	}
	for _, v := range invalid {
		if isValidSemver(v) {
			t.Errorf("isValidSemver(%q) = true, want false", v)
		}
	}
}

func TestSemverOrder(t *testing.T) {
	t.Parallel()
	cases := []struct {
		a, b string
		want int
	}{
		{"v1.2.3", "v1.2.3", 0},
		{"v1.2.4", "v1.2.3", 1},
		{"v1.3.0", "v1.2.9", 1},
		{"v2.0.0", "v1.99.99", 1},
		{"v1.2.3", "v1.2.10", -1},
		// Build metadata is not part of the order, which is what keeps a
		// developer's "+dirty" build from reading as newer than the tag it
		// was cut from.
		{"v1.2.3+dirty", "v1.2.3", 0},
		{"v1.2.3+a", "v1.2.3+b", 0},
		// A prerelease comes before the release it belongs to.
		{"v1.0.0-alpha", "v1.0.0", -1},
		{"v1.0.0-alpha", "v1.0.0-alpha.1", -1},
		{"v1.0.0-alpha.1", "v1.0.0-alpha.beta", -1},
		{"v1.0.0-beta", "v1.0.0-alpha", 1},
		{"v1.0.0-rc.1", "v1.0.0-beta.11", 1},
		// Pseudo-versions order by their embedded timestamp, which is the
		// whole reason an untagged module can still tell newer from older.
		{
			"v0.0.0-20260906000929-155985eecb4d",
			"v0.0.0-20260905172110-5559869235a1",
			1,
		},
	}
	for _, c := range cases {
		if got := compareSemver(c.a, c.b); got != c.want {
			t.Errorf("compareSemver(%q, %q) = %d, want %d", c.a, c.b, got, c.want)
		}
		if got := compareSemver(c.b, c.a); got != -c.want {
			t.Errorf("compareSemver(%q, %q) = %d, want %d", c.b, c.a, got, -c.want)
		}
	}
}

func TestSemverOrdersInvalidBelowValid(t *testing.T) {
	t.Parallel()
	if compareSemver("(devel)", "v0.0.1") != -1 {
		t.Error("an unparseable version did not sort below a real one")
	}
	if compareSemver("(devel)", "also not a version") != 0 {
		t.Error("two unparseable versions were not equal")
	}
}
