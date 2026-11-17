package server

import (
	"fmt"
	"io"
	"io/fs"
	"mime"
	"net/http"
	"strconv"
)

func init() {
	// Not every platform's MIME table knows the web fonts; the page bundles them.
	for ext, typ := range map[string]string{
		".woff2": "font/woff2",
		".woff":  "font/woff",
		".ttf":   "font/ttf",
		".js":    "text/javascript; charset=utf-8",
		".mjs":   "text/javascript; charset=utf-8",
		".css":   "text/css; charset=utf-8",
		".svg":   "image/svg+xml",
	} {
		_ = mime.AddExtensionType(ext, typ)
	}
}

const notBuilt = `<!doctype html><meta charset="utf-8"><title>sitboard</title>
<p>UI not built, run <code>make build</code>.</p>
`

// page serves the embedded web build, or a plain notice when it is absent.
func page(assets fs.FS) http.Handler {
	if assets == nil {
		assets = Dist()
	}
	if _, err := fs.Stat(assets, "index.html"); err != nil {
		return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			_, _ = fmt.Fprint(w, notBuilt)
		})
	}
	return http.FileServerFS(assets)
}

// licenses serves the licence text as plain text, read-only.
func licenses(text string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if text == "" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.Header().Set("Content-Length", strconv.Itoa(len(text)))
		if r.Method == http.MethodHead {
			return
		}
		_, _ = io.WriteString(w, text)
	})
}
