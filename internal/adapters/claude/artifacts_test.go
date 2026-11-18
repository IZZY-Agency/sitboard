package claude

import (
	"bytes"
	"encoding/json"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/IZZY-Agency/sitboard/internal/model"
)

func artURL(n int) string { return fmt.Sprintf("https://claude.ai/artifact/exampleArtifact%04d", n) }

// writeArtifactCall writes an assistant Artifact call and its result as two
// transcript lines. tur is the result line's toolUseResult; nil leaves it out.
func writeArtifactCall(b *bytes.Buffer, id string, input map[string]any, tur any, isErr, sidechain bool, at string) {
	use, _ := json.Marshal(map[string]any{
		"type": "assistant", "isSidechain": sidechain, "timestamp": at,
		"message": map[string]any{"model": "claude-opus-5-5", "content": []any{
			map[string]any{"type": "tool_use", "id": id, "name": "Artifact", "input": input},
		}},
	})
	res := map[string]any{
		"type": "user", "isSidechain": sidechain, "timestamp": at,
		"message": map[string]any{"content": []any{
			map[string]any{"type": "tool_result", "tool_use_id": id, "content": "CANARY-RESULT-TEXT", "is_error": isErr},
		}},
	}
	if tur != nil {
		res["toolUseResult"] = tur
	}
	line, _ := json.Marshal(res)
	b.Write(use)
	b.WriteByte('\n')
	b.Write(line)
	b.WriteByte('\n')
}

func published(url, title string) map[string]any {
	return map[string]any{"artifact_id": "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa", "owned": true, "seq": 1, "title": title, "updated": false, "url": url}
}

// live returns the artifacts a tailer reports.
func live(tl *tailer) []model.ArtifactRef { return tl.d.Artifacts }

func TestArtifactsFromRealRecordShapes(t *testing.T) {
	tl := tailOf(t, fixture(t, "artifacts.jsonl"))
	// Republished once (one artifact, its latest title); an asset upload, an
	// open, a failed publish and an asset delete add or remove nothing; a
	// publish from a type counts.
	want := []model.ArtifactRef{
		{URL: artURL(1), Title: "Placeholder plan, second draft", PublishedAt: ts("2026-11-17T08:30:00Z")},
		{URL: artURL(4), Title: "Placeholder deck", PublishedAt: ts("2026-11-17T08:40:00Z")},
	}
	if got := live(tl); !slices.Equal(got, want) {
		t.Fatalf("artifacts = %+v\nwant %+v", got, want)
	}
	b, _ := json.Marshal(tl.d)
	if bytes.Contains(b, []byte("CANARY")) {
		t.Fatalf("transcript text in derived facts: %s", b)
	}
}

// Deletes are not followed: no real record of a whole artifact's delete
// exists, so a delete call, whatever its result, changes nothing.
func TestDeleteCallsLeaveArtifactsUnchanged(t *testing.T) {
	var b bytes.Buffer
	writeArtifactCall(&b, "a1", map[string]any{"file_path": "/x/a.html"}, published(artURL(1), "Placeholder one"), false, false, "2026-11-17T08:00:00Z")
	results := []any{
		map[string]any{"owned": true, "url": artURL(1)},
		map[string]any{"deleted": true},
		map[string]any{"deleted": false},
		map[string]any{"cancelled": true},
		map[string]any{"error": "not found"},
		map[string]any{},
		map[string]any{"url": artURL(2)},
		map[string]any{"asset_delete": map[string]any{"deleted": true}, "written": map[string]any{"url": artURL(1)}},
		"Error: CANARY",
		nil,
	}
	for i, r := range results {
		writeArtifactCall(&b, fmt.Sprintf("d%d", i), map[string]any{"action": "delete", "url": artURL(1)}, r, i == 8, false, "2026-11-17T08:01:00Z")
	}
	want := []model.ArtifactRef{{URL: artURL(1), Title: "Placeholder one", PublishedAt: ts("2026-11-17T08:00:00Z")}}
	if got := live(tailOf(t, b.Bytes())); !slices.Equal(got, want) {
		t.Fatalf("artifacts after delete calls = %+v", got)
	}
}

// The result's structured form belongs to the line's one tool result. With
// two results on a line it is not known whose it is: nothing is shown.
func TestTwoResultsOnOneLineShowNothing(t *testing.T) {
	var b bytes.Buffer
	use, _ := json.Marshal(map[string]any{"type": "assistant", "timestamp": "2026-11-17T08:00:00Z", "message": map[string]any{"content": []any{
		map[string]any{"type": "tool_use", "id": "p1", "name": "Artifact", "input": map[string]any{"file_path": "/x/a.html"}},
		map[string]any{"type": "tool_use", "id": "p2", "name": "Artifact", "input": map[string]any{"action": "publish", "file_path": "/x/b.html"}},
	}}})
	res, _ := json.Marshal(map[string]any{"type": "user", "timestamp": "2026-11-17T08:00:01Z", "toolUseResult": published(artURL(1), "Placeholder"), "message": map[string]any{"content": []any{
		map[string]any{"type": "tool_result", "tool_use_id": "p1", "content": "CANARY"},
		map[string]any{"type": "tool_result", "tool_use_id": "p2", "content": "CANARY"},
	}}})
	for _, l := range [][]byte{use, res} {
		b.Write(l)
		b.WriteByte('\n')
	}
	// The next publish, alone on its line, still counts.
	writeArtifactCall(&b, "p3", map[string]any{}, published(artURL(3), "Placeholder three"), false, false, "2026-11-17T08:02:00Z")
	got := live(tailOf(t, b.Bytes()))
	if len(got) != 1 || got[0].URL != artURL(3) {
		t.Fatalf("artifacts = %+v; want only the third", got)
	}
}

func TestArtifactAddressMustBeClaudeAI(t *testing.T) {
	bad := []string{
		"http://claude.ai/artifact/exampleArtifact0001",
		"https://claude.ai.example/artifact/exampleArtifact0001",
		"https://example.com/artifact/exampleArtifact0001",
		"javascript:alert(1)//https://claude.ai/artifact/exampleArtifact0001",
		"https://claude.ai/artifact/exampleArtifact0001?x=1",
		"https://claude.ai/artifact/exampleArtifact0001/../../x",
		"https://claude.ai/artifact/short",
		"https://claude.ai/code/artifact/not-a-uuid-at-all",
		"https://claude.ai/artifact/exampleArtifact0001\n",
		"",
	}
	var b bytes.Buffer
	for i, u := range bad {
		writeArtifactCall(&b, fmt.Sprintf("b%d", i), map[string]any{"file_path": "/x/a.html"}, published(u, "Placeholder"), false, false, "2026-11-17T08:00:00Z")
	}
	writeArtifactCall(&b, "ok", map[string]any{"file_path": "/x/a.html"},
		published("https://claude.ai/code/artifact/aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa", "Placeholder code"), false, false, "2026-11-17T08:00:00Z")
	got := live(tailOf(t, b.Bytes()))
	if len(got) != 1 || got[0].Title != "Placeholder code" {
		t.Fatalf("artifacts = %+v; only the claude.ai/code form may pass", got)
	}
}

func TestArtifactCallsThatAreNotPublishes(t *testing.T) {
	var b bytes.Buffer
	for i, action := range []string{"read", "list", "open", "quickstart", "delete", "pin"} {
		writeArtifactCall(&b, fmt.Sprintf("n%d", i), map[string]any{"action": action, "url": artURL(1)}, published(artURL(1), "Placeholder"), false, false, "2026-11-17T08:00:00Z")
	}
	// An asset upload into an artifact is not a publish of it.
	writeArtifactCall(&b, "asset", map[string]any{"action": "publish", "asset": true, "url": artURL(2)}, published(artURL(2), "Placeholder"), false, false, "2026-11-17T08:00:00Z")
	// Another tool's result with the same shape.
	use, _ := json.Marshal(map[string]any{"type": "assistant", "message": map[string]any{"content": []any{
		map[string]any{"type": "tool_use", "id": "other", "name": "Bash", "input": map[string]any{"command": "gh pr create"}},
	}}})
	res, _ := json.Marshal(map[string]any{"type": "user", "toolUseResult": published(artURL(3), "Placeholder"), "message": map[string]any{"content": []any{
		map[string]any{"type": "tool_result", "tool_use_id": "other", "content": ""},
	}}})
	// A request dump that quotes an Artifact call and its result.
	blob, _ := json.Marshal(map[string]any{"type": "api-request-blob", "message": map[string]any{"content": []any{
		map[string]any{"type": "tool_use", "id": "blob", "name": "Artifact", "input": map[string]any{"file_path": "/x"}},
		map[string]any{"type": "tool_result", "tool_use_id": "blob", "content": artURL(4)},
	}}, "toolUseResult": published(artURL(4), "Placeholder")})
	for _, l := range [][]byte{use, res, blob} {
		b.Write(l)
		b.WriteByte('\n')
	}
	if got := live(tailOf(t, b.Bytes())); len(got) != 0 {
		t.Fatalf("artifacts = %+v, want none", got)
	}
}

func TestArtifactTitle(t *testing.T) {
	var b bytes.Buffer
	long := strings.Repeat("é", maxArtifactTitle) // two bytes each
	writeArtifactCall(&b, "t1", map[string]any{}, published(artURL(1), long), false, false, "2026-11-17T08:00:00Z")
	writeArtifactCall(&b, "t2", map[string]any{}, map[string]any{"title": 42, "url": artURL(2), "updated": false}, false, false, "2026-11-17T08:01:00Z")
	writeArtifactCall(&b, "t3", map[string]any{}, map[string]any{"url": artURL(3), "updated": false}, false, false, "2026-11-17T08:02:00Z")
	got := live(tailOf(t, b.Bytes()))
	if len(got) != 3 {
		t.Fatalf("artifacts = %+v", got)
	}
	if n := len(got[0].Title); n > maxArtifactTitle || !strings.HasPrefix(long, got[0].Title) || n < maxArtifactTitle-1 {
		t.Errorf("long title kept %d bytes, want at most %d on a rune boundary", n, maxArtifactTitle)
	}
	if got[1].Title != "" || got[2].Title != "" {
		t.Errorf("a missing or non-string title is unknown: %+v", got[1:])
	}
}

func TestArtifactsAreCapped(t *testing.T) {
	var b bytes.Buffer
	for i := range maxArtifacts + 20 {
		writeArtifactCall(&b, fmt.Sprintf("c%d", i), map[string]any{}, published(artURL(i+1), "Placeholder"), false, false, "2026-11-17T08:00:00Z")
	}
	if n := len(tailOf(t, b.Bytes()).d.Artifacts); n != maxArtifacts {
		t.Fatalf("%d artifacts kept, want %d", n, maxArtifacts)
	}
}

// Only a publish takes one of the capped slots: reads, opens, deletes, asset
// uploads, failed publishes and foreign addresses never do.
func TestOnlyAPublishTakesASlot(t *testing.T) {
	var b bytes.Buffer
	for i := range maxArtifacts - 1 {
		writeArtifactCall(&b, fmt.Sprintf("p%d", i), map[string]any{}, published(artURL(i+1), "Placeholder"), false, false, "2026-11-17T08:00:00Z")
	}
	for i := range 50 {
		u := artURL(1000 + i)
		id := fmt.Sprintf("n%d", i)
		switch i % 6 {
		case 0:
			writeArtifactCall(&b, id, map[string]any{"action": "read", "url": u}, published(u, "Placeholder"), false, false, "2026-11-17T08:01:00Z")
		case 1:
			writeArtifactCall(&b, id, map[string]any{"action": "open", "url": u}, published(u, "Placeholder"), false, false, "2026-11-17T08:01:00Z")
		case 2:
			writeArtifactCall(&b, id, map[string]any{"action": "delete", "url": u}, published(u, "Placeholder"), false, false, "2026-11-17T08:01:00Z")
		case 3:
			writeArtifactCall(&b, id, map[string]any{"asset": true, "url": u}, published(u, "Placeholder"), false, false, "2026-11-17T08:01:00Z")
		case 4:
			writeArtifactCall(&b, id, map[string]any{}, published(u, "Placeholder"), true, false, "2026-11-17T08:01:00Z")
		case 5:
			writeArtifactCall(&b, id, map[string]any{}, published("https://example.com/artifact/exampleArtifact0001", "Placeholder"), false, false, "2026-11-17T08:01:00Z")
		}
	}
	writeArtifactCall(&b, "last", map[string]any{}, published(artURL(5000), "Placeholder last"), false, false, "2026-11-17T08:02:00Z")
	writeArtifactCall(&b, "over", map[string]any{}, published(artURL(5001), "Placeholder over"), false, false, "2026-11-17T08:03:00Z")
	got := live(tailOf(t, b.Bytes()))
	if len(got) != maxArtifacts || got[maxArtifacts-1].URL != artURL(5000) {
		t.Fatalf("%d artifacts, last %+v; want %d ending with the last real publish", len(got), got[len(got)-1], maxArtifacts)
	}
}

func TestSubagentArtifactsMergeIntoTheSession(t *testing.T) {
	e := newEnv(t)
	e.setProc(4242, procStart)
	e.record(4242, nil)
	var main, sub bytes.Buffer
	main.Write(fixture(t, "coordinator.jsonl"))
	writeArtifactCall(&main, "m1", map[string]any{}, published(artURL(1), "Placeholder main"), false, false, "2026-11-17T08:20:00Z")
	// The main session republishes, later, what a subagent published first.
	writeArtifactCall(&main, "m2", map[string]any{"action": "publish"}, published(artURL(3), "Placeholder three, by the session"), false, false, "2026-11-17T08:30:00Z")
	// A subagent republishes the session's artifact later, under a new title.
	writeArtifactCall(&sub, "s1", map[string]any{}, published(artURL(1), "Placeholder main, by the subagent"), false, true, "2026-11-17T08:25:00Z")
	writeArtifactCall(&sub, "s2", map[string]any{}, published(artURL(2), "Placeholder sub"), false, true, "2026-11-17T08:26:00Z")
	writeArtifactCall(&sub, "s3", map[string]any{}, published(artURL(3), "Placeholder three, by the subagent"), false, true, "2026-11-17T08:21:00Z")
	tr := e.transcript("/home/dev/projects/alpha", coordID, "coordinator.jsonl")
	writeFile(t, tr, main.Bytes())
	writeFile(t, filepath.Join(filepath.Dir(tr), coordID, "subagents", "agent-a1.jsonl"), sub.Bytes())
	c := run(t, e.adapter())
	f := c.wait(t, 2*time.Second, "artifacts", session(coordID, func(f model.SessionFact) bool { return f.Subagents == 1 && len(f.Artifacts) > 0 })).(model.SessionFact)
	want := []model.ArtifactRef{
		{URL: artURL(1), Title: "Placeholder main, by the subagent", PublishedAt: ts("2026-11-17T08:25:00Z")},
		{URL: artURL(3), Title: "Placeholder three, by the session", PublishedAt: ts("2026-11-17T08:30:00Z")},
		{URL: artURL(2), Title: "Placeholder sub", PublishedAt: ts("2026-11-17T08:26:00Z")},
	}
	if !slices.Equal(f.Artifacts, want) {
		t.Fatalf("Artifacts = %+v\nwant %+v", f.Artifacts, want)
	}
}

// A key that is present but null says nothing: the publish still counts.
func TestArtifactResultNullMarkers(t *testing.T) {
	var b bytes.Buffer
	r := published(artURL(1), "Placeholder")
	r["opened"], r["asset_upload"], r["asset_delete"] = nil, nil, nil
	writeArtifactCall(&b, "p1", map[string]any{}, r, false, false, "2026-11-17T08:00:00Z")
	got := live(tailOf(t, b.Bytes()))
	if len(got) != 1 || got[0].URL != artURL(1) {
		t.Fatalf("artifacts = %+v; want the publish", got)
	}
}
