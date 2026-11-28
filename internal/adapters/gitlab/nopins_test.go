package gitlab

import (
	"context"
	"slices"
	"testing"

	"github.com/IZZY-Agency/sitboard/internal/model"
)

// mainQuery2 is the query main's buildIssuesQuery(2) builds (b58dea52):
// pinned issues add nothing to it on GitLab (#182).
const mainQuery2 = "query($p0:ID!,$p1:ID!){currentUser{webUrl} p0:project(fullPath:$p0){...I} p1:project(fullPath:$p1){...I} } " +
	"fragment I on Project{webUrl issuesEnabled issues(state:opened,sort:UPDATED_DESC,first:30){count nodes{iid title updatedAt milestone{title dueDate} labels(first:5){nodes{title}}}}}"

// TestPinsDoNotChangeTheGitLabQuery: the owner's pins travel nowhere on
// GitLab. The call is byte for byte what main sends, with pins or without,
// and no answer comes back by number.
func TestPinsDoNotChangeTheGitLabQuery(t *testing.T) {
	other := "example-org/other-repo"
	repos := []model.ForgeRepo{glRepo(selfHosted, project), glRepo(selfHosted, other)}
	ask := func(pinned map[model.ForgeRepo][]int) ([]string, []model.RepoIssues) {
		p, f, _ := newAdapter(t, Options{})
		f.graphql = ok(issuesAnswer(issuesProject(selfHosted+"/"+project, 1, issueNode(1, "Sample issue")), issuesProject(selfHosted+"/"+other, 0)))
		got, err := (&Source{p: p}).OpenIssues(context.Background(), repos, pinned)
		if err != nil || f.count("graphql") != 1 {
			t.Fatalf("one call: %+v, %v, %d calls", got, err, f.count("graphql"))
		}
		return f.calls[len(f.calls)-1], got
	}
	plain, _ := ask(nil)
	pinned, got := ask(map[model.ForgeRepo][]int{repos[0]: {5, 77, 900}, repos[1]: {3}})
	if q := argValue(pinned, "query"); q != mainQuery2 {
		t.Fatalf("the query with pins is not main's:\n got %q\nwant %q", q, mainQuery2)
	}
	if !slices.Equal(plain, pinned) {
		t.Fatalf("pins changed the call:\n%q\n%q", plain, pinned)
	}
	for _, ri := range got {
		if len(ri.Pinned) != 0 {
			t.Fatalf("a by-number answer on GitLab: %+v", ri)
		}
	}
}
