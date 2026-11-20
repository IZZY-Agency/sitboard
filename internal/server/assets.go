package server

import (
	"fmt"
	"io"
	"io/fs"
	"mime"
	"net/http"
	"path"
	"strconv"
	"strings"
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
	files := http.FileServerFS(assets)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// The tab icons may be kept: the "?" one is needed exactly when
		// sitboard is gone or this tab's key no longer works, so the page
		// loads it once while live and the browser answers from its cache.
		// The file server drops the header from an error (a missing file).
		// Only a clean path counts: "/badge/../favicon.svg" is another file.
		if isBadge(r.URL.Path) {
			w.Header().Set("Cache-Control", BadgeCache)
		}
		files.ServeHTTP(w, r)
	})
}

// isBadge reports whether p names one file under BadgePrefix, written plainly.
func isBadge(p string) bool {
	name, ok := strings.CutPrefix(p, BadgePrefix)
	return ok && name != "" && path.Clean(p) == p && !strings.Contains(name, "/")
}

// BadgePrefix is where the tab icons with a count live.
const BadgePrefix = "/badge/"

// BadgeCache lets the browser keep a tab icon for a week without asking again.
const BadgeCache = "private, max-age=604800, immutable"

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
