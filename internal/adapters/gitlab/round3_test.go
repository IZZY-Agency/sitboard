package gitlab

import (
	"context"
	"errors"
	"runtime"
	"sync/atomic"
	"testing"

	"github.com/IZZY-Agency/sitboard/internal/model"
	"github.com/IZZY-Agency/sitboard/internal/source"
)

// Review round 3 of the GitLab issue lister (#174): each test here fails
// without its part.

// P2: GitLab refusing the token is a new login for the host: a check that
// read a yes before keeps none of it, the next check asks glab afresh, and
// the merge requests and closing issues cached under the old token go.
func TestARefusedTokenVoidsAYesReadBeforeIt(t *testing.T) {
	p, f, _ := newUnchecked(t)
	p.mu.Lock()
	p.cache[selfHosted+"\x00old"] = cached{}
	p.closes[selfHosted+"\x00old"] = closing{}
	p.mu.Unlock()
	refused := false
	f.beforeStart = func(args []string) {
		if args[0] == "config" && !refused {
			refused = true // between the check's auth status and its mount read
			p.doubtLogin(selfHosted)
		}
	}
	if _, err := p.host(context.Background(), selfHosted); !errors.Is(err, errNoAnswer) {
		t.Fatalf("a check whose yes was superseded: %v, want no answer", err)
	}
	if !refused {
		t.Fatal("setup: the check read no mount")
	}
	p.mu.Lock()
	kept, inCache, inCloses := p.hosts[selfHosted].known, len(p.cache), len(p.closes)
	p.mu.Unlock()
	if kept {
		t.Fatal("a yes read before the refusal was kept")
	}
	if inCache != 0 || inCloses != 0 {
		t.Fatalf("answers of the refused token stay cached: %d lookups, %d closing", inCache, inCloses)
	}
	auths := f.count("auth")
	if _, err := p.host(context.Background(), selfHosted); err != nil {
		t.Fatal(err)
	}
	if f.count("auth") != auths+1 {
		t.Fatal("the check after the refusal did not ask glab afresh")
	}
}

// P3: a glab process whose start stalls holds up none of the board's reads
// (Failing, Subfolder): only admission is serialised.
func TestAStalledStartBlocksNoBoardRead(t *testing.T) {
	p, f, _ := newAdapter(t, Options{})
	s := &Source{p: p}
	stalled, free := false, false
	f.inStart = func(args []string) {
		if args[0] != "api" || stalled {
			return
		}
		stalled = true
		if free = p.mu.TryLock(); free {
			p.mu.Unlock()
			_ = s.Failing(selfHosted)
			_ = s.Subfolder(selfHosted)
		}
	}
	if _, err := p.Lookup(context.Background(), selfHosted, project, []int{101}, nil); err != nil {
		t.Fatal(err)
	}
	if !stalled {
		t.Fatal("setup: no process started")
	}
	if !free {
		t.Fatal("a process start holds the lock the board's reads wait on")
	}
}

// Review round 4, ruling 6: a check superseded by a refusal while it read
// the mount, with no answer kept since, is inconclusive at the board's
// boundary (Source.Host), never a logout.
func TestASupersededCheckWithoutReplacementIsNoLogout(t *testing.T) {
	p, f, _ := newUnchecked(t)
	s := &Source{p: p}
	refused := false
	f.beforeStart = func(args []string) {
		if args[0] == "config" && !refused {
			refused = true
			p.doubtLogin(selfHosted)
		}
	}
	err := s.Host(context.Background(), selfHosted)
	if !refused {
		t.Fatal("setup: the check read no mount")
	}
	if errors.Is(err, source.ErrNotLoggedIn) || !errors.Is(err, errNoAnswer) {
		t.Fatalf("a superseded check says %v, want no answer", err)
	}
}

// Review round 4, ruling 7: a call waiting to be admitted while another
// call's start stalls gives up when its context ends: a merge request
// lookup at its deadline returns instead of holding up the board's loop.
func TestAWaitForAdmissionEndsWithTheContext(t *testing.T) {
	p, f, _ := newAdapter(t, Options{})
	stalled, release := make(chan struct{}), make(chan struct{})
	t.Cleanup(func() {
		select {
		case <-release:
		default:
			close(release)
		}
	})
	ctx2, cancel2 := context.WithCancel(context.Background())
	defer cancel2()
	var starts, befores atomic.Int32
	f.inStart = func([]string) {
		if starts.Add(1) == 1 {
			close(stalled)
			<-release
		}
	}
	f.beforeStart = func([]string) {
		if befores.Add(1) == 2 {
			cancel2() // the second lookup's deadline, as it waits
		}
	}
	go func() { _, _ = p.Lookup(context.Background(), selfHosted, project, []int{101}, nil) }()
	<-stalled
	done := make(chan error, 1)
	go func() {
		_, err := p.Lookup(ctx2, selfHosted, project, []int{102}, nil)
		done <- err
	}()
	for range 2_000_000 {
		select {
		case err := <-done:
			if !errors.Is(err, context.Canceled) {
				t.Fatalf("the waiting lookup ended with %v, want its context's end", err)
			}
			return
		default:
			runtime.Gosched()
		}
	}
	t.Fatal("a lookup waiting for admission did not end with its context")
}

// Review round 4, ruling 3: an issue answer for another account than the
// host's last one is a new login (the account is kept in memory only):
// nothing of it is passed on, what was cached under the old login goes,
// and the next check asks glab afresh.
func TestAnotherAccountOnTheHostIsANewLogin(t *testing.T) {
	p, f, _ := newAdapter(t, Options{})
	part := issuesProject(selfHosted+"/"+project, 1, issueNode(1, "Sample issue"))
	f.graphql = ok(issuesAnswer(part))
	repos := []model.ForgeRepo{glRepo(selfHosted, project)}
	if got, err := p.OpenIssues(context.Background(), repos); err != nil || len(got) != 1 {
		t.Fatalf("setup: %+v, %v", got, err)
	}
	p.mu.Lock()
	p.cache[selfHosted+"\x00old"] = cached{}
	p.mu.Unlock()
	f.graphql = ok(issuesAnswerAs(`{"webUrl":"https://`+selfHosted+`/other-user"}`, part))
	got, err := p.OpenIssues(context.Background(), repos)
	if !errors.Is(err, source.ErrLoginChanged) || len(got) != 0 {
		t.Fatalf("another account's answer: %+v, %v; want none and a changed login", got, err)
	}
	p.mu.Lock()
	kept, inCache := p.hosts[selfHosted].known, len(p.cache)
	p.mu.Unlock()
	if kept || inCache != 0 {
		t.Fatalf("the old login's yes kept %v, %d answers cached", kept, inCache)
	}
}
