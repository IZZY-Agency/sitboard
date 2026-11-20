package core

import (
	"testing"
	"time"

	"github.com/IZZY-Agency/sitboard/internal/contract"
	"github.com/IZZY-Agency/sitboard/internal/model"
	"github.com/IZZY-Agency/sitboard/internal/source"
)

func TestMatchParent(t *testing.T) {
	coord := claudeKey("coord")
	other := claudeKey("other")
	native := claudeKey("native")
	records := []spawnRecord{
		{key: coord, terminalIDs: []string{termUUID}, names: []string{"worker-a", "worker-b"}},
		{key: other, names: []string{"worker-b"}},
	}
	none := model.Unknown[model.SessionKey]()
	w := func(term string, parent model.Known[model.SessionKey]) model.SessionFact {
		return model.SessionFact{Key: claudeKey("w"), TerminalID: term, Parent: parent}
	}
	cases := []struct {
		name  string
		s     model.SessionFact
		names []string
		want  model.Known[model.SessionKey]
	}{
		{"native parent wins", w(termUUID, model.Of(native)), []string{"worker-a"}, model.Of(native)},
		{"terminal id", w(termUUID, none), []string{"worker-b"}, model.Of(coord)},
		{"unique name", w("", none), []string{"worker-a"}, model.Of(coord)},
		{"name listed by two sessions: none", w("", none), []string{"worker-b"}, none},
		{"no match", w("", none), []string{"solo-a"}, none},
		{"no names", w("", none), nil, none},
	}
	for _, c := range cases {
		if got := matchParent(c.s, c.names, records); got != c.want {
			t.Errorf("%s: got %+v, want %+v", c.name, got, c.want)
		}
	}
	// A session that lists itself is never its own parent.
	self := model.SessionFact{Key: coord, TerminalID: termUUID}
	if got := matchParent(self, []string{"worker-a"}, records[:1]); got.OK {
		t.Errorf("self-parent: %+v", got)
	}
}

func TestSpawnFieldsLinkWorkersOnTheBoard(t *testing.T) {
	b := newTestBoard(t)
	coord := busySession("coord", tNow)
	coord.Name = "coordinator-a"
	coord.SpawnedTerminalIDs = []string{termUUID}
	coord.SpawnedNames = []string{"worker-b", "worker-c"}
	byTerm := busySession("w1", tNow)
	byTerm.Name = ""
	byTerm.TerminalID = termUUID
	byName := busySession("w2", tNow)
	byName.Name = "worker-b"
	// Two sessions carry "worker-c": neither is linked by that name.
	dupA := busySession("w3", tNow)
	dupA.Name = "worker-c"
	dupB := busySession("w4", tNow)
	dupB.Name = "worker-c"
	for _, s := range []model.SessionFact{coord, byTerm, byName, dupA, dupB} {
		b.applySync(s)
	}
	b.applySync(model.TerminalFact{ID: termUUID, Name: "worker-a", HasAgent: true})
	want := map[string]string{"claude:w1": "claude:coord", "claude:w2": "claude:coord", "claude:w3": "", "claude:w4": ""}
	for id, parent := range want {
		c, ok := cardByID(t, b, id)
		if !ok {
			t.Fatalf("no card %s", id)
		}
		if c.Agent.ParentCardID != parent {
			t.Errorf("%s: parent card %q, want %q", id, c.Agent.ParentCardID, parent)
		}
	}
}

// A source is ready when it is not present (nothing to wait for), when it
// answers on demand (git, GitHub), or once its watcher reported SourceReady.
func TestSourceReadiness(t *testing.T) {
	b := newTestBoard(t)
	b.SetSources([]source.Status{
		{Kind: source.KindClaude, State: source.StateOK},
		{Kind: source.KindCodex, State: source.StateMissing},
		{Kind: source.KindAgterm, State: source.StateOK},
		{Kind: source.KindGit, State: source.StateOK},
		{Kind: source.KindGitHub, State: source.StateOff},
	})
	ready := func() map[contract.SourceKind]bool {
		out := map[contract.SourceKind]bool{}
		for _, s := range b.Snapshot().Sources {
			out[s.Kind] = s.Ready
		}
		return out
	}
	got := ready()
	if got[contract.SourceClaude] || got[contract.SourceAgterm] || !got[contract.SourceCodex] || !got[contract.SourceGit] || !got[contract.SourceGitHub] {
		t.Fatalf("before the first pass: %v", got)
	}
	seq := b.Snapshot().Seq
	b.applySync(model.SourceReady{Kind: string(source.KindClaude)})
	if !ready()[contract.SourceClaude] {
		t.Fatal("claude not ready after SourceReady")
	}
	if b.Snapshot().Seq != seq+1 {
		t.Fatal("SourceReady did not publish a new snapshot")
	}
	// Probing again keeps a source that already reported ready.
	b.SetSources([]source.Status{{Kind: source.KindClaude, State: source.StateOK}, {Kind: source.KindAgterm, State: source.StateOK}})
	if got := ready(); !got[contract.SourceClaude] || got[contract.SourceAgterm] {
		t.Fatalf("after a new probe: %v", got)
	}
	// A watcher that failed is ready: there is nothing left to wait for.
	b.DropSource(source.KindAgterm, "stopped")
	if !ready()[contract.SourceAgterm] {
		t.Fatal("failed source not ready")
	}
}

func TestWorkerHintIsDerivedFromPeerSenders(t *testing.T) {
	b := newTestBoard(t)
	coord := busySession("coord", tNow)
	coord.Name = "coordinator-a"
	coord.SpawnedNames = []string{"worker-a"}
	coord.PeerSenders = []string{"worker-a", "worker-t"} // its own child, and a child known only by its terminal name
	child := busySession("w1", tNow)
	child.Name = "worker-a"
	child.PeerSenders = []string{"coordinator-a"} // it has a matched parent: no hint
	termChild := busySession("w2", tNow)
	termChild.Name = ""
	termChild.TerminalID = termUUID
	coord.SpawnedTerminalIDs = []string{termUUID}
	stray := busySession("w3", tNow)
	stray.Name = "worker-z"
	stray.PeerSenders = []string{"coordinator-b"} // someone it was not matched to wrote to it
	adapterOnly := busySession("w4", tNow)
	adapterOnly.Name = "solo-a"
	adapterOnly.WorkerHint = true // the adapter's own flag is no longer read
	for _, f := range []model.Fact{coord, child, termChild, stray, adapterOnly,
		model.TerminalFact{ID: termUUID, Name: "worker-t", HasAgent: true}} {
		b.applySync(f)
	}
	want := map[string]contract.ParentState{
		"claude:coord": contract.ParentNone,
		"claude:w1":    contract.ParentKnown,
		"claude:w2":    contract.ParentKnown,
		"claude:w3":    contract.ParentUnknown,
		"claude:w4":    contract.ParentNone,
	}
	for id, p := range want {
		c, ok := cardByID(t, b, id)
		if !ok {
			t.Fatalf("no card %s", id)
		}
		if c.Agent.Parent != p {
			t.Errorf("%s: parent %q, want %q", id, c.Agent.Parent, p)
		}
	}
}

// Review of #82, B: sessions known only from catch-up's backfill do not
// change the board. A live worker keeps its coordinator when an ended
// session from yesterday has its name, or listed it too.
func TestBackfillTakesNoPartInParents(t *testing.T) {
	ended := func(id string) model.SessionFact {
		s := session(id, model.StatusFinished, tNow.Add(-24*time.Hour))
		s.Live, s.Ended, s.EndedAt, s.Backfill = model.Of(false), true, tNow.Add(-24*time.Hour), true
		return s
	}
	for name, extra := range map[string]func() model.SessionFact{
		"an ended session with the same name": func() model.SessionFact { s := ended("old-w"); s.Name = "worker-a"; return s },
		"an ended coordinator that listed it": func() model.SessionFact {
			s := ended("old-c")
			s.Name, s.SpawnedNames = "coordinator-old", []string{"worker-a"}
			return s
		},
		"an ended session that wrote to it": func() model.SessionFact {
			s := ended("old-x")
			s.Name, s.PeerSenders = "worker-a", []string{"coordinator-a"}
			return s
		},
	} {
		t.Run(name, func(t *testing.T) {
			b := newTestBoard(t)
			coord := busySession("coord", tNow)
			coord.Name, coord.SpawnedNames = "coordinator-a", []string{"worker-a"}
			w := busySession("w1", tNow)
			w.Name = "worker-a"
			for _, f := range []model.Fact{coord, w, extra()} {
				b.applySync(f)
			}
			c, ok := cardByID(t, b, "claude:w1")
			if !ok || c.Agent.Parent != contract.ParentKnown {
				t.Fatalf("worker's parent %q", c.Agent.Parent)
			}
			if c, _ := cardByID(t, b, "claude:coord"); c.Agent.Parent != contract.ParentNone {
				t.Fatalf("coordinator's parent %q", c.Agent.Parent)
			}
		})
	}
}

// A coordinator that ended during this run (not from the backfill) is still
// the spawn record of the worker it started: the worker keeps its parent.
func TestCoordinatorEndedInThisRunKeepsItsWorker(t *testing.T) {
	b := newTestBoard(t)
	coord := busySession("coord", tNow)
	coord.Name, coord.SpawnedNames = "coordinator-a", []string{"worker-a"}
	coord.Live, coord.Ended, coord.EndedAt = model.Of(false), true, tNow
	w := busySession("w1", tNow)
	w.Name = "worker-a"
	b.applySync(coord)
	b.applySync(w)
	if c, ok := cardByID(t, b, "claude:w1"); !ok || c.Agent.Parent != contract.ParentKnown {
		t.Fatalf("worker's parent %q", c.Agent.Parent)
	}
}
