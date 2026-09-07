# upcheck

★ Every Go tool in the fleet can tell its caller it is behind, and update
itself, without a release pipeline.

The ★ line above is a copy kept beside the code; dk edits
`Project/upcheck/NORTH_STAR.md` in the kg and nothing else is a source. What
this file owns is the epic inventory below. upcheck was extracted from mnemd's
`internal/version` (mnemd bead mn-75a, PR #55) once a second tool wanted the
same behaviour, and it carries the one design constraint mnemd's latency rule
imposed: a currency check may never be part of what a caller waited for.

## Epics

Ordered, one line per epic. Progress is never written here — it derives at read
time from the bd DAG joined against these ids.

1. [done] The library — Check, ReadStamp, Notice, Update, zero dependencies →
   mnemd `mn-o9o`
2. [open] mnemd drops `internal/version` for this library → `bd ready`

## Non-goals

- Downloading release artifacts. There are none; the module proxy is the
  distribution channel.
- Signature verification. Integrity and authenticity ride on the module proxy
  and sum.golang.org. The alternative — download plus a pinned ed25519 key —
  was assessed on 2026-09-06 and rejected on the cost of release infrastructure
  and key custody per repo. Revisit if a host without a Go toolchain appears.
- Any host without a Go toolchain. The only cure upcheck recommends is
  `go install`, so a machine that cannot run `go` could not act on the answer.

## Resources

- The bead this repo was built from: mnemd `bd show mn-o9o`
- The mechanism it was extracted from: mnemd PR #55 (df87c09)
- The latency rule that shaped it: mnemd `.claude/rules/standard-latency.md`
