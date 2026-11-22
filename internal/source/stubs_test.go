package source_test

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/IZZY-Agency/sitboard/internal/adapters/agterm"
	"github.com/IZZY-Agency/sitboard/internal/adapters/claude"
	"github.com/IZZY-Agency/sitboard/internal/adapters/codex"
	"github.com/IZZY-Agency/sitboard/internal/adapters/github"
	"github.com/IZZY-Agency/sitboard/internal/adapters/gitsrc"
	"github.com/IZZY-Agency/sitboard/internal/model"
	"github.com/IZZY-Agency/sitboard/internal/source"
)

// Compile-time checks: the constructors keep their frozen signatures.
var (
	_ source.Watcher      = claude.New(claude.Options{})
	_ source.Watcher      = codex.New(codex.Options{})
	_ source.Watcher      = (*agterm.Adapter)(nil)
	_ source.Jumper       = (*agterm.Adapter)(nil)
	_ *agterm.Adapter     = agterm.New(agterm.Options{})
	_ source.RepoResolver = gitsrc.New(gitsrc.Options{})
	_ source.PRProvider   = github.New(github.Options{})
)

// Every adapter reports "missing" when its tool is not on the machine. The
// inputs point at an empty home and a binary that does not exist, so this keeps
// holding once the stubs are replaced by real adapters.
func TestStubsProbeMissing(t *testing.T) {
	ctx := context.Background()
	home := t.TempDir()
	noBin := filepath.Join(home, "no-such-binary")

	cases := []struct {
		kind   source.Kind
		prober source.Prober
	}{
		{source.KindClaude, claude.New(claude.Options{Home: home})},
		{source.KindCodex, codex.New(codex.Options{Home: home})},
		{source.KindAgterm, agterm.New(agterm.Options{Bin: noBin})},
		{source.KindGit, gitsrc.New(gitsrc.Options{Bin: noBin})},
		{source.KindGitHub, github.New(github.Options{Bin: noBin})},
	}
	for _, c := range cases {
		st := c.prober.Probe(ctx)
		if st.State != source.StateMissing {
			t.Errorf("%s: Probe state = %q, want %q", c.kind, st.State, source.StateMissing)
		}
		if st.Kind != c.kind {
			t.Errorf("%s: Probe kind = %q", c.kind, st.Kind)
		}
		if w, ok := c.prober.(source.Watcher); ok && w.Kind() != c.kind {
			t.Errorf("%s: Kind() = %q", c.kind, w.Kind())
		}
	}
}

func TestGitHubOffReportsOff(t *testing.T) {
	st := github.New(github.Options{Off: true}).Probe(context.Background())
	if st.State != source.StateOff {
		t.Fatalf("Probe with Off = %q, want %q", st.State, source.StateOff)
	}
}

// A watcher whose source is missing has nothing to scan, so it is ready at
// once: it emits one SourceReady with its own kind and returns nil. Claude's
// and Codex's also say they have nothing to backfill.
func TestMissingWatcherIsReadyAtOnce(t *testing.T) {
	home := t.TempDir()
	noBin := filepath.Join(home, "no-such-binary")
	for _, w := range []source.Watcher{
		claude.New(claude.Options{Home: home}),
		codex.New(codex.Options{Home: home}),
		agterm.New(agterm.Options{Bin: noBin}),
	} {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		var facts []model.Fact
		err := w.Run(ctx, func(f model.Fact) { facts = append(facts, f) })
		cancel()
		if err != nil {
			t.Errorf("%s: Run = %v, want nil", w.Kind(), err)
		}
		want := 1
		if w.Kind() == source.KindClaude || w.Kind() == source.KindCodex {
			want = 2
			if len(facts) == 2 {
				if d, ok := facts[1].(model.BackfillDone); !ok || d.Kind != string(w.Kind()) {
					t.Errorf("%s: then emitted %#v, want BackfillDone", w.Kind(), facts[1])
				}
			}
		}
		if len(facts) != want {
			t.Errorf("%s: emitted %d facts, want %d", w.Kind(), len(facts), want)
			continue
		}
		if r, ok := facts[0].(model.SourceReady); !ok || r.Kind != string(w.Kind()) {
			t.Errorf("%s: emitted %#v, want SourceReady{Kind: %q}", w.Kind(), facts[0], w.Kind())
		}
	}
}
