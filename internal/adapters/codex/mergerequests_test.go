package codex

import (
	"bytes"
	"context"
	"slices"
	"strconv"
	"testing"
	"time"

	"github.com/IZZY-Agency/sitboard/internal/model"
)

const (
	glHost = "gitlab.example.com"
	glPath = "example-org/example-repo"
)

func glRef(n int) model.PRRef {
	return model.PRRef{Repo: model.GitLabRepo(glHost, glPath), Number: n}
}

func glURL(n int) string { return model.ChangeRequestURL(model.GitLabRepo(glHost, glPath), n) }

// pushOut is what a push prints when GitLab names the merge request for the
// branch (the same whether the push created it or it was open already).
func pushOut(n int) string {
	return "remote: \nremote: View merge request for feature-b:\nremote:   " + glURL(n) + "\nremote: \n"
}

func tailed(t *testing.T, b []byte) *tailer {
	t.Helper()
	tl := newTailer(tailerFile(t, b))
	if _, err := tl.update(context.Background(), fixedNow); err != nil {
		t.Fatal(err)
	}
	return tl
}

// `glab mr create` credits its bare address; a creating push credits its
// "remote:" line and keeps the window of the call. A rollout records a
// command once, when it finished, so the window is that one moment (the core
// widens it).
func TestMergeRequestsOfGlabAndPush(t *testing.T) {
	var b bytes.Buffer
	b.Write(commandLine(t, "glab mr create --fill --yes", "Creating merge request for a into main in "+glPath+"\n\n"+glURL(11)+"\n"))
	b.Write(commandLine(t, "git push -o merge_request.create origin b", pushOut(12)))
	tl := tailed(t, b.Bytes())
	if want := []model.PRRef{glRef(11), glRef(12)}; !slices.Equal(tl.d.CreatedPRs, want) {
		t.Fatalf("CreatedPRs = %+v, want %+v", tl.d.CreatedPRs, want)
	}
	at := time.Date(2026, 11, 20, 11, 1, 0, 0, time.UTC)
	if want := []model.PushedRef{{Ref: glRef(12), From: at, To: at}}; !slices.Equal(tl.d.PushedPRs, want) {
		t.Fatalf("PushedPRs = %+v, want %+v", tl.d.PushedPRs, want)
	}
}

// glab's 409 names the merge request by number, never by address, so a
// refused create (a failed command) credits nothing; a plain push names an
// existing merge request; a github.com address is never a GitLab ref; a view
// creates nothing; a "remote:" address that GitLab did not announce is not the
// push's. The successful create in the same rollout proves the matcher is
// wired: disconnected, it would be missing.
func TestMergeRequestsNotCredited(t *testing.T) {
	var b bytes.Buffer
	b.Write(commandLine(t, "glab mr create --fill --yes", glURL(10)+"\n"))
	b.Write(failedCommandLine(t, "glab mr create --fill --yes", "failed to create merge request. Another open merge request already exists for this source branch: !12 (HTTP 409)\n"))
	b.Write(failedCommandLine(t, "git push -u origin a && glab mr create --fill --yes", "remote: View merge request for a:\nremote:   "+glURL(12)+"\nfailed: Another open merge request already exists: !12\n"))
	b.Write(commandLine(t, "git push -o merge_request.create origin a", "remote: View merge request for a:\nremote:   https://github.com/"+glPath+"/-/merge_requests/13\n"))
	b.Write(commandLine(t, "glab mr view 14", glURL(14)+"\n"))
	b.Write(commandLine(t, "git push origin a", "remote: View merge request for a:\nremote:   "+glURL(15)+"\n"))
	b.Write(commandLine(t, "git push -o merge_request.create origin a", glURL(16)+"\n")) // a bare line is not a push's
	b.Write(commandLine(t, "git push -o merge_request.create origin a", "remote:   "+glURL(17)+"\n"))
	tl := tailed(t, b.Bytes())
	if want := []model.PRRef{glRef(10)}; !slices.Equal(tl.d.CreatedPRs, want) || len(tl.d.PushedPRs) != 0 {
		t.Fatalf("credited: %+v %+v, want only %+v", tl.d.CreatedPRs, tl.d.PushedPRs, want)
	}
}

// The hand-written GitLab rollout (testdata/fixtures/codex/0.171.0/gitlab-mr.jsonl):
// a created merge request (41), a creating push (43), a refused create for
// 43's branch (409, a failed command), a refused create followed by a view
// (44), then a creating push that names 41 again. 41 stays a created one,
// with no window, although a push named it later; 43 keeps the window of its
// push, the refusal after it changes nothing.
func TestMergeRequestsFixture(t *testing.T) {
	tl := tailed(t, fixture(t, "gitlab-mr.jsonl"))
	if want := []model.PRRef{glRef(41), glRef(43)}; !slices.Equal(tl.d.CreatedPRs, want) {
		t.Fatalf("CreatedPRs = %+v, want %+v", tl.d.CreatedPRs, want)
	}
	at := time.Date(2026, 11, 20, 11, 11, 5, 0, time.UTC)
	if want := []model.PushedRef{{Ref: glRef(43), From: at, To: at}}; !slices.Equal(tl.d.PushedPRs, want) {
		t.Fatalf("PushedPRs = %+v, want %+v", tl.d.PushedPRs, want)
	}
}

// Mixed calls: glab's announcement ties the address to the create; without
// a terminal (no announcement) the first address is the create's when the
// create ran first and nothing was refused.
func TestMergeRequestsMixedCalls(t *testing.T) {
	var b bytes.Buffer
	b.Write(commandLine(t, "glab mr create --fill --yes && glab mr view 52", "Creating merge request for a into main in "+glPath+"\n\n"+glURL(51)+"\n"+glURL(52)+"\n"))
	b.Write(commandLine(t, "glab mr create --fill --yes; glab mr view 54", glURL(53)+"\n"+glURL(54)+"\n"))
	b.Write(commandLine(t, "glab mr view 55; glab mr create --fill --yes", glURL(55)+"\n"+glURL(56)+"\n"))
	b.Write(failedCommandLine(t, "glab mr create --fill --yes || glab mr view 57", "failed to create merge request: 409 {message: [Another open merge request already exists for this source branch: !57]}\n"+glURL(57)+"\n"))
	tl := tailed(t, b.Bytes())
	if want := []model.PRRef{glRef(51), glRef(53)}; !slices.Equal(tl.d.CreatedPRs, want) {
		t.Fatalf("CreatedPRs = %+v, want %+v", tl.d.CreatedPRs, want)
	}
}

// Each forge has its own budget: a flood of merge requests leaves room for
// a GitHub pull request created after it.
func TestMergeRequestsFloodLeavesRoomForGitHub(t *testing.T) {
	var b bytes.Buffer
	for i := 1; i <= maxPRs+3; i++ {
		b.Write(commandLine(t, "glab mr create --fill --yes", glURL(i)+"\n"))
	}
	b.Write(commandLine(t, "gh pr create --fill", "https://github.com/example-org/example-repo/pull/21\n"))
	tl := tailed(t, b.Bytes())
	gh := model.PRRef{Repo: model.GitHubRepo("example-org/example-repo"), Number: 21}
	if len(tl.d.CreatedPRs) != maxPRs+1 || tl.d.CreatedPRs[maxPRs] != gh {
		t.Fatalf("%d created, last %+v", len(tl.d.CreatedPRs), tl.d.CreatedPRs[len(tl.d.CreatedPRs)-1])
	}
}

// A command recorded without a time has no window: its push candidate is
// dropped whole, so a later push with a time still records it.
func TestMergeRequestsZeroTimeDropped(t *testing.T) {
	zero := bytes.Replace(commandLine(t, "git push -o merge_request.create origin b", pushOut(12)), []byte(`"timestamp":"2026-11-20T11:01:00.000Z",`), nil, 1)
	tl := tailed(t, zero)
	if len(tl.d.CreatedPRs) != 0 || len(tl.d.PushedPRs) != 0 {
		t.Fatalf("credited without a time: %+v %+v", tl.d.CreatedPRs, tl.d.PushedPRs)
	}
	tl = tailed(t, append(zero, commandLine(t, "git push -o merge_request.create origin b", pushOut(12))...))
	at := time.Date(2026, 11, 20, 11, 1, 0, 0, time.UTC)
	if want := []model.PushedRef{{Ref: glRef(12), From: at, To: at}}; !slices.Equal(tl.d.PushedPRs, want) {
		t.Fatalf("PushedPRs = %+v, want %+v", tl.d.PushedPRs, want)
	}
}

// Both forges in one rollout; neither's address becomes the other's.
func TestMergeRequestsBesideGitHub(t *testing.T) {
	var b bytes.Buffer
	b.Write(commandLine(t, "gh pr create --fill", "https://github.com/example-org/example-repo/pull/21\n"+glURL(22)+"\n"))
	b.Write(commandLine(t, "glab mr create --fill", "https://github.com/example-org/example-repo/pull/23\n"+glURL(24)+"\n"))
	tl := tailed(t, b.Bytes())
	want := []model.PRRef{{Repo: model.GitHubRepo("example-org/example-repo"), Number: 21}, glRef(24)}
	if !slices.Equal(tl.d.CreatedPRs, want) {
		t.Fatalf("CreatedPRs = %+v, want %+v", tl.d.CreatedPRs, want)
	}
}

// The cap on CreatedPRs also bounds PushedPRs.
func TestMergeRequestsCap(t *testing.T) {
	var b bytes.Buffer
	for i := 1; i <= maxPRs+3; i++ {
		b.Write(commandLine(t, "git push -o merge_request.create origin f"+strconv.Itoa(i), pushOut(i)))
	}
	tl := tailed(t, b.Bytes())
	if len(tl.d.CreatedPRs) != maxPRs || len(tl.d.PushedPRs) != maxPRs {
		t.Fatalf("%d created, %d pushed, want %d each", len(tl.d.CreatedPRs), len(tl.d.PushedPRs), maxPRs)
	}
}

// The session's fact carries the merge request and its push window.
func TestMergeRequestsReachTheFact(t *testing.T) {
	e := newEnv(t)
	rollout := append(fixture(t, "worker.jsonl"), commandLine(t, "git push -o merge_request.create origin b", pushOut(12))...)
	e.addBytes(idWorker, rollout, fixedNow.Add(-55*time.Minute))
	e.lock(idWorker, 4242)
	e.rescan()
	f, ok := e.last(idWorker)
	if !ok {
		t.Fatal("no fact")
	}
	at := time.Date(2026, 11, 20, 11, 1, 0, 0, time.UTC)
	if !slices.Contains(f.CreatedPRs, glRef(12)) || !slices.Equal(f.PushedPRs, []model.PushedRef{{Ref: glRef(12), From: at, To: at}}) {
		t.Fatalf("CreatedPRs = %v, PushedPRs = %v", f.CreatedPRs, f.PushedPRs)
	}
}

// commandLineAt is commandLine finished at the given time.
func commandLineAt(t *testing.T, script, output, at string) []byte {
	t.Helper()
	return bytes.Replace(commandLine(t, script, output), []byte("2026-11-20T11:01:00.000Z"), []byte(at), 1)
}

// A rollout knows only when a command finished. Two pushes that name the same
// merge request keep the earliest finish, whichever is read first.
func TestMergeRequestsKeepTheEarliestFinish(t *testing.T) {
	creating := "git push -o merge_request.create origin b"
	var b bytes.Buffer
	b.Write(commandLineAt(t, creating, pushOut(12), "2026-11-20T11:05:00.000Z"))
	b.Write(commandLineAt(t, creating, pushOut(12), "2026-11-20T11:00:30.000Z"))
	tl := tailed(t, b.Bytes())
	at := time.Date(2026, 11, 20, 11, 0, 30, 0, time.UTC)
	if want := []model.PushedRef{{Ref: glRef(12), From: at, To: at}}; !slices.Equal(tl.d.PushedPRs, want) || len(tl.d.CreatedPRs) != 1 {
		t.Fatalf("PushedPRs = %+v, CreatedPRs = %+v, want the window %+v", tl.d.PushedPRs, tl.d.CreatedPRs, want)
	}
}

// A `glab mr create` that fails with an error other than the 409, followed in
// the same call by a view that prints an existing merge request, credits
// nothing; and a merge request an earlier push announced stays a pushed one.
func TestMergeRequestsFailedCreateThenViewCreditsNothing(t *testing.T) {
	var b bytes.Buffer
	b.Write(commandLine(t, "git push -o merge_request.create origin b", pushOut(12)))
	b.Write(commandLine(t, "glab mr create --fill --yes; glab mr view 12", "Creating merge request for b into main in "+glPath+"\n\nPOST https://"+glHost+"/api/v4/projects/example-org%2Fexample-repo/merge_requests: 422 {message: [Validate branches Source branch is invalid]}\n"+glURL(12)+"\n"))
	b.Write(commandLine(t, "glab mr create --fill --yes; glab mr view 14", "ERROR: could not create the merge request\n"+glURL(14)+"\n"))
	tl := tailed(t, b.Bytes())
	at := time.Date(2026, 11, 20, 11, 1, 0, 0, time.UTC)
	if want := []model.PRRef{glRef(12)}; !slices.Equal(tl.d.CreatedPRs, want) {
		t.Fatalf("CreatedPRs = %+v, want %+v", tl.d.CreatedPRs, want)
	}
	if want := []model.PushedRef{{Ref: glRef(12), From: at, To: at}}; !slices.Equal(tl.d.PushedPRs, want) {
		t.Fatalf("PushedPRs = %+v, want %+v: a failed create turned a pushed merge request into a created one", tl.d.PushedPRs, want)
	}
}
