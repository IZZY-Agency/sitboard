package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sync/atomic"
	"time"

	"github.com/IZZY-Agency/sitboard/internal/app"
	"github.com/IZZY-Agency/sitboard/internal/contract"
)

// heartbeat keeps idle event streams open through proxies and lets the
// server notice a client that went away. A variable so tests can shorten it.
var heartbeat = 15 * time.Second

// maxStreams caps the event streams one server holds open at once; each costs
// a goroutine and an encoding of the snapshot on every change. The page opens
// one per tab, and a browser opens at most six connections to one host (HTTP/1.1),
// so this leaves room for several browsers and the moment of a reload, when
// the old stream has not yet ended, and still bounds a client that leaks them.
// The one past the cap gets 503 with Retry-After; the page then retries with
// its backoff, as after any failed stream.
const maxStreams = 32

type route struct {
	method string
	// sends is the value the page receives from this path (its type is what
	// counts), or nil for a path that answers with a status only. The facts
	// test (internal/facts) checks that every field of every such type is
	// disclosed, so a new path must say here what it sends.
	sends any
	h     http.HandlerFunc
}

// Sends lists, by API path, the value each path sends the page; a path that
// answers with a status only is not listed.
func Sends() map[string]any {
	out := map[string]any{}
	for path, rt := range routes(Options{}) {
		if rt.sends != nil {
			out[path] = rt.sends
		}
	}
	return out
}

// api serves /api/. Each path has exactly one method: another method is 405,
// an unknown path is 404.
func api(opts Options) http.Handler {
	routes := routes(opts)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rt, ok := routes[r.URL.Path]
		if !ok {
			http.NotFound(w, r)
			return
		}
		if r.Method != rt.method {
			w.Header().Set("Allow", rt.method)
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		rt.h(w, r)
	})
}

// routes are the API paths; each call has its own count of open event
// streams, so api, which calls it once, holds one for the server.
func routes(opts Options) map[string]route {
	var streams atomic.Int32 // event streams open now
	return map[string]route{
		contract.PathSnapshot: {http.MethodGet, contract.Snapshot{}, func(w http.ResponseWriter, _ *http.Request) {
			writeJSON(w, opts.Board.Snapshot())
		}},
		contract.PathEvents: {http.MethodGet, contract.Snapshot{}, func(w http.ResponseWriter, r *http.Request) {
			defer streams.Add(-1)
			if streams.Add(1) > maxStreams {
				w.Header().Set("Retry-After", "5")
				http.Error(w, fmt.Sprintf("too many event streams open (at most %d): close a tab of the board", maxStreams), http.StatusServiceUnavailable)
				return
			}
			events(w, r, opts.Board)
		}},
		contract.PathCatchup: {http.MethodGet, contract.Catchup{}, func(w http.ResponseWriter, r *http.Request) {
			c, err := opts.Board.Catchup(r.Context())
			if err != nil {
				http.Error(w, "catch-up unavailable", http.StatusInternalServerError)
				return
			}
			writeJSON(w, c)
		}},
		contract.PathLeftovers: {http.MethodGet, contract.Leftovers{}, func(w http.ResponseWriter, r *http.Request) {
			l, err := opts.Board.Leftovers(r.Context())
			if err != nil {
				http.Error(w, "leftovers unavailable", http.StatusInternalServerError)
				return
			}
			writeJSON(w, l)
		}},
		contract.PathIssues: {http.MethodGet, contract.Issues{}, func(w http.ResponseWriter, r *http.Request) {
			l, err := opts.Board.Issues(r.Context())
			if err != nil {
				http.Error(w, "issues unavailable", http.StatusInternalServerError)
				return
			}
			writeJSON(w, l)
		}},
		contract.PathJump:            {http.MethodPost, nil, jump(opts)},
		contract.PathSeen:            {http.MethodPost, nil, seen(opts)},
		contract.PathCatchupRead:     {http.MethodPost, nil, catchupRead(opts)},
		contract.PathForgetGrouping:  {http.MethodPost, nil, forgetGrouping(opts)},
		contract.PathGroupingSetting: {http.MethodPost, nil, groupingSetting(opts)},
	}
}

func writeJSON(w http.ResponseWriter, v any) {
	data, err := json.Marshal(v) // escapes <, > and & as <...
	if err != nil {
		http.Error(w, "encode failed", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(append(data, '\n'))
}

// events streams the snapshot on connect, after each change, and a comment
// line as a heartbeat. It ends when the client goes away or the server stops.
func events(w http.ResponseWriter, r *http.Request, board app.Board) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	// Subscribe before reading the snapshot, so no change falls in between.
	updates := board.Subscribe(r.Context())
	w.Header().Set("Content-Type", "text/event-stream")
	w.WriteHeader(http.StatusOK)

	send := func(s contract.Snapshot) bool {
		data, err := json.Marshal(s)
		if err != nil {
			return false
		}
		if _, err := fmt.Fprintf(w, "event: %s\ndata: %s\n\n", contract.EventSnapshot, data); err != nil {
			return false
		}
		flusher.Flush()
		return true
	}
	if !send(board.Snapshot()) {
		return
	}
	beat := time.NewTicker(heartbeat)
	defer beat.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case s, open := <-updates:
			if !open || !send(s) {
				return
			}
		case <-beat.C:
			if _, err := fmt.Fprint(w, ": ping\n\n"); err != nil {
				return
			}
			flusher.Flush()
		}
	}
}
