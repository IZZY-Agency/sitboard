package server

import (
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"

	"github.com/IZZY-Agency/sitboard/internal/contract"
)

func TestRejectsForeignHost(t *testing.T) {
	h := newHarness()
	for _, host := range []string{"evil.example", "evil.example:" + testPort, "127.0.0.1:9999", "0.0.0.0:" + testPort, "[::1]:" + testPort, ""} {
		r := req(http.MethodGet, contract.PathSnapshot, "")
		r.Host = host
		if w := h.do(r); w.Code != http.StatusForbidden {
			t.Errorf("host %q: status %d, want 403", host, w.Code)
		}
	}
}

func TestAcceptsLoopbackHostsWithPort(t *testing.T) {
	h := newHarness()
	for _, host := range []string{"127.0.0.1:" + testPort, "localhost:" + testPort} {
		r := req(http.MethodGet, contract.PathSnapshot, "")
		r.Host = host
		wantStatus(t, h.do(r), http.StatusOK)
	}
}

func TestRejectsForeignOrigin(t *testing.T) {
	h := newHarness()
	for _, origin := range []string{"http://evil.example", "null", "https://127.0.0.1:" + testPort, "http://127.0.0.1:9999"} {
		for _, m := range []string{http.MethodGet, http.MethodPost} {
			path := contract.PathSnapshot
			if m == http.MethodPost {
				path = contract.PathSeen
			}
			r := req(m, path, `{"cardId":"`+cardA+`"}`)
			r.Header.Set("Origin", origin)
			if w := h.do(r); w.Code != http.StatusForbidden {
				t.Errorf("%s origin %q: status %d, want 403", m, origin, w.Code)
			}
		}
	}
	if len(h.board.seen) != 0 {
		t.Fatalf("a cross-origin POST changed state: %v", h.board.seen)
	}
}

func TestCrossSiteFetchIsRefused(t *testing.T) {
	h := newHarness()
	r := req(http.MethodGet, contract.PathSnapshot, "")
	r.Header.Set("Sec-Fetch-Site", "cross-site")
	wantStatus(t, h.do(r), http.StatusForbidden)
}

func TestPostRequiresSameOrigin(t *testing.T) {
	h := newHarness()
	r := req(http.MethodPost, contract.PathSeen, `{"cardId":"`+cardA+`"}`)
	r.Header.Del("Origin")
	wantStatus(t, h.do(r), http.StatusForbidden)
	r = req(http.MethodPost, contract.PathSeen, `{"cardId":"`+cardA+`"}`)
	r.Header.Set("Origin", "http://localhost:"+testPort)
	wantStatus(t, h.do(r), http.StatusNoContent)
}

func TestNoTokenIs401(t *testing.T) {
	h := newHarness()
	for _, path := range []string{"/", contract.PathSnapshot, contract.PathEvents, "/assets/app-1234.js"} {
		r := httptest.NewRequest(http.MethodGet, "http://"+testHost+path, nil)
		r.Host = testHost
		if w := h.do(r); w.Code != http.StatusUnauthorized {
			t.Errorf("%s: status %d, want 401", path, w.Code)
		}
	}
	r := req(http.MethodPost, contract.PathSeen, `{"cardId":"`+cardA+`"}`)
	r.Header.Del("Cookie")
	wantStatus(t, h.do(r), http.StatusUnauthorized)
}

func TestTokenInURLSetsCookieAndRedirects(t *testing.T) {
	h := newHarness()
	r := httptest.NewRequest(http.MethodGet, "http://"+testHost+"/?token="+testToken, nil)
	r.Host = testHost
	w := h.do(r)
	wantStatus(t, w, http.StatusSeeOther)
	if loc := w.Header().Get("Location"); loc != "/" {
		t.Fatalf("location %q, want /", loc)
	}
	cookies := w.Result().Cookies()
	if len(cookies) != 1 {
		t.Fatalf("%d cookies", len(cookies))
	}
	c := cookies[0]
	if c.Name != "sitboard_"+testPort || c.Value != testToken || !c.HttpOnly || c.SameSite != http.SameSiteStrictMode || c.Path != "/" {
		t.Fatalf("cookie %+v", c)
	}

	// A wrong token in the link is refused and sets nothing.
	r = httptest.NewRequest(http.MethodGet, "http://"+testHost+"/?token=nope", nil)
	r.Host = testHost
	w = h.do(r)
	wantStatus(t, w, http.StatusUnauthorized)
	if len(w.Result().Cookies()) != 0 {
		t.Fatal("cookie set for a wrong token")
	}

	// The redirect never points off-site, whatever the path.
	r = httptest.NewRequest(http.MethodGet, "http://"+testHost+"//evil.example/?token="+testToken, nil)
	r.Host = testHost
	if loc := h.do(r).Header().Get("Location"); loc != "/" {
		t.Fatalf("location %q, want /", loc)
	}
}

func TestTwoInstancesKeepSeparateCookies(t *testing.T) {
	one := httptest.NewServer(nil)
	two := httptest.NewServer(nil)
	defer one.Close()
	defer two.Close()
	setup := func(s *httptest.Server, token string) {
		s.Config.Handler = New(Options{Board: newFakeBoard(), Token: token, Addr: s.Listener.Addr().String(), Assets: testAssets})
	}
	setup(one, strings.Repeat("1", 64))
	setup(two, strings.Repeat("2", 64))

	jar, _ := cookiejar.New(nil)
	c := &http.Client{Jar: jar}
	get := func(u string) int {
		resp, err := c.Get(u)
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		return resp.StatusCode
	}
	if code := get(one.URL + "/?token=" + strings.Repeat("1", 64)); code != http.StatusOK {
		t.Fatalf("first sign-in: %d", code)
	}
	if code := get(two.URL + "/?token=" + strings.Repeat("2", 64)); code != http.StatusOK {
		t.Fatalf("second sign-in: %d", code)
	}
	if code := get(one.URL + contract.PathSnapshot); code != http.StatusOK {
		t.Fatalf("first instance signed out by the second: %d", code)
	}
	if code := get(two.URL + contract.PathSnapshot); code != http.StatusOK {
		t.Fatalf("second instance: %d", code)
	}
	u, _ := url.Parse(one.URL)
	if n := len(jar.Cookies(u)); n != 2 {
		t.Fatalf("%d cookies in the jar, want one per instance", n)
	}
}

func TestWrongCookieIs401WithReentryPage(t *testing.T) {
	h := newHarness()
	r := httptest.NewRequest(http.MethodGet, "http://"+testHost+contract.PathSnapshot, nil)
	r.Host = testHost
	r.AddCookie(&http.Cookie{Name: CookieName(testPort), Value: strings.Repeat("f", 64)}) // an earlier run's cookie
	w := h.do(r)
	wantStatus(t, w, http.StatusUnauthorized)
	body := w.Body.String()
	if !strings.Contains(body, "Open the link sitboard printed when it started. If that terminal is gone, start sitboard again.") {
		t.Fatalf("body %q", body)
	}
	if strings.Contains(body, "worker-a") || strings.Contains(body, cardA) {
		t.Fatal("the re-entry page leaks board data")
	}
}

func TestTokenComparisonIsConstantTime(t *testing.T) {
	src, err := os.ReadFile("security.go")
	if err != nil {
		t.Fatal(err)
	}
	s := string(src)
	if !strings.Contains(s, "subtle.ConstantTimeCompare") {
		t.Fatal("token not compared with subtle.ConstantTimeCompare")
	}
	for _, bad := range []string{"== token", "token ==", "!= token", "token !="} {
		if strings.Contains(s, bad) {
			t.Fatalf("token compared with %q", bad)
		}
	}
}

func TestFailsClosedWithoutToken(t *testing.T) {
	h := New(Options{Board: newFakeBoard(), Addr: testAddr, Assets: testAssets})
	r := req(http.MethodGet, contract.PathSnapshot, "")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code == http.StatusOK {
		t.Fatal("served without a token configured")
	}
}

func TestSecurityHeaders(t *testing.T) {
	h := newHarness()
	const csp = "default-src 'none'; script-src 'self'; style-src 'self'; img-src 'self'; font-src 'self'; connect-src 'self'; base-uri 'none'; form-action 'none'; frame-ancestors 'none'"
	unauth := httptest.NewRequest(http.MethodGet, "http://"+testHost+"/", nil)
	unauth.Host = testHost
	for _, r := range []*http.Request{req(http.MethodGet, "/", ""), req(http.MethodGet, contract.PathSnapshot, ""), unauth} {
		w := h.do(r)
		if got := w.Header().Get("Content-Security-Policy"); got != csp {
			t.Errorf("%s: CSP %q", r.URL.Path, got)
		}
		if got := w.Header().Get("X-Content-Type-Options"); got != "nosniff" {
			t.Errorf("%s: nosniff %q", r.URL.Path, got)
		}
		if got := w.Header().Get("Referrer-Policy"); got != "no-referrer" {
			t.Errorf("%s: referrer %q", r.URL.Path, got)
		}
	}
	if got := h.do(req(http.MethodGet, contract.PathSnapshot, "")).Header().Get("Cache-Control"); got != "no-store" {
		t.Errorf("api Cache-Control %q", got)
	}
}
