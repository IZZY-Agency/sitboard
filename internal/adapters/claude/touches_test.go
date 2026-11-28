package claude

import (
	"bytes"
	"encoding/json"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/IZZY-Agency/sitboard/internal/model"
)

// toolUseIn is toolUse on a line that says the folder the call ran in.
func toolUseIn(b *bytes.Buffer, id, cmd string, sidechain bool, at, cwd string) {
	use, _ := json.Marshal(map[string]any{
		"type": "assistant", "isSidechain": sidechain, "timestamp": at, "cwd": cwd,
		"message": map[string]any{"model": "claude-opus-5-5", "content": []any{
			map[string]any{"type": "tool_use", "id": id, "name": "Bash", "input": map[string]any{"command": cmd}},
		}},
	})
	b.Write(use)
	b.WriteByte('\n')
}

// The session's own issue commands become touches from the call alone (no
// result is needed), with the folder of the line when no repository is named.
func TestIssueCommandsBecomeTouches(t *testing.T) {
	var b bytes.Buffer
	toolUseIn(&b, "t1", `gh issue view 7`, false, "2026-11-17T08:00:00.000Z", "/home/dev/projects/alpha")
	toolUseIn(&b, "t2", `gh issue comment 8 --repo example-org/example-repo --body "see 99"`, false, "2026-11-17T08:01:00.000Z", "/home/dev/projects/alpha")
	toolUseIn(&b, "t3", `glab issue view https://gitlab.example.com/group/proj/-/issues/9`, false, "2026-11-17T08:02:00.000Z", "/home/dev/projects/alpha")
	toolUseIn(&b, "t4", `cd /elsewhere && gh issue view 10`, false, "2026-11-17T08:03:00.000Z", "/home/dev/projects/alpha")
	toolUseIn(&b, "t5", `gh issue view 7`, false, "2026-11-17T08:04:00.000Z", "/home/dev/projects/alpha")
	toolUseIn(&b, "t6", `gh issue list --limit 5`, false, "2026-11-17T08:05:00.000Z", "/home/dev/projects/alpha")
	tl := tailOf(t, b.Bytes())
	want := []model.IssueTouch{
		{Forge: model.ForgeGitHub, Folder: "/home/dev/projects/alpha", Number: 7, At: ts("2026-11-17T08:04:00Z")},
		{Forge: model.ForgeGitHub, Repo: model.GitHubRepo("example-org/example-repo"), Number: 8, At: ts("2026-11-17T08:01:00Z")},
		{Forge: model.ForgeGitLab, Repo: model.GitLabRepo("gitlab.example.com", "group/proj"), Number: 9, At: ts("2026-11-17T08:02:00Z")},
	}
	if !slices.EqualFunc(tl.d.TouchedIssues, want, func(a, b model.IssueTouch) bool {
		return a.Forge == b.Forge && a.Repo == b.Repo && a.Folder == b.Folder && a.Number == b.Number && a.At.Equal(b.At)
	}) {
		t.Fatalf("TouchedIssues = %+v, want %+v", tl.d.TouchedIssues, want)
	}
}

// A line with no usable folder gives the touches that name their repository
// and none that are left to the folder.
func TestIssueTouchWithoutFolderIsDropped(t *testing.T) {
	var b bytes.Buffer
	toolUseIn(&b, "t1", `gh issue view 7; gh issue view 8 -R example-org/example-repo`, false, "2026-11-17T08:00:00.000Z", "relative/path")
	tl := tailOf(t, b.Bytes())
	if len(tl.d.TouchedIssues) != 1 || tl.d.TouchedIssues[0].Number != 8 {
		t.Fatalf("TouchedIssues = %+v", tl.d.TouchedIssues)
	}
}

// The touches are capped.
func TestIssueTouchesAreCapped(t *testing.T) {
	var b bytes.Buffer
	for n := 1; n <= maxPRs+20; n++ {
		at := time.Date(2026, 11, 17, 8, 0, 0, 0, time.UTC).Add(time.Duration(n) * time.Second).Format("2006-01-02T15:04:05.000Z")
		toolUseIn(&b, "t"+itoa(n), "gh issue view "+itoa(n), false, at, "/home/dev/projects/alpha")
	}
	tl := tailOf(t, b.Bytes())
	if len(tl.d.TouchedIssues) != maxPRs {
		t.Fatalf("%d touches, want %d", len(tl.d.TouchedIssues), maxPRs)
	}
	if !slices.ContainsFunc(tl.d.TouchedIssues, func(x model.IssueTouch) bool { return x.Number == maxPRs+20 }) {
		t.Fatal("the newest touch was dropped")
	}
}

// toolUseOf is toolUseIn on a line that names its session and, when forked,
// carries the marker of a line copied from a parent.
func toolUseOf(b *bytes.Buffer, id, cmd, at, cwd, session string, forkedFrom any) {
	m := map[string]any{
		"type": "assistant", "isSidechain": false, "timestamp": at, "cwd": cwd,
		"message": map[string]any{"model": "claude-opus-5-5", "content": []any{
			map[string]any{"type": "tool_use", "id": id, "name": "Bash", "input": map[string]any{"command": cmd}},
		}},
	}
	if session != "" {
		m["sessionId"] = session
	}
	if forkedFrom != nil {
		m["forkedFrom"] = forkedFrom
	}
	use, _ := json.Marshal(m)
	b.Write(use)
	b.WriteByte('\n')
}

// Only the session's own lines name its issues, as for spend: a line of
// another session, or a copy of a parent's line in a fork, is not.
func TestIssueTouchesAreTheSessionsOwn(t *testing.T) {
	var b bytes.Buffer
	const alpha = "/home/dev/projects/alpha"
	toolUseOf(&b, "t1", `gh issue view 1`, "2026-11-17T08:00:00.000Z", alpha, "", nil)                                                            // no session named: own
	toolUseOf(&b, "t2", `gh issue view 2`, "2026-11-17T08:01:00.000Z", alpha, spendID, nil)                                                       // the session's id: own
	toolUseOf(&b, "t3", `gh issue view 3`, "2026-11-17T08:02:00.000Z", alpha, "other-session", nil)                                               // another session's line
	toolUseOf(&b, "t4", `gh issue view 4`, "2026-11-17T08:03:00.000Z", alpha, spendID, map[string]any{"sessionId": "parent", "messageUuid": "m"}) // a copy of the parent's
	toolUseOf(&b, "t5", `gh issue view 5`, "2026-11-17T08:04:00.000Z", alpha, spendID, "parent")                                                  // a forkedFrom of another shape is a marker too
	toolUseOf(&b, "t6", `gh issue view 6`, "2026-11-17T08:05:00.000Z", alpha, spendID, json.RawMessage("null"))                                   // null is no marker
	tl := tailOf(t, b.Bytes())
	var got []int
	for _, it := range tl.d.TouchedIssues {
		got = append(got, it.Number)
	}
	slices.Sort(got)
	if want := []int{1, 2, 6}; !slices.Equal(got, want) {
		t.Fatalf("touched %v, want %v", got, want)
	}
}

// A session resumed in another folder goes on in a transcript of the same
// name there: the issues it touched before are still its own.
func TestIssueTouchesOfAnOlderFolderStay(t *testing.T) {
	const betaF = "/home/dev/projects/beta"
	e := newEnv(t)
	e.setProc(4242, procStart)
	e.record(4242, nil)
	var old, cur bytes.Buffer
	toolUseIn(&old, "t1", `gh issue view 7`, false, "2026-11-17T07:00:00.000Z", betaF)
	writeFile(t, filepath.Join(e.home, ".claude", "projects", projectDirName(betaF), coordID+".jsonl"), old.Bytes())
	cur.Write(fixture(t, "coordinator.jsonl"))
	toolUseIn(&cur, "t2", `gh issue view 8`, false, "2026-11-17T08:20:00.000Z", alphaF)
	writeFile(t, e.transcript(alphaF, coordID, "coordinator.jsonl"), cur.Bytes())
	c := run(t, e.adapter())
	f := c.wait(t, 2*time.Second, "touches", session(coordID, func(f model.SessionFact) bool {
		return len(f.TouchedIssues) >= 2
	})).(model.SessionFact)
	got := map[int]string{}
	for _, it := range f.TouchedIssues {
		got[it.Number] = it.Folder
	}
	if len(got) != 2 || got[7] != betaF || got[8] != alphaF {
		t.Fatalf("TouchedIssues = %+v, want 7 in beta and 8 in alpha", f.TouchedIssues)
	}
}

// Issue commands are read from the decoded command, so a quoted spelling
// counts; and what lets a line in for them does not let it into the pull
// request reader, which sees exactly the lines it always did.
func TestIssueGateIsSeparateFromThePullRequestGate(t *testing.T) {
	url := "https://github.com/example-org/example-repo/pull/301\n"
	var b bytes.Buffer
	toolCall(&b, "t1", `gh is'sue' view 7 -R example-org/example-repo`, "", false)
	toolCall(&b, "t2", `gh pr cr'e'ate --title 'issue fix'`, url, false)
	tl := tailOf(t, b.Bytes())
	if len(tl.d.TouchedIssues) != 1 || tl.d.TouchedIssues[0].Number != 7 {
		t.Errorf("TouchedIssues = %+v, want the quoted spelling of issue 7", tl.d.TouchedIssues)
	}
	if len(tl.d.CreatedPRs) != 0 {
		t.Errorf("CreatedPRs = %+v: a line the pull request reader never saw was read as a create", tl.d.CreatedPRs)
	}
	// With the word in the clear, the create is read as before.
	b.Reset()
	toolCall(&b, "t3", `gh pr create --title 'issue fix'`, url, false)
	tl = tailOf(t, b.Bytes())
	if len(tl.d.CreatedPRs) != 1 || tl.d.CreatedPRs[0].Number != 301 {
		t.Errorf("CreatedPRs = %+v", tl.d.CreatedPRs)
	}
}

// A line that only the issue gate let in (a Bash `gh issue view` and an
// Artifact publish whose JSON name is spelled "\u0041rtifact", so the
// artifact gate's raw match fails) is read for its touches alone: the publish
// in it registers nothing, as before the issue reader existed.
func TestIssueGateLetsNoArtifactPublishIn(t *testing.T) {
	use := `{"type":"assistant","timestamp":"2026-11-17T08:00:00.000Z","cwd":"/home/dev/projects/alpha","message":{"model":"claude-opus-5-5","content":[` +
		`{"type":"tool_use","id":"b1","name":"Bash","input":{"command":"gh issue view 7"}},` +
		`{"type":"tool_use","id":"a1","name":"\u0041rtifact","input":{"file_path":"/x/a.html"}}]}}`
	res, _ := json.Marshal(map[string]any{
		"type": "user", "timestamp": "2026-11-17T08:00:01.000Z",
		"message":       map[string]any{"content": []any{map[string]any{"type": "tool_result", "tool_use_id": "a1", "content": "ok"}}},
		"toolUseResult": published(artURL(9), "Placeholder"),
	})
	tl := tailOf(t, []byte(use+"\n"+string(res)+"\n"))
	if len(tl.d.TouchedIssues) != 1 || tl.d.TouchedIssues[0].Number != 7 {
		t.Errorf("TouchedIssues = %+v, want issue 7", tl.d.TouchedIssues)
	}
	if got := live(tl); len(got) != 0 {
		t.Errorf("artifacts = %+v: the issue gate let an Artifact publish in", got)
	}
	// The same publish on a line the artifact gate reads (the name in the
	// clear) still registers.
	clear := bytes.Replace([]byte(use), []byte(`\u0041rtifact`), []byte(`Artifact`), 1)
	tl = tailOf(t, append(append(clear, '\n'), append(res, '\n')...))
	if got := live(tl); len(got) != 1 {
		t.Errorf("artifacts = %+v, want the publish", got)
	}
}
