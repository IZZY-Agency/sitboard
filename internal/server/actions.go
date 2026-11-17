package server

import (
	"encoding/json"
	"errors"
	"io"
	"log"
	"mime"
	"net/http"
	"regexp"
	"time"

	"github.com/IZZY-Agency/sitboard/internal/contract"
	"github.com/IZZY-Agency/sitboard/internal/source"
)

// maxBody caps an action's request body.
const maxBody = 1024

// bodyReadTimeout bounds how long a POST may take to send its body. The
// server has no global read timeout because the event stream is long-lived.
var bodyReadTimeout = 10 * time.Second

// terminalIDRe is the shape of an agterm session id. The id comes from the
// board, not the page; this is defence in depth before it reaches agtermctl.
var terminalIDRe = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

// jump focuses the card's agterm session. The server resolves the session from
// the card id; a terminal id sent by the page is never read.
func jump(opts Options) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		cardID, ok := readCard(w, r)
		if !ok {
			return
		}
		if !onBoard(opts, cardID) {
			http.Error(w, "unknown card", http.StatusNotFound)
			return
		}
		terminal, ok := opts.Board.TerminalFor(cardID)
		if !ok || opts.Jumper == nil || !terminalIDRe.MatchString(terminal) {
			http.Error(w, "this card has no agterm session to focus", http.StatusConflict)
			return
		}
		if err := opts.Jumper.Jump(r.Context(), terminal); err != nil {
			if errors.Is(err, source.ErrUnknownTerminal) {
				http.Error(w, "the agterm session is gone", http.StatusConflict)
				return
			}
			http.Error(w, "agterm did not answer", http.StatusServiceUnavailable)
			return
		}
		// Jumping to a card is looking at it.
		if err := opts.Board.MarkSeen(cardID); err != nil {
			log.Printf("server: jump: could not mark the card seen: %v", err)
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

func seen(opts Options) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		cardID, ok := readCard(w, r)
		if !ok {
			return
		}
		if !onBoard(opts, cardID) {
			http.Error(w, "unknown card", http.StatusNotFound)
			return
		}
		if err := opts.Board.MarkSeen(cardID); err != nil {
			http.Error(w, "could not mark the card seen", http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

func catchupRead(opts Options) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if _, ok := readBody(w, r); !ok {
			return
		}
		if err := opts.Board.MarkCatchupRead(); err != nil {
			http.Error(w, "could not save the catch-up marker", http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

// readCard reads a JSON CardRequest. Unknown fields are ignored.
func readCard(w http.ResponseWriter, r *http.Request) (string, bool) {
	mt, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mt != "application/json" {
		http.Error(w, "send application/json", http.StatusUnsupportedMediaType)
		return "", false
	}
	body, ok := readBody(w, r)
	if !ok {
		return "", false
	}
	var req contract.CardRequest
	if err := json.Unmarshal(body, &req); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return "", false
	}
	if _, _, ok := contract.ParseCardID(req.CardID); !ok {
		http.Error(w, "bad card id", http.StatusBadRequest)
		return "", false
	}
	return req.CardID, true
}

func readBody(w http.ResponseWriter, r *http.Request) ([]byte, bool) {
	// Not every ResponseWriter supports deadlines (test recorders do not).
	_ = http.NewResponseController(w).SetReadDeadline(time.Now().Add(bodyReadTimeout))
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxBody))
	if err != nil {
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			http.Error(w, "body too large", http.StatusRequestEntityTooLarge)
			return nil, false
		}
		http.Error(w, "bad request", http.StatusBadRequest)
		return nil, false
	}
	return body, true
}

func onBoard(opts Options, cardID string) bool {
	for _, c := range opts.Board.Snapshot().Cards {
		if c.ID == cardID {
			return true
		}
	}
	return false
}
