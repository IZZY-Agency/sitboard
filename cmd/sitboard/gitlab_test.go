package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/IZZY-Agency/sitboard/internal/adapters/github"
	"github.com/IZZY-Agency/sitboard/internal/contract"
	"github.com/IZZY-Agency/sitboard/internal/model"
	"github.com/IZZY-Agency/sitboard/internal/source"
)

// fakeGlab stands for the GitLab source: it reports the status it was given
// and records the hosts the core asks about; it never answers about one.
type fakeGlab struct {
	st    source.Status
	wait  chan struct{} // when set, Probe answers once it is closed
	mu    sync.Mutex
	hosts []string
}

func (f *fakeGlab) Probe(context.Context) source.Status {
	if f.wait != nil {
		<-f.wait
	}
	return f.st
}
func (f *fakeGlab) Lookup(context.Context, model.ForgeRepo, []int, []string) ([]model.PullRequest, error) {
	return nil, nil
}
func (f *fakeGlab) Host(_ context.Context, host string) error {
	f.mu.Lock()
	f.hosts = append(f.hosts, host)
	f.mu.Unlock()
	return errors.New("no answer")
}

func (f *fakeGlab) Failing(string) string { return "" }

func (f *fakeGlab) asked() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.hosts)
}

func TestNoGitlabFlagTurnsProviderOff(t *testing.T) {
	cfg, err := parseArgs([]string{"--no-gitlab"}, &bytes.Buffer{})
	if err != nil || !cfg.noGitLab || cfg.noGitHub {
		t.Fatalf("cfg %+v, %v", cfg, err)
	}
	src := realSources(cfg, t.TempDir())
	if st := src.gitlab.Probe(context.Background()); st.Kind != source.KindGitLab || st.State != source.StateOff {
		t.Fatalf("gitlab %+v, want off", st)
	}
	if cfg, _ := parseArgs(nil, &bytes.Buffer{}); cfg.noGitLab || realSources(cfg, t.TempDir()).gitlab == nil {
		t.Fatal("GitLab is on by default")
	}
}

// run reports the GitLab source as probed and hands the provider to the
// core, which asks it about the host of a merge request a session created.
func TestRunReportsAndWiresGitLab(t *testing.T) {
	feed := feedWatcher{send: make(chan model.Fact, 1)}
	gl := &fakeGlab{st: source.Status{Kind: source.KindGitLab, State: source.StateOK, Version: "1.131.0", Tested: true}}
	r := startIn(t, cfgWith(0, true), t.TempDir(), func(env *runEnv) {
		env.sources = func(config, string) sources {
			return sources{watchers: []source.Watcher{feed}, repos: quietRepos{}, prs: github.New(github.Options{Off: true}), gitlab: gl}
		}
	})
	if l := waitGitLabLine(t, r); l.State != contract.SourceOK || l.Version != "1.131.0" || !l.Ready {
		t.Fatalf("gitlab line %+v", l)
	}

	s := model.SessionFact{
		Key: model.SessionKey{Tool: model.ToolClaude, ID: "w"}, Name: "worker-w", Folder: t.TempDir(),
		Status: model.Of(model.StatusBusy), Live: model.Of(true),
		CreatedPRs: []model.PRRef{{Repo: model.GitLabRepo("gitlab.example.com", "example-group/example-project"), Number: 3}},
	}
	feed.send <- s
	deadline := time.Now().Add(5 * time.Second)
	for !slices.Contains(gl.asked(), "gitlab.example.com") {
		if time.Now().After(deadline) {
			t.Fatalf("the core never asked about the host: %v", gl.asked())
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// snapshot reads the page's snapshot once.
func snapshot(t *testing.T, r *running) contract.Snapshot {
	t.Helper()
	resp, err := client(t, r).Get("http://127.0.0.1:" + r.port + contract.PathSnapshot)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	var snap contract.Snapshot
	if err := json.NewDecoder(resp.Body).Decode(&snap); err != nil {
		t.Fatal(err)
	}
	return snap
}

// waitGitLabLine waits for the GitLab line the background probe adds.
func waitGitLabLine(t *testing.T, r *running) contract.SourceStatus {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		snap := snapshot(t, r)
		if i := slices.IndexFunc(snap.Sources, func(s contract.SourceStatus) bool { return s.Kind == contract.SourceGitLab }); i >= 0 {
			return snap.Sources[i]
		}
		if time.Now().After(deadline) {
			t.Fatalf("no GitLab line: %+v", snap.Sources)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// A glab that does not answer its probe delays nothing: sitboard listens and
// serves the page, and the GitLab line comes once glab answers.
func TestStuckGlabProbeDelaysNothing(t *testing.T) {
	gl := &fakeGlab{st: source.Status{Kind: source.KindGitLab, State: source.StateOK, Version: "1.131.0", Tested: true}, wait: make(chan struct{})}
	t.Cleanup(func() {
		select {
		case <-gl.wait:
		default:
			close(gl.wait)
		}
	})
	r := startIn(t, cfgWith(0, true), t.TempDir(), func(env *runEnv) {
		env.sources = func(config, string) sources {
			return sources{repos: quietRepos{}, prs: github.New(github.Options{Off: true}), gitlab: gl}
		}
	})
	if snap := snapshot(t, r); slices.ContainsFunc(snap.Sources, func(s contract.SourceStatus) bool { return s.Kind == contract.SourceGitLab }) {
		t.Fatalf("GitLab line before glab answered: %+v", snap.Sources)
	}
	close(gl.wait)
	if l := waitGitLabLine(t, r); l.State != contract.SourceOK {
		t.Fatalf("gitlab line %+v", l)
	}
}
