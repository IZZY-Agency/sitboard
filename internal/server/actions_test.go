package server

import (
	"io/fs"
	"net/http"
	"os"
	"strings"
	"testing"

	"github.com/IZZY-Agency/sitboard/internal/contract"
	"github.com/IZZY-Agency/sitboard/internal/source"
)

type emptyFS struct{}

func (emptyFS) Open(string) (fs.File, error) { return nil, fs.ErrNotExist }

func cardBody(id string) string { return `{"cardId":"` + id + `"}` }

func TestJumpFocusesTerminalOfCard(t *testing.T) {
	h := newHarness()
	wantStatus(t, h.do(req(http.MethodPost, contract.PathJump, cardBody(cardA))), http.StatusNoContent)
	if len(h.jumper.ids) != 1 || h.jumper.ids[0] != termA {
		t.Fatalf("jumper got %v", h.jumper.ids)
	}
	if len(h.board.seen) != 1 || h.board.seen[0] != cardA {
		t.Fatalf("seen %v, want the card marked seen", h.board.seen)
	}
}

func TestJumpUnknownCardIs404(t *testing.T) {
	h := newHarness()
	wantStatus(t, h.do(req(http.MethodPost, contract.PathJump, cardBody("claude:nope"))), http.StatusNotFound)
	if len(h.jumper.ids) != 0 {
		t.Fatal("jumper called")
	}
}

func TestJumpCardWithoutTerminalIs409(t *testing.T) {
	h := newHarness()
	delete(h.board.terminals, cardA)
	wantStatus(t, h.do(req(http.MethodPost, contract.PathJump, cardBody(cardA))), http.StatusConflict)

	h = newHarness()
	h.jumper.err = source.ErrUnknownTerminal
	wantStatus(t, h.do(req(http.MethodPost, contract.PathJump, cardBody(cardA))), http.StatusConflict)

	h = newHarness()
	h.jumper.err = errAgterm
	wantStatus(t, h.do(req(http.MethodPost, contract.PathJump, cardBody(cardA))), http.StatusServiceUnavailable)
	if len(h.board.seen) != 0 {
		t.Fatal("marked seen after a failed jump")
	}
}

func TestJumpNeverAcceptsATerminalIDFromTheClient(t *testing.T) {
	h := newHarness()
	body := `{"cardId":"` + cardA + `","terminalId":"bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb"}`
	wantStatus(t, h.do(req(http.MethodPost, contract.PathJump, body)), http.StatusNoContent)
	if h.jumper.ids[0] != termA {
		t.Fatalf("jumped to %q, want the server-resolved %q", h.jumper.ids[0], termA)
	}
	src, _ := os.ReadFile("actions.go")
	if strings.Contains(strings.ToLower(string(src)), `"terminalid"`) {
		t.Fatal("actions read a terminal id from the request")
	}
}

func TestBodyOver1KBIs413(t *testing.T) {
	h := newHarness()
	body := `{"cardId":"` + cardA + `","pad":"` + strings.Repeat("x", 1100) + `"}`
	wantStatus(t, h.do(req(http.MethodPost, contract.PathSeen, body)), http.StatusRequestEntityTooLarge)
	wantStatus(t, h.do(req(http.MethodPost, contract.PathCatchupRead, strings.Repeat("x", 1100))), http.StatusRequestEntityTooLarge)
}

func TestNonJSONContentTypeIs415(t *testing.T) {
	h := newHarness()
	for _, ct := range []string{"text/plain", "application/x-www-form-urlencoded", "multipart/form-data; boundary=x", ""} {
		r := req(http.MethodPost, contract.PathSeen, cardBody(cardA))
		r.Header.Set("Content-Type", ct)
		if w := h.do(r); w.Code != http.StatusUnsupportedMediaType {
			t.Errorf("content type %q: %d", ct, w.Code)
		}
	}
	r := req(http.MethodPost, contract.PathSeen, cardBody(cardA))
	r.Header.Set("Content-Type", "application/json; charset=utf-8")
	wantStatus(t, h.do(r), http.StatusNoContent)
}

func TestSeenAndCatchupReadCallBoard(t *testing.T) {
	h := newHarness()
	wantStatus(t, h.do(req(http.MethodPost, contract.PathSeen, cardBody(cardA))), http.StatusNoContent)
	wantStatus(t, h.do(req(http.MethodPost, contract.PathSeen, cardBody("claude:nope"))), http.StatusNotFound)
	wantStatus(t, h.do(req(http.MethodPost, contract.PathSeen, `{}`)), http.StatusBadRequest)
	wantStatus(t, h.do(req(http.MethodPost, contract.PathCatchupRead, "")), http.StatusNoContent)
	if len(h.board.seen) != 1 || h.board.catchup != 1 {
		t.Fatalf("seen %v catchup %d", h.board.seen, h.board.catchup)
	}
}

func TestGetOnActionIs405(t *testing.T) {
	h := newHarness()
	for _, p := range []string{contract.PathJump, contract.PathSeen, contract.PathCatchupRead} {
		w := h.do(req(http.MethodGet, p, ""))
		wantStatus(t, w, http.StatusMethodNotAllowed)
		if w.Header().Get("Allow") != http.MethodPost {
			t.Errorf("%s: Allow %q", p, w.Header().Get("Allow"))
		}
	}
	wantStatus(t, h.do(req(http.MethodPost, contract.PathSnapshot, "{}")), http.StatusMethodNotAllowed)
	wantStatus(t, h.do(req(http.MethodPost, "/", "{}")), http.StatusMethodNotAllowed)
	wantStatus(t, h.do(req(http.MethodDelete, contract.PathSeen, "")), http.StatusMethodNotAllowed)
}
