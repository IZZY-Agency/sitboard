package source_test

import (
	"context"
	"errors"
	"testing"

	"github.com/IZZY-Agency/sitboard/internal/model"
	"github.com/IZZY-Agency/sitboard/internal/source"
)

type recordJumper struct{ got []model.FocusTarget }

func (r *recordJumper) Jump(_ context.Context, t model.FocusTarget) error {
	r.got = append(r.got, t)
	return nil
}

func TestJumpersSendEachKindToItsProvider(t *testing.T) {
	ag, tm := &recordJumper{}, &recordJumper{}
	j := source.Jumpers{model.FocusAgterm: ag, model.FocusTmux: tm}
	a := model.FocusTarget{Kind: model.FocusAgterm, ID: "x"}
	p := model.FocusTarget{Kind: model.FocusTmux, ID: "%1", Socket: "/tmp/s"}
	if j.Jump(context.Background(), a) != nil || j.Jump(context.Background(), p) != nil {
		t.Fatal("jump failed")
	}
	if len(ag.got) != 1 || ag.got[0] != a || len(tm.got) != 1 || tm.got[0] != p {
		t.Fatalf("agterm got %+v, tmux got %+v", ag.got, tm.got)
	}
	if err := (source.Jumpers{model.FocusAgterm: ag}).Jump(context.Background(), p); !errors.Is(err, source.ErrNotInstalled) {
		t.Fatalf("kind without a provider: %v", err)
	}
	if err := (source.Jumpers{model.FocusTmux: nil}).Jump(context.Background(), p); !errors.Is(err, source.ErrNotInstalled) {
		t.Fatalf("nil provider: %v", err)
	}
}
