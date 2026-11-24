package shellcmd

import (
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/IZZY-Agency/sitboard/internal/model"
)

// mr is the merge request n of the project path on host, as the matcher
// returns it.
func mr(host, path string, n int, pushed bool) MergeRequest {
	return MergeRequest{Ref: model.PRRef{Repo: model.GitLabRepo(host, path), Number: n}, Pushed: pushed}
}

func mrURL(host, path string, n int) string {
	return model.ChangeRequestURL(model.GitLabRepo(host, path), n)
}

func TestCreatesMR(t *testing.T) {
	yes := []string{
		`glab mr create --fill --yes`,
		`git push -u origin feature-a && glab mr create --title "Placeholder" --yes`,
		`cd /home/dev/projects/alpha && /opt/homebrew/bin/glab mr create -f`,
		`git push -o merge_request.create origin feature-a`,
		`git -C /home/dev/projects/alpha push --push-option=merge_request.create -u origin HEAD`,
		`git push --push-option merge_request.create origin feature-a`,
		`git push -omerge_request.create origin feature-a`,
		`git push -o merge_request.create=true -o merge_request.target=main origin feature-a`,
		`git -c push.default=current --no-pager push -o merge_request.create`,
		`git --git-dir .git --work-tree . push -o merge_request.create origin feature-a`,
		`bash -c 'glab mr create --fill'`,
	}
	no := []string{
		`glab mr list`,
		`glab mr view 12`,
		`echo glab mr create`,
		`grep "glab mr create" notes.txt`,
		`git push origin feature-a`,
		`git push -o ci.skip origin feature-a`,
		`git log --grep merge_request.create`,
		`git commit -m "-o merge_request.create"`,
		`gh pr create --fill`,
		`git push origin feature-a; echo -o merge_request.create`,
		`git fetch -o merge_request.create origin`, // fetch has -o too
		`git fetch push -o merge_request.create`,   // a remote named push
		`git -C push fetch -o merge_request.create`,
		`git push origin -- -o merge_request.create`, // after --, not an option
		`git push -o merge_request.target=main origin feature-a`,
		`git push -o merge_request.remove_source_branch origin feature-a`,
	}
	for _, s := range yes {
		if !Parse(s).MRCreation().Any() {
			t.Errorf("not seen as creating a merge request: %s", s)
		}
	}
	for _, s := range no {
		if Parse(s).MRCreation().Any() {
			t.Errorf("seen as creating a merge request: %s", s)
		}
	}
}

func TestMRRefs(t *testing.T) {
	// The shapes `glab mr create` (no terminal) and a GitLab push print.
	out := strings.Join([]string{
		"",
		"Creating merge request for feature-a into main in example-org/example-repo",
		"",
		"https://gitlab.com/example-org/example-repo/-/merge_requests/101",
		"remote: ",
		"remote: View merge request for feature-b:        ",
		"remote:   https://example.com/example-org/sub/example-repo/-/merge_requests/12   ",
		"remote: To create a merge request for feature-c, visit:        ",
		"remote:   https://example.com/example-org/example-repo/-/merge_requests/new?merge_request%5Bsource_branch%5D=feature-c        ",
		"see https://gitlab.com/example-org/example-repo/-/merge_requests/102 for details",
		"https://gitlab.com/example-org/example-repo/-/merge_requests/101",
		"https://gitlab.com/example-org/example-repo/-/merge_requests/0",
		"https://Example.com/example-org/example-repo/-/merge_requests/5",
		"https://example.com/example-repo/-/merge_requests/6",
		"https://example.com/example-org/../example-repo/-/merge_requests/7",
		"https://github.com/example-org/example-repo/pull/103\r",
		"https://gitlab.com/example-org/example-repo/-/merge_requests/104 (draft)",
		"https://-x.example.com/example-org/example-repo/-/merge_requests/8",
		"https://localhost/example-org/example-repo/-/merge_requests/9",
	}, "\n")
	both := MRCreation{Glab: true, Push: true}
	got := MergeRequests(out, both)
	want := []MergeRequest{
		mr("gitlab.com", "example-org/example-repo", 101, false),
		mr("example.com", "example-org/sub/example-repo", 12, true),
	}
	if !slices.Equal(got, want) {
		t.Fatalf("MergeRequests = %+v, want %+v", got, want)
	}

	var many []string
	for i := 1; i <= maxIDsCall+10; i++ {
		many = append(many, mrURL("gitlab.com", "example-org/example-repo", i))
	}
	if n := len(MergeRequests(strings.Join(many, "\n"), both)); n != maxIDsCall {
		t.Fatalf("%d refs, want the cap %d", n, maxIDsCall)
	}
}

// Every address the URL builder makes, the matcher reads back.
func TestMRRefsReadsBackBuiltAddresses(t *testing.T) {
	deep := "a/" + strings.Repeat("b/", 20) + "c"
	for _, n := range []int{1, 1<<31 - 1} {
		u := mrURL("example.com", deep, n)
		if got := MergeRequests(u+"\n", MRCreation{Glab: true}); len(got) != 1 || got[0].Ref.Number != n || got[0].Ref.Repo.Path != deep {
			t.Errorf("MergeRequests(%q) = %v", u, got)
		}
	}
	if got := MergeRequests("https://example.com/a/b/-/merge_requests/2147483648\n", MRCreation{Glab: true}); len(got) != 0 {
		t.Errorf("a number over GitLab's 32 bits: %v", got)
	}
}

// A plain push names a merge request that already exists; the
// `glab mr create` after it fails with 409 and prints no address. The
// session created nothing.
func TestMRRefsPlainPushThenFailedCreate(t *testing.T) {
	cmd := Parse(`git push -u origin feature-a && glab mr create --fill --yes`)
	k := cmd.MRCreation()
	if !k.Glab || k.Push {
		t.Fatalf("kind %+v, want glab only", k)
	}
	out := strings.Join([]string{
		"remote: ",
		"remote: View merge request for feature-a:",
		"remote:   https://example.com/example-org/example-repo/-/merge_requests/12",
		"remote: ",
		"Creating merge request for feature-a into main in example-org/example-repo",
		"",
		"failed to create merge request. Another open merge request already exists for this source branch: !12 (HTTP 409)",
	}, "\n")
	if got := MergeRequests(out, k); len(got) != 0 {
		t.Fatalf("credited with the existing merge request: %v", got)
	}
	// A creating push: its remote line counts, a bare line does not.
	push := Parse(`git push -o merge_request.create origin feature-a`).MRCreation()
	if got := MergeRequests(out, push); len(got) != 1 || got[0].Ref.Number != 12 || !got[0].Pushed {
		t.Fatalf("creating push: %v", got)
	}
	if got := MergeRequests("https://example.com/example-org/example-repo/-/merge_requests/13\n", push); len(got) != 0 {
		t.Fatalf("a bare line after a push: %v", got)
	}
}

// Two pushes in one call: a plain one that names an existing merge request
// and a creating one. Their "remote:" lines are not told apart, so neither
// is credited.
func TestMRCreationSeveralPushes(t *testing.T) {
	for _, s := range []string{
		`git push origin feature-a && git push -o merge_request.create origin feature-b`,
		`git push -o merge_request.create origin feature-b; git push origin feature-a`,
		`git push -o merge_request.create origin a && git push -o merge_request.create origin b`,
	} {
		if Parse(s).MRCreation().Push {
			t.Errorf("a push credited with several pushes in the call: %s", s)
		}
	}
	// glab mr create next to two pushes still counts for its bare line.
	if k := Parse(`git push origin a && git push origin b && glab mr create -f`).MRCreation(); !k.Glab || k.Push {
		t.Errorf("kind %+v, want glab only", k)
	}
}

// glab has no "already exists" answer that carries an address: it does not
// look for an open merge request first, the create goes to GitLab and fails
// with 409 ("Another open merge request already exists for this source
// branch: !12"), which names the merge request by its number, not its
// address (gitlab-org/cli internal/commands/mr/create/mr_create.go, which
// returns the API error; the message is GitLab's, app/models/merge_request.rb).
// So a refused `glab mr create` credits nothing, and a retry that worked after
// it still counts (#116).
func TestMRRefsRefusedGlabCreateCreditsNothing(t *testing.T) {
	k := Parse(`glab mr create --fill --yes; glab mr create --fill --yes --target-branch dev`).MRCreation()
	refused := strings.Join([]string{
		"Creating merge request for feature-a into main in example-org/example-repo",
		"",
		"POST https://example.com/api/v4/projects/example-org%2Fexample-repo/merge_requests: 409 {message: [Another open merge request already exists for this source branch: !12]}",
	}, "\n")
	if got := MergeRequests(refused, k); len(got) != 0 {
		t.Fatalf("a refused create credited: %v", got)
	}
	retried := refused + "\nhttps://example.com/example-org/example-repo/-/merge_requests/13\n"
	if got := MergeRequests(retried, k); len(got) != 1 || got[0].Ref.Number != 13 || got[0].Pushed {
		t.Fatalf("the retry that worked: %v", got)
	}
}

// A github.com address never becomes a GitLab ref, even in GitLab's shape,
// and a GitLab address never becomes a GitHub one: the host decides the
// forge, never the text around it.
func TestMRRefsHostRule(t *testing.T) {
	out := strings.Join([]string{
		"https://github.com/example-org/example-repo/-/merge_requests/1",
		"remote:   https://github.com/example-org/example-repo/-/merge_requests/2",
		"https://github.com/example-org/example-repo/pull/3",
		"https://example.com/example-org/example-repo/-/merge_requests/4",
		"https://github.com:8443/example-org/example-repo/-/merge_requests/8",
		"https://example.com:443/example-org/example-repo/-/merge_requests/9",
		"https://example.com:0/example-org/example-repo/-/merge_requests/10",
		"https://example.com:8443/example-org/example-repo/-/merge_requests/11",
		"https://example.com/gitlab/example-org/example-repo/-/merge_requests/12",
	}, "\n")
	got := MergeRequests(out, MRCreation{Glab: true, Push: true})
	// A web port stays on the host and a subfolder in the path: the core
	// asks glab by the host name and the adapter checks the rest.
	if want := []MergeRequest{
		mr("example.com", "example-org/example-repo", 4, false),
		mr("example.com", "example-org/example-repo", 9, false), // https's own port written out is no port
		mr("example.com:8443", "example-org/example-repo", 11, false),
		mr("example.com", "gitlab/example-org/example-repo", 12, false),
	}; !slices.Equal(got, want) {
		t.Fatalf("MergeRequests = %+v, want %+v", got, want)
	}
	gl := "https://example.com/example-org/example-repo/-/merge_requests/5\nremote:   https://example.com/example-org/example-repo/-/merge_requests/6\nhttps://example.com/example-org/example-repo/pull/7\n"
	if got := PullRequests(gl); len(got) != 0 {
		t.Fatalf("a GitLab address read as a pull request: %v", got)
	}
	for _, r := range MergeRequests(gl, MRCreation{Glab: true, Push: true}) {
		if r.Ref.Repo.Forge != model.ForgeGitLab || !r.Ref.Repo.Valid() {
			t.Errorf("not a valid GitLab ref: %+v", r)
		}
	}
}

func TestAddMergeRequests(t *testing.T) {
	from := time.Date(2026, 11, 22, 8, 0, 0, 0, time.UTC)
	to := from.Add(4 * time.Second)
	glab := mr("example.com", "example-org/example-repo", 1, false)
	pushed := mr("example.com", "example-org/example-repo", 2, true)
	other := mr("example.com", "example-org/other-repo", 2, true)

	created, pushes := AddMergeRequests(nil, nil, []MergeRequest{glab, pushed, other}, from, to, 10)
	if want := []model.PRRef{glab.Ref, pushed.Ref, other.Ref}; !slices.Equal(created, want) {
		t.Fatalf("created = %v, want %v", created, want)
	}
	wantPushes := []model.PushedRef{{Ref: pushed.Ref, From: from, To: to}, {Ref: other.Ref, From: from, To: to}}
	if !slices.Equal(pushes, wantPushes) {
		t.Fatalf("pushed = %v, want %v", pushes, wantPushes)
	}

	// A ref already kept is not added again, and a later window never
	// replaces the first: only the push that named it first can have
	// created it.
	later := from.Add(time.Hour)
	created2, pushes2 := AddMergeRequests(created, pushes, []MergeRequest{pushed, glab}, later, later, 10)
	if !slices.Equal(created2, created) || !slices.Equal(pushes2, pushes) {
		t.Fatalf("a repeat changed the lists: %v %v", created2, pushes2)
	}
	// A merge request a bare line announced earlier is not turned into a
	// push-created one by a later push naming it.
	c3, p3 := AddMergeRequests([]model.PRRef{pushed.Ref}, nil, []MergeRequest{pushed}, from, to, 10)
	if len(c3) != 1 || len(p3) != 0 {
		t.Fatalf("an earlier credit gained a window: %v %v", c3, p3)
	}

	// The cap counts CreatedPRs; a ref over it gets no window either.
	c4, p4 := AddMergeRequests([]model.PRRef{glab.Ref}, nil, []MergeRequest{pushed, other}, from, to, 2)
	if len(c4) != 2 || len(p4) != 1 || p4[0].Ref != pushed.Ref {
		t.Fatalf("cap: %v %v", c4, p4)
	}
}

func TestMRCreationMixed(t *testing.T) {
	for _, c := range []struct {
		cmd   string
		mixed bool
	}{
		{`glab mr create --fill`, false},
		{`cd /home/dev/projects/alpha && glab mr create --fill`, false},
		{`git push -u origin a && glab mr create --fill --yes`, false},
		{`glab mr create -f; glab mr create -f -b dev`, false},
		{`bash -c 'glab mr create --fill'`, false},
		{`glab mr create --title "$(git log -1 --format=%s)" --yes`, false},
		{`glab mr create --fill || glab mr view`, true},
		{`glab mr create --fill && glab mr view 12 --web=false`, true},
		{`glab mr create --fill; cat notes.md`, true},
		{`glab mr create --fill && echo done`, true},
		{`glab mr create --title "$(cat title.txt)" --yes`, true},
		{`bash -c 'glab mr create --fill' || glab mr view`, true},
		{`git push -o merge_request.create origin a`, false}, // not glab at all
	} {
		if got := Parse(c.cmd).MRCreation().Mixed; got != c.mixed {
			t.Errorf("%s: Mixed = %v, want %v", c.cmd, got, c.mixed)
		}
	}
}

// In a call that also runs other commands, only the address glab announced
// ("Creating merge request for ...") is the create's: a refused create
// (GitLab's 409 names the merge request by number) followed by a view credits
// nothing, and a create that worked credits its own address alone.
func TestMRRefsMixedCallsNeedTheAnnouncement(t *testing.T) {
	view := mrURL("example.com", "example-org/example-repo", 12)
	created := mrURL("example.com", "example-org/example-repo", 13)
	announce := "\nCreating merge request for feature-a into main in example-org/example-repo\n\n"
	refused := announce + "failed to create merge request. Another open merge request already exists for this source branch: !12 (HTTP 409)\n"
	for _, c := range []struct {
		name, cmd, out string
		want           []int
	}{
		{"refused, then a view", `glab mr create --fill || glab mr view`, refused + view + "\n", nil},
		{"refused, then a view after ;", `glab mr create --fill; glab mr view 12`, refused + view + "\n", nil},
		{"refused, then a file read", `glab mr create --fill; cat notes.md`, refused + "see\n" + view + "\n", nil},
		{"created, then a view", `glab mr create --fill && glab mr view 12`, announce + created + "\n" + view + "\n", []int{13}},
		{"a view, then a create", `glab mr view 12; glab mr create --fill`, view + "\n" + announce + created + "\n", []int{13}},
		{"refused, retried and worked", `glab mr create --fill; glab mr create --fill -b dev; glab mr view`, refused + announce + created + "\n" + view + "\n", []int{13}},
		{"draft announcement", `glab mr create --draft && cat notes.md`, "Creating draft merge request for a into main in example-org/example-repo\n" + created + "\n", []int{13}},
		// glab without a terminal prints the address alone (spike 11): the
		// first address is the create's when the create ran first and nothing
		// was refused; after another command, or after a refusal, none is.
		{"no announcement, create first", `glab mr create --fill && glab mr view`, created + "\n" + view + "\n", []int{13}},
		{"no announcement, a view first", `glab mr view 12; glab mr create --fill`, view + "\n" + created + "\n", nil},
		{"no announcement, refused, then a view", `glab mr create --fill || glab mr view`, "failed to create merge request. Another open merge request already exists for this source branch: !12 (HTTP 409)\n" + view + "\n", nil},
		{"a pure call needs no announcement", `cd a && glab mr create --fill`, created + "\n", []int{13}},
	} {
		var got []int
		for _, m := range MergeRequests(c.out, Parse(c.cmd).MRCreation()) {
			got = append(got, m.Ref.Number)
		}
		if !slices.Equal(got, c.want) {
			t.Errorf("%s: credited %v, want %v", c.name, got, c.want)
		}
	}
}

// A creating push credits only the address after GitLab's own "View merge
// request for <branch>:" line, not any "remote:" address.
func TestMRRefsPushNeedsGitLabsAnnouncement(t *testing.T) {
	push := Parse(`git push -o merge_request.create origin feature-a`).MRCreation()
	u := func(n int) string { return "remote:   " + mrURL("example.com", "example-org/example-repo", n) }
	for _, c := range []struct {
		name, out string
		want      []int
	}{
		{"announced", "remote: \nremote: View merge request for feature-a:\n" + u(1) + "\nremote: \n", []int{1}},
		{"no announcement", u(2) + "\n", nil},
		{"after another remote line", "remote: View merge request for feature-a:\nremote: Resolving deltas\n" + u(3) + "\n", nil},
		{"the announcement covers one address", "remote: View merge request for feature-a:\n" + u(4) + "\n" + u(5) + "\n", []int{4}},
		{"a creation hint is not the announcement", "remote: To create a merge request for feature-a, visit:\nremote:   https://example.com/example-org/example-repo/-/merge_requests/new?x=1\n" + u(6) + "\n", nil},
		{"a bare announcement line is not remote's", "View merge request for feature-a:\n" + u(7) + "\n", nil},
	} {
		var got []int
		for _, m := range MergeRequests(c.out, push) {
			got = append(got, m.Ref.Number)
		}
		if !slices.Equal(got, c.want) {
			t.Errorf("%s: credited %v, want %v", c.name, got, c.want)
		}
	}
}

// The per-output cap counts accepted refs, after the host, path, mode and
// duplicate checks: a genuine address after many rejected or repeated lines
// is still read.
func TestMRRefsCapAfterTheChecks(t *testing.T) {
	var lines []string
	for range maxIDsCall + 5 {
		lines = append(lines,
			"https://example.com/solo/-/merge_requests/1",                               // a path of one segment
			"https://github.com/example-org/example-repo/-/merge_requests/2",            // github.com
			"remote:   https://example.com/example-org/example-repo/-/merge_requests/3", // not a bare line for glab
		)
	}
	dup := mrURL("example.com", "example-org/example-repo", 4)
	for range maxIDsCall + 5 {
		lines = append(lines, dup)
	}
	lines = append(lines, mrURL("example.com", "example-org/example-repo", 5))
	got := MergeRequests(strings.Join(lines, "\n"), MRCreation{Glab: true})
	if want := []MergeRequest{mr("example.com", "example-org/example-repo", 4, false), mr("example.com", "example-org/example-repo", 5, false)}; !slices.Equal(got, want) {
		t.Fatalf("MergeRequests = %+v", got)
	}
}

// Each forge has its own budget: merge requests never push a GitHub pull
// request out of the list, nor the reverse.
func TestAddMergeRequestsBudgetsPerForge(t *testing.T) {
	var created []model.PRRef
	for i := 1; i <= 3; i++ {
		created = append(created, model.PRRef{Repo: model.GitLabRepo("example.com", "example-org/example-repo"), Number: i})
	}
	gh := model.PRRef{Repo: model.GitHubRepo("example-org/example-repo"), Number: 9}
	if !Room(created, gh, 3) {
		t.Fatal("a full GitLab budget refused a GitHub pull request")
	}
	if Room(created, model.PRRef{Repo: created[0].Repo, Number: 4}, 3) {
		t.Fatal("a full GitLab budget took another merge request")
	}
	created = append(created, gh)
	more := mr("example.com", "example-org/example-repo", 4, false)
	if got, _ := AddMergeRequests(created, nil, []MergeRequest{more}, time.Time{}, time.Time{}, 3); len(got) != 4 {
		t.Fatalf("a GitHub pull request took a GitLab slot: %v", got)
	}
}

// A push candidate with a zero or reversed window is dropped whole (it would
// otherwise count as created, or block a later good window); equal nonzero
// ends are fine (Codex knows only when the command finished). A bare line
// that credits a ref a push named earlier removes its window.
func TestAddMergeRequestsWindowRules(t *testing.T) {
	t0 := time.Date(2026, 11, 22, 8, 0, 0, 0, time.UTC)
	p := mr("example.com", "example-org/example-repo", 2, true)
	for _, c := range []struct {
		name     string
		from, to time.Time
		kept     bool
	}{
		{"zero start", time.Time{}, t0, false},
		{"zero end", t0, time.Time{}, false},
		{"both zero", time.Time{}, time.Time{}, false},
		{"reversed", t0.Add(time.Minute), t0, false},
		{"equal", t0, t0, true},
		{"ordered", t0, t0.Add(time.Second), true},
	} {
		created, pushed := AddMergeRequests(nil, nil, []MergeRequest{p}, c.from, c.to, 10)
		if (len(created) == 1) != c.kept || (len(pushed) == 1) != c.kept {
			t.Errorf("%s: created %v pushed %v, want kept=%v", c.name, created, pushed, c.kept)
		}
	}
	// A bad window does not block a later good one.
	created, pushed := AddMergeRequests(nil, nil, []MergeRequest{p}, time.Time{}, t0, 10)
	created, pushed = AddMergeRequests(created, pushed, []MergeRequest{p}, t0, t0.Add(time.Second), 10)
	if len(created) != 1 || len(pushed) != 1 || !pushed[0].From.Equal(t0) {
		t.Fatalf("later good window: %v %v", created, pushed)
	}
	// A bare line for the same ref ends the need for a window.
	bare := mr("example.com", "example-org/example-repo", 2, false)
	created, pushed = AddMergeRequests(created, pushed, []MergeRequest{bare}, t0, t0, 10)
	if len(created) != 1 || len(pushed) != 0 {
		t.Fatalf("after a bare line: %v %v", created, pushed)
	}
}

// A ref several pushes named keeps the window of the push with the earliest
// start, whatever order the results come in: a later push that merely
// reported the merge request may be read first, and the creating push's window
// must still replace it.
func TestAddMergeRequestsKeepsTheEarliestWindowInAnyOrder(t *testing.T) {
	t0 := time.Date(2026, 11, 22, 8, 0, 0, 0, time.UTC)
	p := mr("example.com", "example-org/example-repo", 2, true)
	early := model.PushedRef{Ref: p.Ref, From: t0, To: t0.Add(30 * time.Second)}
	late := model.PushedRef{Ref: p.Ref, From: t0.Add(5 * time.Minute), To: t0.Add(5*time.Minute + 10*time.Second)}
	for _, c := range []struct {
		name  string
		first [2]time.Time
		then  [2]time.Time
	}{
		{"in order", [2]time.Time{early.From, early.To}, [2]time.Time{late.From, late.To}},
		{"reversed", [2]time.Time{late.From, late.To}, [2]time.Time{early.From, early.To}},
	} {
		created, pushed := AddMergeRequests(nil, nil, []MergeRequest{p}, c.first[0], c.first[1], 10)
		created, pushed = AddMergeRequests(created, pushed, []MergeRequest{p}, c.then[0], c.then[1], 10)
		if len(created) != 1 || !slices.Equal(pushed, []model.PushedRef{early}) {
			t.Errorf("%s: created %v pushed %+v, want the window %+v", c.name, created, pushed, early)
		}
	}
	// A bad window of the earlier push replaces nothing; a ref a bare line
	// credited has no window to replace.
	created, pushed := AddMergeRequests(nil, nil, []MergeRequest{p}, late.From, late.To, 10)
	_, pushed = AddMergeRequests(created, pushed, []MergeRequest{p}, early.To, early.From, 10)
	if !slices.Equal(pushed, []model.PushedRef{late}) {
		t.Errorf("a reversed window replaced the kept one: %+v", pushed)
	}
	bare := mr("example.com", "example-org/example-repo", 2, false)
	created, pushed = AddMergeRequests(nil, nil, []MergeRequest{bare}, t0, t0, 10)
	created, pushed = AddMergeRequests(created, pushed, []MergeRequest{p}, early.From, early.To, 10)
	if len(created) != 1 || len(pushed) != 0 {
		t.Errorf("a push window was added to a created one: %v %+v", created, pushed)
	}
}

// A `glab mr create` that fails, with whatever error, credits nothing from the
// rest of a mixed call: the address that follows (a view of an existing merge
// request) is not the create's. A create that printed no error, or whose
// outcome is unknown and that ran first, still credits the first address.
func TestMRRefsFailedCreateInMixedCallCreditsNothing(t *testing.T) {
	view := mrURL("example.com", "example-org/example-repo", 12)
	created := mrURL("example.com", "example-org/example-repo", 13)
	announce := "Creating merge request for feature-a into main in example-org/example-repo\n\n"
	for _, c := range []struct {
		name, cmd, out string
		want           []int
	}{
		{"validation error", `glab mr create --fill; glab mr view 12`, announce + "POST https://example.com/api/v4/projects/example-org%2Fexample-repo/merge_requests: 422 {message: [Validate branches Source branch is invalid]}\n" + view + "\n", nil},
		{"server error", `glab mr create --fill; glab mr view 12`, announce + "failed to create merge request: 500 Internal Server Error\n" + view + "\n", nil},
		{"error line", `glab mr create --fill; glab mr view 12`, "ERROR: could not find the source branch\n" + view + "\n", nil},
		{"fatal", `glab mr create --fill || glab mr view 12`, "fatal: not a git repository\n" + view + "\n", nil},
		{"not logged in", `glab mr create --fill; glab mr view 12`, "401 Unauthorized\n" + view + "\n", nil},
		{"error with no announcement", `glab mr create --fill; cat notes.md`, "failed to create merge request: 403 Forbidden\nsee\n" + view + "\n", nil},
		{"no error, create first", `glab mr create --fill; glab mr view 12`, created + "\n" + view + "\n", []int{13}},
		{"announced create that worked", `glab mr create --fill; glab mr view 12`, announce + created + "\n" + view + "\n", []int{13}},
	} {
		var got []int
		for _, m := range MergeRequests(c.out, Parse(c.cmd).MRCreation()) {
			got = append(got, m.Ref.Number)
		}
		if !slices.Equal(got, c.want) {
			t.Errorf("%s: credited %v, want %v", c.name, got, c.want)
		}
	}
}
