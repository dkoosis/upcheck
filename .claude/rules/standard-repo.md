# This repo — upcheck

upcheck is a zero-dependency Go library that tells a program whether a newer
build of itself has been published, and installs it on request. It has no
command; it is imported by the fleet's tools.

**Direction:** `~/Projects/kg/Project/upcheck/NORTH_STAR.md` — dk edits it;
nothing else is a source. `docs/ROADMAP.md` mirrors its ★ line over the epic
inventory.

**The queue:** bd — `bd ready` / `bd show <id>` / `bd update <id> --claim` /
`bd close <id>`.

**The gate:** `make check`.

‡ **The zero-dependency rule is the repo's one hard constraint.** Nothing in
`go list -deps ./...` but this module and the standard library. It is why
`semver.go` exists rather than an import of `golang.org/x/mod/semver`. A change
that adds an import outside `std` is a change to what upcheck is, not a
convenience — say so out loud before making it. Tool dependencies in `go.mod`
(conform) are not runtime dependencies and do not count.

‡ **A currency check never runs on the caller's path.** `Notice` is one file
read. `Check` starts a detached child and returns. Anything that would make a
verb wait on the network belongs in `Refresh`, which only the child and an
explicit refresh verb call.
