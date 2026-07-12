// Package ui embeds the web console build served by the Edge binary. The real
// production build (apps/web) is copied into dist/ at build time; a placeholder
// index.html is committed so `go build` always works with only the placeholder.
package ui

import (
	"embed"
	"io/fs"
)

//go:embed dist
var embedded embed.FS

// FS returns the embedded UI rooted at dist/ (so "/" maps to index.html).
func FS() fs.FS {
	sub, err := fs.Sub(embedded, "dist")
	if err != nil {
		// Unreachable: dist is embedded at compile time.
		panic(err)
	}
	return sub
}
