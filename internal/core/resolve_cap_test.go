package core

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/IZZY-Agency/sitboard/internal/model"
	"github.com/IZZY-Agency/sitboard/internal/source"
)

// slowRepos holds every lookup until released and counts how many run at once.
type slowRepos struct {
	mu            sync.Mutex
	running, most int
	release       chan struct{}
}

func (s *slowRepos) Probe(context.Context) source.Status { return source.Status{Kind: source.KindGit} }
func (s *slowRepos) Resolve(_ context.Context, folder string) (model.Repo, error) {
	s.mu.Lock()
	s.running++
	s.most = max(s.most, s.running)
	s.mu.Unlock()
	<-s.release
	s.mu.Lock()
	s.running--
	s.mu.Unlock()
	return model.Repo{Root: folder, MainRoot: folder}, nil
}

// A start that brings many sessions in many folders runs at most maxResolves
// git lookups at once.
func TestGitLookupsAreCapped(t *testing.T) {
	repos := &slowRepos{release: make(chan struct{})}
	clock := &fakeClock{t: tNow}
	b := newBoard(t, Options{Repos: repos, Now: clock.Now, StaleAfter: 20 * time.Minute})
	for i := range 40 {
		s := session(fmt.Sprintf("s%d", i), model.StatusFinished, tNow)
		s.Folder = fmt.Sprintf("%s/f%d", alpha, i)
		b.Apply(s) // not applySync: that waits for the lookups held here
	}
	time.Sleep(100 * time.Millisecond) // let every lookup that may start, start
	repos.mu.Lock()
	most := repos.most
	repos.mu.Unlock()
	close(repos.release)
	b.resolving.Wait()
	if most != maxResolves {
		t.Fatalf("%d lookups at once, want %d", most, maxResolves)
	}
}
