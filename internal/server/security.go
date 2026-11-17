package server

import (
	"crypto/subtle"
	"fmt"
	"net"
	"net/http"
	"strings"
)

// CSP allows only sitboard's own files: no inline script or style, no other origin.
const CSP = "default-src 'none'; script-src 'self'; style-src 'self'; img-src 'self'; font-src 'self'; connect-src 'self'; base-uri 'none'; form-action 'none'; frame-ancestors 'none'"

// TokenParam is the query parameter of the opening link.
const TokenParam = "token"

// CookieName is per port, so two sitboards on one machine keep separate
// cookies in one browser (cookies do not separate by port).
func CookieName(port string) string { return "sitboard_" + port }

const reentry = `<!doctype html><meta charset="utf-8"><title>sitboard</title>
<p>Open the link sitboard printed when it started. If that terminal is gone, start sitboard again.</p>
`

// secure wraps next with the security layer. It fails closed: with no token,
// or an address it cannot parse, every request is refused.
func secure(token, addr string, next http.Handler) http.Handler {
	_, port, err := net.SplitHostPort(addr)
	if err != nil || port == "" || port == "0" || len(token) == 0 {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			setHeaders(w, r)
			http.Error(w, "sitboard is misconfigured", http.StatusInternalServerError)
		})
	}
	hosts := []string{"127.0.0.1:" + port, "localhost:" + port}
	origins := []string{"http://127.0.0.1:" + port, "http://localhost:" + port}
	cookie := CookieName(port)

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		setHeaders(w, r)

		// DNS rebinding: a foreign name pointed at 127.0.0.1 carries its own Host.
		if !oneOf(r.Host, hosts) {
			http.Error(w, "forbidden host", http.StatusForbidden)
			return
		}
		// Cross-site requests: any Origin must be ours, and a POST must carry one.
		origin := r.Header.Get("Origin")
		if origin != "" && !oneOf(origin, origins) {
			http.Error(w, "forbidden origin", http.StatusForbidden)
			return
		}
		if r.Method != http.MethodGet && r.Method != http.MethodHead && origin == "" {
			http.Error(w, "missing origin", http.StatusForbidden)
			return
		}
		// Browsers say where a request comes from; only our own page or the
		// owner typing the link is accepted.
		if site := r.Header.Get("Sec-Fetch-Site"); site != "" && site != "same-origin" && site != "none" {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}

		// The opening link: trade the token for the cookie and drop it from the URL.
		if r.URL.Query().Has(TokenParam) && r.Method == http.MethodGet {
			if !equal(r.URL.Query().Get(TokenParam), token) {
				unauthorized(w)
				return
			}
			http.SetCookie(w, &http.Cookie{
				Name:     cookie,
				Value:    token,
				Path:     "/",
				HttpOnly: true,
				SameSite: http.SameSiteStrictMode,
			})
			http.Redirect(w, r, "/", http.StatusSeeOther)
			return
		}

		if !hasToken(r, cookie, token) {
			unauthorized(w)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// hasToken accepts the request when any cookie of that name holds the token.
// Another local page can set a same-named cookie on a narrower path, which the
// browser sends first; checking only the first would lock the owner out.
func hasToken(r *http.Request, name, token string) bool {
	ok := false
	for _, c := range r.CookiesNamed(name) {
		if equal(c.Value, token) {
			ok = true
		}
	}
	return ok
}

func setHeaders(w http.ResponseWriter, r *http.Request) {
	h := w.Header()
	h.Set("Content-Security-Policy", CSP)
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Referrer-Policy", "no-referrer")
	h.Set("Cross-Origin-Resource-Policy", "same-origin")
	h.Set("Cross-Origin-Opener-Policy", "same-origin")
	if strings.HasPrefix(r.URL.Path, "/api/") {
		h.Set("Cache-Control", "no-store")
	}
}

func unauthorized(w http.ResponseWriter) {
	h := w.Header()
	h.Set("Content-Type", "text/html; charset=utf-8")
	h.Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusUnauthorized)
	_, _ = fmt.Fprint(w, reentry)
}

// equal compares in constant time, so the token cannot be guessed byte by byte.
func equal(got, want string) bool {
	return subtle.ConstantTimeCompare([]byte(got), []byte(want)) == 1
}

func oneOf(s string, allowed []string) bool {
	for _, a := range allowed {
		if s == a {
			return true
		}
	}
	return false
}
