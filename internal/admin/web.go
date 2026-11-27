package admin

import (
	"embed"
	"io/fs"
	"net/http"
	"strings"
)

// The dashboard SPA is built from web/ (npm run build) into dist/ and
// committed, so `go build` works on machines without node.
//
//go:embed all:dist
var embeddedUI embed.FS

// sub returns the dist subtree as an fs.FS rooted at dist/.
func sub() (fs.FS, error) {
	return fs.Sub(embeddedUI, "dist")
}

// UIHandler serves the embedded dashboard files, falling back to index.html
// for client-side routes. Static assets are harmless; the sensitive surface
// (/api/*) is authenticated separately.
func UIHandler() http.Handler {
	dist, err := sub()
	if err != nil {
		// The embed is compile-time; this branch exists for fs.Sub's contract.
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, "dashboard unavailable", http.StatusInternalServerError)
		})
	}
	files := http.FileServer(http.FS(dist))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := strings.TrimPrefix(r.URL.Path, "/")
		if path == "" {
			path = "index.html"
		}
		if _, err := fs.Stat(dist, path); err != nil {
			index, readErr := fs.ReadFile(dist, "index.html")
			if readErr != nil {
				http.NotFound(w, r)
				return
			}
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			_, _ = w.Write(index)
			return
		}
		files.ServeHTTP(w, r)
	})
}
