// Package web holds the dashboard's built assets. They are generated from
// the sources under web/ by `make web` and committed, so building the Go
// binary needs no JavaScript toolchain.
package web

import (
	"embed"
	"io/fs"
)

//go:embed all:dist
var dist embed.FS

// FS returns the dashboard's assets, with index.html at the root.
func FS() fs.FS {
	assets, err := fs.Sub(dist, "dist")
	if err != nil {
		// fs.Sub fails only for an invalid path, and "dist" is a valid one.
		panic(err)
	}
	return assets
}
