package claude

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/IZZY-Agency/sitboard/internal/model"
)

func fixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", "..", "testdata", "fixtures", "claude", "2.1.326", name))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func writeFile(t *testing.T, path string, b []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, b, 0o644); err != nil {
		t.Fatal(err)
	}
}

func appendFile(t *testing.T, path string, b []byte) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	if _, err := f.Write(b); err != nil {
		t.Fatal(err)
	}
}

var fixedNow = time.Date(2026, 11, 17, 9, 0, 0, 0, time.UTC)

func ts(s string) time.Time {
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		panic(err)
	}
	return t
}

func TestTailReadsOnlyAppendedBytes(t *testing.T) {
	p := filepath.Join(t.TempDir(), "s.jsonl")
	first := fixture(t, "coordinator.jsonl")
	writeFile(t, p, first)
	tl := newTailer(p, staticProber(t))
	if _, err := tl.update(context.Background(), fixedNow); err != nil {
		t.Fatal(err)
	}
	if tl.read != int64(len(first)) || tl.off != int64(len(first)) {
		t.Fatalf("first pass read %d, off %d, want %d", tl.read, tl.off, len(first))
	}
	extra := []byte(`{"type":"system","subtype":"turn_duration","timestamp":"2026-11-17T08:50:00.000Z","cwd":"/home/dev/projects/gamma","isSidechain":false}` + "\n")
	appendFile(t, p, extra)
	tl.read = 0
	changed, err := tl.update(context.Background(), fixedNow)
	if err != nil || !changed {
		t.Fatalf("changed=%v err=%v", changed, err)
	}
	if tl.read != int64(len(extra)) {
		t.Fatalf("second pass read %d bytes, want %d", tl.read, len(extra))
	}
	// One record elsewhere does not move the folder (see folder.go).
	if tl.d.Folder != "/home/dev/projects/alpha" || len(tl.d.TurnEnds) != 2 {
		t.Fatalf("derived after append = %+v", tl.d)
	}
	tl.read = 0
	if changed, _ := tl.update(context.Background(), fixedNow); changed || tl.read != 0 {
		t.Fatalf("unchanged file: changed=%v read=%d", changed, tl.read)
	}
}

func TestIncompleteLastLineIsNotConsumed(t *testing.T) {
	p := filepath.Join(t.TempDir(), "s.jsonl")
	full := `{"type":"system","subtype":"turn_duration","timestamp":"2026-11-17T08:50:00.000Z","cwd":"/home/dev/projects/gamma"}` + "\n"
	writeFile(t, p, []byte(full[:40]))
	tl := newTailer(p, staticProber(t))
	if _, err := tl.update(context.Background(), fixedNow); err != nil {
		t.Fatal(err)
	}
	if tl.off != 0 || len(tl.d.TurnEnds) != 0 {
		t.Fatalf("half line consumed: off=%d d=%+v", tl.off, tl.d)
	}
	appendFile(t, p, []byte(full[40:]))
	if _, err := tl.update(context.Background(), fixedNow); err != nil {
		t.Fatal(err)
	}
	if tl.off != int64(len(full)) || len(tl.d.TurnEnds) != 1 || tl.d.Folder != "/home/dev/projects/gamma" {
		t.Fatalf("after completion: off=%d d=%+v", tl.off, tl.d)
	}
}

func TestShrunkFileRestartsFromZero(t *testing.T) {
	p := filepath.Join(t.TempDir(), "s.jsonl")
	writeFile(t, p, fixture(t, "coordinator.jsonl"))
	tl := newTailer(p, staticProber(t))
	_, _ = tl.update(context.Background(), fixedNow)
	if len(tl.d.CreatedPRs) != 1 {
		t.Fatalf("setup: %+v", tl.d.CreatedPRs)
	}
	w := fixture(t, "worker.jsonl")
	if err := os.Truncate(p, 0); err != nil {
		t.Fatal(err)
	}
	appendFile(t, p, w[:len(w)/2])
	appendFile(t, p, w[len(w)/2:])
	if _, err := tl.update(context.Background(), fixedNow); err != nil {
		t.Fatal(err)
	}
	if len(tl.d.CreatedPRs) != 0 || tl.d.Model != "claude-sonnet-5-5" || tl.off != int64(len(w)) {
		t.Fatalf("after shrink: off=%d d=%+v", tl.off, tl.d)
	}
}

func TestReplacedFileRestartsFromZero(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "s.jsonl")
	writeFile(t, p, fixture(t, "worker.jsonl"))
	tl := newTailer(p, staticProber(t))
	_, _ = tl.update(context.Background(), fixedNow)
	// A bigger, different file renamed into place.
	writeFile(t, filepath.Join(dir, "new"), fixture(t, "coordinator.jsonl"))
	if err := os.Rename(filepath.Join(dir, "new"), p); err != nil {
		t.Fatal(err)
	}
	if _, err := tl.update(context.Background(), fixedNow); err != nil {
		t.Fatal(err)
	}
	if tl.d.Model != "claude-opus-5-5" || len(tl.d.CreatedPRs) != 1 || len(tl.d.PeerSenders) != 1 {
		t.Fatalf("after replace: %+v", tl.d)
	}
}

func TestHugeAndBadLinesAreSkipped(t *testing.T) {
	p := filepath.Join(t.TempDir(), "s.jsonl")
	var b bytes.Buffer
	b.WriteString("not json at all\n")
	b.WriteString(`{"type":"assistant","message":"a string, not an object","timestamp":"2026-11-17T08:00:00.000Z","cwd":"/home/dev/projects/alpha"}` + "\n")
	b.WriteString(`{"type":"user","timestamp":"2026-11-17T08:00:01.000Z","message":{"content":` + `"` + strings.Repeat("x", maxLine+10) + `"}}` + "\n")
	b.WriteString(`{"type":"system","subtype":"turn_duration","timestamp":"2026-11-17T08:00:02.000Z"}` + "\n")
	writeFile(t, p, b.Bytes())
	tl := newTailer(p, staticProber(t))
	if _, err := tl.update(context.Background(), fixedNow); err != nil {
		t.Fatal(err)
	}
	if tl.off != int64(b.Len()) {
		t.Fatalf("off = %d, want %d", tl.off, b.Len())
	}
	if tl.d.Folder != "/home/dev/projects/alpha" || len(tl.d.TurnEnds) != 1 {
		t.Fatalf("derived = %+v", tl.d)
	}
}

func TestDerivesModelFolderLastActivityTurnEnds(t *testing.T) {
	p := filepath.Join(t.TempDir(), "s.jsonl")
	writeFile(t, p, fixture(t, "coordinator.jsonl"))
	tl := newTailer(p, staticProber(t))
	if _, err := tl.update(context.Background(), fixedNow); err != nil {
		t.Fatal(err)
	}
	d := tl.d
	if d.Model != "claude-opus-5-5" {
		t.Errorf("Model = %q (the <synthetic> model must be ignored)", d.Model)
	}
	if d.Folder != "/home/dev/projects/alpha" { // 8 records there, 3 in beta at the end
		t.Errorf("Folder = %q", d.Folder)
	}
	if !d.LastActivity.Equal(ts("2026-11-17T08:30:03Z")) {
		t.Errorf("LastActivity = %v", d.LastActivity)
	}
	if !slices.EqualFunc(d.TurnEnds, []time.Time{ts("2026-11-17T08:30:01Z")}, time.Time.Equal) {
		t.Errorf("TurnEnds = %v", d.TurnEnds)
	}
}

func TestTurnEndsKeepOnly7Days(t *testing.T) {
	p := filepath.Join(t.TempDir(), "s.jsonl")
	var b bytes.Buffer
	for _, s := range []string{"2026-11-01T00:00:00Z", "2026-11-10T08:59:59Z", "2026-11-12T00:00:00Z"} {
		fmt.Fprintf(&b, `{"type":"system","subtype":"turn_duration","timestamp":%q}`+"\n", s)
	}
	writeFile(t, p, b.Bytes())
	tl := newTailer(p, staticProber(t))
	_, _ = tl.update(context.Background(), fixedNow)
	if len(tl.d.TurnEnds) != 1 || !tl.d.TurnEnds[0].Equal(ts("2026-11-12T00:00:00Z")) {
		t.Fatalf("TurnEnds = %v", tl.d.TurnEnds)
	}
}

func TestCreatedPRsFollowSpikeRule(t *testing.T) {
	p := filepath.Join(t.TempDir(), "s.jsonl")
	writeFile(t, p, fixture(t, "coordinator.jsonl"))
	tl := newTailer(p, staticProber(t))
	_, _ = tl.update(context.Background(), fixedNow)
	// 101 came from the session's own `gh pr create`; 102 was merged and 103
	// viewed, and both appear in pr-link records: they must be dropped.
	want := []model.PRRef{{Repo: "example-org/example-repo", Number: 101}}
	if !reflect.DeepEqual(tl.d.CreatedPRs, want) {
		t.Fatalf("CreatedPRs = %+v", tl.d.CreatedPRs)
	}
}

func TestSpawnNamesAndTerminalIDs(t *testing.T) {
	p := filepath.Join(t.TempDir(), "s.jsonl")
	writeFile(t, p, fixture(t, "coordinator.jsonl"))
	tl := newTailer(p, staticProber(t))
	_, _ = tl.update(context.Background(), fixedNow)
	if !slices.Equal(tl.d.SpawnedNames, []string{"worker-a"}) {
		t.Errorf("SpawnedNames = %v", tl.d.SpawnedNames)
	}
	if !slices.Equal(tl.d.SpawnedTerminalIDs, []string{"33333333-3333-4333-8333-333333333333"}) {
		t.Errorf("SpawnedTerminalIDs = %v", tl.d.SpawnedTerminalIDs)
	}
	if !slices.Equal(tl.d.PeerSenders, []string{"worker-a"}) {
		t.Errorf("PeerSenders = %v", tl.d.PeerSenders)
	}
}

func TestSpawnNameParsing(t *testing.T) {
	cases := map[string][]string{
		`agtermctl session new --name worker-a --cwd /x`:                                     {"worker-a"},
		`agtermctl session new --cwd /x --name=worker-b`:                                     {"worker-b"},
		`agtermctl session new --name "worker c" && agtermctl session new --name 'worker-d'`: {"worker c", "worker-d"},
		`/opt/bin/agtermctl session new --workspace w --name worker-e; echo --name nope`:     {"worker-e"},
		`for n in a b; do agtermctl session new --name "$n"; done`:                           nil,
		`agtermctl session rename --name worker-f`:                                           nil,
		`echo agtermctl session new --name`:                                                  nil,
		"agtermctl session new \\\n  --name worker-f \\\n  --cwd /x":                         {"worker-f"},
	}
	for cmd, want := range cases {
		if got := parseCommand(cmd).spawnNames(); !slices.Equal(got, want) {
			t.Errorf("spawnNames(%q) = %q, want %q", cmd, got, want)
		}
	}
}

func TestTerminalIDsFromSpawnResult(t *testing.T) {
	u1, u2, u3 := "33333333-3333-4333-8333-333333333333", "44444444-4444-4444-8444-444444444444", "55555555-5555-4555-8555-555555555555"
	cases := map[string][]string{
		u1 + "\n":                                                     {u1},
		"worker-a: " + u1 + "\nTue Nov 17\n":                          {u1},
		`{"ok":true,"result":{"id":"` + u2 + `"}}`:                    {u2},
		u1 + "\n" + u2 + "\n":                                         {u1, u2},
		"session " + u3 + " opened in workspace":                      nil, // the id does not end the line
		"no id here":                                                  nil,
		"worker-a " + u1 + "\n" + "session worker-b " + u2 + "\n":     {u1, u2}, // a spawn loop
		"created session for the worker " + u3:                        nil,      // more than four words
		`  "session": "` + u3 + `",`:                                  nil,      // a quoted id in a dump
		`{"id":"` + u2 + `","x":"` + strings.Repeat("y", 5000) + `"}`: nil,      // a large JSON dump
	}
	for res, want := range cases {
		if got := terminalIDs(res); !slices.Equal(got, want) {
			t.Errorf("terminalIDs(%q) = %v, want %v", res, got, want)
		}
	}
}

func TestNoTranscriptTextInFacts(t *testing.T) {
	home := t.TempDir()
	writeFile(t, filepath.Join(home, "t.jsonl"), fixture(t, "coordinator.jsonl"))
	writeFile(t, filepath.Join(home, "w.jsonl"), fixture(t, "worker.jsonl"))
	for _, f := range []string{"t.jsonl", "w.jsonl"} {
		tl := newTailer(filepath.Join(home, f), staticProber(t))
		_, _ = tl.update(context.Background(), fixedNow)
		b, _ := json.Marshal(tl.d)
		if bytes.Contains(b, []byte("CANARY")) {
			t.Fatalf("%s: transcript text in derived facts: %s", f, b)
		}
	}
}

// toolCall writes an assistant Bash call and its result as two transcript lines.
func toolCall(b *bytes.Buffer, id, cmd, result string, sidechain bool) {
	use, _ := json.Marshal(map[string]any{
		"type": "assistant", "isSidechain": sidechain, "timestamp": "2026-11-17T08:00:00.000Z",
		"message": map[string]any{"model": "claude-opus-5-5", "content": []any{
			map[string]any{"type": "tool_use", "id": id, "name": "Bash", "input": map[string]any{"command": cmd}},
		}},
	})
	res, _ := json.Marshal(map[string]any{
		"type": "user", "isSidechain": sidechain, "timestamp": "2026-11-17T08:00:01.000Z",
		"message": map[string]any{"content": []any{
			map[string]any{"type": "tool_result", "tool_use_id": id, "content": result},
		}},
	})
	b.Write(use)
	b.WriteByte('\n')
	b.Write(res)
	b.WriteByte('\n')
}

// tailOf reads a transcript in one update; every folder exists and is its
// own group (staticProber).
func tailOf(t *testing.T, b []byte) *tailer {
	t.Helper()
	return tailWith(t, staticProber(t), b)
}

// realTail is tailOf with the real folder lookups, for fixtures in
// temporary folders.
func realTail(t *testing.T, b []byte) *tailer {
	t.Helper()
	return tailWith(t, realProber(t), b)
}

func tailWith(t *testing.T, pr *prober, b []byte) *tailer {
	t.Helper()
	p := filepath.Join(t.TempDir(), "s.jsonl")
	writeFile(t, p, b)
	tl := newTailer(p, pr)
	if _, err := tl.update(context.Background(), fixedNow); err != nil {
		t.Fatal(err)
	}
	return tl
}

func TestPRCreateOnlyAtCommandPosition(t *testing.T) {
	url := func(n string) string { return "https://github.com/example-org/example-repo/pull/" + n }
	var b bytes.Buffer
	toolCall(&b, "t1", `grep -n "gh pr create" notes.txt`, url("201")+"\n", false)
	toolCall(&b, "t2", `echo 'gh pr create' && gh pr view 202`, url("202")+"\n", false)
	toolCall(&b, "t3", `rg 'gh pr create' -l | xargs cat`, url("203"), false)
	toolCall(&b, "t4", "git push -u origin worker-a && gh pr create --fill", "remote: see "+url("299")+" for details\n"+url("204")+"\n", false)
	toolCall(&b, "t5", "GH_PROMPT_DISABLED=1 /opt/bin/gh pr create --fill", "a pull request already exists:\n  "+url("205")+"  \n", false)
	toolCall(&b, "t6", "cd /home/dev/projects/alpha; gh pr create -f", "Creating pull request\n"+url("206")+" and more text", false)
	tl := tailOf(t, b.Bytes())
	var got []int
	for _, pr := range tl.d.CreatedPRs {
		got = append(got, pr.Number)
	}
	if !slices.Equal(got, []int{204, 205}) {
		t.Fatalf("created = %v, want [204 205]", got)
	}
}

func TestSpawnOnlyAtCommandPosition(t *testing.T) {
	id := "33333333-3333-4333-8333-333333333333"
	var b bytes.Buffer
	toolCall(&b, "t1", `grep "agtermctl session new --name worker-x" history.txt`, id+"\n", false)
	toolCall(&b, "t2", `echo agtermctl session new --name worker-y`, id+"\n", false)
	tl := tailOf(t, b.Bytes())
	if len(tl.d.SpawnedNames) != 0 || len(tl.d.SpawnedTerminalIDs) != 0 {
		t.Fatalf("spawns from non-commands: %v %v", tl.d.SpawnedNames, tl.d.SpawnedTerminalIDs)
	}
	b.Reset()
	toolCall(&b, "t3", `X=1 agtermctl session new --name worker-z --no-select`, id+"\n", false)
	tl = tailOf(t, b.Bytes())
	if !slices.Equal(tl.d.SpawnedNames, []string{"worker-z"}) || !slices.Equal(tl.d.SpawnedTerminalIDs, []string{id}) {
		t.Fatalf("spawn = %v %v", tl.d.SpawnedNames, tl.d.SpawnedTerminalIDs)
	}
}

func TestSidechainLinesDoNotSetSessionFacts(t *testing.T) {
	var b bytes.Buffer
	b.WriteString(`{"type":"assistant","isSidechain":false,"timestamp":"2026-11-17T08:00:00.000Z","cwd":"/home/dev/projects/alpha","message":{"model":"claude-opus-5-5"}}` + "\n")
	b.WriteString(`{"type":"assistant","isSidechain":true,"timestamp":"2026-11-17T08:01:00.000Z","cwd":"/home/dev/projects/beta","message":{"model":"claude-haiku-5-5"}}` + "\n")
	b.WriteString(`{"type":"system","subtype":"turn_duration","isSidechain":true,"timestamp":"2026-11-17T08:02:00.000Z"}` + "\n")
	b.WriteString(`{"type":"user","isSidechain":true,"timestamp":"2026-11-17T08:03:00.000Z","origin":{"kind":"peer","name":"worker-b"}}` + "\n")
	toolCall(&b, "t1", "gh pr create --fill", "https://github.com/example-org/example-repo/pull/301\n", true)
	tl := tailOf(t, b.Bytes())
	d := tl.d
	if d.Model != "claude-opus-5-5" || d.Folder != "/home/dev/projects/alpha" || len(d.TurnEnds) != 0 || len(d.PeerSenders) != 0 {
		t.Fatalf("sidechain set session facts: %+v", d)
	}
	if len(d.CreatedPRs) != 1 || d.CreatedPRs[0].Number != 301 {
		t.Fatalf("a subagent's pull request must still count: %+v", d.CreatedPRs)
	}
	if !d.LastActivity.Equal(ts("2026-11-17T08:03:00Z")) { // a subagent at work is activity
		t.Fatalf("LastActivity = %v", d.LastActivity)
	}
}

func TestTurnEndsSortedAndCapped(t *testing.T) {
	var b bytes.Buffer
	for _, s := range []string{"2026-11-17T08:03:00Z", "2026-11-17T08:01:00Z", "2026-11-17T08:02:00Z"} {
		fmt.Fprintf(&b, `{"type":"system","subtype":"turn_duration","timestamp":%q}`+"\n", s)
	}
	tl := tailOf(t, b.Bytes())
	if !slices.IsSortedFunc(tl.d.TurnEnds, time.Time.Compare) || len(tl.d.TurnEnds) != 3 {
		t.Fatalf("TurnEnds = %v", tl.d.TurnEnds)
	}
	b.Reset()
	base := ts("2026-11-16T00:00:00Z")
	for i := range maxTurns + 50 {
		fmt.Fprintf(&b, `{"type":"system","subtype":"turn_duration","timestamp":%q}`+"\n", base.Add(time.Duration(i)*time.Second).Format(time.RFC3339))
	}
	tl = tailOf(t, b.Bytes())
	if len(tl.d.TurnEnds) != maxTurns || !tl.d.TurnEnds[maxTurns-1].Equal(base.Add(time.Duration(maxTurns+49)*time.Second)) {
		t.Fatalf("len %d, last %v: the newest must be kept", len(tl.d.TurnEnds), tl.d.TurnEnds[len(tl.d.TurnEnds)-1])
	}
}

func TestUpdateStopsWhenContextEnds(t *testing.T) {
	var b bytes.Buffer
	for i := range 5000 {
		fmt.Fprintf(&b, `{"type":"system","subtype":"turn_duration","timestamp":"2026-11-17T08:00:%02d.000Z"}`+"\n", i%60)
	}
	p := filepath.Join(t.TempDir(), "s.jsonl")
	writeFile(t, p, b.Bytes())
	tl := newTailer(p, staticProber(t))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := tl.update(ctx, fixedNow); err == nil {
		t.Fatal("update ignored a cancelled context")
	}
	if tl.off >= int64(b.Len()) || (tl.off > 0 && b.Bytes()[tl.off-1] != '\n') {
		t.Fatalf("off = %d of %d, must stop early at a line boundary", tl.off, b.Len())
	}
	if _, err := tl.update(context.Background(), fixedNow); err != nil {
		t.Fatal(err)
	}
	if tl.off != int64(b.Len()) || len(tl.d.TurnEnds) != 5000 {
		t.Fatalf("resume: off %d, turns %d", tl.off, len(tl.d.TurnEnds))
	}
}

func TestUnchangedFileIsNotOpened(t *testing.T) {
	p := filepath.Join(t.TempDir(), "s.jsonl")
	writeFile(t, p, fixture(t, "worker.jsonl"))
	tl := newTailer(p, staticProber(t))
	if _, err := tl.update(context.Background(), fixedNow); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(p, 0); err != nil { // opening it now would fail
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(p, 0o644) })
	if _, err := tl.update(context.Background(), fixedNow); err != nil {
		t.Fatalf("an unchanged file was opened: %v", err)
	}
}
