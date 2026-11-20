package server

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"testing/fstest"
	"time"

	"github.com/IZZY-Agency/sitboard/internal/app"
	"github.com/IZZY-Agency/sitboard/internal/contract"
)

const (
	testToken = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	testPort  = "4317"
	testAddr  = "127.0.0.1:" + testPort
	testHost  = "127.0.0.1:" + testPort
	testOrig  = "http://127.0.0.1:" + testPort
	cardA     = "claude:11111111-1111-4111-8111-111111111111"
	termA     = "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
)

// fakeBoard is an app.Board the tests drive by hand.
type fakeBoard struct {
	mu        sync.Mutex
	snap      contract.Snapshot
	subs      map[chan contract.Snapshot]struct{}
	terminals map[string]string
	seen      []string
	catchup   int
	upTo      time.Time
	groupings map[string]bool            // project id -> it has parts
	forgotten []string                   // project ids ForgetGrouping accepted
	forgetErr error                      // what ForgetGrouping returns for a grouping
	set       []contract.GroupingRequest // what SetGrouping accepted
	setErr    error                      // what SetGrouping returns for a grouping
}

var _ app.Board = (*fakeBoard)(nil)

func newFakeBoard() *fakeBoard {
	return &fakeBoard{
		snap: contract.Snapshot{
			Version: contract.Version, Sources: []contract.SourceStatus{}, Projects: []contract.Project{},
			Cards: []contract.Card{{ID: cardA, Title: "worker-a", Attention: []contract.Attention{}, PullRequests: []contract.PullRequest{}, Issues: []contract.Issue{}}},
		},
		subs:      map[chan contract.Snapshot]struct{}{},
		terminals: map[string]string{cardA: termA},
	}
}

func (f *fakeBoard) Snapshot() contract.Snapshot {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.snap
}

func (f *fakeBoard) Subscribe(ctx context.Context) <-chan contract.Snapshot {
	ch := make(chan contract.Snapshot, 1)
	f.mu.Lock()
	f.subs[ch] = struct{}{}
	f.mu.Unlock()
	go func() {
		<-ctx.Done()
		f.mu.Lock()
		delete(f.subs, ch)
		close(ch)
		f.mu.Unlock()
	}()
	return ch
}

func (f *fakeBoard) subscribers() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.subs)
}

// publish changes the board and tells subscribers.
func (f *fakeBoard) publish(change func(*contract.Snapshot)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	change(&f.snap)
	f.snap.Seq++
	for ch := range f.subs {
		select {
		case <-ch:
		default:
		}
		ch <- f.snap
	}
}

func (f *fakeBoard) Catchup(context.Context) (contract.Catchup, error) {
	return contract.Catchup{Items: []contract.CatchupItem{}}, nil
}

func (f *fakeBoard) Leftovers(context.Context) (contract.Leftovers, error) {
	return contract.Leftovers{Items: []contract.Leftover{}}, nil
}

func (f *fakeBoard) MarkSeen(id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.seen = append(f.seen, id)
	return nil
}

func (f *fakeBoard) MarkCatchupRead(upTo time.Time) error {
	f.mu.Lock()
	f.catchup++
	f.upTo = upTo
	f.mu.Unlock()
	return nil
}

func (f *fakeBoard) ForgetGrouping(id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.groupings[id] {
		for _, p := range f.snap.Projects {
			if p.ID == id {
				return app.ErrNotGrouping
			}
		}
		return app.ErrUnknownProject
	}
	if f.forgetErr != nil {
		return f.forgetErr
	}
	f.forgotten = append(f.forgotten, id)
	return nil
}

func (f *fakeBoard) SetGrouping(id string, mode contract.GroupingMode) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.groupings[id] {
		for _, p := range f.snap.Projects {
			if p.ID == id {
				return app.ErrNotGrouping
			}
		}
		return app.ErrUnknownProject
	}
	if f.setErr != nil {
		return f.setErr
	}
	f.set = append(f.set, contract.GroupingRequest{ProjectID: id, Mode: mode})
	return nil
}

func (f *fakeBoard) TerminalFor(id string) (string, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	t, ok := f.terminals[id]
	return t, ok
}

type fakeJumper struct {
	mu  sync.Mutex
	ids []string
	err error
}

func (j *fakeJumper) Jump(_ context.Context, id string) error {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.ids = append(j.ids, id)
	return j.err
}

var errAgterm = errors.New("agterm down")

var testAssets = fstest.MapFS{
	"index.html":                {Data: []byte("<!doctype html><title>sitboard</title>")},
	"assets/app-1234.js":        {Data: []byte("console.log(1)")},
	"assets/app-1234.css":       {Data: []byte("body{}")},
	"fonts/mono.woff2":          {Data: []byte("wOF2")},
	"assets/icon.svg":           {Data: []byte("<svg/>")},
	"assets/nested/data-1.json": {Data: []byte("{}")},
	"badge/lost.svg":            {Data: []byte("<svg/>")},
	"favicon.svg":               {Data: []byte("<svg/>")},
}

type harness struct {
	board  *fakeBoard
	jumper *fakeJumper
	h      http.Handler
}

func newHarness() *harness {
	h := &harness{board: newFakeBoard(), jumper: &fakeJumper{}}
	h.h = New(Options{Board: h.board, Jumper: h.jumper, Token: testToken, Addr: testAddr, Assets: testAssets})
	return h
}

// req builds a request that passes the security layer unless the test
// changes it: our Host, our Origin on non-GET, and the cookie.
func req(method, path, body string) *http.Request {
	r := httptest.NewRequest(method, "http://"+testHost+path, strings.NewReader(body))
	r.Host = testHost
	if method != http.MethodGet && method != http.MethodHead {
		r.Header.Set("Origin", testOrig)
		r.Header.Set("Content-Type", "application/json")
	}
	r.AddCookie(&http.Cookie{Name: CookieName(testPort), Value: testToken})
	return r
}

func (h *harness) do(r *http.Request) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	h.h.ServeHTTP(w, r)
	return w
}

func wantStatus(t *testing.T, w *httptest.ResponseRecorder, code int) {
	t.Helper()
	if w.Code != code {
		t.Fatalf("status %d, want %d; body %q", w.Code, code, w.Body.String())
	}
}
