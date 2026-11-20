package core

import (
	"testing"

	"github.com/IZZY-Agency/sitboard/internal/contract"
	"github.com/IZZY-Agency/sitboard/internal/model"
)

// The card carries a known level with where it came from, and nothing else.
func TestCardEffortAndSource(t *testing.T) {
	cases := []struct {
		effort   model.Known[string]
		atLaunch bool
		want     contract.Effort
		src      contract.EffortSource
	}{
		{model.Of("xhigh"), false, contract.EffortXHigh, contract.EffortSourceRecorded},
		{model.Of("low"), true, contract.EffortLow, contract.EffortSourceLaunch},
		{model.Unknown[string](), false, contract.EffortUnknown, contract.EffortSourceNone},
		{model.Unknown[string](), true, contract.EffortUnknown, contract.EffortSourceNone},
		{model.Of("turbo"), false, contract.EffortUnknown, contract.EffortSourceNone},
		{model.Of(""), true, contract.EffortUnknown, contract.EffortSourceNone},
	}
	for _, c := range cases {
		b := newTestBoard(t)
		s := busySession("s1", tNow)
		s.Effort, s.EffortAtLaunch = c.effort, c.atLaunch
		b.applySync(s)
		a := onlyCard(t, b).Agent
		if a.Effort != c.want || a.EffortSource != c.src {
			t.Errorf("%+v launch=%v: card %q %q, want %q %q", c.effort, c.atLaunch, a.Effort, a.EffortSource, c.want, c.src)
		}
	}
}
