// Package web embeds the built single-page application.
//
// The SPA is compiled by Vite into web/dist and baked into the binary, so a
// deployment is still one container with no separate web server, no Node
// runtime, and no static-file volume to keep in step with the binary.
package web

import (
	"embed"
	"io/fs"
)

//go:embed all:dist
var distFS embed.FS

// Assets returns the built SPA rooted at dist.
func Assets() (fs.FS, error) {
	return fs.Sub(distFS, "dist")
}
