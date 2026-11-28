package shellcmd

import (
	"math"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/IZZY-Agency/sitboard/internal/model"
)

// This file finds the issues a session's own `gh issue …` and `glab issue …`
// commands name (the "looked at" evidence of #171). Only the issue is read,
// from the command line: its output is not needed, so no call is kept
// pending. The command text never leaves this package.
//
// The reader is strict: a touch comes only from a plain invocation whose
// options are all known, with the arity the tool gives them, and whose
// environment and folder nothing in the line could change. Anything else
// gives no touch: a missed touch costs a "looked at"; a wrong one credits an
// agent with an issue it never looked at.

// maxIssueNumber is the largest issue number: both forges number issues with
// a 32-bit integer.
const maxIssueNumber = math.MaxInt32

// IssueCommand is an issue a command names.
type IssueCommand struct {
	// Forge is the tool's own: gh is GitHub, glab is GitLab. It is kept when
	// Repo is zero, so the core can tell which tool named the issue.
	Forge model.Forge
	// Repo is the repository the command names with -R/--repo or a full issue
	// URL. Zero when it names none: the issue then belongs to the repository
	// of the folder the call ran in, which only the caller knows.
	Repo   model.ForgeRepo
	Number int
}

// optSpec is the options of one subcommand, with their arity. --help is not
// listed: a command that asks for help is not read. The -R/--repo option of
// every issue subcommand is added by optsOf.
type optSpec struct {
	shortBool, shortVal string
	longBool, longVal   []string
}

// issueSpecs are the `issue` subcommands that name one issue and are read,
// from the tools' own help (gh 2.106.0, glab 1.133.0). An option the table
// lacks makes the invocation unknown.
var issueSpecs = map[string]optSpec{
	"gh view":    {shortBool: "cw", shortVal: "qt", longBool: []string{"comments", "web"}, longVal: []string{"jq", "json", "template"}},
	"gh comment": {shortBool: "ew", shortVal: "bF", longBool: []string{"create-if-none", "delete-last", "edit-last", "editor", "web", "yes"}, longVal: []string{"attach", "body", "body-file"}},
	"gh edit": {shortVal: "bFmt", longBool: []string{"remove-milestone", "remove-parent", "remove-type"}, longVal: []string{
		"add-assignee", "add-blocked-by", "add-blocking", "add-label", "add-project", "add-sub-issue", "attach", "body", "body-file",
		"milestone", "parent", "remove-assignee", "remove-blocked-by", "remove-blocking", "remove-label", "remove-project",
		"remove-sub-issue", "title", "type"}},
	"gh develop": {shortBool: "cl", shortVal: "bn", longBool: []string{"checkout", "list"}, longVal: []string{"base", "branch-repo", "name", "worktree"}},
	"gh close":   {shortVal: "cr", longVal: []string{"comment", "duplicate-of", "reason"}},
	"gh reopen":  {shortVal: "c", longVal: []string{"comment"}},

	"glab view":   {shortBool: "csw", shortVal: "FpP", longBool: []string{"comments", "system-logs", "web"}, longVal: []string{"jq", "output", "page", "per-page"}},
	"glab note":   {shortVal: "m", longBool: []string{"internal"}, longVal: []string{"attach", "message"}},
	"glab update": {shortBool: "cp", shortVal: "adlmtuw", longBool: []string{"confidential", "lock-discussion", "public", "remove-estimate", "remove-time-spent", "unassign", "unlock-discussion"}, longVal: []string{"assignee", "attach", "description", "description-file", "due-date", "estimate", "label", "milestone", "spend", "title", "unlabel", "weight"}},
	"glab close":  {},
	"glab reopen": {},
}

// helpFlags are the flags that make a command print its help instead of
// running: glab has -h as well.
func isHelp(prog, name string) bool { return name == "help" || (prog == "glab" && name == "h") }

// optKind is what a flag is.
type optKind int

const (
	optUnknown optKind = iota
	optBool
	optValue
	optRepo // -R/--repo, which takes a value that is read
)

func (o optSpec) short(prog string, c byte) optKind {
	switch {
	case c == 'R':
		return optRepo
	case prog == "glab" && c == 'h': // help
		return optBool
	case strings.IndexByte(o.shortBool, c) >= 0:
		return optBool
	case strings.IndexByte(o.shortVal, c) >= 0:
		return optValue
	}
	return optUnknown
}

func (o optSpec) long(name string) optKind {
	switch {
	case name == "repo":
		return optRepo
	case name == "help" || slices.Contains(o.longBool, name):
		return optBool
	case slices.Contains(o.longVal, name):
		return optValue
	}
	return optUnknown
}

// stateBuiltins are the shell words that set, import or run something that
// can change the variables or the command a later line runs with: a line that
// has one at a command position is not read. (A plain assignment word, with or
// without export, is caught by assignRe.) printf is one only with -v, set only
// with -n or -o noexec; see changesState.
var stateBuiltins = []string{"export", "declare", "typeset", "local", "readonly", "read", "source", ".", "eval", "unset", "mapfile", "readarray", "let", "getopts", "builtin", "for", "select"}

// changesState says whether the command at words[k] sets a variable (printf
// -v NAME) or turns the shell into one that runs nothing (set -n,
// set -o noexec).
func (c Command) changesState(k int) bool {
	switch filepath.Base(c.words[k]) {
	case "printf":
		for j := k + 1; j < len(c.words) && !c.sep[j]; j++ {
			if strings.HasPrefix(c.words[j], "-v") {
				return true
			}
		}
	case "set":
		for j := k + 1; j < len(c.words) && !c.sep[j]; j++ {
			if w := c.words[j]; w == "noexec" || len(w) > 1 && w[0] == '-' && w[1] != '-' && strings.IndexByte(w, 'n') > 0 {
				return true
			}
		}
	}
	return false
}

// envPrograms are the programs that run another command with a changed
// environment or folder, or as another user. A line that runs one is not
// read at all: where its inner command runs, and with what, is not known
// (env -C, env -S, sudo -D, and a shell script under any of them).
var envPrograms = []string{"env", "sudo", "doas", "chroot", "su", "runuser", "nsenter"}

// cwdPrograms change the folder the commands after them run in.
var cwdPrograms = []string{"cd", "pushd", "popd"}

// IssueCommands returns the issues the `gh issue view|comment|edit|develop|
// close|reopen` and `glab issue view|note|update|close|reopen` commands of
// the call name, each once, at most maxIDsCall. Strictly:
//   - the options of the invocation are all in the tool's table, read with
//     their arity as cobra reads them (attached values, short clusters,
//     -R=x, --name=value, and "--"), and exactly one issue is named: a
//     number (7, "#7") or an issue address;
//   - the repository is the one -R/--repo or the address names. A hostless gh
//     -R is read as github.com (an accepted limit, see flagRepo); a glab -R,
//     in any form, gives no touch (an address as the argument still does);
//   - the whole line is refused (nothing is read from it) when: a cap of the
//     lexer cut it (Command.inexact, which includes a `bash -n` script); it
//     runs an env, sudo or the like; it has an assignment word anywhere
//     (GH_REPO=x, GIT_DIR=x, FOO=x) or a builtin that sets, imports or
//     evaluates (stateBuiltins, printf -v, set -n); it has a || list or a
//     `gh repo set-default`; it has an unquoted backslash-newline, which may
//     join the halves of an operator (Command.joined); or a word in or before
//     the invocation depends on the shell at run time (an expansion, $'..', a
//     glob, a brace): the arguments are then not known;
//   - an invocation that follows an && in its list is not read: whether it
//     runs depends on an earlier exit status. The first command of the line,
//     one after ; & or a newline, and one that an && only precedes are read.
//     A group opened or closed after an && (parentheses, braces) is not
//     followed, and the rest of the line stays guarded; a shell -c script is
//     guarded when an && stands anywhere in the call;
//   - a line with a cd, pushd or popd anywhere gives only the touches that
//     name their repository.
//
// Accepted limit, in the class of an inherited GH_HOST (see flagRepo): a
// number with no -R names the folder's repository, but a GH_REPO (or GH_HOST)
// that the session inherited from its launcher makes gh read another one. Only
// an assignment in the line is seen, and the launcher's environment is not in
// the transcript. The core links a touch only to an issue listed for a
// repository on the board, so a wrong credit needs that number open there.
//
// The line is read once: the facts that hold for the whole line are worked
// out in one pass, and each invocation is read only up to its separator, so
// a line of any number of invocations costs a linear amount.
//
// gh with a global flag in front (`gh -R x issue view 5`) is not a match, as
// for every other command here: Runs wants the subcommand first.
func (c Command) IssueCommands() []IssueCommand {
	if c.inexact || c.orList || c.joined {
		return nil // cut short; a branch that may not run (a || list); or an operator that may be joined across lines
	}
	// A repository chosen by `gh repo set-default` is not the folder's.
	if len(c.Runs("gh", "repo", "set-default")) > 0 {
		return nil
	}
	moves := false
	firstDyn := -1 // the first word that depends on the shell at run time
	for k, w := range c.words {
		if c.sep[k] {
			continue
		}
		if c.dyn[k] && firstDyn < 0 {
			firstDyn = k
		}
		switch {
		case assignRe.MatchString(w):
			return nil
		case c.at[k] && slices.Contains(envPrograms, filepath.Base(w)):
			return nil
		case c.at[k] && (slices.Contains(stateBuiltins, filepath.Base(w)) || c.changesState(k)):
			return nil
		case c.at[k] && slices.Contains(cwdPrograms, filepath.Base(w)):
			moves = true
		}
	}
	// guard: an && stands before, in the list that is being read, so what runs
	// depends on an earlier exit status. sticky: a group opened or closed while
	// guarded, which is not followed; the rest of the script stays guarded.
	// A script (bash -c) is read apart from the call, and runs wherever its
	// wrapper does: it is guarded when any && stands in the call.
	var out []IssueCommand
	guard, sticky, andAbove, scriptsGuarded := false, false, false, false
	nextScript := 0
	for i, w := range c.words {
		if len(out) >= maxIDsCall {
			break
		}
		if nextScript < len(c.scripts) && i == c.scripts[nextScript] {
			if nextScript == 0 {
				scriptsGuarded = andAbove
			}
			nextScript++
			guard, sticky = scriptsGuarded, scriptsGuarded
		}
		if c.sep[i] {
			k := c.skind[i]
			if k&kAnd != 0 {
				guard = true
				if nextScript == 0 {
					andAbove = true
				}
			}
			if k&kParen != 0 && guard {
				sticky = true
			} else if k&kSemi != 0 && !sticky {
				guard = false
			}
			continue
		}
		if guard && (w == "{" || w == "}") { // a brace group: not followed
			sticky = true
		}
		if guard || !c.at[i] || i+2 >= len(c.words) || c.sep[i+1] || c.sep[i+2] || c.words[i+1] != "issue" {
			continue
		}
		prog := filepath.Base(w)
		if prog != "gh" && prog != "glab" {
			continue
		}
		spec, ok := issueSpecs[prog+" "+c.words[i+2]]
		if !ok {
			continue
		}
		ic, ok := c.issueCommand(prog, spec, i, firstDyn)
		if !ok || (ic.Repo.IsZero() && moves) {
			continue
		}
		if !slices.ContainsFunc(out, func(o IssueCommand) bool { return sameIssue(o.Forge, o.Repo, o.Number, ic.Forge, ic.Repo, ic.Number) }) {
			out = append(out, ic)
		}
	}
	return out
}

// issueCommand reads the invocation of prog starting at word i, up to the
// next separator: its options by spec, and its one positional word.
func (c Command) issueCommand(prog string, spec optSpec, i, firstDyn int) (IssueCommand, bool) {
	forge := model.ForgeGitHub
	if prog == "glab" {
		forge = model.ForgeGitLab
	}
	var args []string
	var repoArg string
	repoGiven := false
	end := i + 3
	for end < len(c.words) && !c.sep[end] {
		end++
	}
	if firstDyn >= 0 && firstDyn < end {
		return IssueCommand{}, false // the arguments, or what stands before them, are not known
	}
	// value returns the next word as an option's value.
	for j := i + 3; j < end; j++ {
		w := c.words[j]
		value := func() (string, bool) {
			if j+1 < end {
				j++
				return c.words[j], true
			}
			return "", false
		}
		switch {
		case w == "--":
			args = append(args, c.words[j+1:end]...)
			j = end
		case strings.HasPrefix(w, "--"):
			name, v, has := strings.Cut(w[2:], "=")
			switch spec.long(name) {
			case optBool:
				if isHelp(prog, name) {
					return IssueCommand{}, false
				}
			case optValue, optRepo:
				if !has {
					var ok bool
					if v, ok = value(); !ok {
						return IssueCommand{}, false
					}
				}
				if name == "repo" {
					repoGiven, repoArg = true, v
				}
			default:
				return IssueCommand{}, false
			}
		case len(w) > 1 && w[0] == '-':
			// A cluster of short options as pflag reads it: a bool flag
			// continues the cluster; a value flag takes the rest of the word
			// (after an optional "="), or the next word.
			for s := w[1:]; s != ""; {
				c0 := s[0]
				kind := spec.short(prog, c0)
				if kind == optUnknown || isHelp(prog, string(c0)) {
					return IssueCommand{}, false
				}
				if len(s) > 2 && s[1] == '=' {
					if kind == optRepo {
						repoGiven, repoArg = true, s[2:]
					}
					break
				}
				if kind == optBool {
					s = s[1:]
					continue
				}
				v := s[1:]
				if v == "" {
					var ok bool
					if v, ok = value(); !ok {
						return IssueCommand{}, false
					}
				}
				if kind == optRepo {
					repoGiven, repoArg = true, v
				}
				break
			}
		default:
			args = append(args, w)
		}
	}
	if len(args) != 1 {
		return IssueCommand{}, false
	}
	n, urlRepo, ok := issueArg(prog, args[0])
	if !ok {
		return IssueCommand{}, false
	}
	repo := urlRepo
	if repoGiven {
		if prog == "glab" {
			return IssueCommand{}, false // an accepted limit: see flagRepo
		}
		r, ok := flagRepo(repoArg)
		if !ok || (!urlRepo.IsZero() && !sameRepo(urlRepo, r)) {
			return IssueCommand{}, false
		}
		repo = r
	}
	return IssueCommand{Forge: forge, Repo: cloneRepo(repo), Number: n}, true
}

// cloneRepo copies a repository's strings out of the command's words: the
// touch outlives the parsed command, and a substring of a word would keep
// the whole word (an address with its query) alive.
func cloneRepo(r model.ForgeRepo) model.ForgeRepo {
	r.Host, r.Path = strings.Clone(r.Host), strings.Clone(r.Path)
	return r
}

// sameRepo says whether a and b are one repository the way the core keys
// them: GitHub names are compared without case, GitLab paths exactly.
func sameRepo(a, b model.ForgeRepo) bool {
	if a.Forge == model.ForgeGitHub && b.Forge == model.ForgeGitHub {
		return a.Host == b.Host && strings.EqualFold(a.Path, b.Path)
	}
	return a == b
}

// sameIssue is the identity of an issue touch: the tool, the repository (or,
// when unresolved, nothing: the caller compares the folders) and the number.
func sameIssue(fa model.Forge, ra model.ForgeRepo, na int, fb model.Forge, rb model.ForgeRepo, nb int) bool {
	return fa == fb && na == nb && sameRepo(ra, rb)
}

// issueArg reads the issue word: a number with an optional #, or an issue
// URL of the forge prog talks to. For a URL it also returns its repository.
func issueArg(prog, w string) (n int, repo model.ForgeRepo, ok bool) {
	if len(w) > 2048 {
		return 0, model.ForgeRepo{}, false
	}
	if num := strings.TrimPrefix(w, "#"); isDigits(num) && len(num) <= 10 {
		n, err := strconv.Atoi(num)
		return n, model.ForgeRepo{}, err == nil && n >= 1 && n <= maxIssueNumber
	}
	u := w
	if k := strings.IndexAny(u, "#?"); k >= 0 { // a comment's anchor, a query
		u = u[:k]
	}
	rest, found := strings.CutPrefix(u, "https://")
	if !found {
		return 0, model.ForgeRepo{}, false
	}
	host, path, found := strings.Cut(rest, "/")
	if !found {
		return 0, model.ForgeRepo{}, false
	}
	var p, num string
	switch prog {
	case "gh":
		// Nothing but github.com: the issue address pattern accepts no other host.
		if host != model.GitHubHost {
			return 0, model.ForgeRepo{}, false
		}
		if p, num, found = strings.CutLast(path, "/issues/"); !found {
			return 0, model.ForgeRepo{}, false
		}
		repo = model.GitHubRepo(p)
	default:
		// The host as written is kept, port included: the round trip below
		// accepts only an address in the allowlist's shape (so never ":443").
		// GitHub's host is never GitLab's.
		if model.GitLabHostname(host) == model.GitHubHost {
			return 0, model.ForgeRepo{}, false
		}
		if p, num, found = strings.CutLast(path, "/-/issues/"); !found {
			return 0, model.ForgeRepo{}, false
		}
		repo = model.GitLabRepo(host, p)
	}
	n, err := strconv.Atoi(num)
	if err != nil || !isDigits(num) || len(num) > 10 || n < 1 || n > maxIssueNumber {
		return 0, model.ForgeRepo{}, false
	}
	return n, repo, model.ForgeIssueURL(repo, n) == u
}

// flagRepo reads the value of a gh -R/--repo: "OWNER/NAME",
// "github.com/OWNER/NAME" or the https address of the repository. Another
// host is not placed.
//
// Accepted limits (decided for #176):
//   - A hostless "OWNER/NAME" is read as github.com. gh itself uses its
//     default host, which GH_HOST, or a configuration with one Enterprise
//     host, can make another. Nothing in a transcript says so, and the host is
//     not resolved here. The core links a touch only to an issue listed for a
//     repository on the board, so a wrong credit needs the same owner and name
//     on both hosts with that number open.
//   - glab -R, in any form, gives no touch: "group/project" cannot be placed
//     without glab's configured host and mount, and a dotted group cannot be
//     told from a host. A missed touch is fine.
func flagRepo(v string) (model.ForgeRepo, bool) {
	if v == "" || len(v) > 1100 {
		return model.ForgeRepo{}, false
	}
	rest, scheme := strings.CutPrefix(v, "https://")
	rest = strings.TrimSuffix(rest, "/")
	if scheme || strings.Count(rest, "/") == 2 {
		var host string
		host, rest, _ = strings.Cut(rest, "/")
		if host != model.GitHubHost {
			return model.ForgeRepo{}, false
		}
	}
	r := model.GitHubRepo(rest)
	return r, r.Valid()
}

// AddIssueTouches adds what a call's commands named to a session's touches,
// at most limit kept: one entry per tool, repository (or folder, when the
// command named none) and number, with the latest time. A command without a
// repository is left to the folder it ran in: without a valid folder it is
// dropped. The strings kept are copies. When the list is full, a new issue
// replaces the least recently touched one.
func AddIssueTouches(list []model.IssueTouch, cmds []IssueCommand, folder string, at time.Time, limit int) []model.IssueTouch {
	for _, ic := range cmds {
		t := model.IssueTouch{Forge: ic.Forge, Repo: cloneRepo(ic.Repo), Number: ic.Number, At: at}
		if t.Repo.IsZero() {
			if folder == "" {
				continue
			}
			t.Folder = strings.Clone(folder)
		}
		list = addTouch(list, t, limit)
	}
	return list
}

// AddIssueTouchList adds the touches of another transcript (a subagent's, or
// one of the session's older folders) to a session's, as AddIssueTouches
// does.
func AddIssueTouchList(list, more []model.IssueTouch, limit int) []model.IssueTouch {
	for _, t := range more {
		list = addTouch(list, t, limit)
	}
	return list
}

func addTouch(list []model.IssueTouch, t model.IssueTouch, limit int) []model.IssueTouch {
	i := slices.IndexFunc(list, func(o model.IssueTouch) bool {
		return o.Folder == t.Folder && sameIssue(o.Forge, o.Repo, o.Number, t.Forge, t.Repo, t.Number)
	})
	switch {
	case i >= 0:
		if t.At.After(list[i].At) {
			list[i].At = t.At
		}
	case len(list) < limit:
		list = append(list, t)
	default:
		old := 0
		for k, o := range list {
			if o.At.Before(list[old].At) {
				old = k
			}
		}
		if !list[old].At.After(t.At) {
			list[old] = t
		}
	}
	return list
}
