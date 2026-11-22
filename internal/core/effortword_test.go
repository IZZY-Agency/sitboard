package core

import (
	"testing"

	"github.com/IZZY-Agency/sitboard/internal/contract"
	"github.com/IZZY-Agency/sitboard/internal/model"
)

// An effort word that is not one of the levels (Codex's "ultra") reaches the
// page as written, in EffortWord, with no level; a level stays a level; any
// other text is dropped.
func TestEffortWordOffTheRamp(t *testing.T) {
	cases := []struct {
		in     string
		effort contract.Effort
		word   string
	}{
		{"ultra", contract.EffortUnknown, "ultra"},
		{"minimal", contract.EffortUnknown, "minimal"},
		{"high", contract.EffortHigh, ""},
		{"Ultra", contract.EffortUnknown, ""},
		{"ultra-max", contract.EffortUnknown, ""},
		{"abcdefghijklmnopq", contract.EffortUnknown, ""},
	}
	for _, c := range cases {
		e := newEnv(t)
		s := session("c1", model.StatusFinished, tNow)
		s.Key = model.SessionKey{Tool: model.ToolCodex, ID: "c1"}
		s.Effort = model.Of(c.in)
		e.board.applySync(s)
		e.board.tick()
		a := onlyCard(t, e.board).Agent
		if a.Effort != c.effort || a.EffortWord != c.word {
			t.Errorf("%q: effort %q word %q, want %q %q", c.in, a.Effort, a.EffortWord, c.effort, c.word)
		}
		if a.EffortWord != "" && a.EffortSource != contract.EffortSourceNone {
			t.Errorf("%q: a word off the ramp has source %q", c.in, a.EffortSource)
		}
	}
}
