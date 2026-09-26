//go:build tools

// tools.go makes the ruleguard DSL importable so gocritic can typecheck the
// rule files under .golangci-rules/. Never compiled into the binary.
package tools

import _ "github.com/quasilyte/go-ruleguard/dsl"
