package gitlab

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/IZZY-Agency/sitboard/internal/adapters/internal/runner"
	"github.com/IZZY-Agency/sitboard/internal/model"
	"github.com/IZZY-Agency/sitboard/internal/source"
)

// issuesProject is one project's part of an issues answer, at the address
// web (a site and path, "https://" left out).
func issuesProject(web string, count int, nodes ...string) string {
	return fmt.Sprintf(`{"webUrl":"https://%s","issuesEnabled":true,"issues":{"count":%d,"nodes":[%s]}}`, web, count, strings.Join(nodes, ","))
}

// issueNode is one open issue as GitLab answers it.
func issueNode(iid int, title string) string {
	return fmt.Sprintf(`{"iid":"%d","title":%q,"updatedAt":"2026-11-19T10:00:00Z","milestone":null,"labels":{"nodes":[]}}`, iid, title)
}

// sampleUser is the account glab is logged in with on selfHosted, as GitLab
// answers currentUser: its address on the default HTTPS port, no mount.
const sampleUser = `{"webUrl":"https://` + selfHosted + `/sample-user"}`

// issuesAnswer wraps project parts as aliases p0, p1, ..., answered by
// selfHosted's default instance for sampleUser.
func issuesAnswer(parts ...string) []byte { return issuesAnswerAs(sampleUser, parts...) }

// issuesAnswerAs is issuesAnswer with user as currentUser (null: no login).
func issuesAnswerAs(user string, parts ...string) []byte {
	var b strings.Builder
	b.WriteString(`{"data":{"currentUser":`)
	b.WriteString(user)
	for i, p := range parts {
		fmt.Fprintf(&b, `,"p%d":%s`, i, p)
	}
	b.WriteString(`}}`)
	return []byte(b.String())
}

func glRepo(host, path string) model.ForgeRepo { return model.GitLabRepo(host, path) }

// argValue is the value of the -f variable name in args, "" when none.
func argValue(args []string, name string) string {
	for i := 0; i+1 < len(args); i++ {
		if args[i] == "-f" {
			if v, ok := strings.CutPrefix(args[i+1], name+"="); ok {
				return v
			}
		}
	}
	return ""
}

func TestOpenIssuesReadsTheAnswer(t *testing.T) {
	p, f, _ := newAdapter(t, Options{})
	other := "example-org/other-repo"
	gone := "example-org/gone-repo"
	off := "example-org/off-repo"
	f.graphql = ok(issuesAnswer(
		`{"webUrl":"https://example.com/`+project+`","issuesEnabled":true,"issues":{"count":42,"nodes":[`+
			`{"iid":"7","title":"Sample issue","updatedAt":"2026-11-19T10:00:00Z","milestone":{"title":"Sample 1.0","dueDate":"2026-12-01T00:00:00Z"},"labels":{"nodes":[{"title":"zeta"},{"title":"Alpha"}]}},`+
			`{"iid":"9","title":"Another","updatedAt":"2026-11-18T10:00:00Z","milestone":{"title":"Old","dueDate":"2026-12-02"},"labels":null},`+
			`{"iid":"x","title":"not a number"},null]}}`,
		issuesProject(selfHosted+"/"+other, 0),
		`null`,
		`{"webUrl":"https://example.com/`+off+`","issuesEnabled":false,"issues":null}`,
	))
	repos := []model.ForgeRepo{glRepo(selfHosted, project), glRepo(selfHosted, other), glRepo(selfHosted, gone), glRepo(selfHosted, off)}
	got, err := p.OpenIssues(context.Background(), repos)
	if err != nil {
		t.Fatal(err)
	}
	if f.count("graphql") != 1 {
		t.Fatalf("one call for four projects of one host, got %d", f.count("graphql"))
	}
	args := f.calls[len(f.calls)-1]
	q := argValue(args, "query")
	if strings.Contains(q, "example") || argValue(args, "p0") != project || argValue(args, "p3") != off {
		t.Fatalf("paths travel as variables, never in the query: %q", args)
	}
	if !slices.Contains(args, "--hostname") || args[slices.Index(args, "--hostname")+1] != selfHosted {
		t.Fatalf("the host is asked by name: %q", args)
	}
	if len(got) != 4 {
		t.Fatalf("every project answered: %+v", got)
	}
	a := got[0]
	if a.State != model.RepoIssuesOK || a.Open != 42 || len(a.List) != 2 {
		t.Fatalf("first project: %+v", a)
	}
	is := a.List[0]
	if is.Number != 7 || is.Title != "Sample issue" || is.Milestone != "Sample 1.0" ||
		!is.MilestoneDue.Equal(time.Date(2026, 12, 1, 0, 0, 0, 0, time.UTC)) || !slices.Equal(is.Labels, []string{"Alpha", "zeta"}) ||
		!is.UpdatedAt.Equal(time.Date(2026, 11, 19, 10, 0, 0, 0, time.UTC)) {
		t.Fatalf("issue read wrong: %+v", is)
	}
	if !a.List[1].MilestoneDue.Equal(time.Date(2026, 12, 2, 0, 0, 0, 0, time.UTC)) || a.List[1].Labels != nil {
		t.Fatalf("a due day and no labels: %+v", a.List[1])
	}
	if got[1].State != model.RepoIssuesOK || got[1].Open != 0 || len(got[1].List) != 0 {
		t.Fatalf("no open issues: %+v", got[1])
	}
	if got[2].State != model.RepoIssuesNotFound || got[3].State != model.RepoIssuesDisabled {
		t.Fatalf("not found and disabled: %+v %+v", got[2], got[3])
	}
	if p.Failing(selfHosted) != "" {
		t.Fatalf("a whole answer fails nothing: %q", p.Failing(selfHosted))
	}
}

func TestOpenIssuesAsksOnlyCheckedHosts(t *testing.T) {
	p, f, _ := newAdapter(t, Options{})
	ctx := context.Background()
	if _, err := p.OpenIssues(ctx, []model.ForgeRepo{glRepo("other.example.com", project)}); !errors.Is(err, source.ErrNotChecked) || f.count("graphql") != 0 {
		t.Fatalf("a host never checked is not asked: %v, %d calls", err, f.count("graphql"))
	}
	p.mu.Lock()
	p.hosts["other.example.com"] = hostAnswer{at: p.now()}
	p.mu.Unlock()
	if _, err := p.OpenIssues(ctx, []model.ForgeRepo{glRepo("other.example.com", project)}); !errors.Is(err, source.ErrNotLoggedIn) || f.count("graphql") != 0 {
		t.Fatalf("a host glab has no login for is not asked: %v, %d calls", err, f.count("graphql"))
	}
	if f.count("auth") != 0 {
		t.Fatal("the lister never checks a login itself")
	}
	gh := model.ForgeRepo{Forge: model.ForgeGitHub, Host: model.GitHubHost, Path: project}
	if got, err := p.OpenIssues(ctx, []model.ForgeRepo{gh}); err != nil || got != nil || f.count("graphql") != 0 {
		t.Fatalf("a GitHub repository is left out: %v %v", got, err)
	}
}

func TestOpenIssuesOffRunsNoGlab(t *testing.T) {
	p, f, _ := newAdapter(t, Options{Off: true})
	p.mu.Lock()
	p.hosts[selfHosted] = hostAnswer{at: p.now(), known: true}
	p.mu.Unlock()
	if got, err := p.OpenIssues(context.Background(), []model.ForgeRepo{glRepo(selfHosted, project)}); got != nil || err != nil || len(f.calls) != 0 {
		t.Fatalf("off: no glab at all: %v %v %q", got, err, f.calls)
	}
}

func TestOpenIssuesUnderASubfolder(t *testing.T) {
	p, f, _ := newAdapter(t, Options{})
	p.mu.Lock()
	p.hosts[selfHosted] = hostAnswer{at: p.now(), known: true, subfolder: "gitlab"}
	p.mu.Unlock()
	f.graphql = ok(issuesAnswer(issuesProject(selfHosted+"/gitlab/"+project, 1, issueNode(3, "Under a mount"))))
	got, err := p.OpenIssues(context.Background(), []model.ForgeRepo{glRepo(selfHosted, "gitlab/"+project), glRepo(selfHosted, project)})
	if err != nil {
		t.Fatal(err)
	}
	if f.count("graphql") != 1 || argValue(f.calls[len(f.calls)-1], "p0") != project || argValue(f.calls[len(f.calls)-1], "p1") != "" {
		t.Fatalf("the API path has no subfolder, and an address outside it is not asked: %q", f.calls)
	}
	if len(got) != 2 {
		t.Fatalf("%+v", got)
	}
	byPath := map[string]model.RepoIssues{}
	for _, ri := range got {
		byPath[ri.Repo.Path] = ri
	}
	if r := byPath["gitlab/"+project]; r.State != model.RepoIssuesOK || len(r.List) != 1 || r.List[0].Number != 3 {
		t.Fatalf("the project under the mount: %+v", r)
	}
	if r := byPath[project]; r.State != model.RepoIssuesNotFound {
		t.Fatalf("an address outside the mount is no project there: %+v", r)
	}
}

func TestOpenIssuesOnAWebPortTakesOnlyThatAddress(t *testing.T) {
	site := selfHosted + ":8443"
	p, f, _ := newAdapter(t, Options{})
	repos := []model.ForgeRepo{glRepo(site, project), glRepo(site, "example-org/two"), glRepo(site, "example-org/three")}
	f.graphql = ok(issuesAnswer(
		issuesProject(site+"/"+project, 1, issueNode(1, "Right port")),
		issuesProject(selfHosted+"/example-org/two", 1, issueNode(2, "Default port")),
		`{"issuesEnabled":true,"issues":{"count":1,"nodes":[`+issueNode(3, "No address")+`]}}`,
	))
	got, err := p.OpenIssues(context.Background(), repos)
	if err != nil {
		t.Fatal(err)
	}
	if args := f.calls[len(f.calls)-1]; args[slices.Index(args, "--hostname")+1] != selfHosted {
		t.Fatalf("glab is asked by the host name, no port: %q", args)
	}
	if len(got) != 1 || got[0].Repo.Path != project || got[0].List[0].Title != "Right port" {
		t.Fatalf("only the answer at the address asked counts: %+v", got)
	}
	if p.Failing(selfHosted) == "" {
		t.Fatal("projects left out: the host's line says the answer was partial")
	}
}

func TestOpenIssuesTakesThePathInAnotherCase(t *testing.T) {
	p, f, _ := newAdapter(t, Options{})
	f.graphql = ok(issuesAnswer(
		issuesProject(selfHosted+"/Example-Org/Example-Repo", 1, issueNode(1, "Same project")),
		issuesProject("other.example.com/example-org/two", 1, issueNode(2, "Other host")),
		`{"webUrl":"https://example.com/example-org/three?x=1","issuesEnabled":true,"issues":{"count":0,"nodes":[]}}`,
	))
	got, err := p.OpenIssues(context.Background(), []model.ForgeRepo{glRepo(selfHosted, project), glRepo(selfHosted, "example-org/two"), glRepo(selfHosted, "example-org/three")})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Repo.Path != project {
		t.Fatalf("a path in another case is the same project; another host or a query is not: %+v", got)
	}
}

func TestOpenIssuesAsksEightProjectsACall(t *testing.T) {
	p, f, _ := newAdapter(t, Options{})
	f.graphql = func(args []string) ([]byte, error) {
		var parts []string
		for i := 0; argValue(args, "p"+fmt.Sprint(i)) != ""; i++ {
			parts = append(parts, issuesProject(selfHosted+"/"+argValue(args, "p"+fmt.Sprint(i)), 0))
		}
		return issuesAnswer(parts...), nil
	}
	var repos []model.ForgeRepo
	for i := range 9 {
		repos = append(repos, glRepo(selfHosted, fmt.Sprintf("example-org/repo-%02d", i)))
	}
	got, err := p.OpenIssues(context.Background(), repos)
	if err != nil || len(got) != 9 {
		t.Fatalf("%d answered, %v", len(got), err)
	}
	if f.count("graphql") != 2 || argValue(f.calls[0], "p7") == "" || argValue(f.calls[0], "p8") != "" {
		t.Fatalf("eight projects a call: %d calls", f.count("graphql"))
	}
}

func TestOpenIssuesKeepsTheProjectsThatAnswered(t *testing.T) {
	p, f, _ := newAdapter(t, Options{})
	body := `{"errors":[{"message":"boom","path":["p1","issues"]}],"data":{"p0":` + issuesProject(selfHosted+"/"+project, 1, issueNode(1, "Fine")) +
		`,"p1":null}}`
	f.graphql = func([]string) ([]byte, error) {
		return nil, &runner.ExitError{Code: 1, Stdout: []byte(body), Stderr: []byte("glab: something failed\n")}
	}
	got, err := p.OpenIssues(context.Background(), []model.ForgeRepo{glRepo(selfHosted, project), glRepo(selfHosted, "example-org/two")})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Repo.Path != project {
		t.Fatalf("the project with an error is left out, never read as not found: %+v", got)
	}
	if p.Failing(selfHosted) == "" {
		t.Fatal("a partial answer shows on the host's line")
	}

	// An error that names no project may concern any: none is taken.
	f.graphql = ok([]byte(`{"errors":[{"message":"boom"}],"data":{"p0":null}}`))
	if got, err := p.OpenIssues(context.Background(), []model.ForgeRepo{glRepo(selfHosted, project)}); err == nil || len(got) != 0 {
		t.Fatalf("an error naming no project: %+v %v", got, err)
	}
}

func TestOpenIssuesRateLimitBacksOff(t *testing.T) {
	p, f, c := newAdapter(t, Options{})
	f.graphql = func([]string) ([]byte, error) {
		return nil, &runner.ExitError{Code: 1, Stderr: []byte("glab: 429 Too Many Requests (HTTP 429)\n")}
	}
	repos := []model.ForgeRepo{glRepo(selfHosted, project)}
	if _, err := p.OpenIssues(context.Background(), repos); !errors.Is(err, source.ErrRateLimited) {
		t.Fatalf("a 429 is a rate limit: %v", err)
	}
	if _, err := p.OpenIssues(context.Background(), repos); !errors.Is(err, source.ErrRateLimited) || f.count("graphql") != 1 {
		t.Fatalf("the host is left alone while its backoff runs: %v, %d calls", err, f.count("graphql"))
	}
	if _, err := p.Lookup(context.Background(), selfHosted, project, []int{1}, nil); !errors.Is(err, source.ErrRateLimited) || f.count("graphql") != 1 {
		t.Fatalf("merge request lookups share the backoff: %v", err)
	}
	c.add(rateLimitBackoff)
	f.graphql = ok(issuesAnswer(issuesProject(selfHosted+"/"+project, 0)))
	if _, err := p.OpenIssues(context.Background(), repos); err != nil || f.count("graphql") != 2 {
		t.Fatalf("asked again after the backoff: %v", err)
	}
}

func TestOpenIssuesLoginRefusedDropsTheHost(t *testing.T) {
	p, f, _ := newAdapter(t, Options{})
	f.graphql = func([]string) ([]byte, error) {
		return nil, &runner.ExitError{Code: 1, Stderr: []byte("glab: 401 Unauthorized (HTTP 401)\n")}
	}
	got, err := p.OpenIssues(context.Background(), []model.ForgeRepo{glRepo(selfHosted, project)})
	if !errors.Is(err, source.ErrNotLoggedIn) || got != nil {
		t.Fatalf("a refused login is not logged in, with no rows: %v %v", got, err)
	}
	if p.Known(context.Background(), selfHosted) {
		t.Fatal("the host's yes went: it is checked again")
	}
	if !strings.Contains(p.Failing(selfHosted), "not logged in") {
		t.Fatalf("the host's line: %q", p.Failing(selfHosted))
	}
}

func TestOpenIssuesDropsAnAnswerReadAcrossALogout(t *testing.T) {
	p, f, _ := newAdapter(t, Options{})
	f.graphql = func([]string) ([]byte, error) {
		p.loggedOut(selfHosted) // another lookup found the login gone meanwhile
		return issuesAnswer(issuesProject(selfHosted+"/"+project, 1, issueNode(1, "Read with a login that went"))), nil
	}
	got, err := p.OpenIssues(context.Background(), []model.ForgeRepo{glRepo(selfHosted, project)})
	if len(got) != 0 {
		t.Fatalf("nothing read across a logout is passed on: %+v %v", got, err)
	}
}

func TestOpenIssuesAnotherFailureLeavesTheProjectsOut(t *testing.T) {
	p, f, _ := newAdapter(t, Options{})
	f.graphql = func([]string) ([]byte, error) {
		return nil, &runner.ExitError{Code: 1, Stderr: []byte("glab: 500 Internal Server Error (HTTP 500)\n")}
	}
	got, err := p.OpenIssues(context.Background(), []model.ForgeRepo{glRepo(selfHosted, project)})
	if err == nil || errors.Is(err, source.ErrNotLoggedIn) || errors.Is(err, source.ErrRateLimited) || len(got) != 0 {
		t.Fatalf("a failure answers nothing, and is neither a logout nor a limit: %v %v", got, err)
	}
	if p.Failing(selfHosted) != "glab failed" {
		t.Fatalf("the host's line: %q", p.Failing(selfHosted))
	}
	if !p.Known(context.Background(), selfHosted) || f.count("auth") != 0 {
		t.Fatal("the login stays")
	}
}

func TestOpenIssuesQueryStaysSmall(t *testing.T) {
	q := buildIssuesQuery(issueReposPerCall)
	if len(q) > 10000 {
		t.Fatalf("GitLab takes at most 10,000 characters a query: %d", len(q))
	}
	// gitlab.com scored this query 5 plus 19 a project (queryComplexity,
	// 27 Nov 2026: 3 plus 19 without currentUser, which takes 2); it allows
	// 200 points without a login, 250 with one.
	if !strings.Contains(q, "{currentUser{webUrl} ") {
		t.Fatalf("the query asks which instance answers: %s", q)
	}
	if score := 5 + 19*issueReposPerCall; score > 200 {
		t.Fatalf("%d points a call", score)
	}
}

// open-issues.json is gitlab.com's real answer to buildIssuesQuery with
// placeholder text: a project with issues, one asked in another case than
// GitLab writes it, one with issues turned off (issuesEnabled false, yet an
// empty connection), one that does not exist (null, no error). Due dates
// come as a day, iids as strings. Asked anonymously, currentUser is null:
// the null project is then no answer (no route shown). With a login
// currentUser carries the account's address (the shape GitLab's users API
// documents and gitlab.com answers for a public account,
// "https://<host>/<username>"): the same null is not found.
func TestOpenIssuesReadsGitLabsRealAnswer(t *testing.T) {
	repos := []model.ForgeRepo{
		glRepo(selfHosted, project), glRepo(selfHosted, "Example-Org/Other-Repo"),
		glRepo(selfHosted, "example-org/mirror-repo"), glRepo(selfHosted, "example-org/gone-repo"),
	}
	anon := fixture(t, "open-issues.json")
	if !strings.Contains(string(anon), `"currentUser": null,`) {
		t.Fatal("the fixture is the anonymous answer")
	}
	p, f, _ := newAdapter(t, Options{})
	f.graphql = ok(anon)
	got, err := p.OpenIssues(context.Background(), repos)
	if err != nil || len(got) != 3 || slices.ContainsFunc(got, func(r model.RepoIssues) bool { return r.Repo.Path == "example-org/gone-repo" }) {
		t.Fatalf("anonymous: the null project is no answer: %+v %v", got, err)
	}
	p, f, _ = newAdapter(t, Options{})
	f.graphql = ok([]byte(strings.Replace(string(anon), `"currentUser": null,`, `"currentUser": `+sampleUser+`,`, 1)))
	got, err = p.OpenIssues(context.Background(), repos)
	if err != nil || len(got) != 4 {
		t.Fatalf("%+v %v", got, err)
	}
	a := got[0]
	if a.State != model.RepoIssuesOK || a.Open != 267 || len(a.List) != 3 || a.List[0].Number != 101 || a.List[0].Title != "Placeholder issue 101" {
		t.Fatalf("first project: %+v", a)
	}
	if a.List[0].Milestone != "" || !a.List[0].MilestoneDue.IsZero() || !a.List[0].UpdatedAt.Equal(time.Date(2026, 11, 27, 7, 24, 25, 0, time.UTC)) {
		t.Fatalf("no milestone: %+v", a.List[0])
	}
	if !slices.Equal(a.List[0].Labels, []string{"Another Label", "placeholder label", "Placeholder::Kind", "sample", "zz last"}) {
		t.Fatalf("labels by name: %q", a.List[0].Labels)
	}
	if m := a.List[1]; m.Milestone != "Sample 1.0" || !m.MilestoneDue.Equal(time.Date(2026, 12, 11, 0, 0, 0, 0, time.UTC)) {
		t.Fatalf("a milestone due on a day: %+v", m)
	}
	if b := got[1]; b.State != model.RepoIssuesOK || b.Repo.Path != "Example-Org/Other-Repo" || b.Open != 2555 || len(b.List) != 2 {
		t.Fatalf("asked in another case: %+v", b)
	}
	if got[2].State != model.RepoIssuesDisabled || got[3].State != model.RepoIssuesNotFound {
		t.Fatalf("disabled and not found: %+v %+v", got[2], got[3])
	}
}

// GitLab refusing the token ({"errors":[{"message":"Invalid token"}]},
// "glab: Invalid token" with no status) is no logout by itself, but nothing
// read with that token is passed on, and the host's yes goes: its next
// check runs `glab auth status` afresh (the production host check,
// Source.Host). A 401 in glab's own line for the host (what authAnswer
// reads) denies the host; an inconclusive answer (a 502, out of time) is no
// answer, and the yes stays dropped until a check says yes.
func TestOpenIssuesARefusedTokenSendsTheHostToItsLoginCheck(t *testing.T) {
	ctx := context.Background()
	repos := []model.ForgeRepo{glRepo(selfHosted, project)}
	authLine := func(status string) error {
		return &runner.ExitError{Code: 1, Stderr: []byte(selfHosted + "\n  x " + selfHosted + ": API call failed: GET https://" + selfHosted + "/api/v4/user: " + status + "\n")}
	}
	refuse := func(t *testing.T) (*Provider, *Source, *fakeGlab) {
		t.Helper()
		p, f, _ := newAdapter(t, Options{})
		f.graphql = func([]string) ([]byte, error) {
			return nil, &runner.ExitError{Code: 1, Stdout: []byte(`{"errors":[{"message":"Invalid token"}]}`), Stderr: []byte("glab: Invalid token\n")}
		}
		got, err := p.OpenIssues(ctx, repos)
		if got != nil || !errors.Is(err, source.ErrTokenRefused) || !errors.Is(err, source.ErrNotChecked) || errors.Is(err, source.ErrNotLoggedIn) {
			t.Fatalf("a refused token: no rows, not checked: %v %v", got, err)
		}
		if _, err := p.OpenIssues(ctx, repos); !errors.Is(err, source.ErrNotChecked) || f.count("graphql") != 1 {
			t.Fatalf("nothing more is asked before a login check: %v, %d calls", err, f.count("graphql"))
		}
		return p, &Source{p: p}, f
	}

	t.Run("401", func(t *testing.T) {
		p, s, f := refuse(t)
		f.auth[selfHosted] = authLine("401 {message: 401 Unauthorized}")
		if err := s.Host(ctx, selfHosted); !errors.Is(err, source.ErrNotLoggedIn) || f.count("auth") != 1 {
			t.Fatalf("the check runs glab auth status afresh and hears the 401: %v, %d auth calls", err, f.count("auth"))
		}
		if _, err := p.OpenIssues(ctx, repos); !errors.Is(err, source.ErrNotLoggedIn) || f.count("graphql") != 1 {
			t.Fatalf("the host is known to have no login: %v, %d calls", err, f.count("graphql"))
		}
		if err := s.Host(ctx, selfHosted); !errors.Is(err, source.ErrNotLoggedIn) || f.count("auth") != 1 {
			t.Fatalf("the no is kept: %v, %d auth calls", err, f.count("auth"))
		}
	})

	for _, c := range []struct {
		name string
		auth error
	}{
		{"502", authLine("502 {message: 502 Bad Gateway}")},
		{"out of time", context.DeadlineExceeded},
	} {
		t.Run(c.name, func(t *testing.T) {
			p, s, f := refuse(t)
			f.auth[selfHosted] = c.auth
			for range 2 {
				if err := s.Host(ctx, selfHosted); err == nil || errors.Is(err, source.ErrNotLoggedIn) {
					t.Fatalf("an inconclusive check is no answer, not a definite no: %v", err)
				}
				if _, err := p.OpenIssues(ctx, repos); !errors.Is(err, source.ErrNotChecked) || f.count("graphql") != 1 {
					t.Fatalf("the yes stays dropped: %v, %d calls", err, f.count("graphql"))
				}
			}
			if f.count("auth") != 2 {
				t.Fatalf("each check asks glab again: %d", f.count("auth"))
			}
			f.auth[selfHosted] = nil
			f.graphql = ok(issuesAnswer(issuesProject(selfHosted+"/"+project, 0)))
			if err := s.Host(ctx, selfHosted); err != nil {
				t.Fatalf("a check that says yes: %v", err)
			}
			if got, err := p.OpenIssues(ctx, repos); err != nil || len(got) != 1 {
				t.Fatalf("asked again after the yes: %+v %v", got, err)
			}
		})
	}
}

// The login is looked at again before every call: gone, run out or under
// another mount since the call before, the next path is never sent.
func TestOpenIssuesChecksTheLoginBeforeEveryCall(t *testing.T) {
	repos := make([]model.ForgeRepo, 0, 9)
	for i := range 9 {
		repos = append(repos, glRepo(selfHosted, fmt.Sprintf("example-org/repo-%02d", i)))
	}
	answer := func(args []string) []byte {
		var parts []string
		for i := 0; argValue(args, "p"+fmt.Sprint(i)) != ""; i++ {
			parts = append(parts, issuesProject(selfHosted+"/"+argValue(args, "p"+fmt.Sprint(i)), 0))
		}
		return issuesAnswer(parts...)
	}
	for _, c := range []struct {
		name   string
		change func(p *Provider, c *clock)
		want   error
		rows   bool
	}{
		{"logged out", func(p *Provider, _ *clock) { p.loggedOut(selfHosted) }, source.ErrNotLoggedIn, false},
		{"yes ran out", func(_ *Provider, c *clock) { c.add(knownFor) }, source.ErrNotChecked, true},
		{"mount moved", func(p *Provider, _ *clock) {
			p.mu.Lock()
			p.hosts[selfHosted] = hostAnswer{at: p.now(), known: true, subfolder: "gitlab"}
			p.mu.Unlock()
		}, source.ErrNotChecked, false},
	} {
		p, f, clk := newAdapter(t, Options{})
		f.graphql = func(args []string) ([]byte, error) {
			c.change(p, clk)
			return answer(args), nil
		}
		got, err := p.OpenIssues(context.Background(), repos)
		if f.count("graphql") != 1 || !errors.Is(err, c.want) {
			t.Errorf("%s: %d calls, %v", c.name, f.count("graphql"), err)
		}
		if (len(got) > 0) != c.rows {
			t.Errorf("%s: rows %+v", c.name, got)
		}
	}
}

// glab routes --hostname by its own configuration, so the instance that
// answers may not be the one at the checkout's address (another port, the
// default one, another scheme, another mount). A null project is not found
// only when currentUser's address shows the answer came from the instance
// at the project's address; otherwise it is no answer, never a loss of rows.
func TestOpenIssuesANullProjectIsNotFoundOnlyOnAVerifiedRoute(t *testing.T) {
	port := selfHosted + ":8443"
	user := func(url string) string { return `{"webUrl":"` + url + `"}` }
	for _, c := range []struct {
		name, site, sub, user string
		found                 bool
	}{
		{"default checkout, answered on :8443", selfHosted, "", user("https://" + port + "/sample-user"), false},
		{":8443 checkout, answered on the default port", port, "", user("https://" + selfHosted + "/sample-user"), false},
		{"anonymous", selfHosted, "", `null`, false},
		{"no address", selfHosted, "", `{"webUrl":null}`, false},
		{"plain http", selfHosted, "", user("http://" + selfHosted + "/sample-user"), false},
		{"another host", selfHosted, "", user("https://other.example.com/sample-user"), false},
		{"no user segment", selfHosted, "", user("https://" + selfHosted + "/"), false},
		{"verified, default port", selfHosted, "", user("https://" + selfHosted + "/sample-user"), true},
		{"verified, :443 spelled out", selfHosted, "", user("https://" + selfHosted + ":443/sample-user"), true},
		{"verified, :8443", port, "", user("https://" + port + "/sample-user"), true},
		{"verified, under the mount", selfHosted, "gitlab", user("https://" + selfHosted + "/gitlab/sample-user"), true},
		{"mount asked, none answered", selfHosted, "gitlab", user("https://" + selfHosted + "/sample-user"), false},
		{"no mount asked, one answered", selfHosted, "", user("https://" + selfHosted + "/gitlab/sample-user"), false},
		{"another mount", selfHosted, "gitlab", user("https://" + selfHosted + "/GitLab/sample-user"), false},
	} {
		p, f, _ := newAdapter(t, Options{})
		p.mu.Lock()
		p.hosts[selfHosted] = hostAnswer{at: p.now(), known: true, subfolder: c.sub}
		p.mu.Unlock()
		path := project
		if c.sub != "" {
			path = c.sub + "/" + project
		}
		f.graphql = ok(issuesAnswerAs(c.user, `null`))
		got, _ := p.OpenIssues(context.Background(), []model.ForgeRepo{glRepo(c.site, path)})
		switch {
		case c.found && (len(got) != 1 || got[0].State != model.RepoIssuesNotFound):
			t.Errorf("%s: not found: %+v", c.name, got)
		case !c.found && len(got) != 0:
			t.Errorf("%s: no answer: %+v", c.name, got)
		case !c.found && p.Failing(selfHosted) == "":
			t.Errorf("%s: the host's line says the answer was partial", c.name)
		}
	}

	// A project GitLab answered needs no route: its own address is checked.
	p, f, _ := newAdapter(t, Options{})
	f.graphql = ok(issuesAnswerAs(`null`, issuesProject(selfHosted+"/"+project, 1, issueNode(1, "Shown"))))
	if got, err := p.OpenIssues(context.Background(), []model.ForgeRepo{glRepo(selfHosted, project)}); err != nil || len(got) != 1 || got[0].State != model.RepoIssuesOK {
		t.Fatalf("an answered project, anonymously: %+v %v", got, err)
	}
}

// The subfolder must be the one configured, exactly; the project's path a
// valid GitLab path equal but for ASCII case; and an answer without an
// address proves nothing.
func TestOpenIssuesAddressCheckIsStrictOnTheMountAndTheLetters(t *testing.T) {
	p, f, _ := newAdapter(t, Options{})
	p.mu.Lock()
	p.hosts[selfHosted] = hostAnswer{at: p.now(), known: true, subfolder: "gitlab"}
	p.mu.Unlock()
	kit := "gitlab/example-org/kit"
	f.graphql = ok(issuesAnswer(
		issuesProject(selfHosted+"/GitLab/example-org/repo-a", 1, issueNode(1, "Other mount")),
		issuesProject(selfHosted+"/gitlab/example-org/Kit", 1, issueNode(2, "Kelvin sign")),
		`{"issuesEnabled":true,"issues":{"count":1,"nodes":[`+issueNode(3, "No address")+`]}}`,
		issuesProject(selfHosted+"/gitlab/Example-Org/Repo-D", 1, issueNode(4, "Same, in capitals")),
	))
	got, err := p.OpenIssues(context.Background(), []model.ForgeRepo{
		glRepo(selfHosted, "gitlab/example-org/repo-a"), glRepo(selfHosted, kit),
		glRepo(selfHosted, "gitlab/example-org/repo-c"), glRepo(selfHosted, "gitlab/example-org/repo-d"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Repo.Path != "gitlab/example-org/repo-d" {
		t.Fatalf("only the exact mount and an ASCII case change pass: %+v", got)
	}
}

// A project answer without its count, or without its list, or with a count
// below zero, is no answer: never "no open issues".
func TestOpenIssuesAnAnswerWithoutCountOrListIsNoAnswer(t *testing.T) {
	p, f, _ := newAdapter(t, Options{})
	w := `"webUrl":"https://example.com/example-org/repo-%d","issuesEnabled":true`
	f.graphql = ok(issuesAnswer(
		"{"+fmt.Sprintf(w, 0)+`,"issues":{"nodes":[]}}`,
		"{"+fmt.Sprintf(w, 1)+`,"issues":{"count":3}}`,
		"{"+fmt.Sprintf(w, 2)+`,"issues":{"count":-1,"nodes":[]}}`,
		"{"+fmt.Sprintf(w, 3)+`,"issues":{"count":null,"nodes":null}}`,
		"{"+fmt.Sprintf(w, 4)+`,"issues":{"count":0,"nodes":[]}}`,
	))
	var repos []model.ForgeRepo
	for i := range 5 {
		repos = append(repos, glRepo(selfHosted, fmt.Sprintf("example-org/repo-%d", i)))
	}
	got, err := p.OpenIssues(context.Background(), repos)
	if err != nil || len(got) != 1 || got[0].Repo.Path != "example-org/repo-4" || got[0].State != model.RepoIssuesOK {
		t.Fatalf("only the whole answer counts: %+v %v", got, err)
	}
}

// A host that did not answer in time is not asked the rest of the look: each
// further call would only wait as long.
func TestOpenIssuesAHostThatTimesOutIsNotAskedFurther(t *testing.T) {
	p, f, _ := newAdapter(t, Options{})
	f.graphql = func([]string) ([]byte, error) { return nil, context.DeadlineExceeded }
	var repos []model.ForgeRepo
	for i := range 20 {
		repos = append(repos, glRepo(selfHosted, fmt.Sprintf("example-org/repo-%02d", i)))
	}
	if _, err := p.OpenIssues(context.Background(), repos); err == nil || f.count("graphql") != 1 {
		t.Fatalf("one call for a host that does not answer: %v, %d calls", err, f.count("graphql"))
	}
}

// The last check and the start of the glab process are one step under the
// provider's lock: a call paused after the look's own check, while a host
// check or a merge request lookup records a logout (or a 429, glab logged in
// nowhere, the yes running out), never starts, and no path goes out.
func TestOpenIssuesAChangeRecordedAsTheCallStartsKeepsThePathIn(t *testing.T) {
	for _, c := range []struct {
		name   string
		record func(p *Provider, c *clock)
		want   error
	}{
		{"logged out", func(p *Provider, _ *clock) { p.loggedOut(selfHosted) }, source.ErrNotLoggedIn},
		{"rate limited", func(p *Provider, _ *clock) { p.limited(selfHosted, errors.New("429")) }, source.ErrRateLimited},
		{"logged in nowhere", func(p *Provider, _ *clock) { p.loggedInNowhere() }, source.ErrNoLogins},
		{"yes ran out", func(_ *Provider, c *clock) { c.add(knownFor) }, source.ErrNotChecked},
	} {
		p, f, clk := newAdapter(t, Options{})
		f.graphql = ok(issuesAnswer(issuesProject(selfHosted+"/"+project, 1, issueNode(1, "Sent anyway"))))
		paused, release := make(chan struct{}), make(chan struct{})
		f.beforeStart = func(args []string) {
			if len(args) > 1 && args[1] == "graphql" {
				close(paused)
				<-release
			}
		}
		type answer struct {
			got []model.RepoIssues
			err error
		}
		done := make(chan answer, 1)
		go func() {
			got, err := p.OpenIssues(context.Background(), []model.ForgeRepo{glRepo(selfHosted, project)})
			done <- answer{got, err}
		}()
		<-paused // past the look's own check, before the process starts
		c.record(p, clk)
		close(release)
		a := <-done
		if !errors.Is(a.err, c.want) || len(a.got) != 0 {
			t.Errorf("%s: %+v %v", c.name, a.got, a.err)
		}
		f.mu.Lock()
		for _, args := range f.calls {
			if slices.ContainsFunc(args, func(s string) bool { return strings.Contains(s, project) }) {
				t.Errorf("%s: the path went out: %q", c.name, args)
			}
		}
		f.mu.Unlock()
		if f.count("graphql") != 0 {
			t.Errorf("%s: the process started", c.name)
		}
	}
}
