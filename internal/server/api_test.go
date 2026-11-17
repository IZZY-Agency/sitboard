package server

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
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
	srv := httptest.NewUnstartedServer(nil)
	srv.Start()
	srv.Config.Handler = New(Options{Board: h.board, Jumper: h.jumper, Token: testToken, Addr: srv.Listener.Addr().String(), Assets: testAssets})
	ctx, stop := context.WithCancel(context.Background())
	r, _ := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL+contract.PathEvents, nil)
	_, port, _ := strings.Cut(srv.Listener.Addr().String(), ":")
	r.AddCookie(&http.Cookie{Name: CookieName(port), Value: testToken})
	resp, err := http.DefaultClient.Do(r) //nolint:bodyclose // closed by the returned cancel
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("events status %d", resp.StatusCode)
	}
	out := make(chan string, 64)
	go func() {
		defer close(out)
		sc := bufio.NewScanner(resp.Body)
		sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
		for sc.Scan() {
			out <- sc.Text()
		}
	}()
	return out, func() {
		stop()
		_ = resp.Body.Close()
		srv.Close()
	}
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
				var s contract.Snapshot
				if err := json.Unmarshal([]byte(v), &s); err != nil {
					t.Fatal(err)
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

func TestNotBuiltPage(t *testing.T) {
	h := New(Options{Board: newFakeBoard(), Token: testToken, Addr: testAddr, Assets: emptyFS{}})
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req(http.MethodGet, "/", ""))
	if !strings.Contains(w.Body.String(), "make build") {
		t.Fatalf("body %q", w.Body.String())
	}
}
