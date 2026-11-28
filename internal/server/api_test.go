package server

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/IZZY-Agency/sitboard/internal/contract"
)

func TestSnapshotEndpointReturnsBoardSnapshot(t *testing.T) {
	h := newHarness()
	w := h.do(req(http.MethodGet, contract.PathSnapshot, ""))
	wantStatus(t, w, http.StatusOK)
	var s contract.Snapshot
	if err := json.Unmarshal(w.Body.Bytes(), &s); err != nil {
		t.Fatal(err)
	}
	if len(s.Cards) != 1 || s.Cards[0].ID != cardA {
		t.Fatalf("cards %+v", s.Cards)
	}
}

// openStream starts a real server and reads its event stream line by line.
func openStream(t *testing.T, h *harness) (lines <-chan string, cancel func()) {
	t.Helper()
	srv := streamServer(h)
	resp, end := openOn(t, srv) //nolint:bodyclose // end closes it
	if resp.StatusCode != http.StatusOK {
		end()
		srv.Close()
		t.Fatalf("events status %d", resp.StatusCode)
	}
	return readLines(resp.Body), func() {
		end()
		srv.Close()
	}
}

// streamServer starts a real server for h, whose event stream tests open with
// openOn as often as they need.
func streamServer(h *harness) *httptest.Server {
	srv := httptest.NewUnstartedServer(nil)
	srv.Start()
	srv.Config.Handler = New(Options{Board: h.board, Jumper: h.jumper, Token: testToken, Addr: srv.Listener.Addr().String(), Assets: testAssets})
	return srv
}

// openOn opens srv's event stream, as a tab of the page does; end closes it.
func openOn(t *testing.T, srv *httptest.Server) (resp *http.Response, end func()) {
	t.Helper()
	ctx, stop := context.WithCancel(context.Background())
	r, _ := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL+contract.PathEvents, nil)
	_, port, _ := strings.Cut(srv.Listener.Addr().String(), ":")
	r.AddCookie(&http.Cookie{Name: CookieName(port), Value: testToken})
	resp, err := srv.Client().Do(r) //nolint:bodyclose // closed by the returned end
	if err != nil {
		stop()
		t.Fatal(err)
	}
	return resp, func() {
		stop()
		_ = resp.Body.Close()
	}
}

// readLines reads body line by line until it ends.
func readLines(body io.Reader) <-chan string {
	out := make(chan string, 64)
	go func() {
		defer close(out)
		sc := bufio.NewScanner(body)
		sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
		for sc.Scan() {
			out <- sc.Text()
		}
	}()
	return out
}

func nextData(t *testing.T, lines <-chan string, within time.Duration) contract.Snapshot {
	t.Helper()
	deadline := time.After(within)
	event := ""
	for {
		select {
		case l, ok := <-lines:
			if !ok {
				t.Fatal("stream ended")
			}
			if v, ok := strings.CutPrefix(l, "event: "); ok {
				event = v
			}
			if v, ok := strings.CutPrefix(l, "data: "); ok {
				if event != contract.EventSnapshot {
					t.Fatalf("event %q", event)
				}
				// Strictly: a field the stream sends beyond contract.Snapshot
				// (what Sends declares for it) fails, as for the other paths.
				var s contract.Snapshot
				dec := json.NewDecoder(strings.NewReader(v))
				dec.DisallowUnknownFields()
				if err := dec.Decode(&s); err != nil {
					t.Fatalf("the stream sends something other than contract.Snapshot: %v", err)
				}
				if dec.More() {
					t.Fatal("the stream sends more than one value in a data line")
				}
				return s
			}
		case <-deadline:
			t.Fatal("no snapshot event in time")
		}
	}
}

func TestEventsSendSnapshotOnChange(t *testing.T) {
	h := newHarness()
	lines, cancel := openStream(t, h)
	defer cancel()
	first := nextData(t, lines, time.Second)
	h.board.publish(func(s *contract.Snapshot) { s.Cards[0].Title = "changed" })
	next := nextData(t, lines, time.Second)
	if next.Seq != first.Seq+1 || next.Cards[0].Title != "changed" {
		t.Fatalf("got seq %d title %q", next.Seq, next.Cards[0].Title)
	}
}

func TestEventsHeartbeatEvery15s(t *testing.T) {
	if heartbeat != 15*time.Second {
		t.Fatalf("heartbeat %v, want 15s", heartbeat)
	}
	old := heartbeat
	heartbeat = 50 * time.Millisecond
	defer func() { heartbeat = old }()
	h := newHarness()
	lines, cancel := openStream(t, h)
	defer cancel()
	nextData(t, lines, time.Second)
	deadline := time.After(time.Second)
	for {
		select {
		case l := <-lines:
			if strings.HasPrefix(l, ": ping") {
				return
			}
		case <-deadline:
			t.Fatal("no heartbeat")
		}
	}
}

func TestClientDisconnectReleasesSubscription(t *testing.T) {
	h := newHarness()
	lines, cancel := openStream(t, h)
	nextData(t, lines, time.Second)
	if n := h.board.subscribers(); n != 1 {
		t.Fatalf("%d subscribers while connected", n)
	}
	cancel()
	deadline := time.Now().Add(2 * time.Second)
	for h.board.subscribers() != 0 {
		if time.Now().After(deadline) {
			t.Fatal("subscription kept after the client left")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestHostileTitleIsJSONEncodedNotInterpreted(t *testing.T) {
	h := newHarness()
	const hostile = `</script><script>alert(1)</script>`
	h.board.publish(func(s *contract.Snapshot) { s.Cards[0].Title = hostile })
	w := h.do(req(http.MethodGet, contract.PathSnapshot, ""))
	if ct := w.Header().Get("Content-Type"); ct != "application/json" {
		t.Fatalf("content type %q", ct)
	}
	if strings.Contains(w.Body.String(), "<script>") {
		t.Fatalf("raw markup in the response: %s", w.Body.String())
	}
	var s contract.Snapshot
	if err := json.Unmarshal(w.Body.Bytes(), &s); err != nil {
		t.Fatal(err)
	}
	if s.Cards[0].Title != hostile {
		t.Fatalf("title %q did not round-trip", s.Cards[0].Title)
	}
}

func TestCatchupAndLeftoversEndpoints(t *testing.T) {
	h := newHarness()
	for _, p := range []string{contract.PathCatchup, contract.PathLeftovers} {
		w := h.do(req(http.MethodGet, p, ""))
		wantStatus(t, w, http.StatusOK)
		if !strings.Contains(w.Body.String(), `"items":[]`) {
			t.Fatalf("%s: %s", p, w.Body.String())
		}
	}
}

func TestIssuesEndpointIsAGet(t *testing.T) {
	h := newHarness()
	w := h.do(req(http.MethodGet, contract.PathIssues, ""))
	wantStatus(t, w, http.StatusOK)
	if !strings.Contains(w.Body.String(), `"repos":[]`) {
		t.Fatalf("%s", w.Body.String())
	}
	wantStatus(t, h.do(req(http.MethodPost, contract.PathIssues, "{}")), http.StatusMethodNotAllowed)
}

// Every path that sends the page data says what it sends (route.sends), and
// what it sends decodes into that type with no field left over: the facts
// test discloses every field of those types, so a new path or a new field
// cannot reach the page unnamed.
func TestEveryDataPathSaysWhatItSends(t *testing.T) {
	h := newHarness()
	for path, rt := range routes(Options{}) {
		switch {
		case rt.method == http.MethodGet && rt.sends == nil:
			t.Errorf("%s: a GET path that does not say what it sends", path)
		case rt.method != http.MethodGet && rt.sends != nil:
			t.Errorf("%s: a %s path that says it sends data", path, rt.method)
		}
		if rt.sends == nil || path == contract.PathEvents { // the stream: TestEventsSendSnapshotOnChange
			continue
		}
		w := h.do(req(http.MethodGet, path, ""))
		wantStatus(t, w, http.StatusOK)
		dec := json.NewDecoder(strings.NewReader(w.Body.String()))
		dec.DisallowUnknownFields()
		v := reflect.New(reflect.TypeOf(rt.sends)).Interface()
		if err := dec.Decode(v); err != nil {
			t.Errorf("%s sends something other than %T: %v", path, rt.sends, err)
		}
	}
	if got := Sends(); len(got) == 0 || reflect.TypeOf(got[contract.PathEvents]) != reflect.TypeOf(contract.Snapshot{}) {
		t.Errorf("Sends() = %v", got)
	}
}

// A path that says it answers with a status only sends no body when it
// succeeds: data it sent would reach the page without the facts test
// seeing it. Each such path is called here with a request it accepts; a new
// status-only path must be added.
func TestStatusOnlyPathsSendNoBody(t *testing.T) {
	accepted := map[string]struct {
		h    func() *harness
		body string
	}{
		contract.PathJump:            {newHarness, cardBody(cardA)},
		contract.PathSeen:            {newHarness, cardBody(cardA)},
		contract.PathCatchupRead:     {newHarness, `{"upTo":"2026-11-17T09:55:00Z"}`},
		contract.PathForgetGrouping:  {newForgetHarness, projectBody(groupP)},
		contract.PathGroupingSetting: {newForgetHarness, settingBody(groupP, string(contract.GroupingNever))},
		contract.PathIssuePin:        {newPinHarness, pinBody("acme/widgets", 1, true)},
	}
	for path, rt := range routes(Options{}) {
		if rt.sends != nil {
			continue
		}
		c, ok := accepted[path]
		if !ok {
			t.Errorf("%s: a status-only path this test does not call", path)
			continue
		}
		w := c.h().do(req(rt.method, path, c.body))
		if w.Code < 200 || w.Code > 299 {
			t.Errorf("%s: status %d for an accepted request; body %q", path, w.Code, w.Body.String())
			continue
		}
		if w.Body.Len() != 0 {
			t.Errorf("%s: a status-only path sent %q", path, w.Body.String())
		}
	}
}

func TestUnknownPathIs404(t *testing.T) {
	h := newHarness()
	wantStatus(t, h.do(req(http.MethodGet, "/api/v1/nope", "")), http.StatusNotFound)
	wantStatus(t, h.do(req(http.MethodPost, "/api/v1/nope", "{}")), http.StatusNotFound)
	wantStatus(t, h.do(req(http.MethodGet, "/no-such-file.js", "")), http.StatusNotFound)
}

func TestAssetsServedWithCorrectTypes(t *testing.T) {
	h := newHarness()
	for path, want := range map[string]string{
		"/":                          "text/html",
		"/assets/app-1234.js":        "text/javascript",
		"/assets/app-1234.css":       "text/css",
		"/fonts/mono.woff2":          "font/woff2",
		"/assets/icon.svg":           "image/svg+xml",
		"/assets/nested/data-1.json": "application/json",
	} {
		w := h.do(req(http.MethodGet, path, ""))
		wantStatus(t, w, http.StatusOK)
		if ct := w.Header().Get("Content-Type"); !strings.HasPrefix(ct, want) {
			t.Errorf("%s: content type %q, want %s", path, ct, want)
		}
	}
}

func TestTabIconsMayBeKeptButNothingElse(t *testing.T) {
	h := newHarness()
	w := h.do(req(http.MethodGet, "/badge/lost.svg", ""))
	wantStatus(t, w, http.StatusOK)
	if got := w.Header().Get("Cache-Control"); got != BadgeCache {
		t.Errorf("badge Cache-Control %q, want %q", got, BadgeCache)
	}
	if !strings.Contains(BadgeCache, "private") || !strings.Contains(BadgeCache, "max-age=") {
		t.Errorf("BadgeCache %q must be private with a max-age", BadgeCache)
	}
	// Not an error, not another file, and never without the key.
	for _, path := range []string{"/badge/", "/badge/nope.svg", "/favicon.svg", "/", "/assets/icon.svg", "/badge/../favicon.svg", "/badge/..", "/badge/./lost.svg", "/badge//lost.svg"} {
		if got := h.do(req(http.MethodGet, path, "")).Header().Get("Cache-Control"); got != "" {
			t.Errorf("%s: Cache-Control %q, want none", path, got)
		}
	}
	// A version query names the same file and keeps the header.
	w = h.do(req(http.MethodGet, "/badge/lost.svg?v=abc1234", ""))
	wantStatus(t, w, http.StatusOK)
	if got := w.Header().Get("Cache-Control"); got != BadgeCache {
		t.Errorf("versioned badge Cache-Control %q", got)
	}
	keyless := req(http.MethodGet, "/badge/lost.svg", "")
	keyless.Header.Del("Cookie")
	w = h.do(keyless)
	wantStatus(t, w, http.StatusUnauthorized)
	if got := w.Header().Get("Cache-Control"); got == BadgeCache {
		t.Errorf("a refused badge request must not be kept: Cache-Control %q", got)
	}
}

func TestNotBuiltPage(t *testing.T) {
	h := New(Options{Board: newFakeBoard(), Token: testToken, Addr: testAddr, Assets: emptyFS{}})
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req(http.MethodGet, "/", ""))
	if !strings.Contains(w.Body.String(), "make build") {
		t.Fatalf("body %q", w.Body.String())
	}
}
