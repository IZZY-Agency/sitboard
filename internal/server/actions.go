package server

import (
	"encoding/json"
	"errors"
	"io"
	"log"
	"mime"
	"net/http"
	"regexp"
	"slices"
	"time"

	"github.com/IZZY-Agency/sitboard/internal/app"
	"github.com/IZZY-Agency/sitboard/internal/contract"
	"github.com/IZZY-Agency/sitboard/internal/model"
	"github.com/IZZY-Agency/sitboard/internal/source"
)

// maxBody caps an action's request body.
const maxBody = 1024

// bodyReadTimeout bounds how long a POST may take to send its body. The
// server has no global read timeout because the event stream is long-lived.
var bodyReadTimeout = 10 * time.Second

// terminalIDRe is the shape of an agterm session id. The target comes from
// the board, not the page; this check and TmuxPane.Valid are defence in depth
// before an id reaches agtermctl or tmux.
var terminalIDRe = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

// validTarget says whether t has the shape its kind requires.
func validTarget(t model.FocusTarget) bool {
	switch t.Kind {
	case model.FocusAgterm:
		return terminalIDRe.MatchString(t.ID) && t.Socket == "" && t.TTY == ""
	case model.FocusTmux:
		return model.TmuxPane{Socket: t.Socket, Pane: t.ID, TTY: t.TTY}.Valid()
	}
	return false
}

// Jump failures the page shows as they are (web/src/data/actions.ts).
const (
	jumpNoTarget = "this card has no agterm session or tmux pane to focus"
	jumpNoClient = "no tmux client is attached"
)

// jump focuses the card's tmux pane or agterm session. The server resolves the
// target from the card id; a terminal id sent by the page is never read.
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
		target, ok := opts.Board.JumpTarget(cardID)
		if !ok || opts.Jumper == nil || !validTarget(target) {
			http.Error(w, jumpNoTarget, http.StatusConflict)
			return
		}
		kind := string(target.Kind) // a constant: validTarget accepted it
		if err := opts.Jumper.Jump(r.Context(), target); err != nil {
			switch {
			case errors.Is(err, source.ErrUnknownTerminal):
				http.Error(w, "the "+kind+" session is gone", http.StatusConflict)
			case errors.Is(err, source.ErrNotInstalled):
				http.Error(w, kind+" not found", http.StatusServiceUnavailable)
			case errors.Is(err, source.ErrNoClient):
				http.Error(w, jumpNoClient, http.StatusServiceUnavailable)
			default:
				http.Error(w, kind+" did not answer", http.StatusServiceUnavailable)
			}
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

// catchupRead marks read the items of the list the page names. A list that is
// not current (or none named: a page from before this build) is 409; one the
// state file has no room for is 507. In both, nothing is marked.
func catchupRead(opts Options) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !isJSON(w, r) {
			return
		}
		body, ok := readBody(w, r)
		if !ok {
			return
		}
		var req contract.CatchupReadRequest
		if err := json.Unmarshal(body, &req); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		if err := opts.Board.MarkCatchupRead(req.List); err != nil {
			if errors.Is(err, app.ErrCatchupStale) {
				http.Error(w, "that list is not current: load it again", http.StatusConflict)
				return
			}
			if errors.Is(err, app.ErrCatchupFull) {
				http.Error(w, "too many items marked read in the last 7 days: nothing was marked", http.StatusInsufficientStorage)
				return
			}
			http.Error(w, "could not mark the list read", http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

// forgetGrouping forgets the grouping of a project with parts. The server
// resolves the folder from the project id; nothing else in the body is read.
func forgetGrouping(opts Options) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !isJSON(w, r) {
			return
		}
		body, ok := readBody(w, r)
		if !ok {
			return
		}
		var req contract.ProjectRequest
		if err := json.Unmarshal(body, &req); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		if !contract.ValidProjectID(req.ProjectID) {
			http.Error(w, "bad project id", http.StatusBadRequest)
			return
		}
		switch err := opts.Board.ForgetGrouping(req.ProjectID); {
		case err == nil:
			w.WriteHeader(http.StatusNoContent)
		case errors.Is(err, app.ErrUnknownProject):
			http.Error(w, "unknown project", http.StatusNotFound)
		case errors.Is(err, app.ErrNotGrouping):
			http.Error(w, "this project is not a grouping", http.StatusConflict)
		default:
			log.Printf("server: forget grouping: %v", err)
			http.Error(w, "could not save the change", http.StatusInternalServerError)
		}
	}
}

// groupingSetting sets a folder to auto, always or never group. The server
// resolves the folder from the project id; nothing else in the body is read
// but the mode, which must be one the contract lists.
func groupingSetting(opts Options) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !isJSON(w, r) {
			return
		}
		body, ok := readBody(w, r)
		if !ok {
			return
		}
		var req contract.GroupingRequest
		if err := json.Unmarshal(body, &req); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		if !contract.ValidProjectID(req.ProjectID) {
			http.Error(w, "bad project id", http.StatusBadRequest)
			return
		}
		if !slices.Contains(contract.GroupingModes, req.Mode) {
			http.Error(w, "bad mode", http.StatusBadRequest)
			return
		}
		switch err := opts.Board.SetGrouping(req.ProjectID, req.Mode); {
		case err == nil:
			w.WriteHeader(http.StatusNoContent)
		case errors.Is(err, app.ErrUnknownProject):
			http.Error(w, "unknown project", http.StatusNotFound)
		case errors.Is(err, app.ErrNotGrouping):
			http.Error(w, "this project is not a grouping", http.StatusConflict)
		case errors.Is(err, app.ErrTooManySettings):
			http.Error(w, "too many folders have a grouping setting", http.StatusConflict)
		default:
			log.Printf("server: grouping setting: %v", err)
			http.Error(w, "could not save the change", http.StatusInternalServerError)
		}
	}
}

func isJSON(w http.ResponseWriter, r *http.Request) bool {
	mt, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mt != "application/json" {
		http.Error(w, "send application/json", http.StatusUnsupportedMediaType)
		return false
	}
	return true
}

// readCard reads a JSON CardRequest. Unknown fields are ignored.
func readCard(w http.ResponseWriter, r *http.Request) (string, bool) {
	if !isJSON(w, r) {
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
