package server

import (
	"errors"
	"net/http"
	"strconv"
	"strings"
	"testing"

	"github.com/IZZY-Agency/sitboard/internal/app"
	"github.com/IZZY-Agency/sitboard/internal/contract"
	"github.com/IZZY-Agency/sitboard/internal/model"
)

var pinRepo = model.ForgeRepo{Forge: model.ForgeGitHub, Host: "github.com", Path: "acme/widgets"}

func newPinHarness() *harness {
	h := newHarness()
	h.board.pinRepos = []model.ForgeRepo{pinRepo}
	return h
}

func pinBody(repo string, number int, pinned bool) string {
	return `{"forge":"github","host":"github.com","repo":"` + repo + `","number":` + strconv.Itoa(number) + `,"pinned":` + strconv.FormatBool(pinned) + `}`
}

// A pin reaches the board with the repository as the body names it; the
// same pin twice is one pin, and an unpin takes it away. Each answers 204.
func TestIssuePinCallsBoardAndIsIdempotent(t *testing.T) {
	h := newPinHarness()
	for range 2 {
		wantStatus(t, h.do(req(http.MethodPost, contract.PathIssuePin, pinBody("acme/widgets", 145, true))), http.StatusNoContent)
	}
	if len(h.board.pins) != 1 || !h.board.pins["github|github.com|acme/widgets#145"] {
		t.Fatalf("pins %v", h.board.pins)
	}
	for range 2 {
		wantStatus(t, h.do(req(http.MethodPost, contract.PathIssuePin, pinBody("acme/widgets", 145, false))), http.StatusNoContent)
	}
	if len(h.board.pins) != 0 {
		t.Fatalf("pins after unpinning %v", h.board.pins)
	}
	// The largest number GitHub and GitLab use, and any other field ignored.
	body := `{"forge":"github","host":"github.com","repo":"acme/widgets","number":2147483647,"pinned":true,"path":"/etc","title":"x"}`
	wantStatus(t, h.do(req(http.MethodPost, contract.PathIssuePin, body)), http.StatusNoContent)
	if !h.board.pins["github|github.com|acme/widgets#2147483647"] {
		t.Fatalf("pins %v", h.board.pins)
	}
}

func TestIssuePinRefusals(t *testing.T) {
	long := `{"forge":"github","host":"github.com","repo":"acme/widgets","number":1,"pinned":true,"pad":"` + strings.Repeat("x", 1100) + `"}`
	ok := pinBody("acme/widgets", 1, true)
	cases := []struct {
		name string
		mod  func(*http.Request)
		body string
		want int
	}{
		{"unknown repository", nil, pinBody("acme/other", 1, true), http.StatusNotFound},
		{"another host", nil, strings.Replace(ok, `"host":"github.com"`, `"host":"git.example.com"`, 1), http.StatusNotFound},
		{"another forge", nil, strings.Replace(ok, `"forge":"github"`, `"forge":"gitlab"`, 1), http.StatusNotFound},
		{"forge none", nil, strings.Replace(ok, `"forge":"github"`, `"forge":"none"`, 1), http.StatusBadRequest},
		{"unknown forge", nil, strings.Replace(ok, `"forge":"github"`, `"forge":"svn"`, 1), http.StatusBadRequest},
		{"no host", nil, strings.Replace(ok, `"host":"github.com"`, `"host":""`, 1), http.StatusBadRequest},
		{"no repo", nil, pinBody("", 1, true), http.StatusBadRequest},
		{"number 0", nil, pinBody("acme/widgets", 0, true), http.StatusBadRequest},
		{"negative number", nil, pinBody("acme/widgets", -3, true), http.StatusBadRequest},
		{"number 2^31", nil, pinBody("acme/widgets", 2147483648, true), http.StatusBadRequest},
		{"fractional number", nil, strings.Replace(ok, `"number":1`, `"number":1.5`, 1), http.StatusBadRequest},
		{"number as a string", nil, strings.Replace(ok, `"number":1`, `"number":"1"`, 1), http.StatusBadRequest},
		{"no number", nil, `{"forge":"github","host":"github.com","repo":"acme/widgets","pinned":true}`, http.StatusBadRequest},
		{"pinned as a string", nil, strings.Replace(ok, `"pinned":true`, `"pinned":"yes"`, 1), http.StatusBadRequest},
		{"empty body", nil, ``, http.StatusBadRequest},
		{"body over 1 KiB", nil, long, http.StatusRequestEntityTooLarge},
		{"form content type", func(r *http.Request) { r.Header.Set("Content-Type", "application/x-www-form-urlencoded") }, ok, http.StatusUnsupportedMediaType},
		{"no origin", func(r *http.Request) { r.Header.Del("Origin") }, ok, http.StatusForbidden},
		{"foreign origin", func(r *http.Request) { r.Header.Set("Origin", "http://evil.example") }, ok, http.StatusForbidden},
		{"cross-site fetch", func(r *http.Request) { r.Header.Set("Sec-Fetch-Site", "cross-site") }, ok, http.StatusForbidden},
		{"foreign host", func(r *http.Request) { r.Host = "evil.example:" + testPort }, ok, http.StatusForbidden},
		{"no token", func(r *http.Request) { r.Header.Del("Cookie") }, ok, http.StatusUnauthorized},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			h := newPinHarness()
			r := req(http.MethodPost, contract.PathIssuePin, c.body)
			if c.mod != nil {
				c.mod(r)
			}
			wantStatus(t, h.do(r), c.want)
			if len(h.board.pins) != 0 || h.board.pinCalls != 0 {
				t.Fatalf("a refused request pinned %v", h.board.pins)
			}
		})
	}
}

func TestIssuePinOnlyByPost(t *testing.T) {
	h := newPinHarness()
	for _, m := range []string{http.MethodGet, http.MethodPut, http.MethodDelete} {
		w := h.do(req(m, contract.PathIssuePin, pinBody("acme/widgets", 1, true)))
		wantStatus(t, w, http.StatusMethodNotAllowed)
		if w.Header().Get("Allow") != http.MethodPost {
			t.Errorf("%s: Allow %q", m, w.Header().Get("Allow"))
		}
	}
	if len(h.board.pins) != 0 {
		t.Fatal("pinned on a method other than POST")
	}
}

// Too many pins is a 409 the page can name; a state file that cannot be
// written is a 500 that says nothing more.
func TestIssuePinBoardErrors(t *testing.T) {
	h := newPinHarness()
	h.board.pinErr = app.ErrTooManyPins
	w := h.do(req(http.MethodPost, contract.PathIssuePin, pinBody("acme/widgets", 1, true)))
	wantStatus(t, w, http.StatusConflict)
	if got := strings.TrimSpace(w.Body.String()); got != "too many issues are pinned" {
		t.Errorf("body %q", got)
	}
	h.board.pinErr = errors.New("state: rename: state.json: read-only file system")
	w = h.do(req(http.MethodPost, contract.PathIssuePin, pinBody("acme/widgets", 1, true)))
	wantStatus(t, w, http.StatusInternalServerError)
	if got := strings.TrimSpace(w.Body.String()); got != "could not save the change" {
		t.Fatalf("the error body leaks the failure: %q", got)
	}
}

// pinned must be said: a body that leaves it out, or sends null, is refused
// and leaves an existing pin alone; false is an unpin.
func TestIssuePinNeedsAnExplicitPinned(t *testing.T) {
	h := newPinHarness()
	wantStatus(t, h.do(req(http.MethodPost, contract.PathIssuePin, pinBody("acme/widgets", 145, true))), http.StatusNoContent)
	for name, body := range map[string]string{
		"missing": `{"forge":"github","host":"github.com","repo":"acme/widgets","number":145}`,
		"null":    `{"forge":"github","host":"github.com","repo":"acme/widgets","number":145,"pinned":null}`,
	} {
		wantStatus(t, h.do(req(http.MethodPost, contract.PathIssuePin, body)), http.StatusBadRequest)
		if !h.board.pins["github|github.com|acme/widgets#145"] || h.board.pinCalls != 1 {
			t.Fatalf("%s: the pin changed: %v", name, h.board.pins)
		}
	}
	wantStatus(t, h.do(req(http.MethodPost, contract.PathIssuePin, pinBody("acme/widgets", 145, false))), http.StatusNoContent)
	if len(h.board.pins) != 0 {
		t.Fatalf("false did not unpin: %v", h.board.pins)
	}
}
