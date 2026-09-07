# upcheck

upcheck tells a Go program whether a newer build of itself has been published,
and installs that build on request. It is for command-line tools distributed
with `go install <module>/cmd/<bin>@latest` — the ones whose users have no way
of learning that a newer tag exists, and no reason to go looking. It depends on
nothing outside the standard library.

The check never runs on the path a caller waited for. `Notice` reads one small
stamp file; `Check` starts a detached child to do the network work and returns
without waiting for it, so the run after this one is the informed one.

## Install

```
go get github.com/dkoosis/upcheck
```

## Use

Build one `Checker` at startup and keep it:

```go
up, err := upcheck.New(upcheck.Config{
	Module:      "github.com/you/yourtool",
	Binary:      "yourtool",
	CacheDir:    yourCacheDir,        // optional; defaults beside the OS cache
	RefreshArgs: []string{"version", "--refresh"},
	VersionArgs: []string{"version"},
})
```

On every verb, one file read and a detached child at most:

```go
if line, ok := up.Notice(); ok {
	fmt.Fprintln(os.Stderr, line) // "yourtool: v0.3.0 is available, running v0.2.0; get it with '...'"
}
up.Check(ctx) // starts a resolution only when the stamp is missing or a day old
```

The command named by `RefreshArgs` is what the detached child runs, and it must
call `Refresh`:

```go
stamp, err := up.Refresh(ctx) // go list -m -json <module>@latest, then write the stamp
```

And the update verb:

```go
res, err := up.Update(ctx) // go install; the replaced binary is kept at res.Previous
fmt.Printf("installed %s (previous binary at %s)\n", res.Version, res.Previous)
```

`Update` takes a lock in the cache directory first, so a second concurrent
update refuses and names the holder; it copies the running binary to
`<bin>.prev` before `go install` writes, so an install that dies halfway leaves
a working binary on disk; and it reports the version by running the file it
just installed, so an install that produces something that will not start is a
failure the caller hears about.

Set `<BINARY>_NO_UPDATE_CHECK` to turn the whole mechanism off.

## Development

```
make check
```

Direction lives in docs/ROADMAP.md; the work lives in bd (`bd ready`).
