package server

import (
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/IZZY-Agency/sitboard/internal/contract"
)

const (
	groupP = "p_0123456789ab" // a project with parts
	plainP = "p_ba9876543210" // a project without parts
)

func projectBody(id string) string { return `{"projectId":"` + id + `"}` }

func newForgetHarness() *harness {
	h := newHarness()
	h.board.snap.Projects = []contract.Project{{ID: groupP, Name: "umbrella"}, {ID: plainP, Name: "repo", ParentID: groupP}}
	h.board.groupings = map[string]bool{groupP: true}
	return h
}

func TestForgetGroupingCallsBoard(t *testing.T) {
	h := newForgetHarness()
	wantStatus(t, h.do(req(http.MethodPost, contract.PathForgetGrouping, projectBody(groupP))), http.StatusNoContent)
	if len(h.board.forgotten) != 1 || h.board.forgotten[0] != groupP {
		t.Fatalf("forgotten %v", h.board.forgotten)
	}
}

// The page sends a project id only. A folder or anything else in the body is
// never read: the server resolves the folder itself.
func TestForgetGroupingIgnoresOtherFields(t *testing.T) {
	h := newForgetHarness()
	body := `{"projectId":"` + groupP + `","path":"/etc","folder":"~/x"}`
	wantStatus(t, h.do(req(http.MethodPost, contract.PathForgetGrouping, body)), http.StatusNoContent)
	if len(h.board.forgotten) != 1 || h.board.forgotten[0] != groupP {
		t.Fatalf("forgotten %v", h.board.forgotten)
	}
}

func TestForgetGroupingRefusals(t *testing.T) {
	long := `{"projectId":"` + groupP + `","pad":"` + strings.Repeat("x", 1100) + `"}`
	cases := []struct {
		name string
		mod  func(*http.Request)
		body string
		want int
	}{
		{"unknown project", nil, projectBody("p_ffffffffffff"), http.StatusNotFound},
		{"not a grouping", nil, projectBody(plainP), http.StatusConflict},
		{"empty body", nil, ``, http.StatusBadRequest},
		{"no id", nil, `{}`, http.StatusBadRequest},
		{"not JSON", nil, `projectId=` + groupP, http.StatusBadRequest},
		{"id as a number", nil, `{"projectId":12}`, http.StatusBadRequest},
		{"upper-case hex", nil, projectBody("p_0123456789AB"), http.StatusBadRequest},
		{"short id", nil, projectBody("p_0123"), http.StatusBadRequest},
		{"long id", nil, projectBody(groupP + "0"), http.StatusBadRequest},
		{"a path", nil, projectBody("../../etc"), http.StatusBadRequest},
		{"a card id", nil, projectBody(cardA), http.StatusBadRequest},
		{"body over 1 KiB", nil, long, http.StatusRequestEntityTooLarge},
		{"form content type", func(r *http.Request) { r.Header.Set("Content-Type", "application/x-www-form-urlencoded") }, projectBody(groupP), http.StatusUnsupportedMediaType},
		{"no content type", func(r *http.Request) { r.Header.Del("Content-Type") }, projectBody(groupP), http.StatusUnsupportedMediaType},
		{"no origin", func(r *http.Request) { r.Header.Del("Origin") }, projectBody(groupP), http.StatusForbidden},
		{"foreign origin", func(r *http.Request) { r.Header.Set("Origin", "http://evil.example") }, projectBody(groupP), http.StatusForbidden},
		{"cross-site fetch", func(r *http.Request) { r.Header.Set("Sec-Fetch-Site", "cross-site") }, projectBody(groupP), http.StatusForbidden},
		{"foreign host", func(r *http.Request) { r.Host = "evil.example:" + testPort }, projectBody(groupP), http.StatusForbidden},
		{"no token", func(r *http.Request) { r.Header.Del("Cookie") }, projectBody(groupP), http.StatusUnauthorized},
		{"wrong token", func(r *http.Request) {
			r.Header.Del("Cookie")
			r.AddCookie(&http.Cookie{Name: CookieName(testPort), Value: strings.Repeat("0", len(testToken))})
		}, projectBody(groupP), http.StatusUnauthorized},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			h := newForgetHarness()
			r := req(http.MethodPost, contract.PathForgetGrouping, c.body)
			if c.mod != nil {
				c.mod(r)
			}
			wantStatus(t, h.do(r), c.want)
			if len(h.board.forgotten) != 0 {
				t.Fatalf("a refused request forgot %v", h.board.forgotten)
			}
		})
	}
}

func TestForgetGroupingOnlyByPost(t *testing.T) {
	h := newForgetHarness()
	for _, m := range []string{http.MethodGet, http.MethodPut, http.MethodDelete} {
		w := h.do(req(m, contract.PathForgetGrouping, projectBody(groupP)))
		wantStatus(t, w, http.StatusMethodNotAllowed)
		if w.Header().Get("Allow") != http.MethodPost {
			t.Errorf("%s: Allow %q", m, w.Header().Get("Allow"))
		}
	}
	if len(h.board.forgotten) != 0 {
		t.Fatal("forgot on a method other than POST")
	}
}

// A state file that cannot be written is a 500, and says nothing more.
func TestForgetGroupingWriteFailureIs500(t *testing.T) {
	h := newForgetHarness()
	h.board.forgetErr = errors.New("state: rename: state.json: read-only file system")
	w := h.do(req(http.MethodPost, contract.PathForgetGrouping, projectBody(groupP)))
	wantStatus(t, w, http.StatusInternalServerError)
	if strings.Contains(w.Body.String(), "state.json") || strings.Contains(w.Body.String(), "read-only") {
		t.Fatalf("the error body leaks the failure: %q", w.Body.String())
	}
}
