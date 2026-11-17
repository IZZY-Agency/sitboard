// Package server serves the API and the embedded page, behind the security
// layer: loopback Host only, same-origin requests only, and the per-run token.
//
// The page and every API route sit behind the same checks. Three POST actions
// change something (jump, mark seen, mark catch-up read); nothing else does.
// Transcript text is never served: the board holds only derived fields.
package server

import (
	"embed"
	"io/fs"
	"net/http"
	"strings"

	"github.com/IZZY-Agency/sitboard/internal/app"
	"github.com/IZZY-Agency/sitboard/internal/source"
)

// The web build (npm run build in web/) writes into dist/. Only dist/.keep is
// tracked, so a fresh checkout still compiles and serves the "not built" page.
//
//go:embed all:dist
var dist embed.FS

// Dist returns the embedded page files.
func Dist() fs.FS {
	sub, err := fs.Sub(dist, "dist")
	if err != nil {
		panic(err) // dist is embedded at build time; it always exists
	}
	return sub
}

// Options configure the handler.
type Options struct {
	Board  app.Board
	Jumper source.Jumper
	Token  string // per-run token; every request must carry it (link once, then the cookie)
	Addr   string // the address the listener is bound to, "127.0.0.1:<port>"
	Assets fs.FS  // the page files, normally Dist()
	// Licenses is the licence text served at PathLicenses: sitboard's licence
	// and the third-party notices. Empty: that path is not found.
	Licenses string
}

// PathLicenses serves Options.Licenses as plain text, behind the same checks
// as every other path.
const PathLicenses = "/licenses.txt"

// New returns the HTTP handler.
func New(opts Options) http.Handler {
	apiH, pageH := api(opts), page(opts.Assets)
	route := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasPrefix(r.URL.Path, "/api/"):
			apiH.ServeHTTP(w, r)
		case r.URL.Path == PathLicenses:
			licenses(opts.Licenses).ServeHTTP(w, r)
		case r.Method == http.MethodGet || r.Method == http.MethodHead:
			pageH.ServeHTTP(w, r)
		default:
			w.Header().Set("Allow", "GET, HEAD")
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
	})
	return secure(opts.Token, opts.Addr, route)
}
