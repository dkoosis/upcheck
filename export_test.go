package upcheck

import (
	"context"
	"testing"
)

// CountSpawnsForTest replaces the detached refresh with a counter, so a test
// can watch Check decide whether a check is worth starting without any process
// being started.
func (c *Checker) CountSpawnsForTest(t *testing.T) *int {
	t.Helper()
	old := c.spawn
	n := 0
	c.spawn = func(context.Context) { n++ }
	t.Cleanup(func() { c.spawn = old })
	return &n
}

// SetGoCmdForTest puts a stub in front of the Go toolchain this Checker shells
// out to, and puts the real one back when the test ends.
//
// A stub rather than the real toolchain, so the tests never reach the network,
// never depend on credentials for a private module, and can produce answers a
// real toolchain would not give — a broken one, a slow one, one that is not a
// version at all, and an install that dies halfway.
//
// It writes a field on one Checker rather than a package-level variable, so
// tests that use it still run in parallel with each other.
func (c *Checker) SetGoCmdForTest(t *testing.T, path string) {
	t.Helper()
	old := c.goCmd
	c.goCmd = path
	t.Cleanup(func() { c.goCmd = old })
}

// LockPathForTest is where this Checker's update lock lives, so a test can
// plant one and watch a second update refuse it.
func (c *Checker) LockPathForTest() string { return c.lockPath() }

// SetBuildForTest puts a fixed build identity in front of the one the toolchain
// stamped. A test binary is never an installed build, so without this no test
// could reach the notice at all.
func (c *Checker) SetBuildForTest(t *testing.T, b Build) {
	t.Helper()
	old := c.buildOf
	c.buildOf = func() Build { return b }
	t.Cleanup(func() { c.buildOf = old })
}
