package shellcmd

import (
	"fmt"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
	"unsafe"

	"github.com/IZZY-Agency/sitboard/internal/cpucost"
	"github.com/IZZY-Agency/sitboard/internal/model"
)

func ghRepo(path string) model.ForgeRepo { return model.GitHubRepo(path) }

func glRepo(host, path string) model.ForgeRepo { return model.GitLabRepo(host, path) }

// checkIssues compares what each command gives with the wanted issues. A
// wanted issue without a Forge gets the tool the command starts with (glab:
// GitLab, else GitHub), so the tables stay short; TestIssueCommandsForge
// states the forge outright.
func checkIssues(t *testing.T, cases map[string][]IssueCommand) {
	t.Helper()
	for cmd, want := range cases {
		forge := model.ForgeGitHub
		if strings.HasPrefix(cmd, "glab") {
			forge = model.ForgeGitLab
		}
		want = slices.Clone(want)
		for i := range want {
			if want[i].Forge == "" {
				want[i].Forge = forge
			}
		}
		got := Parse(cmd).IssueCommands()
		if !slices.Equal(got, want) {
			t.Errorf("IssueCommands(%q) = %v, want %v", cmd, got, want)
		}
	}
}

// Every verb the note names, with the issue as a number, #N or an address.
func TestIssueCommandsVerbs(t *testing.T) {
	cases := map[string][]IssueCommand{}
	for _, v := range []string{"view", "comment", "edit", "develop", "close", "reopen"} {
		cases["gh issue "+v+" 12"] = []IssueCommand{{Number: 12}}
		cases["gh issue "+v+` "#13"`] = []IssueCommand{{Number: 13}}
		cases["gh issue "+v+" https://github.com/example-org/tool/issues/14"] = []IssueCommand{{Repo: ghRepo("example-org/tool"), Number: 14}}
	}
	for _, v := range []string{"view", "note", "update", "close", "reopen"} {
		cases["glab issue "+v+" 22"] = []IssueCommand{{Number: 22}}
		cases["glab issue "+v+" https://gitlab.example.com/group/sub/proj/-/issues/23"] = []IssueCommand{{Repo: glRepo("gitlab.example.com", "group/sub/proj"), Number: 23}}
	}
	checkIssues(t, cases)
}

// Other subcommands, other verbs of the other tool, and a command that is an
// argument or text are not touches.
func TestIssueCommandsNotTouches(t *testing.T) {
	checkIssues(t, map[string][]IssueCommand{
		`gh issue list`:                       nil,
		`gh issue create --title x`:           nil,
		`gh issue status`:                     nil,
		`gh issue pin 5`:                      nil,
		`gh issue note 5`:                     nil, // glab's verb
		`glab issue comment 5`:                nil, // gh's verb
		`glab issue edit 5`:                   nil,
		`glab issue develop 5`:                nil,
		`gh pr view 5`:                        nil,
		`gh issue view`:                       nil,
		`gh issue view --web`:                 nil,
		`echo gh issue view 5`:                nil,
		`grep "gh issue view 5" notes.txt`:    nil,
		`# gh issue view 5`:                   nil,
		`gh issue view 5 #comment`:            {{Number: 5}},
		`gh issue view #5`:                    nil, // the shell reads #5 as a comment: no issue
		`gh -R example-org/tool issue view 5`: nil, // a global flag in front is not matched
		`gh issue view abc`:                   nil,
		`gh issue view 5x`:                    nil,
		`gh issue view 0`:                     nil,
		`gh issue view -3`:                    nil,
		`gh issue view 2147483648`:            nil,
		`gh issue view 99999999999`:           nil,
		`gh issue view 2147483647`:            {{Number: 2147483647}},
		`gh issue view 1`:                     {{Number: 1}},
	})
}

// A flag's value is never the issue; the issue is the first plain word, in
// front of or behind the flags.
func TestIssueCommandsFlags(t *testing.T) {
	checkIssues(t, map[string][]IssueCommand{
		`gh issue comment 7 --body 99`:                                       {{Number: 7}},
		`gh issue comment --body 99 7`:                                       {{Number: 7}},
		`gh issue comment -b 99 7`:                                           {{Number: 7}},
		`gh issue comment 7 -b "see 99"`:                                     {{Number: 7}},
		`gh issue edit 7 --add-label 99`:                                     {{Number: 7}},
		`gh issue edit --title 99 7`:                                         {{Number: 7}},
		`gh issue close 7 -c 99`:                                             {{Number: 7}},
		`gh issue close -r completed 7`:                                      {{Number: 7}},
		`gh issue view -c 7`:                                                 {{Number: 7}}, // --comments takes no value
		`gh issue view 7 --json title,body --jq .title`:                      {{Number: 7}},
		`gh issue develop -c 7`:                                              {{Number: 7}}, // --checkout takes no value
		`gh issue develop 7 --name fix-it --base main`:                       {{Number: 7}},
		`gh issue view --comments 7`:                                         {{Number: 7}},
		`gh issue view -- 7`:                                                 {{Number: 7}},
		`gh issue view 7 8`:                                                  nil, // two issues: unknown which
		`glab issue note 7 -m 99`:                                            {{Number: 7}},
		`glab issue note -m 99 7`:                                            {{Number: 7}},
		`glab issue update 7 -t 99 -l 98`:                                    {{Number: 7}},
		`glab issue update -p 7`:                                             {{Number: 7}}, // --public takes no value
		`glab issue update -c 7`:                                             {{Number: 7}}, // --confidential
		`glab issue view -c 7`:                                               {{Number: 7}},
		`glab issue view 7 -P 99`:                                            {{Number: 7}},
		`glab issue view -p 2 7`:                                             {{Number: 7}}, // --page takes a value
		`gh issue view 7 --repo=example-org/tool`:                            {{Repo: ghRepo("example-org/tool"), Number: 7}},
		`gh issue view 7 --repo example-org/tool`:                            {{Repo: ghRepo("example-org/tool"), Number: 7}},
		`gh issue view 7 -R example-org/tool`:                                {{Repo: ghRepo("example-org/tool"), Number: 7}},
		`gh issue view -R example-org/tool 7`:                                {{Repo: ghRepo("example-org/tool"), Number: 7}},
		`gh issue view 7 -Rexample-org/tool`:                                 {{Repo: ghRepo("example-org/tool"), Number: 7}},
		`gh issue view 7 -R github.com/example-org/tool`:                     {{Repo: ghRepo("example-org/tool"), Number: 7}},
		`gh issue view 7 -R https://github.com/example-org/tool`:             {{Repo: ghRepo("example-org/tool"), Number: 7}},
		`glab issue view 7 -R https://gitlab.example.com/group/proj`:         nil, // glab -R gives no touch, in any form
		`glab issue view 7 --repo https://gitlab.example.com/group/sub/proj`: nil,
	})
}

// -R names a repository this cannot place: the call gives nothing, never the
// folder's repository.
func TestIssueCommandsRepoNotPlaced(t *testing.T) {
	checkIssues(t, map[string][]IssueCommand{
		`gh issue view 7 -R ghe.example.com/example-org/tool`: nil, // another host
		`gh issue view 7 -R example-org`:                      nil,
		`gh issue view 7 -R ../x`:                             nil,
		`gh issue view 7 -R`:                                  nil,
		`gh issue view 7 -R ""`:                               nil,
		`glab issue view 7 -R gitlab.example.com/group/proj`:  nil, // no scheme: "a.b/c/d" may be a dotted group
		`glab issue view 7 -R group/proj`:                     nil, // glab's default host is not known
		`glab issue view 7 -R group/sub/proj`:                 nil, // no host
		`glab issue view 7 -R proj`:                           nil,
		`glab issue view 7 -R my.group/proj`:                  nil,
		`glab issue view 7 -R github.com/example-org/tool`:    nil,
		// The address and -R must agree.
		`gh issue view https://github.com/example-org/tool/issues/7 -R example-org/other`: nil,
		`gh issue view https://github.com/example-org/tool/issues/7 -R example-org/tool`:  {{Repo: ghRepo("example-org/tool"), Number: 7}},
	})
}

// An address is read only in the shapes the page's allowlist patterns accept
// (model.IssueURLPattern, model.GitLabIssueURLPattern).
func TestIssueCommandsAddressShapes(t *testing.T) {
	checkIssues(t, map[string][]IssueCommand{
		`gh issue view https://github.com/example-org/tool/issues/7#issuecomment-123`: {{Repo: ghRepo("example-org/tool"), Number: 7}},
		`gh issue view 'https://github.com/example-org/tool/issues/7?x=1'`:            {{Repo: ghRepo("example-org/tool"), Number: 7}},
		`gh issue view https://github.com/example-org/tool/issues/7?x=1`:              nil, // unquoted ?: a glob
		`gh issue view http://github.com/example-org/tool/issues/7`:                   nil, // not https
		`gh issue view https://github.com/example-org/tool/pull/7`:                    nil,
		`gh issue view https://github.com/example-org/tool/issues/7/`:                 nil,
		`gh issue view https://github.com/example-org/tool/issues/0`:                  nil,
		`gh issue view https://github.com/example-org/tool/issues/07`:                 nil,
		`gh issue view https://github.com/example-org/tool/issues/2147483648`:         nil,
		`gh issue view https://github.com/example-org/tool/issues/99999999999`:        nil,
		`gh issue view https://github.com/../issues/7`:                                nil,
		`gh issue view https://github.com/example-org/../issues/7`:                    nil,
		`gh issue view https://github.com/example-org/issues/7`:                       nil,
		`gh issue view https://github.com/a/b/c/issues/7`:                             nil,
		`gh issue view https://github.example.com/example-org/tool/issues/7`:          nil, // another host
		`gh issue view https://evil.example/github.com/example-org/tool/issues/7`:     nil,
		`gh issue view https://gitlab.example.com/group/proj/-/issues/7`:              nil, // a GitLab address is not gh's
		`glab issue view https://github.com/example-org/tool/issues/7`:                nil,
		`glab issue view https://gitlab.example.com/group/proj/-/issues/7`:            {{Repo: glRepo("gitlab.example.com", "group/proj"), Number: 7}},
		`glab issue view https://gitlab.example.com:443/group/proj/-/issues/7`:        nil, // the host as written must be a valid one
		`glab issue view https://gitlab.example.com:8443/group/proj/-/issues/7`:       {{Repo: glRepo("gitlab.example.com:8443", "group/proj"), Number: 7}},
		`glab issue view https://gitlab.example.com/group/proj/-/issues/7#note_1`:     {{Repo: glRepo("gitlab.example.com", "group/proj"), Number: 7}},
		`glab issue view https://gitlab.example.com/group/proj/-/merge_requests/7`:    nil,
		`glab issue view https://gitlab.example.com/group/proj/issues/7`:              nil, // the old shape
		`glab issue view https://gitlab.example.com/group/-/issues/7`:                 nil, // a project path has two segments or more
		`glab issue view https://gitlab.example.com/group/proj/-/issues/0`:            nil,
		`glab issue view https://gitlab.example.com/group/proj/-/issues/2147483648`:   nil,
		`glab issue view https://gitlab.example.com/group/../-/issues/7`:              nil,
		`glab issue view https://localhost/group/proj/-/issues/7`:                     nil, // a host has a dot
		`glab issue view https://GitLab.Example.com/group/proj/-/issues/7`:            nil, // hosts are lowercase
		`glab issue view https://gitlab.example.com:0/group/proj/-/issues/7`:          nil,
	})
}

// Where the issue's repository would be the folder's, a call that could have
// run elsewhere gives nothing; a repository named by -R or the address is
// exact whatever else the call does.
func TestIssueCommandsOtherFolder(t *testing.T) {
	checkIssues(t, map[string][]IssueCommand{
		`cd /somewhere && gh issue view 7`:                                          nil,
		`cd /somewhere; gh issue view 7`:                                            nil,
		`pushd /somewhere && gh issue view 7`:                                       nil,
		`popd && gh issue view 7`:                                                   nil,
		`(cd /somewhere && gh issue view 7)`:                                        nil,
		`bash -c 'cd /somewhere && gh issue view 7'`:                                nil,
		`gh issue view 7 && cd /somewhere`:                                          nil, // the order across scripts is not known: any cd counts
		`env -C /somewhere gh issue view 7`:                                         nil,
		`env --chdir=/somewhere gh issue view 7`:                                    nil,
		`env --chdir /somewhere gh issue view 7`:                                    nil,
		`sudo -D /somewhere gh issue view 7`:                                        nil,
		`GH_REPO=example-org/other gh issue view 7`:                                 nil,
		`export GH_REPO=example-org/other; gh issue view 7`:                         nil,
		`GH_HOST=ghe.example.com gh issue view 7`:                                   nil,
		`GITLAB_HOST=gitlab.example.com glab issue view 7`:                          nil,
		`cd /somewhere; gh issue view 7 -R example-org/tool`:                        {{Repo: ghRepo("example-org/tool"), Number: 7}},
		`cd /somewhere; gh issue view https://github.com/example-org/tool/issues/7`: {{Repo: ghRepo("example-org/tool"), Number: 7}},
		`echo cd /somewhere; gh issue view 7`:                                       {{Number: 7}}, // an argument, not a cd
		`git -C /somewhere status; gh issue view 7`:                                 {{Number: 7}}, // git's -C is not gh's folder
		`env FOO=1 gh issue view 7`:                                                 nil,           // env may change the folder or the variables
		`time gh issue view 7`:                                                      {{Number: 7}},
		`ls | gh issue view 7`:                                                      {{Number: 7}},
		`echo "GH_REPO=x" && gh issue view 7`:                                       nil, // quotes are gone: it reads as an assignment, so an unknown instead of a guess
	})
}

// Several commands in one call: each issue once, in the order gh then glab.
func TestIssueCommandsSeveral(t *testing.T) {
	checkIssues(t, map[string][]IssueCommand{
		`gh issue view 1; gh issue view 1; gh issue comment 2 -b x`:          {{Number: 1}, {Number: 2}},
		`gh issue view 1; glab issue view 2`:                                 {{Forge: model.ForgeGitHub, Number: 1}, {Forge: model.ForgeGitLab, Number: 2}},
		`for n in 1 2; do gh issue view $n; done`:                            nil, // a variable is not a number
		`gh issue view $(echo 5)`:                                            nil,
		`gh issue view "$N"`:                                                 nil,
		`bash -c 'gh issue view 4'; gh issue view 3`:                         {{Number: 3}, {Number: 4}},
		`gh issue view 1 -R example-org/a; gh issue view 1 -R example-org/b`: {{Repo: ghRepo("example-org/a"), Number: 1}, {Repo: ghRepo("example-org/b"), Number: 1}},
	})
}

// A command with a great many issue commands gives at most maxIDsCall.
func TestIssueCommandsCapped(t *testing.T) {
	var b strings.Builder
	for i := 1; i <= 3*maxIDsCall; i++ {
		fmt.Fprintf(&b, "gh issue view %d; ", i)
	}
	if got := Parse(b.String()).IssueCommands(); len(got) != maxIDsCall {
		t.Errorf("%d issues from %d commands, want %d", len(got), 3*maxIDsCall, maxIDsCall)
	}
}

func TestAddIssueTouches(t *testing.T) {
	at := time.Date(2026, 11, 27, 10, 0, 0, 0, time.UTC)
	repo := ghRepo("example-org/tool")
	cmds := []IssueCommand{{Number: 1}, {Repo: repo, Number: 2}}

	// A command with no repository takes the call's folder; one with a
	// repository does not.
	got := AddIssueTouches(nil, cmds, "/work/a", at, 10)
	want := []model.IssueTouch{{Folder: "/work/a", Number: 1, At: at}, {Repo: repo, Number: 2, At: at}}
	if !slices.Equal(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}

	// Without a folder, the one that needs it is dropped.
	got = AddIssueTouches(nil, cmds, "", at, 10)
	if want := []model.IssueTouch{{Repo: repo, Number: 2, At: at}}; !slices.Equal(got, want) {
		t.Fatalf("no folder: got %v, want %v", got, want)
	}

	// The same issue in another folder is another touch; the same one again
	// keeps its latest time, never an earlier one.
	got = AddIssueTouches(got, cmds[:1], "/work/a", at.Add(time.Minute), 10)
	got = AddIssueTouches(got, cmds[:1], "/work/b", at, 10)
	got = AddIssueTouches(got, cmds[:1], "/work/a", at.Add(-time.Hour), 10)
	want = []model.IssueTouch{{Repo: repo, Number: 2, At: at}, {Folder: "/work/a", Number: 1, At: at.Add(time.Minute)}, {Folder: "/work/b", Number: 1, At: at}}
	if !slices.Equal(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

// At the cap, a new issue replaces the least recently touched one; an older
// one than all kept is not added.
func TestAddIssueTouchesCap(t *testing.T) {
	at := time.Date(2026, 11, 27, 10, 0, 0, 0, time.UTC)
	var list []model.IssueTouch
	for n := 1; n <= 3; n++ {
		list = AddIssueTouches(list, []IssueCommand{{Number: n}}, "/w", at.Add(time.Duration(n)*time.Minute), 3)
	}
	list = AddIssueTouches(list, []IssueCommand{{Number: 4}}, "/w", at.Add(10*time.Minute), 3)
	if len(list) != 3 || slices.ContainsFunc(list, func(t model.IssueTouch) bool { return t.Number == 1 }) || !slices.ContainsFunc(list, func(t model.IssueTouch) bool { return t.Number == 4 }) {
		t.Fatalf("the oldest was not replaced: %v", list)
	}
	list = AddIssueTouches(list, []IssueCommand{{Number: 5}}, "/w", at, 3) // older than every one kept
	if len(list) != 3 || slices.ContainsFunc(list, func(t model.IssueTouch) bool { return t.Number == 5 }) {
		t.Fatalf("an older touch displaced a newer one: %v", list)
	}
	// A subagent's list merges the same way.
	list = AddIssueTouchList(list, []model.IssueTouch{{Folder: "/w", Number: 6, At: at.Add(time.Hour)}}, 3)
	if len(list) != 3 || !slices.ContainsFunc(list, func(t model.IssueTouch) bool { return t.Number == 6 }) {
		t.Fatalf("the merged touch is missing: %v", list)
	}
}

// numbers read are what the command wrote.
func TestIssueCommandsNumberText(t *testing.T) {
	for _, n := range []int{1, 7, 1000, 2147483647} {
		got := Parse("gh issue view " + strconv.Itoa(n)).IssueCommands()
		if len(got) != 1 || got[0].Number != n {
			t.Errorf("%d: %v", n, got)
		}
	}
}

// The option tables give each verb's arity: a flag's value is never the
// issue, whether it is a separate word, attached, or inside a short cluster.
func TestIssueCommandsArity(t *testing.T) {
	checkIssues(t, map[string][]IssueCommand{
		`gh issue edit --parent 123 456`:          {{Number: 456}}, // --parent takes a value
		`gh issue edit --parent=123 456`:          {{Number: 456}},
		`gh issue edit 456 --add-sub-issue 12`:    {{Number: 456}},
		`gh issue edit 456 --type bug`:            {{Number: 456}},
		`glab issue update -w 2 456`:              {{Number: 456}}, // -w is --weight
		`glab issue update 456 -u bug`:            {{Number: 456}},
		`glab issue update -cp 456`:               {{Number: 456}}, // two booleans in one cluster
		`gh issue view -cRexample-org/tool 7`:     {{Repo: ghRepo("example-org/tool"), Number: 7}},
		`gh issue view -c -R example-org/tool 7`:  {{Repo: ghRepo("example-org/tool"), Number: 7}},
		`gh issue view -R=example-org/tool 7`:     {{Repo: ghRepo("example-org/tool"), Number: 7}},
		`gh issue view -cR example-org/tool 7`:    {{Repo: ghRepo("example-org/tool"), Number: 7}},
		`gh issue view --comments=true 7`:         {{Number: 7}},
		`gh issue comment -eb 5 7`:                {{Number: 7}}, // -e is a boolean, -b takes the 5
		`gh issue comment -b5 7`:                  {{Number: 7}}, // attached value
		`glab issue note -m5 7`:                   {{Number: 7}},
		`gh issue close 7 --reason "not planned"`: {{Number: 7}},
		`gh issue develop 7 -b main -n fix`:       {{Number: 7}},
	})
}

// An option the table lacks, or one that asks for help, makes the call
// unknown: nothing is guessed from it.
func TestIssueCommandsUnknownOptions(t *testing.T) {
	checkIssues(t, map[string][]IssueCommand{
		`gh issue view 7 --nope x`:        nil,
		`gh issue view -z 7`:              nil,
		`gh issue view -cz 7`:             nil,
		`gh issue view 7 --nope=x`:        nil,
		`gh issue view 7 --help`:          nil,
		`gh issue view --help 7`:          nil,
		`gh issue view -h 7`:              nil,
		`glab issue view -h 7`:            nil,
		`glab issue view --help 7`:        nil,
		`gh issue edit 7 --title`:         nil, // a value flag with no value
		`gh issue comment 7 -b`:           nil,
		`gh issue view 7 --jq`:            nil,
		`glab issue close 7 --anything x`: nil,
		`gh issue view`:                   nil,
		`gh issue view 7 8`:               nil,
		`gh issue view -- 7 8`:            nil,
	})
}

// A line that runs env, sudo or the like gives nothing at all: where its
// inner command runs, and with what, is not known, also when the command is
// a shell script under it.
func TestIssueCommandsWrappers(t *testing.T) {
	checkIssues(t, map[string][]IssueCommand{
		`env -C /other bash -c 'gh issue view 7'`:                      nil,
		`env -C /other -S 'gh issue view 7'`:                           nil,
		`env --chdir=/other sh -c "gh issue view 7 -R a-b/c-d"`:        nil,
		`sudo -D /other bash -c 'gh issue view 7'`:                     nil,
		`sudo gh issue view 7`:                                         nil,
		`sudo -u other gh issue view 7 -R example-org/tool`:            nil,
		`doas gh issue view 7`:                                         nil,
		`env gh issue view 7`:                                          nil,
		`env FOO=1 gh issue view 7 -R example-org/tool`:                nil,
		`/usr/bin/env gh issue view https://github.com/a-b/c/issues/7`: nil,
		`echo ok; env -i gh issue view 7`:                              nil,
		`nohup gh issue view 7`:                                        {{Number: 7}}, // changes neither folder nor variables
		`timeout 5 gh issue view 7`:                                    {{Number: 7}},
	})
}

// A GH_, GLAB_, GITLAB_, GL_ or GITHUB_ assignment anywhere in the line may
// change the host, the repository or the account the tool talks to, also when
// the repository is named: nothing is read.
func TestIssueCommandsEnvironment(t *testing.T) {
	checkIssues(t, map[string][]IssueCommand{
		`GH_HOST=ghe.example.com gh issue view 7 -R example-org/tool`:                              nil,
		`GH_HOST=ghe.example.com gh issue view https://github.com/a-b/c/issues/7`:                  nil,
		`export GH_HOST=ghe.example.com; gh issue view 7 -R example-org/tool`:                      nil,
		`gh issue view 7 -R example-org/tool; export GH_HOST=ghe.example.com`:                      nil, // any order
		`GH_ENTERPRISE_TOKEN=x gh issue view 7`:                                                    nil,
		`GH_CONFIG_DIR=/x gh issue view 7`:                                                         nil,
		`GLAB_CONFIG_DIR=/x glab issue view 7`:                                                     nil,
		`GITLAB_HOST=gitlab.example.com glab issue view https://gitlab.example.com/g/p/-/issues/7`: nil,
		`GITHUB_TOKEN=x gh issue view 7`:                                                           nil,
		`GL_TOKEN=x glab issue view 7`:                                                             nil,
		`bash -c 'GH_HOST=ghe.example.com gh issue view 7 -R example-org/tool'`:                    nil,
		`FOO=1 gh issue view 7`:                                                                    nil, // any assignment: the line is not read
		`gh issue view 7 -R example-org/tool`:                                                      {{Repo: ghRepo("example-org/tool"), Number: 7}},
	})
}

// A glab -R or --repo gives no touch, in any form (an accepted limit): the
// project cannot be placed without glab's host and mount. An address as the
// argument still does, and its mount is part of the project.
func TestIssueCommandsGitLabRepoFlag(t *testing.T) {
	checkIssues(t, map[string][]IssueCommand{
		`glab issue view https://gitlab.example.com/gitlab/group/project/-/issues/7`:                  {{Repo: glRepo("gitlab.example.com", "gitlab/group/project"), Number: 7}},
		`glab issue view 7 -R https://gitlab.example.com/gitlab/group/project`:                        nil,
		`glab issue view 7 -R group/project`:                                                          nil,
		`glab issue view 7 -Rgroup/project`:                                                           nil,
		`glab issue view 7 -R=group/project`:                                                          nil,
		`glab issue view 7 --repo group/project`:                                                      nil,
		`glab issue view 7 --repo=https://gitlab.example.com/group/project`:                           nil,
		`glab issue view https://gitlab.example.com/g/p/-/issues/7 -R https://gitlab.example.com/g/p`: nil,
	})
}

// A command's tool is part of the touch, also when the repository is left to
// the folder: gh and glab on the same number are two issues.
func TestIssueCommandsForge(t *testing.T) {
	got := Parse(`gh issue view 7; glab issue view 7; gh issue comment 7 -b x`).IssueCommands()
	want := []IssueCommand{{Forge: model.ForgeGitHub, Number: 7}, {Forge: model.ForgeGitLab, Number: 7}}
	if !slices.Equal(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	at := time.Date(2026, 11, 27, 10, 0, 0, 0, time.UTC)
	touches := AddIssueTouches(nil, got, "/work/a", at, 10)
	if len(touches) != 2 || touches[0].Forge != model.ForgeGitHub || touches[1].Forge != model.ForgeGitLab {
		t.Fatalf("the touches do not keep the tool: %v", touches)
	}
	// And another spelling of the same tool's issue is still one touch.
	touches = AddIssueTouches(touches, want[:1], "/work/a", at.Add(time.Minute), 10)
	if len(touches) != 2 {
		t.Fatalf("one issue twice: %v", touches)
	}
}

// GitHub names are one repository whatever their case (the core keys them
// lowercased): an address and -R that differ only in case agree, and two
// spellings take one place. GitLab paths are case-sensitive.
func TestIssueCommandsGitHubCase(t *testing.T) {
	got := Parse(`gh issue view https://github.com/Example-Org/Tool/issues/7 -R example-org/tool`).IssueCommands()
	if len(got) != 1 || got[0].Number != 7 || !strings.EqualFold(got[0].Repo.Path, "example-org/tool") {
		t.Fatalf("an address and -R differing in case disagree: %v", got)
	}
	got = Parse(`gh issue view 7 -R Example-Org/Tool; gh issue view 7 -R example-org/tool; gh issue view https://github.com/EXAMPLE-ORG/TOOL/issues/7`).IssueCommands()
	if len(got) != 1 {
		t.Fatalf("three spellings of one issue: %v", got)
	}
	at := time.Date(2026, 11, 27, 10, 0, 0, 0, time.UTC)
	list := AddIssueTouches(nil, []IssueCommand{{Forge: model.ForgeGitHub, Repo: ghRepo("Example-Org/Tool"), Number: 7}}, "/w", at, 10)
	list = AddIssueTouches(list, []IssueCommand{{Forge: model.ForgeGitHub, Repo: ghRepo("example-org/tool"), Number: 7}}, "/w", at.Add(time.Minute), 10)
	if len(list) != 1 || !list[0].At.Equal(at.Add(time.Minute)) {
		t.Fatalf("two spellings took two places: %v", list)
	}
	list = AddIssueTouches(nil, []IssueCommand{{Forge: model.ForgeGitLab, Repo: glRepo("gitlab.example.com", "Group/Proj"), Number: 7}}, "/w", at, 10)
	list = AddIssueTouches(list, []IssueCommand{{Forge: model.ForgeGitLab, Repo: glRepo("gitlab.example.com", "group/proj"), Number: 7}}, "/w", at, 10)
	if len(list) != 2 {
		t.Fatalf("GitLab paths of another case are one: %v", list)
	}
}

// inWord says whether s's bytes lie inside w's.
func inWord(s, w string) bool {
	if s == "" || w == "" {
		return false
	}
	a, b := uintptr(unsafe.Pointer(unsafe.StringData(s))), uintptr(unsafe.Pointer(unsafe.StringData(w)))
	return a >= b && a < b+uintptr(len(w))
}

// What a touch keeps is a copy: a substring of the command's word would keep
// the whole word (an address with its query) in memory for as long as the
// touch lives.
func TestIssueCommandsOwnTheirStrings(t *testing.T) {
	for _, in := range []string{
		`gh issue view 'https://github.com/example-org/tool/issues/7?private=CANARY'`,
		`gh issue view 7 -R example-org/tool`,
		`glab issue view 'https://gitlab.example.com/group/proj/-/issues/7#CANARY'`,
	} {
		c := Parse(in)
		got := c.IssueCommands()
		if len(got) != 1 || got[0].Repo.IsZero() {
			t.Fatalf("%q: %v", in, got)
		}
		for _, w := range c.words {
			if inWord(got[0].Repo.Path, w) || inWord(got[0].Repo.Host, w) {
				t.Errorf("%q: the repository shares memory with the word %q", in, w)
			}
		}
		list := AddIssueTouches(nil, got, "", time.Now(), 10)
		for _, w := range c.words {
			if len(list) != 1 || inWord(list[0].Repo.Path, w) || inWord(list[0].Repo.Host, w) {
				t.Errorf("%q: the touch shares memory with the word %q", in, w)
			}
		}
	}
	folder := "/work/" + strings.Repeat("a", 8) + "/CANARY"
	list := AddIssueTouches(nil, []IssueCommand{{Forge: model.ForgeGitHub, Number: 1}}, folder, time.Now(), 10)
	if len(list) != 1 || inWord(list[0].Folder, folder) {
		t.Errorf("the touch's folder shares memory with the call's cwd: %v", list)
	}
}

// One line of a transcript may hold the same command thousands of times (up
// to the word cap): reading it costs a linear amount, not a scan of the whole
// line for every command. 8,000 of the longest unit stay under that cap; a
// line over it is not read at all and would measure nothing.
func TestIssueCommandsOneLongLine(t *testing.T) {
	for _, unit := range []string{"gh issue view 7;", "gh issue view 7 -R example-org/tool; ", "glab issue view 7\n", "gh issue view 7 && "} {
		read := func(n int) func() {
			line := strings.Repeat(unit, n)
			return func() {
				if got := Parse(line).IssueCommands(); len(got) != 1 {
					t.Fatalf("%q x %d: read %v, want one issue", unit, n, got)
				}
			}
		}
		cpucost.Linear(t, 3, fmt.Sprintf("%q x 2,000, then x 8,000", unit), read(2000), read(8000))
	}
}

// Round 2: the reader refuses the whole line where it cannot tell what the
// shell runs. Each input is the reviewer's (#176), with a plain twin that is
// still read, so a refusal cannot be a reader that gives nothing.
func TestIssueCommandsRefuseWhatTheShellDecides(t *testing.T) {
	bigScript := strings.Repeat("x", maxShellScript+1)
	deep := strings.Repeat("$(", maxDepth+1) + "x" + strings.Repeat(")", maxDepth+1)
	checkIssues(t, map[string][]IssueCommand{
		// A cap of the lexer cut the line: the -R behind the cut is lost.
		"true " + strings.Repeat("x ", 65530) + "; gh issue view 7 -R github.com/example-org/tool":          nil,
		"true " + strings.Repeat("x ", 100) + "; gh issue view 7 -R github.com/example-org/tool":            {{Repo: ghRepo("example-org/tool"), Number: 7}},
		strings.Repeat("bash -c true; ", maxShellScripts+1) + "gh issue view 7":                             nil,
		strings.Repeat("bash -c true; ", maxShellScripts) + "gh issue view 7":                               {{Number: 7}},
		"bash -c '" + bigScript + "'; gh issue view 7":                                                      nil,
		"gh issue view 7; echo " + deep:                                                                     nil,
		"gh issue view 7; echo " + strings.Repeat("$(", maxDepth-1) + "x" + strings.Repeat(")", maxDepth-1): {{Number: 7}},
		// $'..' is not decoded here: Bash passes 7, the lexer 067.
		`gh issue view $'\067'`: nil,
		`gh issue view $"7"`:    nil,
		// A variable that can hold arguments, before or in the invocation.
		`QUERY='.number -R github.com/example-org/other'; gh issue view -q $QUERY 7 --json number`: nil,
		`gh issue view "$N"`:              nil,
		`gh issue view ${N}`:              nil,
		`gh issue view $(echo 7)`:         nil,
		"gh issue view `echo 7`":          nil,
		`echo $HOME; gh issue view 7`:     nil, // anything before it
		`echo "a $HOME"; gh issue view 7`: nil,
		`gh issue view 7; echo $HOME`:     {{Number: 7}}, // after it: the invocation is known
		`echo '$HOME'; gh issue view 7`:   {{Number: 7}},
		`echo "\$HOME"; gh issue view 7`:  {{Number: 7}},
		// Globs and braces.
		`gh issue view 7 -R example-org/t*`:                   nil,
		`gh issue view {7,8}`:                                 nil,
		`echo x{a,b}; gh issue view 7`:                        nil,
		`gh issue comment 7 -b ?`:                             nil,
		`ls [ab]c; gh issue view 7`:                           nil,
		`gh issue comment 7 -b '*'`:                           {{Number: 7}},
		`gh issue comment 7 -b \*`:                            {{Number: 7}},
		`[ -f x ] && gh issue view 7`:                         nil, // any unquoted [ may be a glob: no look-ahead
		`gh issue view 7; [ -f x ]`:                           {{Number: 7}},
		`gh issue view 7 && gh issue view 8; gh issue view 9`: {{Number: 7}, {Number: 9}},
		// A || list: what follows may not run; the whole line is refused.
		`gh issue view 7 || gh issue view 8`:           nil,
		`gh issue view 7 || true`:                      nil,
		`true || gh issue view 7`:                      nil,
		`gh issue view 7; echo $(true || false)`:       nil,
		`bash -c 'gh issue view 7 || gh issue view 8'`: nil,
		`gh issue comment 7 -b '||'`:                   {{Number: 7}},
		`gh issue view 7 | gh issue view 8`:            {{Number: 7}, {Number: 8}},
		// gh repo set-default changes the repository the next gh command reads.
		`gh repo set-default upstream/tool && gh issue view 7`: nil,
		`gh issue view 7; gh repo set-default upstream/tool`:   nil,
		`gh repo view; gh issue view 7`:                        {{Number: 7}},
		`{ gh issue view 7; }`:                                 {{Number: 7}},
		`gh issue view 7 | jq .number`:                         {{Number: 7}},
		`gh issue view 7 2>&1 | head -3`:                       {{Number: 7}},
		`if true; then gh issue view 7; fi`:                    {{Number: 7}},
		`bash -c 'gh issue view 7'`:                            {{Number: 7}},
		// A shell that only checks syntax runs nothing.
		`bash -n -c 'gh issue view 7'`:        nil,
		`bash -nc 'gh issue view 7'`:          nil,
		`sh -ecn 'gh issue view 7'`:           nil,
		`bash -o noexec -c 'gh issue view 7'`: nil,
		`set -n; gh issue view 7`:             nil,
		`set -o noexec; gh issue view 7`:      nil,
		`set -e; gh issue view 7`:             {{Number: 7}},
		`bash -e -c 'gh issue view 7'`:        {{Number: 7}},
		// Variables set another way than a prefix, or by the line before.
		`printf -v GH_REPO %s github.com/example-org/other; export GH_REPO; gh issue view 7`: nil,
		`printf -v GH_REPO %s github.com/example-org/other; gh issue view 7`:                 nil,
		`printf '%s\n' x; gh issue view 7`:                                                   {{Number: 7}},
		`export GH_REPO=github.com/example-org/other; gh issue view 7`:                       nil,
		`declare -x GH_REPO=x; gh issue view 7`:                                              nil,
		`read GH_REPO; gh issue view 7`:                                                      nil,
		`source ./env.sh; gh issue view 7`:                                                   nil,
		`. ./env.sh; gh issue view 7`:                                                        nil,
		`eval 'GH_REPO=x'; gh issue view 7`:                                                  nil,
		`unset GH_REPO; gh issue view 7`:                                                     nil,
		`mapfile -t a < f; gh issue view 7`:                                                  nil,
		`for GH_REPO in x/y; do gh issue view 7; done`:                                       nil,
		`gh issue view 7; GH_REPO=x/y true`:                                                  nil, // any order
		// A GIT_* variable moves the repository gh finds from the folder.
		`GIT_DIR=/repo-b/.git gh issue view 7`:         nil,
		`GIT_WORK_TREE=/repo-b gh issue view 7`:        nil,
		`export GIT_DIR=/repo-b/.git; gh issue view 7`: nil,
		`bash -c 'GIT_DIR=/b/.git gh issue view 7'`:    nil,
		`gh issue view 7 -R example-org/tool`:          {{Repo: ghRepo("example-org/tool"), Number: 7}},
	})
}

// The refusals leave the other readers alone: what a cap cuts, a substitution,
// a glob or a syntax check does for them is what it was.
func TestRefusalsLeaveTheOtherReadersAlone(t *testing.T) {
	line := `agtermctl session new --name "w-$X" --name plain; bash -n -c 'agtermctl session new --name inner'`
	c := Parse(line)
	if got := c.SpawnNames(); !slices.Equal(got, []string{"plain", "inner"}) {
		t.Fatalf("SpawnNames = %v", got)
	}
	if len(c.Runs("gh", "pr", "create")) != 0 || len(Parse("gh pr create --title x; echo $(date)").Runs("gh", "pr", "create")) != 1 {
		t.Fatalf("Runs changed")
	}
}

// What follows && runs only if what precedes it succeeded, so an invocation
// after one is not a touch; the first command of a line, one after ; or a
// newline, and one that && only precedes are.
func TestIssueCommandsGuardedByAnd(t *testing.T) {
	checkIssues(t, map[string][]IssueCommand{
		`git diff --quiet && gh issue view 7`: nil,
		`git diff --quiet &&
gh issue view 7`: nil,
		`git diff --quiet && git status | gh issue view 7`:   nil,
		`git diff --quiet && echo x; gh issue view 7`:        {{Number: 7}},
		"git diff --quiet && echo x\ngh issue view 7":        {{Number: 7}},
		`git diff --quiet && echo x & gh issue view 7`:       {{Number: 7}},
		`gh issue view 7 && git diff --quiet`:                {{Number: 7}},
		`gh issue view 7 && gh issue view 8`:                 {{Number: 7}},
		`git diff --quiet; gh issue view 7`:                  {{Number: 7}},
		`true && true && gh issue view 7`:                    nil,
		`true && true; gh issue view 7`:                      {{Number: 7}},
		`git diff --quiet && gh issue comment 7 -b 'x && y'`: nil,
		`echo 'a && b'; gh issue view 7`:                     {{Number: 7}},
		// A group opened while guarded is not followed: the rest stays guarded.
		`git diff --quiet && (echo x; gh issue view 7)`:    nil,
		`git diff --quiet && { echo x; gh issue view 7; }`: nil,
		`(git diff --quiet && echo x); gh issue view 7`:    nil,
		`(gh issue view 7; git diff --quiet && echo x)`:    {{Number: 7}},
		`(echo x) && gh issue view 7`:                      nil,
		`{ gh issue view 7; } && echo x`:                   {{Number: 7}},
		// A script runs where its wrapper does: guarded by an && of the call.
		`git diff --quiet && bash -c 'gh issue view 7'`:       nil,
		`bash -c 'gh issue view 7'; git diff --quiet && true`: nil,
		`bash -c 'gh issue view 7'`:                           {{Number: 7}},
		`bash -c 'true && gh issue view 7'`:                   nil,
		`bash -c 'true; gh issue view 7'`:                     {{Number: 7}},
		`bash -c 'gh issue view 7 && true'; echo x`:           {{Number: 7}},
	})
}

// A backslash-newline may join the halves of an operator (| and | make ||),
// which are not modelled: a line with an unquoted one gives no touch.
func TestIssueCommandsRefuseJoinedLines(t *testing.T) {
	checkIssues(t, map[string][]IssueCommand{
		"gh issue view 7 |\\\n| gh issue view 8":   nil,
		"gh issue view 7 \\\n -R example-org/tool": nil,
		"gh issue view 7 \\\n; echo x":             nil,
		"gh issue view 7; echo a \\\nb":            nil,
		"gh issue view 7; echo $(echo a \\\nb)":    nil,
		"gh issue view 7; echo `echo a \\\nb`":     nil,
		"bash -c 'gh issue view 7 \\\n'":           nil,
		`gh issue view 7; echo "a \` + "\n" + `b"`: {{Number: 7}}, // quoted: it joins no operator
		`gh issue view 7; echo 'a \` + "\n" + `b'`: {{Number: 7}},
		"gh issue view 7\ngh issue view 8":         {{Number: 7}, {Number: 8}},
	})
}
