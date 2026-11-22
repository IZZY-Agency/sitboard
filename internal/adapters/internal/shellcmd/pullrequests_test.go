package shellcmd

import (
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/IZZY-Agency/sitboard/internal/model"
)

// A pull request counts when its URL stands alone on a line, as gh pr create
// prints it; each once; at most maxIDsCall per output. The address of an
// "already exists" answer is not one the create made (#116).
func TestPullRequests(t *testing.T) {
	out := "Creating pull request for worker-a into main\n\n" +
		"https://github.com/example-org/example-repo/pull/301\n" +
		"  https://github.com/example-org/example-repo/pull/301  \n" +
		"a pull request for branch \"worker-a\" into branch \"main\" already exists:\nhttps://github.com/example-org/example-repo/pull/302\r\n" +
		"see https://github.com/example-org/example-repo/pull/303 for details\n" +
		"https://github.com/example-org/example-repo/pull/0\n" +
		"https://example.com/example-org/example-repo/pull/304\n"
	want := []model.PRRef{{Repo: model.GitHubRepo("example-org/example-repo"), Number: 301}}
	if got := PullRequests(out); !slices.Equal(got, want) {
		t.Errorf("PullRequests = %v, want %v", got, want)
	}
	var many strings.Builder
	for i := 1; i <= 50; i++ {
		fmt.Fprintf(&many, "https://github.com/example-org/example-repo/pull/%d\n", i)
	}
	if got := PullRequests(many.String()); len(got) != maxIDsCall {
		t.Errorf("%d pull requests from one output, want %d", len(got), maxIDsCall)
	}
}

// gh's refusal when the branch already has an open pull request is an error
// message of its own, "a pull request for branch %q into branch %q already
// exists:" and the address on the next line (cli/cli pkg/cmd/pr/create/create.go,
// createRun: fmt.Errorf("a pull request for branch %q into branch %q already
// exists:\n%s", ...)). That pull request is not the session's: the create
// made nothing. A later create in the same output still counts, and an
// address that cannot be told from the answer's is not counted.
func TestPullRequestsAlreadyExists(t *testing.T) {
	ref := func(n int) model.PRRef {
		return model.PRRef{Repo: model.GitHubRepo("example-org/example-repo"), Number: n}
	}
	exists := "a pull request for branch \"worker-a\" into branch \"main\" already exists:\n"
	for _, c := range []struct {
		name, out string
		want      []model.PRRef
	}{
		{"the answer alone", exists + prA + "\n", nil},
		{"indented address", exists + "  " + prA + "  \n", nil},
		{"a wrapper's prefix", "Error: " + exists + prA + "\n", nil},
		// gh has had one wording since 0.9.0 (command/pr_create.go) to 2.105.0
		// (pkg/cmd/pr/create/create.go). Other text ending the same way is no
		// refusal: the address is then counted.
		{"a shorter form is not gh's", "a pull request already exists:\n" + prA + "\n", []model.PRRef{ref(301)}},
		{"unrelated text with the same ending", "This behavior already exists:\n" + prA + "\n", []model.PRRef{ref(301)}},
		{"another branch wording", "a pull request for branch already exists:\n" + prA + "\n", []model.PRRef{ref(301)}},
		{"carriage returns", "a pull request for branch \"a\" into branch \"main\" already exists:\r\n" + prA + "\r\n", nil},
		{"blank line before the address", exists + "\n" + prA + "\n", nil},
		{"a retry created it, announced", exists + prA + "\n\nCreating pull request for a into main in example-org/example-repo\n\n" + prB + "\n", []model.PRRef{ref(302)}},
		{"a retry created it, no terminal", exists + prA + "\n" + prB + "\n", []model.PRRef{ref(302)}},
		{"created, then the next create was refused", "Creating pull request for a into main\n\n" + prA + "\n" + exists + prB + "\n", []model.PRRef{ref(301)}},
		{"the same address refused and then created", exists + prA + "\nCreating pull request for a into main\n\n" + prA + "\n", []model.PRRef{ref(301)}},
		{"two refusals", exists + prA + "\n" + exists + prB + "\n", nil},
		{"the words in a longer line are no answer", "note: a pull request for branch \"a\" into branch \"main\" already exists here\n" + prA + "\n", []model.PRRef{ref(301)}},
	} {
		if got := PullRequests(c.out); !slices.Equal(got, c.want) {
			t.Errorf("%s: %v, want %v", c.name, got, c.want)
		}
	}
}

const (
	prA = "https://github.com/example-org/example-repo/pull/301"
	prB = "https://github.com/example-org/example-repo/pull/302"
)

// When one call also runs another gh command, the output holds the addresses
// of both. gh announces the create's address only on a terminal; without one
// it prints just the address, so then the create's output is told by its
// place: first, when the create is the call's first gh command.
func TestOwnPullRequests(t *testing.T) {
	ref := func(n int) model.PRRef {
		return model.PRRef{Repo: model.GitHubRepo("example-org/example-repo"), Number: n}
	}
	for _, c := range []struct {
		name, out   string
		createFirst bool
		want        []model.PRRef
	}{
		{"announced, then another address", "\nCreating pull request for a into main in example-org/example-repo\n\n" + prA + "\n" + prB + "\n", true, []model.PRRef{ref(301)}},
		{"another address first, then the announced one", prB + "\n\nCreating pull request for a into main in example-org/example-repo\n\n" + prA + "\n", false, []model.PRRef{ref(301)}},
		{"draft", "Creating draft pull request for a into main\n\n" + prA + "\n" + prB + "\n", false, []model.PRRef{ref(301)}},
		// The refused create's address is not the session's, and what follows
		// it is another command's, or a create's that cannot be told from it.
		{"already exists, another address first", prB + "\na pull request for branch \"a\" into branch \"main\" already exists:\n" + prA + "\n", false, nil},
		{"already exists, create first", "a pull request for branch \"a\" into branch \"main\" already exists:\n" + prA + "\n" + prB + "\n", true, nil},
		{"already exists, then a create announced", "a pull request for branch \"a\" into branch \"main\" already exists:\n" + prA + "\nCreating pull request for b into main\n\n" + prB + "\n", true, []model.PRRef{ref(302)}},
		{"already exists, then a bare address: not known", "a pull request for branch \"a\" into branch \"main\" already exists:\n" + prA + "\n" + prB + "\n", false, nil},
		{"already exists, then a line that is no address, then one: not known", "a pull request for branch \"a\" into branch \"main\" already exists:\nwarning: something\n" + prA + "\n", true, nil},
		{"unrelated text ending in already exists: does not switch the fallback off", prA + "\nThis behavior already exists:\nbody\n" + prB + "\n", true, []model.PRRef{ref(301)}},
		{"announced, then already exists", "Creating pull request for a into main\n\n" + prA + "\na pull request for branch \"b\" into branch \"main\" already exists:\n" + prB + "\n", false, []model.PRRef{ref(301)}},
		{"no terminal, create first: its address is first", prA + "\n" + prB + "\n", true, []model.PRRef{ref(301)}},
		{"no terminal, create after another gh command: nothing is known", prB + "\n" + prA + "\n", false, nil},
		{"no terminal, create first, no address at all", "error: something\n", true, nil},
		{"a line between the announcement and the address, create later", "Creating pull request for a into main\nWarning: something\n" + prB + "\n", false, nil},
		{"two creates", "Creating pull request for a into main\n\n" + prA + "\nCreating pull request for b into main\n\n" + prB + "\n", false, []model.PRRef{ref(301), ref(302)}},
	} {
		if got := OwnPullRequests(c.out, c.createFirst); !slices.Equal(got, c.want) {
			t.Errorf("%s: %v, want %v", c.name, got, c.want)
		}
	}
}

func TestCreateIsFirstGH(t *testing.T) {
	for cmd, want := range map[string]bool{
		"gh pr create --fill":                                  true,
		"git push && gh pr create --fill && gh pr checks":      true,
		"gh pr create --fill && gh pr view --json url -q .url": true,
		"gh pr view 5; gh pr create -f":                        false,
		"bash -c 'gh pr list; gh pr create -f'":                false,
		"gh pr view 5":                                         false,
		"echo hi":                                              false,
	} {
		if got := Parse(cmd).CreateIsFirstGH(); got != want {
			t.Errorf("%q: %v, want %v", cmd, got, want)
		}
	}
}

func TestCreatesAmongOtherGH(t *testing.T) {
	for cmd, want := range map[string]bool{
		"gh pr create --fill":                                  false,
		"git push && gh pr create --fill":                      false,
		"gh pr create --fill && gh pr view --json url -q .url": true,
		"gh pr list; gh pr create -f":                          true,
		"gh pr create -f; gh pr create -f -B dev":              false,
		"bash -c 'gh pr create -f; gh pr view 5 --json url'":   true,
		"bash -c 'gh pr create -f' && gh pr view 5 --json url": false, // the script's words come last: order not known
		"gh pr create -f && sh -c 'gh pr view 5 --json url'":   false,
		"gh pr create -f && echo $(gh pr view 5 --json url)":   true,
		"gh pr view 5": false, // no create at all
		"gh pr create -f && echo https://example.com/pull/1": false,
	} {
		if got := Parse(cmd).CreatesAmongOtherGH(); got != want {
			t.Errorf("%q: %v, want %v", cmd, got, want)
		}
	}
}

// The cap on addresses applies to what counts, not to what is read: 40
// addresses of another command before the create's announced one must not
// push it out, and the first address stays the fallback.
func TestOwnPullRequestsCapAfterFiltering(t *testing.T) {
	var others strings.Builder
	for i := 1; i <= 40; i++ {
		fmt.Fprintf(&others, "https://github.com/example-org/example-repo/pull/%d\n", 1000+i)
	}
	want := []model.PRRef{{Repo: model.GitHubRepo("example-org/example-repo"), Number: 301}}
	if got := OwnPullRequests(others.String()+"Creating pull request for a into main\n\n"+prA+"\n", false); !slices.Equal(got, want) {
		t.Errorf("announced create after 40 addresses: %v, want %v", got, want)
	}
	exists := "a pull request for branch \"a\" into branch \"main\" already exists:\n"
	if got := OwnPullRequests(strings.Repeat(exists+prB+"\n", 40)+"Creating pull request for a into main\n\n"+prA+"\n", false); !slices.Equal(got, want) {
		t.Errorf("announced create after 40 refusals: %v, want %v", got, want)
	}
	first := model.PRRef{Repo: model.GitHubRepo("example-org/example-repo"), Number: 1001}
	if got := OwnPullRequests(others.String(), true); !slices.Equal(got, []model.PRRef{first}) {
		t.Errorf("fallback: %v, want the first address", got)
	}
	if got := OwnPullRequests(others.String(), false); got != nil {
		t.Errorf("no announcement, create not first: %v", got)
	}
	// Not announced and not a refusal: at most maxIDsCall, as before.
	if got := PullRequests(others.String()); len(got) != maxIDsCall {
		t.Errorf("%d pull requests from one output, want %d", len(got), maxIDsCall)
	}
}

// A create that shares the call with other gh commands whose place against it
// is not known (one in a bash -c script, the other outside): the output may
// hold their addresses too. Without a refusal every address stays, as ever;
// with one, only what a "Creating pull request for ..." line announced is the
// create's, since the view that follows a refused create prints its address.
func TestUnorderedPullRequests(t *testing.T) {
	ref := func(n int) model.PRRef {
		return model.PRRef{Repo: model.GitHubRepo("example-org/example-repo"), Number: n}
	}
	exists := "a pull request for branch \"a\" into branch \"main\" already exists:\n"
	for _, c := range []struct {
		name, out string
		want      []model.PRRef
	}{
		{"no refusal: every address", prA + "\n" + prB + "\n", []model.PRRef{ref(301), ref(302)}},
		{"the refusal and the view of the same pull request", exists + prA + "\n" + prA + "\n", nil},
		{"the refusal and the view of another", exists + prA + "\n" + prB + "\n", nil},
		{"the refusal, then an announced create", exists + prA + "\nCreating pull request for b into main\n\n" + prB + "\n", []model.PRRef{ref(302)}},
	} {
		if got := UnorderedPullRequests(c.out); !slices.Equal(got, c.want) {
			t.Errorf("%s: %v, want %v", c.name, got, c.want)
		}
	}
}

func TestRunsOtherGH(t *testing.T) {
	for cmd, want := range map[string]bool{
		"gh pr create --fill":                                            false,
		"gh pr create -f; gh pr create -f -B dev":                        false,
		"git push && gh pr create --fill":                                false,
		"gh pr create --fill && gh pr view --json url -q .url":           true,
		"bash -c 'gh pr create --fill' || gh pr view --json url -q .url": true,
		"gh pr view 5; bash -c 'gh pr create -f'":                        true,
		"gh pr create -f && sh -c 'gh pr view 5 --json url'":             true,
		"gh pr view 5": false, // no create at all
		"gh pr create -f && echo https://example.com/pull/1": false,
	} {
		if got := Parse(cmd).RunsOtherGH(); got != want {
			t.Errorf("%q: %v, want %v", cmd, got, want)
		}
	}
}
