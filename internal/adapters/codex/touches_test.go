package codex

import (
	"bytes"
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/IZZY-Agency/sitboard/internal/model"
)

// commandLineIn is a finished command of a Codex rollout that ran in cwd (the
// item's own "cwd"); an empty cwd writes none, as a legacy record has.
func commandLineIn(t *testing.T, script, output, cwd string) []byte {
	t.Helper()
	item := map[string]any{
		"type": "CommandExecution", "command": []string{"/bin/zsh", "-lc", script},
		"status": "completed", "exit_code": 0, "aggregated_output": output,
	}
	if cwd != "" {
		item["cwd"] = cwd
	}
	b, err := json.Marshal(map[string]any{"timestamp": "2026-11-20T11:01:00.000Z", "type": "event_msg", "payload": map[string]any{"type": "item_completed", "item": item}})
	if err != nil {
		t.Fatal(err)
	}
	return append(b, '\n')
}

// commandLineCwd is commandLineIn with the item's "cwd" given as any JSON
// value, to write a malformed one.
func commandLineCwd(t *testing.T, script, output string, cwd any) []byte {
	t.Helper()
	item := map[string]any{
		"type": "CommandExecution", "command": []string{"/bin/zsh", "-lc", script},
		"status": "completed", "exit_code": 0, "aggregated_output": output, "cwd": cwd,
	}
	b, err := json.Marshal(map[string]any{"timestamp": "2026-11-20T11:01:00.000Z", "type": "event_msg", "payload": map[string]any{"type": "item_completed", "item": item}})
	if err != nil {
		t.Fatal(err)
	}
	return append(b, '\n')
}

// A malformed item.cwd (an object, a number, a list, null) is an unknown
// folder for the issue touches and nothing more: the record's pull request and
// spawn facts are read as they always were.
func TestMalformedItemCwdCostsNoOtherFact(t *testing.T) {
	const termID = "11111111-2222-4333-8444-555555555555"
	url := "https://github.com/example-org/example-repo/pull/301\n"
	for _, cwd := range []any{map[string]any{}, 7, []string{"/x"}, nil, true} {
		var b bytes.Buffer
		b.Write(commandLineCwd(t, `gh pr create --fill`, url, cwd))
		b.Write(commandLineCwd(t, `agtermctl session new --name w-spawn`, termID+"\n", cwd))
		b.Write(commandLineCwd(t, `gh issue view 7; gh issue view 8 -R example-org/example-repo`, "", cwd))
		tl := newTailer(tailerFile(t, b.Bytes()))
		if _, err := tl.update(context.Background(), fixedNow); err != nil {
			t.Fatal(err)
		}
		if len(tl.d.CreatedPRs) != 1 || tl.d.CreatedPRs[0].Number != 301 {
			t.Errorf("cwd %v: CreatedPRs = %+v", cwd, tl.d.CreatedPRs)
		}
		if len(tl.d.SpawnedNames) != 1 || tl.d.SpawnedNames[0] != "w-spawn" || len(tl.d.SpawnedIDs) != 1 {
			t.Errorf("cwd %v: spawned = %v %v", cwd, tl.d.SpawnedNames, tl.d.SpawnedIDs)
		}
		if len(tl.d.TouchedIssues) != 1 || tl.d.TouchedIssues[0].Number != 8 {
			t.Errorf("cwd %v: TouchedIssues = %+v, want only the one naming its repository", cwd, tl.d.TouchedIssues)
		}
	}
}

// A Codex session's own issue commands become touches. The folder of one is
// the command's own working directory (item.cwd), whatever folder the turn's
// context recorded; a command naming its repository needs none. Nothing of
// the command text is kept.
func TestIssueCommandsBecomeTouches(t *testing.T) {
	var b bytes.Buffer
	b.WriteString(`{"timestamp":"2026-11-20T11:00:00.000Z","type":"turn_context","payload":{"cwd":"/home/dev/projects/turn","model":"gpt-6.2-sol","effort":"high"}}` + "\n")
	b.Write(commandLineIn(t, `gh issue view 7`, "CANARY output\n", "/home/dev/projects/beta"))
	b.Write(commandLineIn(t, `gh issue comment 8 --repo example-org/example-repo --body "CANARY body"`, "", "/home/dev/projects/beta"))
	b.Write(commandLineIn(t, `cd /elsewhere && gh issue view 9`, "", "/home/dev/projects/beta"))
	b.Write(commandLineIn(t, `glab issue view https://gitlab.example.com/group/proj/-/issues/10`, "", "/home/dev/projects/beta"))
	b.Write(commandLineIn(t, `gh issue list`, "", "/home/dev/projects/beta"))
	tl := newTailer(tailerFile(t, b.Bytes()))
	if _, err := tl.update(context.Background(), fixedNow); err != nil {
		t.Fatal(err)
	}
	var got []model.IssueTouch
	for _, it := range tl.d.TouchedIssues {
		it.At = it.At.UTC().Truncate(0)
		got = append(got, it)
	}
	want := []model.IssueTouch{
		{Forge: model.ForgeGitHub, Folder: "/home/dev/projects/beta", Number: 7},
		{Forge: model.ForgeGitHub, Repo: model.GitHubRepo("example-org/example-repo"), Number: 8},
		{Forge: model.ForgeGitLab, Repo: model.GitLabRepo("gitlab.example.com", "group/proj"), Number: 10},
	}
	if len(got) != len(want) {
		t.Fatalf("TouchedIssues = %+v, want %+v", got, want)
	}
	for i, w := range want {
		g := got[i]
		if g.Forge != w.Forge || g.Repo != w.Repo || g.Folder != w.Folder || g.Number != w.Number || g.At.IsZero() {
			t.Errorf("touch %d = %+v, want %+v with a time", i, g, w)
		}
	}
	if raw, _ := json.Marshal(tl.d.TouchedIssues); bytes.Contains(raw, []byte("CANARY")) {
		t.Fatalf("command text reached the touches: %s", raw)
	}
}

// The folder of a touch is the command's own item.cwd, never the turn's: a
// turn that started in one folder and ran a command in another credits the
// other; a record with no valid cwd of its own (a legacy one) gives no folder
// touch, whatever turn context stands before it, and a fork's or a replaced
// rollout's copy of one cannot lend its folder. A command naming its
// repository still counts.
func TestIssueTouchFolderIsTheCommandsOwn(t *testing.T) {
	var b bytes.Buffer
	b.WriteString(`{"timestamp":"2026-11-20T11:00:00.000Z","type":"turn_context","payload":{"cwd":"/repo-a","model":"gpt-6.2-sol"}}` + "\n")
	b.Write(commandLineIn(t, `gh issue view 7`, "", "/repo-b"))
	b.Write(commandLineIn(t, `gh issue view 8; gh issue view 9 -R example-org/example-repo`, "", ""))
	b.Write(commandLineIn(t, `gh issue view 10`, "", "relative/dir"))
	b.Write(commandLineIn(t, `gh issue view 11`, "", "/repo-c/../repo-d"))
	b.WriteString(`{"timestamp":"2026-11-20T11:03:00.000Z","type":"turn_context","payload":"unreadable"}` + "\n")
	b.Write(commandLineIn(t, `gh issue view 12`, "", ""))
	tl := newTailer(tailerFile(t, b.Bytes()))
	if _, err := tl.update(context.Background(), fixedNow); err != nil {
		t.Fatal(err)
	}
	got := map[int]string{}
	for _, it := range tl.d.TouchedIssues {
		got[it.Number] = it.Folder
	}
	want := map[int]string{7: "/repo-b", 9: "", 11: "/repo-d"}
	if len(got) != len(want) {
		t.Fatalf("TouchedIssues = %+v, want folders %v", tl.d.TouchedIssues, want)
	}
	for n, f := range want {
		if g, ok := got[n]; !ok || g != f {
			t.Errorf("issue %d: folder %q (present %v), want %q", n, g, ok, f)
		}
	}
}

// The touches reach the session's fact, with the folder of the command.
func TestTouchedIssuesReachTheFact(t *testing.T) {
	e := newEnv(t)
	e.add("worker.jsonl", idWorker, fixedNow.Add(-55*time.Minute))
	e.lock(idWorker, 4242)
	e.appendLines(idWorker, string(bytes.TrimSpace(commandLineIn(t, `gh issue view 7`, "", "/home/dev/projects/alpha"))))
	e.rescan()
	f, ok := e.last(idWorker)
	if !ok {
		t.Fatal("no fact")
	}
	if len(f.TouchedIssues) != 1 || f.TouchedIssues[0].Number != 7 || f.TouchedIssues[0].Forge != model.ForgeGitHub || f.TouchedIssues[0].Folder != "/home/dev/projects/alpha" || f.TouchedIssues[0].At.IsZero() {
		t.Fatalf("TouchedIssues = %+v", f.TouchedIssues)
	}
}
