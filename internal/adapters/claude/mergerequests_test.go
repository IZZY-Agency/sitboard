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

func glRef(host, path string, n int) model.PRRef {
	return model.PRRef{Repo: model.GitLabRepo(host, path), Number: n}
}

const (
	glHost = "gitlab.example.com"
	glPath = "example-org/example-repo"
)

func glURL(n string) string { return "https://" + glHost + "/" + glPath + "/-/merge_requests/" + n }

// pushOut is what a push prints when GitLab names the merge request for the
// branch (the same whether the push created it or it was open already).
func pushOut(n string) string {
	return "remote: \nremote: View merge request for feature-b:\nremote:   " + glURL(n) + "\nremote: \n"
}

// `glab mr create` credits its bare address with no window; a creating push
// credits its "remote:" line and records the call's window (toolCall writes
// the call at 08:00:00 and its result at 08:00:01).
func TestMergeRequestsCreditedFromGlabAndPush(t *testing.T) {
	var b bytes.Buffer
	toolCall(&b, "g1", "glab mr create --fill --yes", "Creating merge request for feature-a into main in "+glPath+"\n\n"+glURL("11")+"\n", false)
	toolCall(&b, "p1", "git push -o merge_request.create origin feature-b", pushOut("12"), false)
	d := tailOf(t, b.Bytes()).d
	if want := []model.PRRef{glRef(glHost, glPath, 11), glRef(glHost, glPath, 12)}; !slices.Equal(d.CreatedPRs, want) {
		t.Fatalf("CreatedPRs = %+v, want %+v", d.CreatedPRs, want)
	}
	want := []model.PushedRef{{Ref: glRef(glHost, glPath, 12), From: ts("2026-11-17T08:00:00Z"), To: ts("2026-11-17T08:00:01Z")}}
	if !slices.Equal(d.PushedPRs, want) {
		t.Fatalf("PushedPRs = %+v, want %+v", d.PushedPRs, want)
	}
}

// What the matcher refuses stays refused through the tailer: glab's 409
// names the merge request by number, a plain push names an existing one, a
// github.com address is never a GitLab ref, and a reading command prints
// addresses without creating any.
func TestMergeRequestsNotCredited(t *testing.T) {
	var b bytes.Buffer
	toolCall(&b, "r1", "glab mr create --fill --yes", "failed to create merge request. Another open merge request already exists for this source branch: !12 (HTTP 409)\n", false)
	toolCall(&b, "r2", "git push -u origin feature-a && glab mr create --fill --yes", "remote: View merge request for feature-a:\nremote:   "+glURL("12")+"\nfailed: Another open merge request already exists: !12\n", false)
	toolCall(&b, "r3", "git push -o merge_request.create origin feature-a", "remote:   https://github.com/"+glPath+"/-/merge_requests/13\n", false)
	toolCall(&b, "r4", "glab mr view 14", glURL("14")+"\n", false)
	toolCall(&b, "r5", "git push origin feature-a", "remote: View merge request for feature-a:\nremote:   "+glURL("15")+"\n", false)
	toolCall(&b, "r6", "git push -o merge_request.create origin feature-a", "see "+glURL("16")+"\n"+glURL("17")+"\n", false) // a bare line is not a push's
	d := tailOf(t, b.Bytes()).d
	if len(d.CreatedPRs) != 0 || len(d.PushedPRs) != 0 {
		t.Fatalf("credited: %+v %+v", d.CreatedPRs, d.PushedPRs)
	}
}

// A GitHub pull request in the same transcript is still found, and neither
// forge's address becomes the other's.
func TestMergeRequestsBesideGitHub(t *testing.T) {
	var b bytes.Buffer
	toolCall(&b, "a1", "gh pr create --fill", "https://github.com/example-org/example-repo/pull/21\n"+glURL("22")+"\n", false)
	toolCall(&b, "a2", "glab mr create --fill", "https://github.com/example-org/example-repo/pull/23\n"+glURL("24")+"\n", false)
	d := tailOf(t, b.Bytes()).d
	want := []model.PRRef{
		{Repo: model.GitHubRepo("example-org/example-repo"), Number: 21},
		glRef(glHost, glPath, 24),
	}
	if !slices.Equal(d.CreatedPRs, want) {
		t.Fatalf("CreatedPRs = %+v, want %+v", d.CreatedPRs, want)
	}
}

// The window starts at the call and ends at its result, however far apart
// the lines are.
func TestMergeRequestsPushWindowSpansTheCall(t *testing.T) {
	var b bytes.Buffer
	toolCall(&b, "p1", "git push -o merge_request.create origin feature-b", pushOut("12"), false)
	d := tailOf(t, bytes.ReplaceAll(b.Bytes(), []byte("08:00:01.000Z"), []byte("08:03:30.000Z"))).d
	if len(d.PushedPRs) != 1 || !d.PushedPRs[0].From.Equal(ts("2026-11-17T08:00:00Z")) || !d.PushedPRs[0].To.Equal(ts("2026-11-17T08:03:30Z")) {
		t.Fatalf("PushedPRs = %+v", d.PushedPRs)
	}
}

// The cap on CreatedPRs also bounds PushedPRs, and a reset forgets both.
func TestMergeRequestsCapAndReset(t *testing.T) {
	var b bytes.Buffer
	for i := 1; i <= maxPRs+3; i++ {
		toolCall(&b, "p"+itoa(i), "git push -o merge_request.create origin f"+itoa(i), pushOut(itoa(i)), false)
	}
	tl := tailOf(t, b.Bytes())
	if len(tl.d.CreatedPRs) != maxPRs || len(tl.d.PushedPRs) != maxPRs {
		t.Fatalf("%d created, %d pushed, want %d each", len(tl.d.CreatedPRs), len(tl.d.PushedPRs), maxPRs)
	}
	tl.reset()
	if len(tl.d.CreatedPRs) != 0 || len(tl.d.PushedPRs) != 0 {
		t.Fatalf("reset kept %+v", tl.d)
	}
}

// A subagent's merge requests join the session's, with their windows; one
// the session already has keeps its own window.
func TestSubagentMergeRequestsMergeIntoTheSession(t *testing.T) {
	e := newEnv(t)
	e.setProc(4242, procStart)
	e.record(4242, nil)
	var main, sub bytes.Buffer
	main.Write(fixture(t, "coordinator.jsonl"))
	toolCall(&main, "m1", "git push -o merge_request.create origin feature-a", pushOut("31"), false)
	toolCall(&sub, "s1", "git push -o merge_request.create origin feature-a", pushOut("31"), true)
	toolCall(&sub, "s2", "git push -o merge_request.create origin feature-b", pushOut("32"), true)
	toolCall(&sub, "s3", "glab mr create -f", glURL("33")+"\n", true)
	tr := e.transcript("/home/dev/projects/alpha", coordID, "coordinator.jsonl")
	writeFile(t, tr, main.Bytes())
	writeFile(t, filepath.Join(filepath.Dir(tr), coordID, "subagents", "agent-a1.jsonl"), sub.Bytes())
	c := run(t, e.adapter())
	f := c.wait(t, 2*time.Second, "merge requests", session(coordID, func(f model.SessionFact) bool { return f.Subagents == 1 && len(f.PushedPRs) > 1 })).(model.SessionFact)
	var got []int
	for _, r := range f.CreatedPRs {
		if r.Repo.Forge == model.ForgeGitLab {
			got = append(got, r.Number)
		}
	}
	if !slices.Equal(got, []int{31, 32, 33}) {
		t.Fatalf("GitLab CreatedPRs = %v", got)
	}
	if len(f.PushedPRs) != 2 || f.PushedPRs[0].Ref != glRef(glHost, glPath, 31) || f.PushedPRs[1].Ref != glRef(glHost, glPath, 32) {
		t.Fatalf("PushedPRs = %+v", f.PushedPRs)
	}
}

// The hand-written GitLab transcript (testdata/fixtures/claude/2.1.326/gitlab-mr.jsonl):
// a created merge request (41), a creating push (43, 08:11:00 to 08:11:05),
// a refused create for 43's branch (409, an error result), a refused create
// followed by a view (44), then a creating push that names 41 again. 41 stays
// a created one, with no window, although a push named it later; 43 keeps the
// window of its push, the refusal after it changes nothing.
func TestMergeRequestsFixture(t *testing.T) {
	d := tailOf(t, fixture(t, "gitlab-mr.jsonl")).d
	if want := []model.PRRef{glRef(glHost, glPath, 41), glRef(glHost, glPath, 43)}; !slices.Equal(d.CreatedPRs, want) {
		t.Fatalf("CreatedPRs = %+v, want %+v", d.CreatedPRs, want)
	}
	want := []model.PushedRef{{Ref: glRef(glHost, glPath, 43), From: ts("2026-11-17T08:11:00Z"), To: ts("2026-11-17T08:11:05Z")}}
	if !slices.Equal(d.PushedPRs, want) {
		t.Fatalf("PushedPRs = %+v, want %+v", d.PushedPRs, want)
	}
}

// Mixed calls: glab's announcement ties the address to the create; without
// a terminal (no announcement) the first address is the create's when the
// create ran first and nothing was refused. An unannounced "remote:" address
// is not a push's.
func TestMergeRequestsMixedCalls(t *testing.T) {
	var b bytes.Buffer
	toolCall(&b, "x1", "glab mr create --fill --yes && glab mr view 52", "Creating merge request for a into main in "+glPath+"\n\n"+glURL("51")+"\n"+glURL("52")+"\n", false)
	toolCall(&b, "x2", "glab mr create --fill --yes; glab mr view 54", glURL("53")+"\n"+glURL("54")+"\n", false)
	toolCall(&b, "x3", "glab mr view 55; glab mr create --fill --yes", glURL("55")+"\n"+glURL("56")+"\n", false)
	toolCall(&b, "x4", "glab mr create --fill --yes || glab mr view 57", "failed to create merge request: 409 {message: [Another open merge request already exists for this source branch: !57]}\n"+glURL("57")+"\n", false)
	toolCall(&b, "x5", "git push -o merge_request.create origin a", "remote:   "+glURL("58")+"\n", false)
	d := tailOf(t, b.Bytes()).d
	if want := []model.PRRef{glRef(glHost, glPath, 51), glRef(glHost, glPath, 53)}; !slices.Equal(d.CreatedPRs, want) {
		t.Fatalf("CreatedPRs = %+v, want %+v", d.CreatedPRs, want)
	}
}

// Each forge has its own budget: a flood of merge requests leaves room for
// a GitHub pull request created after it.
func TestMergeRequestsFloodLeavesRoomForGitHub(t *testing.T) {
	var b bytes.Buffer
	for i := 1; i <= maxPRs+3; i++ {
		toolCall(&b, "g"+itoa(i), "glab mr create --fill --yes", glURL(itoa(i))+"\n", false)
	}
	toolCall(&b, "h1", "gh pr create --fill", "https://github.com/example-org/example-repo/pull/21\n", false)
	d := tailOf(t, b.Bytes()).d
	gh := model.PRRef{Repo: model.GitHubRepo("example-org/example-repo"), Number: 21}
	if len(d.CreatedPRs) != maxPRs+1 || d.CreatedPRs[maxPRs] != gh {
		t.Fatalf("%d created, last %+v", len(d.CreatedPRs), d.CreatedPRs[len(d.CreatedPRs)-1])
	}
}

// When the pending calls overflow, they are dropped together with their
// start times: the push made after the overflow keeps its own start.
func TestMergeRequestsPendingEviction(t *testing.T) {
	var b bytes.Buffer
	for i := range maxPending {
		toolUse(&b, "q"+itoa(i), "git push -o merge_request.create origin f"+itoa(i), false, "2026-11-17T07:00:00.000Z")
	}
	toolCallAt(&b, "p1", "git push -o merge_request.create origin feature-b", pushOut("12"), false, "2026-11-17T08:00:00.000Z", "2026-11-17T08:00:05.000Z")
	d := tailOf(t, b.Bytes()).d
	want := []model.PushedRef{{Ref: glRef(glHost, glPath, 12), From: ts("2026-11-17T08:00:00Z"), To: ts("2026-11-17T08:00:05Z")}}
	if !slices.Equal(d.PushedPRs, want) {
		t.Fatalf("PushedPRs = %+v, want %+v", d.PushedPRs, want)
	}
}

// A push whose result is dated before its call, or whose call has no time,
// has no usable window: it is dropped whole and does not block a later push
// that names the same merge request.
func TestMergeRequestsBadWindowsDropped(t *testing.T) {
	var b bytes.Buffer
	toolCallAt(&b, "p1", "git push -o merge_request.create origin feature-b", pushOut("12"), false, "2026-11-17T08:00:00.000Z", "2026-11-17T07:59:00.000Z")
	toolCallAt(&b, "p2", "git push -o merge_request.create origin feature-b", pushOut("12"), false, "", "2026-11-17T08:00:30.000Z")
	if d := tailOf(t, b.Bytes()).d; len(d.CreatedPRs) != 0 || len(d.PushedPRs) != 0 {
		t.Fatalf("credited with a bad window: %+v %+v", d.CreatedPRs, d.PushedPRs)
	}
	toolCallAt(&b, "p3", "git push -o merge_request.create origin feature-b", pushOut("12"), false, "2026-11-17T08:01:00.000Z", "2026-11-17T08:01:02.000Z")
	d := tailOf(t, b.Bytes()).d
	want := []model.PushedRef{{Ref: glRef(glHost, glPath, 12), From: ts("2026-11-17T08:01:00Z"), To: ts("2026-11-17T08:01:02Z")}}
	if !slices.Equal(d.PushedPRs, want) {
		t.Fatalf("PushedPRs = %+v, want %+v", d.PushedPRs, want)
	}
}

// mergeCreated keeps, for a ref both transcripts have, the earliest window
// (both ends of it); a ref that either side credited without a window ends
// with none; each forge has its own budget.
func TestMergeCreatedEarliestWindow(t *testing.T) {
	r := func(n int) model.PRRef { return glRef(glHost, glPath, n) }
	w := func(n int, from, to string) model.PushedRef {
		return model.PushedRef{Ref: r(n), From: ts(from), To: ts(to)}
	}
	created := []model.PRRef{r(1), r(2), r(3), r(4)}
	pushed := []model.PushedRef{w(1, "2026-11-17T08:10:00Z", "2026-11-17T08:10:04Z"), w(2, "2026-11-17T08:05:00Z", "2026-11-17T08:05:03Z"), w(3, "2026-11-17T08:06:00Z", "2026-11-17T08:06:02Z")}
	subCreated := []model.PRRef{r(1), r(2), r(3), r(4), r(5)}
	subPushed := []model.PushedRef{w(1, "2026-11-17T08:00:00Z", "2026-11-17T08:00:07Z"), w(2, "2026-11-17T08:09:00Z", "2026-11-17T08:09:01Z"), w(4, "2026-11-17T08:01:00Z", "2026-11-17T08:01:01Z"), w(5, "2026-11-17T08:02:00Z", "2026-11-17T08:02:09Z")}
	gotC, gotP := mergeCreated(created, pushed, subCreated, subPushed)
	if want := []model.PRRef{r(1), r(2), r(3), r(4), r(5)}; !slices.Equal(gotC, want) {
		t.Fatalf("created = %+v, want %+v", gotC, want)
	}
	// 1: the subagent's earlier window; 2: the session's earlier one; 3: the
	// subagent created it with glab, so no window; 4: the session did; 5 is new.
	want := []model.PushedRef{w(1, "2026-11-17T08:00:00Z", "2026-11-17T08:00:07Z"), w(2, "2026-11-17T08:05:00Z", "2026-11-17T08:05:03Z"), w(5, "2026-11-17T08:02:00Z", "2026-11-17T08:02:09Z")}
	if !slices.Equal(gotP, want) {
		t.Fatalf("pushed = %+v, want %+v", gotP, want)
	}

	var full []model.PRRef
	for i := 1; i <= maxPRs; i++ {
		full = append(full, r(i))
	}
	gh := model.PRRef{Repo: model.GitHubRepo("example-org/example-repo"), Number: 21}
	gotC, gotP = mergeCreated(full, nil, []model.PRRef{r(maxPRs + 1), gh}, []model.PushedRef{w(maxPRs+1, "2026-11-17T08:00:00Z", "2026-11-17T08:00:01Z")})
	if len(gotC) != maxPRs+1 || gotC[maxPRs] != gh || len(gotP) != 0 {
		t.Fatalf("budget: %d created (last %+v), pushed %+v", len(gotC), gotC[len(gotC)-1], gotP)
	}
}

// toolResult writes the result line of a call that toolUse wrote earlier.
func toolResult(b *bytes.Buffer, id, result string, at string) {
	res, _ := json.Marshal(map[string]any{
		"type": "user", "timestamp": at,
		"message": map[string]any{"content": []any{
			map[string]any{"type": "tool_result", "tool_use_id": id, "content": result},
		}},
	})
	b.Write(res)
	b.WriteByte('\n')
}

// Tool results can arrive out of order. The push that created a merge request
// and a later push that merely reported it both name it; whichever result is
// read first, the session keeps the window of the call that STARTED first.
func TestMergeRequestsOutOfOrderResultsKeepTheEarliestStart(t *testing.T) {
	creating := "git push -o merge_request.create origin feature-b"
	var inOrder, reversed bytes.Buffer
	for _, b := range []*bytes.Buffer{&inOrder, &reversed} {
		toolUse(b, "p1", creating, false, "2026-11-17T08:00:00.000Z")
		toolUse(b, "p2", creating, false, "2026-11-17T08:05:00.000Z")
	}
	toolResult(&inOrder, "p1", pushOut("12"), "2026-11-17T08:00:40.000Z")
	toolResult(&inOrder, "p2", pushOut("12"), "2026-11-17T08:05:10.000Z")
	toolResult(&reversed, "p2", pushOut("12"), "2026-11-17T08:05:10.000Z")
	toolResult(&reversed, "p1", pushOut("12"), "2026-11-17T08:00:40.000Z")
	want := []model.PushedRef{{Ref: glRef(glHost, glPath, 12), From: ts("2026-11-17T08:00:00Z"), To: ts("2026-11-17T08:00:40Z")}}
	for name, b := range map[string]*bytes.Buffer{"in order": &inOrder, "reversed": &reversed} {
		d := tailOf(t, b.Bytes()).d
		if !slices.Equal(d.PushedPRs, want) || len(d.CreatedPRs) != 1 {
			t.Errorf("%s: PushedPRs = %+v, CreatedPRs = %+v, want the window %+v", name, d.PushedPRs, d.CreatedPRs, want)
		}
	}
}

// A `glab mr create` that fails with an error other than the 409, followed in
// the same call by a view that prints an existing merge request, credits
// nothing; and a merge request an earlier push announced stays a pushed one:
// the view's bare address does not turn it into a created one.
func TestMergeRequestsFailedCreateThenViewCreditsNothing(t *testing.T) {
	var b bytes.Buffer
	toolCall(&b, "p1", "git push -o merge_request.create origin feature-b", pushOut("12"), false)
	failed := "Creating merge request for feature-b into main in " + glPath + "\n\nPOST https://" + glHost + "/api/v4/projects/example-org%2Fexample-repo/merge_requests: 422 {message: [Validate branches Source branch is invalid]}\n"
	toolCall(&b, "x1", "glab mr create --fill --yes; glab mr view 12", failed+glURL("12")+"\n", false)
	toolCall(&b, "x2", "glab mr create --fill --yes; glab mr view 14", "ERROR: could not create the merge request\n"+glURL("14")+"\n", false)
	d := tailOf(t, b.Bytes()).d
	if want := []model.PRRef{glRef(glHost, glPath, 12)}; !slices.Equal(d.CreatedPRs, want) {
		t.Fatalf("CreatedPRs = %+v, want %+v", d.CreatedPRs, want)
	}
	want := []model.PushedRef{{Ref: glRef(glHost, glPath, 12), From: ts("2026-11-17T08:00:00Z"), To: ts("2026-11-17T08:00:01Z")}}
	if !slices.Equal(d.PushedPRs, want) {
		t.Fatalf("PushedPRs = %+v, want %+v: a failed create turned a pushed merge request into a created one", d.PushedPRs, want)
	}
}
