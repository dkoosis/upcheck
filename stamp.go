package upcheck

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// stampFile is the stamp's name inside the cache directory. One file per
// program, because the question it answers — what has been published — has
// nothing to do with what the program is being asked to do.
const stampFile = "latest.json"

// Stamp is what the last currency check learned, and when it learned it. It is
// the whole of the state a verb reads, which is why it is one small file: the
// read has to be a stat and a decode rather than anything that can block.
type Stamp struct {
	Latest  string    `json:"latest"`
	Checked time.Time `json:"checked"`
}

// Stale reports whether the stamp is old enough that a fresh check is worth
// starting. A stamp from the future — a clock that moved backwards — is stale
// too, since believing it would silence the check forever.
func (c *Checker) Stale(s Stamp, now time.Time) bool {
	age := now.Sub(s.Checked)
	return age >= c.cfg.StaleAfter || age < 0
}

// StampPath is where the stamp lives: in the cache directory, beside whatever
// else the program derives.
func (c *Checker) StampPath() string {
	return filepath.Join(c.cfg.CacheDir, stampFile)
}

// ReadStamp returns what the last check left behind, and whether a check has
// been recorded at all.
//
// The boolean answers "has anyone checked", not "is there a version to compare
// against". Those come apart on a private module: a laptop without GOPRIVATE
// and git credentials fails every resolution. If a failed check left nothing
// behind, every run would find no stamp, start another check, and fail again —
// a forked process per call for the life of the machine. So a failed check
// records its attempt with an empty Latest, and this reports it as a check that
// happened.
//
// Every read failure is the same answer — no stamp. A stamp that was never
// written, one whose directory is gone and one whose JSON is truncated all mean
// the same thing: nothing has been recorded here, and the honest response is
// silence rather than a complaint about a file the caller never asked for.
func (c *Checker) ReadStamp() (Stamp, bool) {
	data, err := os.ReadFile(c.StampPath())
	if err != nil {
		return Stamp{}, false
	}
	var s Stamp
	if err := json.Unmarshal(data, &s); err != nil {
		return Stamp{}, false
	}
	if s.Checked.IsZero() {
		return Stamp{}, false
	}
	return s, true
}

// WriteStamp records a check. It writes to a temporary file and renames, so a
// reader never sees half a stamp: the file is small enough that a torn write is
// unlikely and cheap enough that ruling it out costs nothing.
func (c *Checker) WriteStamp(s Stamp) error {
	p := c.StampPath()
	dir := filepath.Dir(p)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("upcheck: %w", err)
	}
	data, err := json.Marshal(s)
	if err != nil {
		return fmt.Errorf("upcheck: %w", err)
	}
	tmp, err := os.CreateTemp(dir, stampFile+".*")
	if err != nil {
		return fmt.Errorf("upcheck: %w", err)
	}
	defer func() { _ = os.Remove(tmp.Name()) }()
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("upcheck: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("upcheck: %w", err)
	}
	if err := os.Rename(tmp.Name(), p); err != nil {
		return fmt.Errorf("upcheck: %w", err)
	}
	return nil
}
