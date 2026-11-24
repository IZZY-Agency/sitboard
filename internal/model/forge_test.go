package model

import (
	"strconv"
	"strings"
	"testing"
)

func TestForgeRepoKeysKeepGitHubsStrings(t *testing.T) {
	gh := GitHubRepo("example-org/example-repo")
	if gh.Forge != ForgeGitHub || gh.Host != GitHubHost || gh.Path != "example-org/example-repo" {
		t.Fatalf("GitHubRepo = %+v", gh)
	}
	// The catch-up's saved read marks are hashes of these strings: GitHub's
	// must stay byte for byte what they were before forges.
	if got := gh.Key(); got != "example-org/example-repo" {
		t.Errorf("GitHub Key = %q", got)
	}
	if got := (PRRef{Repo: gh, Number: 12}).Key(); got != "example-org/example-repo#12" {
		t.Errorf("GitHub PRRef.Key = %q", got)
	}
	gl := GitLabRepo("gitlab.example.com", "example-group/sub/example-project")
	if got := gl.Key(); got != "gitlab:gitlab.example.com/example-group/sub/example-project" {
		t.Errorf("GitLab Key = %q", got)
	}
	if got := (PRRef{Repo: gl, Number: 12}).Key(); got != "gitlab:gitlab.example.com/example-group/sub/example-project!12" {
		t.Errorf("GitLab PRRef.Key = %q", got)
	}
	if (ForgeRepo{}).Key() != "" || !(ForgeRepo{}).IsZero() || gh.IsZero() {
		t.Error("the zero ForgeRepo has no key and is zero; a set one is not")
	}
	// The same path on two forges or two hosts never shares a key.
	other := GitLabRepo("gitlab.com", "example-org/example-repo")
	if GitLabRepo("gitlab.example.com:8443", "example-org/example-repo").Key() == GitLabRepo("gitlab.example.com", "example-org/example-repo").Key() {
		t.Error("a web port does not make a key of its own")
	}
	if other.Key() == gh.Key() || other.Key() == GitLabRepo("gitlab.example.com", "example-org/example-repo").Key() {
		t.Error("keys collide across forges or hosts")
	}
}

func TestForgeRepoValid(t *testing.T) {
	for _, r := range []ForgeRepo{
		GitHubRepo("example-org/example-repo"),
		GitHubRepo("example-org/.dotfiles"),
		GitLabRepo("gitlab.com", "example-group/example-project"),
		GitLabRepo("gitlab.example.com", "example-group/a/b/example-project"),
		GitLabRepo("gitlab.example.com:8443", "example-group/example-project"),
		GitLabRepo("gitlab.example.com:1", "example-group/example-project"),
		GitLabRepo("gitlab.example.com:65535", "example-group/example-project"),
	} {
		if !r.Valid() {
			t.Errorf("%+v not valid", r)
		}
	}
	for _, r := range []ForgeRepo{
		{},
		GitHubRepo(""),
		GitHubRepo("example-org"),
		GitHubRepo("example-org/.."),
		GitHubRepo("../example-repo"),
		GitHubRepo("example/org/example-repo"),
		GitHubRepo("example-org/example repo"),
		{Forge: ForgeGitHub, Host: "github.example.com", Path: "example-org/example-repo"},
		{Forge: ForgeGitHub, Path: "example-org/example-repo"},
		GitLabRepo("gitlab.com", "example-project"),
		GitLabRepo("gitlab.com", "example-group/../example-project"),
		GitLabRepo("gitlab.com", "example-group/example-project.git"),
		GitLabRepo("GitLab.com", "example-group/example-project"),
		GitLabRepo("gitlab.com:443", "example-group/example-project"),
		GitLabRepo("gitlab.com:0", "example-group/example-project"),
		GitLabRepo("gitlab.com:08443", "example-group/example-project"),
		GitLabRepo("gitlab.com:65536", "example-group/example-project"),
		GitLabRepo("gitlab.com:", "example-group/example-project"),
		GitLabRepo("gitlab.com:8443:1", "example-group/example-project"),
		GitLabRepo("gitlab.com/gitlab", "example-group/example-project"),
		GitLabRepo(GitHubHost+":8443", "example-org/example-repo"),
		GitLabRepo("localhost", "example-group/example-project"),
		GitLabRepo(GitHubHost, "example-org/example-repo"),
		{Forge: "bitbucket", Host: "bitbucket.org", Path: "example-org/example-repo"},
	} {
		if r.Valid() {
			t.Errorf("%+v valid", r)
		}
	}
}

func TestChangeRequestAndIssueURLsPerForge(t *testing.T) {
	gh := GitHubRepo("example-org/example-repo")
	gl := GitLabRepo("gitlab.example.com", "example-group/sub/example-project")
	for _, c := range []struct{ got, want string }{
		{ChangeRequestURL(gh, 12), "https://github.com/example-org/example-repo/pull/12"},
		{ForgeIssueURL(gh, 7), "https://github.com/example-org/example-repo/issues/7"},
		{ChangeRequestURL(gl, 12), "https://gitlab.example.com/example-group/sub/example-project/-/merge_requests/12"},
		{ForgeIssueURL(gl, 7), "https://gitlab.example.com/example-group/sub/example-project/-/issues/7"},
		{ChangeRequestURL(gl, 2147483647), "https://gitlab.example.com/example-group/sub/example-project/-/merge_requests/2147483647"},
		{ChangeRequestURL(gl, 0), ""},
		{ChangeRequestURL(gl, 2147483648), ""},
		{ForgeIssueURL(gl, -1), ""},
		{ChangeRequestURL(ForgeRepo{}, 1), ""},
		{ChangeRequestURL(GitLabRepo("gitlab.com", "example-project"), 1), ""},
		{ChangeRequestURL(GitLabRepo(GitHubHost, "example-org/example-repo"), 1), ""},
		{ChangeRequestURL(ForgeRepo{Forge: ForgeGitHub, Host: "gitlab.com", Path: "example-org/example-repo"}, 1), ""},
	} {
		if c.got != c.want {
			t.Errorf("got %q, want %q", c.got, c.want)
		}
	}
}

func TestGitLabURLPatterns(t *testing.T) {
	mr := gitlabMergeRequestURLRe
	is := gitlabIssueURLRe
	if !mr.MatchString("https://gitlab.com/example-group/example-project/-/merge_requests/1") {
		t.Fatal("merge request pattern refuses a plain address")
	}
	if !is.MatchString("https://gitlab.com/example-group/example-project/-/issues/1") {
		t.Fatal("issue pattern refuses a plain address")
	}
	if !mr.MatchString("https://gitlab.example.com:8443/example-group/example-project/-/merge_requests/1") ||
		!is.MatchString("https://gitlab.example.com:8443/example-group/example-project/-/issues/1") {
		t.Fatal("a pattern refuses a web port")
	}
	for _, u := range []string{
		"http://gitlab.com/example-group/example-project/-/merge_requests/1",
		"https://gitlab.com/example-project/-/merge_requests/1",
		"https://gitlab.com/example-group/example-project/merge_requests/1",
		"https://gitlab.com/example-group/example-project/-/merge_requests/01",
		"https://gitlab.com/example-group/example-project/-/merge_requests/1/diffs",
		"https://gitlab.com:/example-group/example-project/-/merge_requests/1",
		"https://gitlab.com:08443/example-group/example-project/-/merge_requests/1",
		"https://gitlab.com:123456/example-group/example-project/-/merge_requests/1",
		"https://gitlab.com:8443:1/example-group/example-project/-/merge_requests/1",
		"https://gitlab.com/example-group/example-project/-/issues/1",
	} {
		if mr.MatchString(u) {
			t.Errorf("merge request pattern matches %q", u)
		}
	}
}

// A GitHub name is bounded as GitHub bounds it (an owner of 39 characters, a
// repository of 100): anything longer is no repository to ask about or show.
func TestGitHubRepoValidBoundsItsParts(t *testing.T) {
	for _, tc := range []struct {
		owner, name int
		want        bool
	}{{39, 100, true}, {1, 1, true}, {40, 10, false}, {10, 101, false}, {400, 400, false}} {
		r := GitHubRepo(strings.Repeat("o", tc.owner) + "/" + strings.Repeat("n", tc.name))
		if got := r.Valid(); got != tc.want {
			t.Errorf("owner %d, name %d: Valid = %v, want %v", tc.owner, tc.name, got, tc.want)
		}
	}
}

// The link patterns take exactly the ports ValidWebPort takes, every one
// from 0 to 99999 tried, with and without a leading zero.
func TestLinkPortIsValidWebPort(t *testing.T) {
	for n := range 100000 {
		for _, p := range []string{strconv.Itoa(n), "0" + strconv.Itoa(n)} {
			u := "https://gitlab.example.com:" + p + "/example-group/example-project/-/merge_requests/1"
			if got, want := gitlabMergeRequestURLRe.MatchString(u), ValidWebPort(p); got != want {
				t.Fatalf("port %q: pattern %v, ValidWebPort %v", p, got, want)
			}
		}
	}
}

// ValidWebPort takes digits only: strconv would also read "+8443" and, with
// its error ignored, take "8_443" or "1e3" for 0.
func TestValidWebPort(t *testing.T) {
	for _, p := range []string{"8443", "1", "65535", "80", "444"} {
		if !ValidWebPort(p) {
			t.Errorf("%q refused", p)
		}
	}
	for _, p := range []string{"", "0", "443", "65536", "08443", "+8443", "-1", "8_443", "1e3", "0x50", "８４４３", " 8443", "8443 ", "123456", "99999999999999999999"} {
		if ValidWebPort(p) {
			t.Errorf("%q accepted", p)
		}
	}
}

// A site is a GitLab host name, with a valid web port or none.
func TestValidGitLabSite(t *testing.T) {
	for s, want := range map[string]bool{
		"gitlab.example.com":      true,
		"gitlab.example.com:8443": true,
		"gitlab.example.com:443":  false,
		"gitlab.example.com:0":    false,
		"gitlab.example.com:":     false,
		"gitlab.example.com:x":    false,
		"localhost:8443":          false, // no dot
		"gitlab.example.com/x":    false,
	} {
		if got := ValidGitLabSite(s); got != want {
			t.Errorf("ValidGitLabSite(%q) = %v, want %v", s, got, want)
		}
	}
}

// A project path has 2 to 22 segments. Under a subfolder the whole web path is
// the one checked, so the subfolder's levels count toward the 22.
func TestValidGitLabPathDepth(t *testing.T) {
	path := func(n int) string {
		segs := make([]string, n)
		for i := range segs {
			segs[i] = "g" + strconv.Itoa(i)
		}
		return strings.Join(segs, "/")
	}
	for n, want := range map[int]bool{1: false, 2: true, 22: true, 23: false} {
		if got := ValidGitLabPath(path(n)); got != want {
			t.Errorf("%d segments: %v, want %v", n, got, want)
		}
	}
	if !ValidGitLabPath("gitlab/"+path(21)) || ValidGitLabPath("gitlab/"+path(22)) {
		t.Error("a one-level subfolder leaves a project 21 segments")
	}
}
