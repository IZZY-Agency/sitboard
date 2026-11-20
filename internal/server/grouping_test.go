package server

import (
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/IZZY-Agency/sitboard/internal/app"
	"github.com/IZZY-Agency/sitboard/internal/contract"
	"github.com/IZZY-Agency/sitboard/internal/state"
)

func settingBody(id, mode string) string { return `{"projectId":"` + id + `","mode":"` + mode + `"}` }

func TestGroupingSettingCallsBoard(t *testing.T) {
	for _, mode := range contract.GroupingModes {
		h := newForgetHarness()
		wantStatus(t, h.do(req(http.MethodPost, contract.PathGroupingSetting, settingBody(groupP, string(mode)))), http.StatusNoContent)
		if want := []contract.GroupingRequest{{ProjectID: groupP, Mode: mode}}; len(h.board.set) != 1 || h.board.set[0] != want[0] {
			t.Fatalf("%s: set %v", mode, h.board.set)
		}
	}
}

// A folder or anything else in the body is never read: the server resolves
// the folder from the project id.
func TestGroupingSettingIgnoresOtherFields(t *testing.T) {
	h := newForgetHarness()
	body := `{"projectId":"` + groupP + `","mode":"never","path":"/etc","folder":"~/x"}`
	wantStatus(t, h.do(req(http.MethodPost, contract.PathGroupingSetting, body)), http.StatusNoContent)
	if len(h.board.set) != 1 || h.board.set[0] != (contract.GroupingRequest{ProjectID: groupP, Mode: contract.GroupingNever}) {
		t.Fatalf("set %v", h.board.set)
	}
}

func TestGroupingSettingRefusals(t *testing.T) {
	long := `{"projectId":"` + groupP + `","mode":"never","pad":"` + strings.Repeat("x", 1100) + `"}`
	cases := []struct {
		name string
		mod  func(*http.Request)
		body string
		want int
	}{
		{"unknown project", nil, settingBody("p_ffffffffffff", "never"), http.StatusNotFound},
		{"not a grouping", nil, settingBody(plainP, "never"), http.StatusConflict},
		{"no mode", nil, projectBody(groupP), http.StatusBadRequest},
		{"unknown mode", nil, settingBody(groupP, "sometimes"), http.StatusBadRequest},
		{"upper-case mode", nil, settingBody(groupP, "NEVER"), http.StatusBadRequest},
		{"mode as a number", nil, `{"projectId":"` + groupP + `","mode":1}`, http.StatusBadRequest},
		{"empty body", nil, ``, http.StatusBadRequest},
		{"no id", nil, `{"mode":"never"}`, http.StatusBadRequest},
		{"a path as the id", nil, settingBody("../../etc", "never"), http.StatusBadRequest},
		{"body over 1 KiB", nil, long, http.StatusRequestEntityTooLarge},
		{"form content type", func(r *http.Request) { r.Header.Set("Content-Type", "application/x-www-form-urlencoded") }, settingBody(groupP, "never"), http.StatusUnsupportedMediaType},
		{"no origin", func(r *http.Request) { r.Header.Del("Origin") }, settingBody(groupP, "never"), http.StatusForbidden},
		{"foreign origin", func(r *http.Request) { r.Header.Set("Origin", "http://evil.example") }, settingBody(groupP, "never"), http.StatusForbidden},
		{"cross-site fetch", func(r *http.Request) { r.Header.Set("Sec-Fetch-Site", "cross-site") }, settingBody(groupP, "never"), http.StatusForbidden},
		{"foreign host", func(r *http.Request) { r.Host = "evil.example:" + testPort }, settingBody(groupP, "never"), http.StatusForbidden},
		{"no token", func(r *http.Request) { r.Header.Del("Cookie") }, settingBody(groupP, "never"), http.StatusUnauthorized},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			h := newForgetHarness()
			r := req(http.MethodPost, contract.PathGroupingSetting, c.body)
			if c.mod != nil {
				c.mod(r)
			}
			wantStatus(t, h.do(r), c.want)
			if len(h.board.set) != 0 {
				t.Fatalf("a refused request set %v", h.board.set)
			}
		})
	}
}

func TestGroupingSettingOnlyByPost(t *testing.T) {
	h := newForgetHarness()
	for _, m := range []string{http.MethodGet, http.MethodPut, http.MethodDelete} {
		w := h.do(req(m, contract.PathGroupingSetting, settingBody(groupP, "never")))
		wantStatus(t, w, http.StatusMethodNotAllowed)
		if w.Header().Get("Allow") != http.MethodPost {
			t.Errorf("%s: Allow %q", m, w.Header().Get("Allow"))
		}
	}
	if len(h.board.set) != 0 {
		t.Fatal("set on a method other than POST")
	}
}

// Too many settings is a 409 the page can name; a state file that cannot be
// written is a 500 that says nothing more.
func TestGroupingSettingBoardErrors(t *testing.T) {
	h := newForgetHarness()
	h.board.setErr = app.ErrTooManySettings
	wantStatus(t, h.do(req(http.MethodPost, contract.PathGroupingSetting, settingBody(groupP, "never"))), http.StatusConflict)

	h.board.setErr = errors.New("state: rename: state.json: read-only file system")
	w := h.do(req(http.MethodPost, contract.PathGroupingSetting, settingBody(groupP, "never")))
	wantStatus(t, w, http.StatusInternalServerError)
	if strings.Contains(w.Body.String(), "state.json") || strings.Contains(w.Body.String(), "read-only") {
		t.Fatalf("the error body leaks the failure: %q", w.Body.String())
	}
}

// While sitboard is stopping, a setting or a forget cannot be saved: both
// answer 500, so the page reports it instead of showing a change that is lost.
func TestSettingAndForgetWhileStoppingAre500(t *testing.T) {
	h := newForgetHarness()
	h.board.setErr = state.ErrClosed
	h.board.forgetErr = state.ErrClosed
	for path, body := range map[string]string{
		contract.PathGroupingSetting: settingBody(groupP, "never"),
		contract.PathForgetGrouping:  projectBody(groupP),
	} {
		w := h.do(req(http.MethodPost, path, body))
		wantStatus(t, w, http.StatusInternalServerError)
		if got := strings.TrimSpace(w.Body.String()); got != "could not save the change" {
			t.Errorf("%s: body %q", path, got)
		}
	}
}
