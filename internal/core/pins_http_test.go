package core

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/IZZY-Agency/sitboard/internal/contract"
	"github.com/IZZY-Agency/sitboard/internal/model"
	"github.com/IZZY-Agency/sitboard/internal/server"
	"github.com/IZZY-Agency/sitboard/internal/source"
	"github.com/IZZY-Agency/sitboard/internal/state"
)

const (
	httpToken = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	httpHost  = "127.0.0.1:4317"
)

// call sends one request through the real handler, as the page would.
func call(t *testing.T, h http.Handler, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(method, "http://"+httpHost+path, strings.NewReader(body))
	r.Host = httpHost
	if method == http.MethodPost {
		r.Header.Set("Origin", "http://"+httpHost)
		r.Header.Set("Content-Type", "application/json")
	}
	r.AddCookie(&http.Cookie{Name: server.CookieName("4317"), Value: httpToken})
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func getIssues(t *testing.T, h http.Handler) contract.Issues {
	t.Helper()
	w := call(t, h, http.MethodGet, contract.PathIssues, "")
	var l contract.Issues
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &l) != nil {
		t.Fatalf("GET issues: %d %s", w.Code, w.Body.String())
	}
	return l
}

// A real POST of a pin followed by a GET of the list: the listed row comes
// back pinned and first, a pin outside the listed rows comes back in pinned,
// an unpin takes both away. With a state file the list says the pins are
// saved, and a new board on the same file serves them again; with
// --no-state it says they are not, on every answer, issues off included.
func TestPinThroughTheServer(t *testing.T) {
	for _, mode := range []string{"file", "no-state"} {
		t.Run(mode, func(t *testing.T) {
			e, gh := newIssuesEnv(t)
			st := e.state
			if mode == "no-state" {
				st = state.MemoryAt(e.clock.Now)
			}
			board := func() *Board {
				b := newBoard(t, Options{
					Repos: e.repos, PRs: e.prs, State: st, Now: e.clock.Now, StaleAfter: 20 * time.Minute,
					Issues: map[model.Forge]source.IssueLister{model.ForgeGitHub: gh},
				})
				b.applySync(busySession("a", tNow))
				b.tick()
				return b
			}
			b := board()
			h := server.New(server.Options{Board: b, Token: httpToken, Addr: httpHost, Assets: fstest.MapFS{}})
			unsaved := mode == "no-state"
			if l := getIssues(t, h); l.Unsaved != unsaved || len(l.Repos) != 1 || len(l.Repos[0].Pinned) != 0 || l.Repos[0].Pinned == nil {
				t.Fatalf("before: %+v", l)
			}
			for _, n := range []int{45, 900} {
				body := `{"forge":"github","host":"github.com","repo":"Example-Org/example-repo","number":` + itoa(n) + `,"pinned":true}`
				if w := call(t, h, http.MethodPost, contract.PathIssuePin, body); w.Code != http.StatusNoContent {
					t.Fatalf("POST pin %d: %d %s", n, w.Code, w.Body.String())
				}
			}
			l := getIssues(t, h)
			r := l.Repos[0]
			if l.Unsaved != unsaved || numbers(r.Issues)[0] != 45 || !r.Issues[0].Pinned || r.Issues[1].Pinned ||
				len(r.Pinned) != 1 || r.Pinned[0].Number != 900 || r.Pinned[0].URL != "https://github.com/example-org/example-repo/issues/900" {
				t.Fatalf("after pinning: %+v", l)
			}
			if mode == "file" { // a restart on the same file serves the pins again
				h2 := server.New(server.Options{Board: board(), Token: httpToken, Addr: httpHost, Assets: fstest.MapFS{}})
				if r := getIssues(t, h2).Repos[0]; !r.Issues[0].Pinned || len(r.Pinned) != 1 {
					t.Fatalf("after a restart: %+v", r)
				}
			}
			for _, n := range []int{45, 900} {
				body := `{"forge":"github","host":"github.com","repo":"example-org/example-repo","number":` + itoa(n) + `,"pinned":false}`
				if w := call(t, h, http.MethodPost, contract.PathIssuePin, body); w.Code != http.StatusNoContent {
					t.Fatalf("POST unpin %d: %d", n, w.Code)
				}
			}
			if r := getIssues(t, h).Repos[0]; r.Issues[0].Pinned || r.Issues[1].Pinned || len(r.Pinned) != 0 {
				t.Fatalf("after unpinning: %+v", r)
			}
			b.SetSources([]source.Status{{Kind: source.KindGitHub, State: source.StateOff, Detail: "switched off"}})
			b.tick()
			if l := getIssues(t, h); l.State != contract.IssuesOff || l.Unsaved != unsaved {
				t.Fatalf("issues off: %+v", l)
			}
		})
	}
}

func itoa(n int) string { b, _ := json.Marshal(n); return string(b) }
