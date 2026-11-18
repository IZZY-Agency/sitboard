package core

import (
	"encoding/json"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/IZZY-Agency/sitboard/internal/contract"
	"github.com/IZZY-Agency/sitboard/internal/model"
	"github.com/IZZY-Agency/sitboard/internal/source"
)

func TestOneCardPerTopLevelSession(t *testing.T) {
	b := newTestBoard(t)
	b.applySync(busySession("s1", tNow))
	b.applySync(busySession("s1", tNow.Add(time.Second))) // a newer view of the same session
	b.applySync(busySession("s2", tNow))
	if n := len(b.Snapshot().Cards); n != 2 {
		t.Fatalf("%d cards, want 2", n)
	}
	b.applySync(model.SessionGone{Key: claudeKey("s2")})
	if n := len(b.Snapshot().Cards); n != 1 {
		t.Fatalf("%d cards after SessionGone, want 1", n)
	}
}

func TestSubagentsShowAsACountNotCards(t *testing.T) {
	b := newTestBoard(t)
	s := busySession("s1", tNow)
	s.Subagents = 3
	b.applySync(s)
	if c := onlyCard(t, b); c.Subagents != 3 {
		t.Fatalf("subagents %d, want 3", c.Subagents)
	}
}

func TestWorktreeSessionJoinsMainRepoProject(t *testing.T) {
	e := newEnv(t)
	wt := alpha + "/.worktrees/worker-a"
	e.repos.set(alpha, model.Repo{Root: alpha, MainRoot: alpha, Slug: exRepo, Branch: model.Of("main")})
	e.repos.set(wt, model.Repo{Root: wt, MainRoot: alpha, Slug: exRepo, Branch: model.Of("worker-a")})
	main := busySession("s1", tNow)
	worker := busySession("s2", tNow)
	worker.Folder = wt
	e.board.applySync(main)
	e.board.applySync(worker)
	s := e.board.Snapshot()
	if len(s.Projects) != 1 {
		t.Fatalf("%d projects, want 1: %+v", len(s.Projects), s.Projects)
	}
	p := s.Projects[0]
	if p.ID != contract.ProjectID(alpha) || p.Name != "alpha" || p.Repo != exRepo || p.Path != "~/projects/alpha" {
		t.Fatalf("project %+v", p)
	}
	for _, c := range s.Cards {
		if c.ProjectID != p.ID {
			t.Fatalf("card %s in project %s", c.ID, c.ProjectID)
		}
	}
}

func TestFolderWithoutRepoIsItsOwnProject(t *testing.T) {
	b := newTestBoard(t)
	s := busySession("s1", tNow)
	s.Folder = beta
	b.applySync(s)
	snap := b.Snapshot()
	if len(snap.Projects) != 1 || snap.Projects[0].ID != contract.ProjectID(beta) || snap.Projects[0].Repo != "" || snap.Projects[0].Name != "beta" {
		t.Fatalf("projects %+v", snap.Projects)
	}
}

func TestTitlePrecedence(t *testing.T) {
	e := newEnv(t)
	e.repos.set(alpha, model.Repo{Root: alpha, MainRoot: alpha, Slug: exRepo})
	b := e.board
	id := mustCardID(t, "claude", "s1")
	term := model.TerminalFact{ID: termUUID, Workspace: "alpha", Name: "worker-a", HasAgent: true}
	want := func(step, title string, src contract.TitleSource) {
		t.Helper()
		if c := onlyCard(t, b); c.Title != title || c.TitleSource != src {
			t.Fatalf("%s: got %q %q, want %q %q", step, c.Title, c.TitleSource, title, src)
		}
	}

	s := busySession("s1", tNow)
	s.Name = ""
	b.applySync(s)
	want("folder", "alpha", contract.TitleFromFolder)

	s.TerminalID = termUUID
	b.applySync(s)
	b.applySync(term)
	want("terminal name", "worker-a", contract.TitleFromSession)

	s.Name = "worker-a"
	b.applySync(s)
	want("session name", "worker-a", contract.TitleFromSession)

	// The terminal title says what the agent is doing; its status glyph goes.
	for _, raw := range []string{"\u2733 Fix the login form", "\u2810 Fix the login form", "\u00B7  Fix the login form", "Fix the login form"} {
		term.Title = raw
		b.applySync(term)
		want("terminal title "+raw, "Fix the login form", contract.TitleFromSession)
	}
	// A terminal title that only repeats the name, or is a bare folder path, is skipped.
	for _, raw := range []string{"\u2733 worker-a", "worker-a", "~/projects/alpha", "/home/dev/projects/alpha", "\u2733 ", ""} {
		term.Title = raw
		b.applySync(term)
		want("skipped terminal title "+raw, "worker-a", contract.TitleFromSession)
	}

	term.Title = "\u2733 Fix the login form"
	b.applySync(term)
	e.prs.prs = []model.PullRequest{{Repo: exRepo, Number: 101, Title: "Add the thing", URL: "https://github.com/example-org/example-repo/pull/101", State: model.PRStateOpen, Checks: model.ChecksPassing}}
	s.CreatedPRs = []model.PRRef{{Repo: exRepo, Number: 101}}
	b.applySync(s)
	if c, _ := cardByID(t, b, id); c.TitleSource == contract.TitleFromPR {
		t.Fatal("pull request title used before GitHub answered")
	}
	b.tick()
	want("open pull request", "Add the thing", contract.TitleFromPR)
}

func TestTitleOnlyFromTheNewestOpenPR(t *testing.T) {
	e := newEnv(t)
	b := e.board
	day := 24 * time.Hour
	merged := func(n int, title string, opened time.Duration) model.PullRequest {
		return model.PullRequest{Repo: exRepo, Number: n, Title: title, State: model.PRStateMerged, OpenedAt: tNow.Add(-opened), ClosedAt: tNow.Add(-opened + time.Hour)}
	}
	open := func(n int, title string, opened time.Duration) model.PullRequest {
		return model.PullRequest{Repo: exRepo, Number: n, Title: title, State: model.PRStateOpen, OpenedAt: tNow.Add(-opened)}
	}
	// A long-running coordinator: many merged pull requests, no open one.
	e.prs.prs = []model.PullRequest{merged(1, "old one", 9*day), merged(2, "older merge", 5*day), merged(3, "last merge", 2*time.Hour)}
	s := session("s1", model.StatusFinished, tNow)
	s.Name = "coordinator-a"
	s.CreatedPRs = []model.PRRef{{Repo: exRepo, Number: 1}, {Repo: exRepo, Number: 2}, {Repo: exRepo, Number: 3}}
	b.applySync(s)
	b.tick()
	if c := onlyCard(t, b); c.Title != "coordinator-a" || c.TitleSource != contract.TitleFromSession {
		t.Fatalf("all merged: %q %q, want the session's own title", c.Title, c.TitleSource)
	}

	// Two open ones: the most recently opened wins, whatever its number.
	e.prs.prs = append(e.prs.prs, open(9, "opened earlier, higher number", 3*day), open(4, "opened last", day))
	s.CreatedPRs = append(s.CreatedPRs, model.PRRef{Repo: exRepo, Number: 9}, model.PRRef{Repo: exRepo, Number: 4})
	b.applySync(s)
	b.tick()
	if c := onlyCard(t, b); c.Title != "opened last" || c.TitleSource != contract.TitleFromPR {
		t.Fatalf("open: %q %q", c.Title, c.TitleSource)
	}
}

func TestPullRequestOrderOpenFirstThenNewest(t *testing.T) {
	e := newEnv(t)
	h := time.Hour
	e.prs.prs = []model.PullRequest{
		{Repo: exRepo, Number: 1, State: model.PRStateMerged, OpenedAt: tNow.Add(-50 * h), ClosedAt: tNow.Add(-40 * h)},
		{Repo: exRepo, Number: 2, State: model.PRStateOpen, OpenedAt: tNow.Add(-30 * h)},
		{Repo: exRepo, Number: 3, State: model.PRStateClosed, OpenedAt: tNow.Add(-20 * h), ClosedAt: tNow.Add(-2 * h)},
		{Repo: exRepo, Number: 4, State: model.PRStateOpen, OpenedAt: tNow.Add(-5 * h)},
		{Repo: exRepo, Number: 5, State: model.PRStateMerged, OpenedAt: tNow.Add(-10 * h), ClosedAt: tNow.Add(-9 * h)},
	}
	s := session("s1", model.StatusFinished, tNow)
	for n := 1; n <= 5; n++ {
		s.CreatedPRs = append(s.CreatedPRs, model.PRRef{Repo: exRepo, Number: n})
	}
	e.board.applySync(s)
	e.board.tick()
	var got []int
	for _, pr := range onlyCard(t, e.board).PullRequests {
		got = append(got, pr.Number)
	}
	if want := []int{4, 2, 3, 5, 1}; !slices.Equal(got, want) {
		t.Fatalf("order %v, want %v", got, want)
	}
}

func TestTerminalJoinedByTerminalID(t *testing.T) {
	b := newTestBoard(t)
	s := busySession("s1", tNow)
	s.TerminalID = termUUID
	b.applySync(s)
	if c := onlyCard(t, b); c.Agent.TerminalID != "" {
		t.Fatalf("terminal id %q before the terminal is in the tree", c.Agent.TerminalID)
	}
	b.applySync(model.TerminalFact{ID: termUUID, Workspace: "alpha", Name: "worker-a", HasAgent: true})
	c := onlyCard(t, b)
	if c.Agent.TerminalID != termUUID || c.Agent.Workspace != "alpha" {
		t.Fatalf("agent %+v", c.Agent)
	}
	b.applySync(model.TerminalGone{ID: termUUID})
	if c := onlyCard(t, b); c.Agent.TerminalID != "" || c.Agent.Workspace != "" {
		t.Fatalf("terminal kept after TerminalGone: %+v", c.Agent)
	}
}

func TestCodexSessionLinksPRByBranch(t *testing.T) {
	e := newEnv(t)
	e.repos.set(alpha, model.Repo{Root: alpha, MainRoot: alpha, Slug: exRepo, Branch: model.Of("feature-x")})
	e.prs.prs = []model.PullRequest{
		{Repo: exRepo, Number: 101, Title: "Feature X", HeadBranch: "feature-x", State: model.PRStateOpen, Checks: model.ChecksRunning},
		{Repo: exRepo, Number: 102, Title: "Other", HeadBranch: "other", State: model.PRStateOpen},
	}
	s := session("c1", model.StatusFinished, tNow)
	s.Key = model.SessionKey{Tool: model.ToolCodex, ID: "c1"}
	e.board.applySync(s)
	e.board.tick()
	c := onlyCard(t, e.board)
	if len(c.PullRequests) != 1 || c.PullRequests[0].Number != 101 || c.Column != contract.ColumnInReview {
		t.Fatalf("card %+v", c)
	}

	// A Claude session on the same branch links only pull requests it created.
	s2 := session("s1", model.StatusFinished, tNow)
	e.board.applySync(s2)
	e.board.tick()
	if c, _ := cardByID(t, e.board, "claude:s1"); len(c.PullRequests) != 0 {
		t.Fatalf("claude session linked by branch: %+v", c.PullRequests)
	}
}

func TestIssuesComeFromClosingReferences(t *testing.T) {
	e := newEnv(t)
	e.prs.prs = []model.PullRequest{{Repo: exRepo, Number: 101, State: model.PRStateOpen, ClosingIssues: []int{7, 9}}}
	s := session("s1", model.StatusFinished, tNow)
	s.CreatedPRs = []model.PRRef{{Repo: exRepo, Number: 101}}
	e.board.applySync(s)
	e.board.tick()
	c := onlyCard(t, e.board)
	if len(c.Issues) != 2 || c.Issues[0].Number != 7 || c.Issues[1].URL != "https://github.com/example-org/example-repo/issues/9" {
		t.Fatalf("issues %+v", c.Issues)
	}
}

func TestPlainShellsCountedNotCarded(t *testing.T) {
	b := newTestBoard(t)
	b.applySync(model.TerminalFact{ID: "t1", HasAgent: false})
	b.applySync(model.TerminalFact{ID: "t2", HasAgent: false})
	b.applySync(model.TerminalFact{ID: "t3", HasAgent: true})
	s := b.Snapshot()
	if s.PlainShells != 2 || len(s.Cards) != 0 {
		t.Fatalf("plain shells %d, cards %d", s.PlainShells, len(s.Cards))
	}
}

func TestRootsLimitProjects(t *testing.T) {
	t.Setenv("HOME", testHome)
	b := newBoard(t, Options{Now: func() time.Time { return tNow }, Roots: []string{"~/projects/alpha"}})
	in := busySession("s1", tNow)
	out := busySession("s2", tNow)
	out.Folder = beta
	sibling := busySession("s3", tNow)
	sibling.Folder = alpha + "-other" // a prefix match is not "under"
	b.applySync(in)
	b.applySync(out)
	b.applySync(sibling)
	c := onlyCard(t, b)
	if c.ID != "claude:s1" {
		t.Fatalf("card %s, want claude:s1", c.ID)
	}
}

func TestEndedWithoutPRLeavesTheBoard(t *testing.T) {
	for _, seen := range []bool{false, true} {
		e := newEnv(t)
		s := session("s1", model.StatusFinished, tNow.Add(-3*time.Hour))
		e.board.applySync(s)
		if seen {
			if err := e.board.MarkSeen("claude:s1"); err != nil {
				t.Fatal(err)
			}
		}
		s.Ended, s.EndedAt, s.Live = true, tNow.Add(-time.Hour), model.Of(false)
		e.board.applySync(s)
		if n := len(e.board.Snapshot().Cards); n != 0 {
			t.Fatalf("seen=%v: %d cards for an ended session without pull requests", seen, n)
		}
	}
}

func TestEndedWithMergedPRIsDoneFor24h(t *testing.T) {
	withFullEndedRule(t)
	e := newEnv(t)
	e.prs.prs = []model.PullRequest{{Repo: exRepo, Number: 101, State: model.PRStateMerged, ClosedAt: tNow.Add(-2 * time.Hour)}}
	s := session("s1", model.StatusFinished, tNow.Add(-3*time.Hour))
	s.Ended, s.EndedAt = true, tNow.Add(-time.Hour)
	s.CreatedPRs = []model.PRRef{{Repo: exRepo, Number: 101}}
	e.board.applySync(s)
	e.board.tick()
	if c := onlyCard(t, e.board); c.Column != contract.ColumnDone || c.Agent.Status != contract.AgentStatusEnded {
		t.Fatalf("column %q status %q", c.Column, c.Agent.Status)
	}
	e.clock.Set(tNow.Add(22*time.Hour + time.Minute))
	e.board.tick()
	if n := len(e.board.Snapshot().Cards); n != 0 {
		t.Fatalf("%d cards 24h after the merge", n)
	}
}

func TestEndedWithOpenPRKeptUpTo7Days(t *testing.T) {
	withFullEndedRule(t)
	e := newEnv(t)
	e.prs.prs = []model.PullRequest{{Repo: exRepo, Number: 101, State: model.PRStateOpen}}
	s := session("s1", model.StatusFinished, tNow)
	s.Ended, s.EndedAt = true, tNow
	s.CreatedPRs = []model.PRRef{{Repo: exRepo, Number: 101}}
	e.board.applySync(s)
	e.board.tick()
	if c := onlyCard(t, e.board); c.Column != contract.ColumnInReview {
		t.Fatalf("column %q", c.Column)
	}
	e.clock.Set(tNow.Add(6 * 24 * time.Hour))
	e.board.tick()
	if n := len(e.board.Snapshot().Cards); n != 1 {
		t.Fatalf("%d cards after 6 days", n)
	}
	e.clock.Set(tNow.Add(7*24*time.Hour + time.Minute))
	e.board.tick()
	if n := len(e.board.Snapshot().Cards); n != 0 {
		t.Fatalf("%d cards after 7 days", n)
	}
}

func TestEndedNeverGetsUnseenAttention(t *testing.T) {
	withFullEndedRule(t)
	e := newEnv(t)
	e.prs.prs = []model.PullRequest{{Repo: exRepo, Number: 101, State: model.PRStateOpen}}
	s := session("s1", model.StatusFinished, tNow)
	s.Ended, s.EndedAt = true, tNow
	s.CreatedPRs = []model.PRRef{{Repo: exRepo, Number: 101}}
	e.board.applySync(s)
	e.board.tick()
	c := onlyCard(t, e.board)
	if len(c.Attention) != 0 || c.Column == contract.ColumnNeedsYou {
		t.Fatalf("ended card: %q %v", c.Column, c.Attention)
	}
}

var cardIDRe = regexp.MustCompile(`^claude:s1$`)

func TestCardAndProjectIDsUseContractFunctions(t *testing.T) {
	b := newTestBoard(t)
	b.applySync(busySession("s1", tNow))
	c := onlyCard(t, b)
	if c.ID != mustCardID(t, "claude", "s1") || !cardIDRe.MatchString(c.ID) {
		t.Fatalf("card id %q", c.ID)
	}
	if c.ProjectID != contract.ProjectID(alpha) {
		t.Fatalf("project id %q", c.ProjectID)
	}
}

func TestPathsAreHomeRelative(t *testing.T) {
	b := newTestBoard(t)
	in := busySession("s1", tNow)
	in.Folder = alpha + "/sub"
	out := busySession("s2", tNow)
	out.Folder = "/srv/elsewhere"
	b.applySync(in)
	b.applySync(out)
	raw, err := json.Marshal(b.Snapshot())
	if err != nil {
		t.Fatal(err)
	}
	hasNoAbsolutePath(t, string(raw))
	if strings.Contains(string(raw), "/srv/elsewhere") {
		t.Fatalf("absolute path outside home leaked: %s", raw)
	}
	c, _ := cardByID(t, b, "claude:s1")
	if c.Agent.Folder != "~/projects/alpha/sub" {
		t.Fatalf("folder %q", c.Agent.Folder)
	}
	c2, _ := cardByID(t, b, "claude:s2")
	if c2.Agent.Folder != "elsewhere" {
		t.Fatalf("folder outside home %q, want its base name", c2.Agent.Folder)
	}
	for _, p := range b.Snapshot().Projects {
		if p.ID == c2.ProjectID && p.Path != "elsewhere" {
			t.Fatalf("project path outside home %q, want its base name", p.Path)
		}
	}
}

func TestParentKnownNoneUnknown(t *testing.T) {
	b := newTestBoard(t)
	coord := session("c", model.StatusBusy, tNow)
	known := session("w1", model.StatusBusy, tNow)
	known.Parent = model.Of(claudeKey("c"))
	hinted := session("w2", model.StatusBusy, tNow)
	hinted.PeerSenders = []string{"coordinator-x"}
	orphan := session("w3", model.StatusBusy, tNow)
	orphan.Parent = model.Of(claudeKey("gone"))
	for _, s := range []model.SessionFact{coord, known, hinted, orphan} {
		b.applySync(s)
	}
	want := map[string]struct {
		p      contract.ParentState
		parent string
	}{
		"claude:c":  {contract.ParentNone, ""},
		"claude:w1": {contract.ParentKnown, "claude:c"},
		"claude:w2": {contract.ParentUnknown, ""},
		"claude:w3": {contract.ParentKnown, ""},
	}
	for id, w := range want {
		c, ok := cardByID(t, b, id)
		if !ok {
			t.Fatalf("no card %s", id)
		}
		if c.Agent.Parent != w.p || c.Agent.ParentCardID != w.parent {
			t.Errorf("%s: parent %q card %q, want %q %q", id, c.Agent.Parent, c.Agent.ParentCardID, w.p, w.parent)
		}
	}
}

func TestHostileStringsAreCleaned(t *testing.T) {
	b := newTestBoard(t)
	s := busySession("s1", tNow)
	s.Name = "\u202Eevil\x1b[31m" + strings.Repeat("x", 10000)
	b.applySync(s)
	c := onlyCard(t, b)
	if strings.ContainsAny(c.Title, "\u202E\x1b") {
		t.Fatalf("control characters kept: %q", c.Title[:20])
	}
	if n := len([]rune(c.Title)); n > maxText+1 {
		t.Fatalf("title has %d runes, want at most %d", n, maxText+1)
	}
}

func TestUnsafePRURLIsDropped(t *testing.T) {
	e := newEnv(t)
	e.prs.prs = []model.PullRequest{{Repo: exRepo, Number: 101, State: model.PRStateOpen, URL: "javascript:alert(1)"}}
	s := session("s1", model.StatusFinished, tNow)
	s.CreatedPRs = []model.PRRef{{Repo: exRepo, Number: 101}}
	e.board.applySync(s)
	e.board.tick()
	if c := onlyCard(t, e.board); c.PullRequests[0].URL != "" {
		t.Fatalf("url %q passed through", c.PullRequests[0].URL)
	}
}

// withFullEndedRule switches on the full retention rule for ended sessions
// (the proof of concept drops them at once) for one test.
func withFullEndedRule(t *testing.T) {
	t.Helper()
	keepEnded = true
	t.Cleanup(func() { keepEnded = false })
}

func TestPOCEndedSessionLeavesTheBoardEvenWithAPR(t *testing.T) {
	e := newEnv(t)
	e.prs.prs = []model.PullRequest{{Repo: exRepo, Number: 101, State: model.PRStateOpen}}
	s := session("s1", model.StatusFinished, tNow)
	s.CreatedPRs = []model.PRRef{{Repo: exRepo, Number: 101}}
	e.board.applySync(s)
	e.board.tick()
	if n := len(e.board.Snapshot().Cards); n != 1 {
		t.Fatalf("%d cards while live", n)
	}
	s.Ended, s.EndedAt, s.Live = true, tNow, model.Of(false)
	e.board.applySync(s)
	if n := len(e.board.Snapshot().Cards); n != 0 {
		t.Fatalf("%d cards after the session ended", n)
	}
}

func TestInvalidSessionIDHasNoCard(t *testing.T) {
	b := newTestBoard(t)
	for _, id := range []string{"", "../etc/passwd", "a b", "<script>", strings.Repeat("a", 129)} {
		b.applySync(busySession(id, tNow))
	}
	b.applySync(busySession("ok-1.2_3", tNow))
	if c := onlyCard(t, b); c.ID != "claude:ok-1.2_3" {
		t.Fatalf("card %q", c.ID)
	}
}

func TestDropSourceForgetsItsFactsAndShowsFailing(t *testing.T) {
	b := newTestBoard(t)
	b.SetSources([]source.Status{{Kind: source.KindClaude, State: source.StateOK}, {Kind: source.KindAgterm, State: source.StateOK}})
	s := busySession("s1", tNow)
	s.TerminalID = termUUID
	b.applySync(s)
	b.applySync(model.TerminalFact{ID: termUUID, HasAgent: true})
	b.applySync(model.TerminalFact{ID: "t2"})
	b.DropSource(source.KindAgterm, "stopped")
	snap := b.Snapshot()
	if snap.PlainShells != 0 || snap.AgtermPresent || len(snap.Cards) != 1 || snap.Cards[0].Agent.TerminalID != "" {
		t.Fatalf("agterm facts kept: %+v", snap)
	}
	b.DropSource(source.KindClaude, "stopped")
	snap = b.Snapshot()
	if len(snap.Cards) != 0 {
		t.Fatalf("%d cards after the claude watcher failed", len(snap.Cards))
	}
	for _, st := range snap.Sources {
		if st.State != contract.SourceFailing || st.Detail != "stopped" {
			t.Fatalf("source %+v, want failing", st)
		}
	}
}
