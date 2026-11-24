package server

import (
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

// openStreams opens n event streams on srv, each of which must be accepted
// and send its first snapshot, and returns their ends.
func openStreams(t *testing.T, open func() (*http.Response, func()), n int) []func() {
	t.Helper()
	var ends []func()
	for i := range n {
		resp, end := open() //nolint:bodyclose // end closes it
		ends = append(ends, end)
		if resp.StatusCode != http.StatusOK {
			for _, e := range ends {
				e()
			}
			t.Fatalf("stream %d of %d: status %d", i+1, n, resp.StatusCode)
		}
		nextData(t, readLines(resp.Body), time.Second)
	}
	return ends
}

// waitFor polls cond until it holds; it fails t after a while.
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal(what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// The server holds at most maxStreams event streams at once. The one past the
// cap is refused with 503, Retry-After and a reason, and holds no
// subscription; a stream that ends makes room for a new one.
func TestEventStreamsAreCapped(t *testing.T) {
	h := newHarness()
	srv := streamServer(h)
	defer srv.Close()
	open := func() (*http.Response, func()) { return openOn(t, srv) }
	ends := openStreams(t, open, maxStreams)
	defer func() {
		for _, e := range ends {
			e()
		}
	}()

	refused := func() {
		t.Helper()
		resp, end := openOn(t, srv) //nolint:bodyclose // end closes it
		defer end()
		if resp.StatusCode != http.StatusServiceUnavailable {
			t.Fatalf("past the cap: status %d, want %d", resp.StatusCode, http.StatusServiceUnavailable)
		}
		body, _ := io.ReadAll(resp.Body)
		if resp.Header.Get("Retry-After") == "" ||
			!strings.Contains(string(body), "too many event streams") {
			t.Fatalf("past the cap: status %d, Retry-After %q, body %q", resp.StatusCode, resp.Header.Get("Retry-After"), body)
		}
	}
	refused()
	if n := h.board.subscribers(); n != maxStreams {
		t.Fatalf("%d subscribers, want %d: a refused stream must not subscribe", n, maxStreams)
	}

	ends[0]()
	ends = ends[1:]
	waitFor(t, "a stream that ended kept its place", func() bool {
		resp, end := openOn(t, srv) //nolint:bodyclose // end closes it, or ends does
		if resp.StatusCode == http.StatusOK {
			ends = append(ends, end)
			return true
		}
		end()
		return false
	})
	refused()
}

// A board open in a browser's six tabs (the most it opens to one host),
// reloaded again and again, keeps streaming: each reload opens the new stream
// before the old one ends, the worst overlap, and the old ones' places come
// back. More streams are opened in all than the cap.
func TestReloadedTabsKeepStreaming(t *testing.T) {
	const tabs, reloads = 6, 5
	if tabs*(reloads+1) <= maxStreams {
		t.Fatalf("%d tabs reloaded %d times open no more than the cap of %d", tabs, reloads, maxStreams)
	}
	h := newHarness()
	srv := streamServer(h)
	defer srv.Close()
	var ends []func()
	defer func() { // before srv.Close, which waits for open streams
		for _, e := range ends {
			e()
		}
	}()
	open := func() (*http.Response, func()) {
		resp, end := openOn(t, srv)
		ends = append(ends, end)
		return resp, end
	}
	tabsNow := openStreams(t, open, tabs)
	for range reloads {
		next := openStreams(t, open, tabs)
		for _, e := range tabsNow {
			e()
		}
		tabsNow = next
		waitFor(t, "reloaded tabs' old streams kept their subscriptions", func() bool { return h.board.subscribers() == tabs })
	}
}
