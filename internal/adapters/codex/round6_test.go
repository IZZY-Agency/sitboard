package codex

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"
)

// wholeTailer is a tailer as the start-up read makes one: the file is read
// whole or not at all.
func wholeTailer(path, id string) *tailer {
	tl := newTailer(path)
	tl.id, tl.whole, tl.cap = id, true, 1<<30
	return tl
}

// A rollout whose first session_meta is another thread's is not the thread's:
// no turn or pull request of it is taken, in a whole read and a live one
// (round 6, finding 38).
func TestRolloutOfAnotherThreadGivesNothing(t *testing.T) {
	// Dated after the read, so that only the rule for another thread's file,
	// not the one for a head in doubt (nothing before now), leaves them out.
	soon := fixedNow.Add(time.Hour).Format(time.RFC3339Nano)
	content := []byte(strings.Join([]string{metaAt(idWorker, "2026-11-20T11:00:00.000Z"), doneAt(soon), ghAt(soon, 901)}, "\n") + "\n")
	for name, mk := range map[string]func(string) *tailer{
		"whole": func(p string) *tailer { return wholeTailer(p, idFork) },
		"live": func(p string) *tailer {
			tl := newTailer(p)
			tl.id = idFork
			return tl
		},
	} {
		tl := mk(tailerFile(t, content))
		if _, err := tl.update(context.Background(), fixedNow); err != nil {
			t.Fatal(err)
		}
		if len(tl.d.TurnEnds) != 0 || len(tl.d.CreatedPRs) != 0 {
			t.Errorf("%s read: turns %v, pull requests %v, want none of another thread's", name, tl.d.TurnEnds, tl.d.CreatedPRs)
		}
	}
}

// A whole read of a fork whose head has a record it cannot read (longer than
// maxMetaLine, or not JSON) before the parent's session_meta takes nothing
// from before the fork's own start (round 6, finding 40).
func TestWholeReadForkWithUnreadableHeadTakesOnlyItsOwn(t *testing.T) {
	// The fork's own session_meta is read; its parent's (which tells the copied
	// history from the fork's own) is not: past the line limit, or not JSON.
	parent := metaAt(idWorker, "2026-11-20T11:20:00.001Z")
	for name, unreadable := range map[string]string{
		"too long": strings.Replace(parent, `"cwd"`, `"pad":"`+strings.Repeat("x", maxMetaLine)+`","cwd"`, 1),
		"not JSON": parent[:len(parent)-10],
	} {
		lines := []string{metaAt(idFork, "2026-11-20T11:20:00.000Z"), unreadable,
			doneAt("2026-11-20T11:10:00.000Z"), ghAt("2026-11-20T11:12:00.000Z", 901), doneAt("2026-11-20T11:30:00.000Z")}
		tl := wholeTailer(tailerFile(t, []byte(strings.Join(lines, "\n")+"\n")), idFork)
		if _, err := tl.update(context.Background(), fixedNow); err != nil {
			t.Fatal(err)
		}
		if !slices.Equal(tl.d.TurnEnds, []time.Time{at(11, 30)}) || len(tl.d.CreatedPRs) != 0 {
			t.Errorf("%s: turns %v, pull requests %v, want only the fork's own", name, tl.d.TurnEnds, tl.d.CreatedPRs)
		}
	}
}

// A whole read whose file grew past the cap since it was read last is too
// large, not half read (round 6, finding 36).
func TestWholeReadOfAFileGrownPastTheCapIsTooLarge(t *testing.T) {
	first := metaAt(idWorker, "2026-11-20T11:00:00.000Z") + "\n"
	p := tailerFile(t, []byte(first))
	tl := wholeTailer(p, idWorker)
	tl.cap = int64(len(first)) + 10
	if _, err := tl.update(context.Background(), fixedNow); err != nil {
		t.Fatal(err)
	}
	appendTo(t, p, []byte(doneAt("2026-11-20T11:05:00.000Z")+"\n"+doneAt("2026-11-20T11:06:00.000Z")+"\n"))
	if _, err := tl.update(context.Background(), fixedNow); !errors.Is(err, errTooLarge) {
		t.Errorf("err = %v, want errTooLarge", err)
	}
	if tl.off > tl.cap { // the read stopped at the cap, not at the end of what grew
		t.Errorf("read to %d, past the cap %d", tl.off, tl.cap)
	}
}

// A first session_meta with no usable time cannot tell where a fork's own
// history starts: nothing from before the read is taken (round 6, finding 40).
func TestWholeReadWithoutAStartTimeTakesNothingFromBefore(t *testing.T) {
	lines := []string{`{"type":"session_meta","payload":{"id":"` + idFork + `"}}`,
		doneAt("2026-11-20T11:10:00.000Z"), ghAt("2026-11-20T11:12:00.000Z", 901)}
	tl := wholeTailer(tailerFile(t, []byte(strings.Join(lines, "\n")+"\n")), idFork)
	if _, err := tl.update(context.Background(), fixedNow); err != nil {
		t.Fatal(err)
	}
	if len(tl.d.TurnEnds) != 0 || len(tl.d.CreatedPRs) != 0 {
		t.Errorf("turns %v, pull requests %v, want none", tl.d.TurnEnds, tl.d.CreatedPRs)
	}
}
