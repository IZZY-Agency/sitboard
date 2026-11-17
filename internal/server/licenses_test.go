package server

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

const testLicenses = "MIT License\n\n# Third-party notices\n"

func licensesHarness() *harness {
	h := &harness{board: newFakeBoard(), jumper: &fakeJumper{}}
	h.h = New(Options{Board: h.board, Jumper: h.jumper, Token: testToken, Addr: testAddr, Assets: testAssets, Licenses: testLicenses})
	return h
}

func TestLicensesTxtIsPlainText(t *testing.T) {
	h := licensesHarness()
	for _, method := range []string{http.MethodGet, http.MethodHead} {
		w := h.do(req(method, PathLicenses, ""))
		wantStatus(t, w, http.StatusOK)
		if got := w.Header().Get("Content-Type"); got != "text/plain; charset=utf-8" {
			t.Errorf("%s: Content-Type %q", method, got)
		}
		if got := w.Header().Get("X-Content-Type-Options"); got != "nosniff" {
			t.Errorf("%s: nosniff %q", method, got)
		}
		if got := w.Header().Get("Content-Security-Policy"); got == "" {
			t.Errorf("%s: no CSP", method)
		}
		switch {
		case method == http.MethodGet && w.Body.String() != testLicenses:
			t.Errorf("GET body %q", w.Body.String())
		case method == http.MethodHead && w.Body.Len() != 0:
			t.Errorf("HEAD body %q, want none", w.Body.String())
		}
	}
}

func TestLicensesTxtNeedsTheToken(t *testing.T) {
	h := licensesHarness()
	r := httptest.NewRequest(http.MethodGet, "http://"+testHost+PathLicenses, nil)
	r.Host = testHost
	w := h.do(r)
	wantStatus(t, w, http.StatusUnauthorized)
	if w.Body.String() == testLicenses {
		t.Fatal("served the text without the token")
	}
}

func TestLicensesTxtIsReadOnly(t *testing.T) {
	w := licensesHarness().do(req(http.MethodPost, PathLicenses, ""))
	wantStatus(t, w, http.StatusMethodNotAllowed)
}

func TestLicensesTxtWithoutTextIsNotFound(t *testing.T) {
	// A handler built without the texts never serves an empty licence page.
	w := newHarness().do(req(http.MethodGet, PathLicenses, ""))
	wantStatus(t, w, http.StatusNotFound)
}
