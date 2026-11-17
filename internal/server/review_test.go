package server

import (
	"bufio"
	"fmt"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"net/http/httptest"

	"github.com/IZZY-Agency/sitboard/internal/contract"
)

func TestShadowCookieDoesNotLockTheOwnerOut(t *testing.T) {
	h := newHarness()
	r := httptest.NewRequest(http.MethodGet, "http://"+testHost+contract.PathSnapshot, nil)
	r.Host = testHost
	// A cookie set on a narrower path by another local page is sent first.
	r.Header.Add("Cookie", CookieName(testPort)+"=planted; "+CookieName(testPort)+"="+testToken)
	wantStatus(t, h.do(r), http.StatusOK)

	r = httptest.NewRequest(http.MethodGet, "http://"+testHost+contract.PathSnapshot, nil)
	r.Host = testHost
	r.Header.Add("Cookie", CookieName(testPort)+"=planted; "+CookieName(testPort)+"=other")
	wantStatus(t, h.do(r), http.StatusUnauthorized)
}

func TestSecFetchSiteAllowsOnlyOurOwnPageOrTheOwner(t *testing.T) {
	h := newHarness()
	for site, want := range map[string]int{
		"":            http.StatusOK,
		"same-origin": http.StatusOK,
		"none":        http.StatusOK,
		"same-site":   http.StatusForbidden,
		"cross-site":  http.StatusForbidden,
		"weird":       http.StatusForbidden,
	} {
		r := req(http.MethodGet, contract.PathSnapshot, "")
		if site != "" {
			r.Header.Set("Sec-Fetch-Site", site)
		}
		if w := h.do(r); w.Code != want {
			t.Errorf("Sec-Fetch-Site %q: %d, want %d", site, w.Code, want)
		}
	}
}

func TestCrossOriginIsolationHeaders(t *testing.T) {
	h := newHarness()
	w := h.do(req(http.MethodGet, "/", ""))
	if got := w.Header().Get("Cross-Origin-Resource-Policy"); got != "same-origin" {
		t.Errorf("CORP %q", got)
	}
	if got := w.Header().Get("Cross-Origin-Opener-Policy"); got != "same-origin" {
		t.Errorf("COOP %q", got)
	}
}

func TestEventStreamIsGetOnly(t *testing.T) {
	h := newHarness()
	w := h.do(req(http.MethodHead, contract.PathEvents, ""))
	wantStatus(t, w, http.StatusMethodNotAllowed)
	if w.Header().Get("Allow") != http.MethodGet {
		t.Fatalf("Allow %q", w.Header().Get("Allow"))
	}
}

func TestJumpRefusesAMalformedTerminalID(t *testing.T) {
	for _, bad := range []string{"", "not-a-uuid", "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa --flag", "../../x"} {
		h := newHarness()
		h.board.terminals[cardA] = bad
		wantStatus(t, h.do(req(http.MethodPost, contract.PathJump, cardBody(cardA))), http.StatusConflict)
		if len(h.jumper.ids) != 0 {
			t.Fatalf("%q reached the jumper", bad)
		}
	}
}

func TestSlowPostBodyIsCutOff(t *testing.T) {
	old := bodyReadTimeout
	bodyReadTimeout = 200 * time.Millisecond
	defer func() { bodyReadTimeout = old }()
	h := newHarness()
	srv := httptest.NewUnstartedServer(nil)
	srv.Start()
	defer srv.Close()
	addr := srv.Listener.Addr().String()
	_, port, _ := net.SplitHostPort(addr)
	srv.Config.Handler = New(Options{Board: h.board, Jumper: h.jumper, Token: testToken, Addr: addr, Assets: testAssets})

	c, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = c.Close() }()
	// Headers promise 100 bytes; only a few arrive.
	_, _ = fmt.Fprintf(c, "POST %s HTTP/1.1\r\nHost: %s\r\nOrigin: http://%s\r\nContent-Type: application/json\r\nCookie: %s=%s\r\nContent-Length: 100\r\n\r\n{\"ca",
		contract.PathSeen, addr, addr, CookieName(port), testToken)
	_ = c.SetReadDeadline(time.Now().Add(3 * time.Second))
	began := time.Now()
	line, err := bufio.NewReader(c).ReadString('\n')
	if err != nil {
		t.Fatalf("no answer to a stalled body: %v", err)
	}
	if !strings.Contains(line, "400") {
		t.Fatalf("status line %q, want 400", line)
	}
	if d := time.Since(began); d > 2*time.Second {
		t.Fatalf("stalled body held the handler for %v", d)
	}
}

func TestMalformedCardIDIs400(t *testing.T) {
	h := newHarness()
	for _, id := range []string{"", "nope", "claude:", "shell:abc", "claude:../x", "claude:a b", "claude:" + strings.Repeat("a", 129)} {
		for _, p := range []string{contract.PathJump, contract.PathSeen} {
			if w := h.do(req(http.MethodPost, p, cardBody(id))); w.Code != http.StatusBadRequest {
				t.Errorf("%s %q: %d, want 400", p, id, w.Code)
			}
		}
	}
	if len(h.jumper.ids) != 0 || len(h.board.seen) != 0 {
		t.Fatal("a malformed id reached the board or agterm")
	}
}
