package web

import (
	"embed"
	"io/fs"
)

//go:embed all:dist
var distEmbed embed.FS

// DistFS returns the embedded `web/dist` filesystem for single-binary serving (§2).
func DistFS() (fs.FS, error) {
	return fs.Sub(distEmbed, "dist")
}
