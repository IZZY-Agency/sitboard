package model

import "testing"

func TestKnownZeroValueIsUnknown(t *testing.T) {
	if (Known[string]{}).OK {
		t.Fatal("zero Known[string] must be unknown")
	}
	if (Known[Status]{}).OK || (Known[bool]{}).OK || (Known[SessionKey]{}).OK {
		t.Fatal("zero Known of any type must be unknown")
	}
}

func TestOfAndUnknown(t *testing.T) {
	k := Of(StatusBusy)
	if !k.OK || k.V != StatusBusy {
		t.Fatalf("Of(busy) = %+v", k)
	}
	if u := Unknown[string](); u.OK {
		t.Fatal("Unknown() reports a value")
	}
	if v, ok := Of("x").Get(); !ok || v != "x" {
		t.Fatalf("Get = %q, %v", v, ok)
	}
}

func TestFactsImplementFact(t *testing.T) {
	facts := []Fact{
		SessionFact{},
		SessionGone{},
		TerminalFact{},
		TerminalGone{},
		TerminalSelected{},
		SourceReady{},
	}
	if len(facts) != 6 {
		t.Fatal("unexpected fact count")
	}
}

func TestPullRequestAndIssueURLs(t *testing.T) {
	if got := PullRequestURL("example-org/example-repo", 12); got != "https://github.com/example-org/example-repo/pull/12" {
		t.Fatalf("PullRequestURL = %q", got)
	}
	if got := IssueURL("example-org/example.repo", 7); got != "https://github.com/example-org/example.repo/issues/7" {
		t.Fatalf("IssueURL = %q", got)
	}
	if got := PullRequestURL("Example_Org/.dotfiles", 2147483647); got != "https://github.com/Example_Org/.dotfiles/pull/2147483647" {
		t.Fatalf("PullRequestURL at the largest number = %q", got)
	}
	for _, c := range []struct {
		repo string
		n    int
	}{
		{"example-org/example-repo", 0},
		{"example-org/example-repo", -3},
		{"example-org/example-repo", 2147483648},
		{"", 1},
		{"example-org", 1},
		{"example-org/", 1},
		{"/example-repo", 1},
		{"example-org/..", 1},
		{"example-org/.", 1},
		{"../example-repo", 1},
		{"example/org/example-repo", 1},
		{"example-org/example-repo/pull/9", 1},
		{"-example/example-repo", 1},
		{"example.org/example-repo", 1},
		{"example-org/example repo", 1},
		{"example-org/example-repo?x=1", 1},
		{"example-org/example-repo#frag", 1},
		{"example-org/example-repo\n", 1},
		{"example-org/exämple", 1},
		{"@evil.example/x", 1},
	} {
		if got := PullRequestURL(c.repo, c.n); got != "" {
			t.Errorf("PullRequestURL(%q, %d) = %q, want none", c.repo, c.n, got)
		}
		if got := IssueURL(c.repo, c.n); got != "" {
			t.Errorf("IssueURL(%q, %d) = %q, want none", c.repo, c.n, got)
		}
	}
}

func TestPullRequestURLPatternRefusesOtherForms(t *testing.T) {
	for _, u := range []string{
		"https://github.com/example-org/example-repo/pull/12",
		"https://github.com/example-org/example-repo/issues/12",
	} {
		if !pullRequestURLRe.MatchString(u) && !issueURLRe.MatchString(u) {
			t.Fatalf("%q matches neither pattern", u)
		}
	}
	for _, u := range []string{
		"http://github.com/example-org/example-repo/pull/12",
		"https://github.example.com/example-org/example-repo/pull/12",
		"https://github.com.example/example-org/example-repo/pull/12",
		"https://ghe.example/example-org/example-repo/pull/12",
		"https://github.com/example-org/example-repo/pull/012",
		"https://github.com/example-org/example-repo/pull/12/files",
		"https://github.com/example-org/example-repo/pull/12\n",
		"https://github.com/example-org/../pull/12",
		"https://github.com/example-org/example-repo/issues/12",
		"javascript:alert(1)//https://github.com/example-org/example-repo/pull/12",
		"//github.com/example-org/example-repo/pull/12",
	} {
		if pullRequestURLRe.MatchString(u) {
			t.Errorf("PullRequestURLPattern matches %q", u)
		}
	}
	if issueURLRe.MatchString("https://github.com/example-org/example-repo/pull/12") {
		t.Error("IssueURLPattern matches a pull request")
	}
}
