package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/IZZY-Agency/sitboard/internal/app"
	"github.com/IZZY-Agency/sitboard/internal/contract"
)

// heartbeat keeps idle event streams open through proxies and lets the
// server notice a client that went away. A variable so tests can shorten it.
var heartbeat = 15 * time.Second

type route struct {
	method string
	h      http.HandlerFunc
}

// api serves /api/. Each path has exactly one method: another method is 405,
// an unknown path is 404.
func api(opts Options) http.Handler {
	routes := map[string]route{
		contract.PathSnapshot: {http.MethodGet, func(w http.ResponseWriter, _ *http.Request) {
			writeJSON(w, opts.Board.Snapshot())
		}},
		contract.PathEvents: {http.MethodGet, func(w http.ResponseWriter, r *http.Request) {
			events(w, r, opts.Board)
		}},
		contract.PathCatchup: {http.MethodGet, func(w http.ResponseWriter, r *http.Request) {
			c, err := opts.Board.Catchup(r.Context())
			if err != nil {
				http.Error(w, "catch-up unavailable", http.StatusInternalServerError)
				return
			}
			writeJSON(w, c)
		}},
		contract.PathLeftovers: {http.MethodGet, func(w http.ResponseWriter, r *http.Request) {
			l, err := opts.Board.Leftovers(r.Context())
			if err != nil {
				http.Error(w, "leftovers unavailable", http.StatusInternalServerError)
				return
			}
			writeJSON(w, l)
		}},
		contract.PathJump:           {http.MethodPost, jump(opts)},
		contract.PathSeen:           {http.MethodPost, seen(opts)},
		contract.PathCatchupRead:    {http.MethodPost, catchupRead(opts)},
		contract.PathForgetGrouping: {http.MethodPost, forgetGrouping(opts)},
	}
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
