package server

import (
	"net/http"
	"strings"
	"testing"

	"github.com/IZZY-Agency/sitboard/internal/contract"
	"github.com/IZZY-Agency/sitboard/internal/model"
	"github.com/IZZY-Agency/sitboard/internal/source"
)

var paneA = model.TmuxPane{Socket: "/tmp/tmux-501/default", Pane: "%4", TTY: "/dev/ttys004"}

func TestJumpSendsTheTmuxPaneOfTheCard(t *testing.T) {
	h := newHarness()
	h.board.tmux[cardA] = paneA
	wantStatus(t, h.do(req(http.MethodPost, contract.PathJump, cardBody(cardA))), http.StatusNoContent)
	want := model.FocusTarget{Kind: model.FocusTmux, ID: "%4", Socket: "/tmp/tmux-501/default", TTY: "/dev/ttys004"}
	if len(h.jumper.targets) != 1 || h.jumper.targets[0] != want {
		t.Fatalf("jumper got %+v, want %+v", h.jumper.targets, want)
	}
	if len(h.board.seen) != 1 {
		t.Fatal("card not marked seen after the jump")
	}
}

func TestJumpRefusesAMalformedTmuxPane(t *testing.T) {
	for _, bad := range []model.TmuxPane{
		{Socket: paneA.Socket, Pane: "4", TTY: paneA.TTY},
		{Socket: paneA.Socket, Pane: "%4 -t x", TTY: paneA.TTY},
		{Socket: paneA.Socket, Pane: "-%4", TTY: paneA.TTY},
		{Socket: "", Pane: "%4", TTY: paneA.TTY},
		{Socket: "relative", Pane: "%4", TTY: paneA.TTY},
		{Socket: "/tmp/../x", Pane: "%4", TTY: paneA.TTY},
		{Socket: "/tmp/a b", Pane: "%4", TTY: paneA.TTY},
		{Socket: "/tmp/a,1,0", Pane: "%4", TTY: paneA.TTY},
		{Socket: "/" + strings.Repeat("a", 300), Pane: "%4", TTY: paneA.TTY},
		{Socket: paneA.Socket, Pane: "%4", TTY: ""},
		{Socket: paneA.Socket, Pane: "%4", TTY: "ttys004"},
		{Socket: paneA.Socket, Pane: "%4", TTY: "/dev/../etc/x"},
		{Socket: paneA.Socket, Pane: "%4", TTY: "/dev/a b"},
	} {
		h := newHarness()
		h.board.tmux[cardA] = bad
		wantStatus(t, h.do(req(http.MethodPost, contract.PathJump, cardBody(cardA))), http.StatusConflict)
		if len(h.jumper.ids) != 0 {
			t.Fatalf("%+v reached the jumper", bad)
		}
	}
}

func TestAgtermTargetWithASocketIsRefused(t *testing.T) {
	if validTarget(model.FocusTarget{Kind: model.FocusAgterm, ID: termA, Socket: "/tmp/x"}) {
		t.Fatal("an agterm target with a socket passed")
	}
	if validTarget(model.FocusTarget{Kind: model.FocusAgterm, ID: termA, TTY: "/dev/ttys004"}) {
		t.Fatal("an agterm target with a tty passed")
	}
	if validTarget(model.FocusTarget{Kind: "iterm", ID: termA}) {
		t.Fatal("an unknown kind passed")
	}
}

// The page shows these texts as they are (web/src/data/actions.ts).
func TestJumpFailuresNameTheirKind(t *testing.T) {
	cases := []struct {
		err  error
		code int
		text string
	}{
		{source.ErrUnknownTerminal, http.StatusConflict, "the tmux session is gone"},
		{source.ErrNotInstalled, http.StatusServiceUnavailable, "tmux not found"},
		{source.ErrNoClient, http.StatusServiceUnavailable, "no tmux client is attached"},
		{errAgterm, http.StatusServiceUnavailable, "tmux did not answer"},
	}
	for _, c := range cases {
		h := newHarness()
		h.board.tmux[cardA] = paneA
		h.jumper.err = c.err
		w := h.do(req(http.MethodPost, contract.PathJump, cardBody(cardA)))
		wantStatus(t, w, c.code)
		if got := strings.TrimSpace(w.Body.String()); got != c.text {
			t.Errorf("%v: body %q, want %q", c.err, got, c.text)
		}
		if len(h.board.seen) != 0 {
			t.Errorf("%v: marked seen after a failed jump", c.err)
		}
	}
	h := newHarness()
	h.jumper.err = errAgterm
	w := h.do(req(http.MethodPost, contract.PathJump, cardBody(cardA)))
	if got := strings.TrimSpace(w.Body.String()); got != "agterm did not answer" {
		t.Errorf("agterm: body %q", got)
	}
}
