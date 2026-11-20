package web

import (
	"embed"
	"io/fs"
	"net/http"
)

//go:embed src/*.js src/styles.css src/vendor
var assets embed.FS

// Assets serves static browser assets from the application binary.
func Assets() http.Handler {
	files, err := fs.Sub(assets, "src")
	if err != nil {
		panic(err) // The embedded source directory is a build invariant.
	}
	// Match files explicitly so the server never exposes directory listings.
	mux := http.NewServeMux()
	server := http.StripPrefix("/assets", http.FileServerFS(files))
	mux.Handle("GET /assets/{file}", server)
	mux.Handle("GET /assets/vendor/{file}", server)
	return mux
}
