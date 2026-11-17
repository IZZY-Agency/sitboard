package claude

import (
	"path/filepath"
	"regexp"
	"slices"
	"strings"
)

// A Bash command from a transcript is untrusted text. It is split into words
// here, never evaluated or run, and every step is linear in its length.
const (
	maxWords        = 64 << 10 // words read per command, scripts and substitutions included
	maxShellScripts = 16       // -c and env -S scripts read per command
	maxShellScript  = 64 << 10 // longest script read
	maxDepth        = 8        // nesting of $(...) and backticks read
)

// command is a command line split into words, roughly the way a POSIX shell
// does it, closely enough to find agtermctl and gh and read their flags.
type command struct {
	words []string
	sep   []bool // words[i] stands for a separator (; & | newline parentheses); its text is ""
	at    []bool // words[i] is the program of a command (see positions)
}

// shells are the shells whose -c script is read.
var shells = []string{"bash", "sh", "zsh"}

// parseCommand splits a command line into words and marks where commands
// start. The commands inside $(...) and backticks are read as commands of
// their own, after the command they appear in. The script of a `bash -c`,
// `sh -c` or `zsh -c`, and the string of an `env -S`, that runs at a command
// position is read as well. Only one level of scripts is read: a shell -c
// inside such a script is not opened again, so deeper nesting is not
// supported. At most maxShellScripts scripts are read, and a script longer
// than maxShellScript is skipped.
func parseCommand(s string) command {
	var c command
	c.lex(s)
	c.positions(0)
	top := len(c.words)
	for i, n := 0, 0; i < top && n < maxShellScripts; i++ {
		if !c.at[i] {
			continue
		}
		var script string
		var ok bool
		switch base := filepath.Base(c.words[i]); {
		case slices.Contains(shells, base):
			script, ok = c.shellScript(i + 1)
		case base == "env":
			script, ok = c.envScript(i + 1)
		}
		if !ok || script == "" || len(script) > maxShellScript {
			continue
		}
		from := len(c.words)
		c.lex(script)
		c.positions(from)
		n++
	}
	return c
}

// runs returns the positions where prog runs with args as its first
// arguments: at a word that starts a command (see positions). An argument of
// another command, text inside quotes (a command substitution in it is read
// as a command of its own), a heredoc body and a comment never count.
func (c command) runs(prog string, args ...string) []int {
	var out []int
	for i, w := range c.words {
		if !c.at[i] || i+len(args) >= len(c.words) || filepath.Base(w) != prog {
			continue
		}
		ok := true
		for k, a := range args {
			if c.sep[i+1+k] || c.words[i+1+k] != a {
				ok = false
				break
			}
		}
		if ok {
			out = append(out, i)
		}
	}
	return out
}

// spawnNames returns the --name values of every `agtermctl session new` in
// the command. Values that depend on the shell (a variable or a substitution)
// are not resolved and are skipped: an unknown name is better than a guessed
// one.
func (c command) spawnNames() []string {
	var names []string
	for _, i := range c.runs("agtermctl", "session", "new") {
		for j := i + 3; j < len(c.words) && !c.sep[j]; j++ {
			var v string
			switch w := c.words[j]; {
			case w == "--name" && j+1 < len(c.words) && !c.sep[j+1]:
				v = c.words[j+1]
				j++
			case strings.HasPrefix(w, "--name="):
				v = strings.TrimPrefix(w, "--name=")
			default:
				continue
			}
			if v != "" && len(v) <= 256 && !strings.ContainsAny(v, "$`") {
				names = append(names, v)
			}
		}
	}
	return names
}

var assignRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*=`)

// keywords are the shell words after which a command starts, as in
// `if CMD`, `then CMD`, `do CMD`, `{ CMD; }` or `! CMD`.
var keywords = []string{"if", "then", "else", "elif", "while", "until", "do", "{", "!"}

// wrapper describes a program that runs the command after it.
type wrapper struct {
	args  string   // one-letter options that take an argument
	long  []string // long options that take the next word as their argument
	noRun string   // one-letter options with which the command is not run
}

var wrappers = map[string]wrapper{
	"env":     {args: "uCS", long: []string{"--unset", "--chdir", "--split-string"}},
	"sudo":    {args: "ugCDhprtTU", long: []string{"--user", "--group", "--host", "--prompt", "--role", "--type", "--chdir", "--chroot", "--close-from", "--command-timeout", "--other-user"}, noRun: "lveVK"},
	"exec":    {args: "a"},
	"command": {noRun: "vV"}, // `command -v` only looks the program up
	"nohup":   {},
	"timeout": {args: "sk", long: []string{"--signal", "--kill-after"}},
	"time":    {args: "of", long: []string{"--output", "--format"}},
}

// positions marks, from words[from] on, the words that are the program of a
// command: the first word after a separator, after VAR=value assignments, a
// keyword, or a wrapper with its options (and timeout's duration). It is one
// pass over the words.
func (c *command) positions(from int) {
	c.at = append(c.at, make([]bool, len(c.words)-len(c.at))...)
	expect, wrap, needArg, gotDuration := true, "", false, false
	for i := from; i < len(c.words); i++ {
		if c.sep[i] {
			expect, wrap, needArg, gotDuration = true, "", false, false
			continue
		}
		if !expect {
			continue
		}
		w := c.words[i]
		switch {
		case needArg: // the argument of a wrapper's option
			needArg = false
		case wrap != "" && len(w) > 1 && w[0] == '-':
			spec := wrappers[wrap]
			if strings.HasPrefix(w, "--") {
				needArg = slices.Contains(spec.long, w)
				continue
			}
			for k := 1; k < len(w); k++ {
				if strings.IndexByte(spec.noRun, w[k]) >= 0 {
					expect = false
					break
				}
				if strings.IndexByte(spec.args, w[k]) >= 0 {
					needArg = k == len(w)-1 // otherwise the argument is attached
					break
				}
			}
		case wrap == "timeout" && !gotDuration:
			gotDuration = true
		case (wrap == "" || wrap == "env" || wrap == "sudo") && assignRe.MatchString(w):
		case wrap == "time" && w == "!":
			wrap = ""
		case wrap == "" && slices.Contains(keywords, w):
		default:
			c.at[i] = true
			if _, ok := wrappers[filepath.Base(w)]; ok {
				wrap, gotDuration = filepath.Base(w), false
			} else {
				expect = false
			}
		}
	}
}

// shellScript returns the script of a shell whose arguments start at
// words[from], when -c is among its options: the first word that is not an
// option or an option's argument. An option cluster with o or O (-o NAME,
// -euo NAME), --rcfile and --init-file take the next word. It never reads
// past a separator.
func (c command) shellScript(from int) (string, bool) {
	hasC := false
	for j := from; j < len(c.words) && !c.sep[j]; j++ {
		switch a := c.words[j]; {
		case a == "--":
			if hasC && j+1 < len(c.words) && !c.sep[j+1] {
				return c.words[j+1], true
			}
			return "", false
		case a == "--rcfile" || a == "--init-file":
			if j+1 < len(c.words) && !c.sep[j+1] {
				j++ // its argument; a separator is never one
			}
		case strings.HasPrefix(a, "--"):
		case len(a) > 1 && (a[0] == '-' || a[0] == '+'):
			if a[0] == '-' && strings.IndexByte(a[1:], 'c') >= 0 {
				hasC = true
			}
			if strings.ContainsAny(a[1:], "oO") && j+1 < len(c.words) && !c.sep[j+1] {
				j++
			}
		default:
			return a, hasC
		}
	}
	return "", false
}

// envScript returns the string of an `env -S STRING` (also -SSTRING and
// --split-string[=]STRING) whose arguments start at words[from]: env splits
// it into the command it runs. It never reads past a separator.
func (c command) envScript(from int) (string, bool) {
	for j := from; j < len(c.words) && !c.sep[j]; j++ {
		switch a := c.words[j]; {
		case a == "-S" || a == "--split-string":
			if j+1 < len(c.words) && !c.sep[j+1] {
				return c.words[j+1], true
			}
			return "", false
		case strings.HasPrefix(a, "--split-string="):
			return strings.TrimPrefix(a, "--split-string="), true
		case strings.HasPrefix(a, "-S"):
			return a[2:], true
		case a == "-u" || a == "-C" || a == "--unset" || a == "--chdir":
			if j+1 < len(c.words) && !c.sep[j+1] {
				j++
			}
		case strings.HasPrefix(a, "-") || assignRe.MatchString(a):
		default:
			return "", false
		}
	}
	return "", false
}

// heredoc is a here-document waiting for the end of its line.
type heredoc struct {
	delim string
	tabs  bool // <<- strips leading tabs from the body and the delimiter line
}

// lexer splits one string into words. The words of each $(...) or backtick
// substitution go to a command of their own (subs), so a substitution never
// splits the command around it; they are appended after it.
type lexer struct {
	s    string
	left int       // words still allowed under maxWords
	docs []heredoc // heredocs waiting for the end of their line
	subs []*command
}

// lex appends the words of s to c, then the commands of its substitutions,
// each after a separator.
func (c *command) lex(s string) {
	l := lexer{s: s, left: maxWords - len(c.words)}
	l.add(c, "", true)
	l.run(c, 0, 0, 0)
	for _, sub := range l.subs { // its words were counted against maxWords when read
		if len(sub.words) == 0 {
			continue
		}
		if n := len(c.sep); n > 0 && !c.sep[n-1] {
			c.words, c.sep = append(c.words, ""), append(c.sep, true)
		}
		c.words, c.sep = append(c.words, sub.words...), append(c.sep, sub.sep...)
	}
	if len(c.words) > maxWords {
		c.words, c.sep = c.words[:maxWords], c.sep[:maxWords]
	}
}

func (l *lexer) add(c *command, w string, sep bool) {
	if l.left <= 0 {
		return
	}
	if sep && (len(c.sep) == 0 || c.sep[len(c.sep)-1]) {
		return // no leading or repeated separators
	}
	c.words = append(c.words, w)
	c.sep = append(c.sep, sep)
	l.left--
}

// run reads s[i:] outside quotes into c until the byte stop (')' ends a
// $(...), 0 the string) and returns the index after it.
// Quotes and backslashes are removed; ; & | newline and parentheses are
// separators. A comment (an unquoted # at the start of a word) runs to the end
// of its line. A here-document's body (after << or <<-, up to its delimiter
// line) is data and is skipped. A redirection and its target are dropped.
// Every byte is read once; depth bounds the nesting.
func (l *lexer) run(c *command, i int, stop byte, depth int) int {
	s := l.s
	var cur strings.Builder
	inWord := false
	parens := 0
	flush := func() {
		if inWord {
			l.add(c, cur.String(), false)
			cur.Reset()
			inWord = false
		}
	}
	for ; i < len(s) && l.left > 0; i++ {
		ch := s[i]
		switch {
		case ch == stop && stop != 0 && parens == 0:
			flush()
			return i + 1
		case ch == '\\' && i+1 < len(s):
			i++
			if s[i] != '\n' { // backslash-newline continues the line
				cur.WriteByte(s[i])
				inWord = true
			}
		case ch == '\'':
			end := strings.IndexByte(s[i+1:], '\'')
			if end < 0 {
				end = len(s) - i - 1
			}
			cur.WriteString(s[i+1 : i+1+end])
			inWord = true
			i += end + 1
		case ch == '$' && i+1 < len(s) && s[i+1] == '\'':
			i = ansiC(s, i+2, &cur)
			inWord = true
		case ch == '"':
			i = l.dquote(i+1, &cur, depth)
			inWord = true
		case strings.HasPrefix(s[i:], "$(("):
			i = l.arith(i+3, depth) - 1
			cur.WriteByte('$')
			inWord = true
		case ch == '$' && i+1 < len(s) && s[i+1] == '(' && depth < maxDepth:
			i = l.subst(i+2, ')', depth) - 1
			cur.WriteByte('$')
			inWord = true
		case ch == '`' && depth < maxDepth:
			i = l.backtick(i+1, depth, false) - 1
			cur.WriteByte('$')
			inWord = true
		case ch == '#' && !inWord:
			end := strings.IndexByte(s[i:], '\n')
			if end < 0 {
				flush()
				return len(s)
			}
			i += end - 1 // the newline is read next
		case ch == '(' && !inWord && i+1 < len(s) && s[i+1] == '(': // (( arithmetic ))
			l.add(c, "((", false)
			i = l.arith(i+2, depth) - 1
		case strings.HasPrefix(s[i:], "<<<"): // a here-string, not a here-document
			cur.WriteString("<<<")
			inWord = true
			i += 2
		case strings.HasPrefix(s[i:], "<<"):
			flush()
			i += 2
			tabs := i < len(s) && s[i] == '-'
			if tabs {
				i++
			}
			for i < len(s) && (s[i] == ' ' || s[i] == '\t') {
				i++
			}
			delim, next := redirWord(s, i)
			if next > i { // a delimiter was written, even an empty one ("")
				l.docs = append(l.docs, heredoc{delim: delim, tabs: tabs})
			}
			i = next - 1
		case ch == '<' || ch == '>' || (ch == '&' && i+1 < len(s) && s[i+1] == '>'):
			if isDigits(cur.String()) {
				cur.Reset() // a file descriptor number: 2>&1
				inWord = false
			}
			flush()
			i = l.redirect(i, depth) - 1
		case ch == '\n':
			flush()
			l.add(c, "", true)
			if len(l.docs) > 0 {
				i = skipBodies(s, i+1, l.docs) - 1
				l.docs = l.docs[:0]
			}
		case ch == ' ' || ch == '\t':
			flush()
		case ch == ';' || ch == '&' || ch == '|':
			flush()
			l.add(c, "", true)
		case ch == '(' || ch == ')':
			flush()
			l.add(c, "", true)
			if ch == '(' {
				parens++
			} else if parens > 0 {
				parens--
			}
		default:
			cur.WriteByte(ch)
			inWord = true
		}
	}
	flush()
	return len(s)
}

// subst reads a $(...) substitution from s[i:] into a command of its own and
// returns the index after its end.
func (l *lexer) subst(i int, stop byte, depth int) int {
	// Each substitution counts as a word, so empty ones are bounded too.
	if l.left--; l.left < 0 {
		return len(l.s)
	}
	sub := &command{}
	l.subs = append(l.subs, sub)
	return l.run(sub, i, stop, depth+1)
}

// backtick reads a backtick substitution from s[i:] and returns the index
// after its closing backtick. As the shell does, the backslash is first
// removed before $, ` and \ (and before " when the backtick is inside double
// quotes); the text that is left is read as a command of its own. A nested
// backtick (\`) is read at the next depth, so each level copies the text
// once.
func (l *lexer) backtick(i int, depth int, inDQ bool) int {
	s := l.s
	if l.left--; l.left < 0 { // counted as a word, as in subst
		return len(s)
	}
	end := i // the closing backtick; a backslash always escapes the next byte
	for end < len(s) && s[end] != '`' {
		if s[end] == '\\' {
			end++
		}
		end++
	}
	end = min(end, len(s))
	var b strings.Builder
	b.Grow(end - i)
	for ; i < end; i++ {
		if s[i] == '\\' && i+1 < len(s) && (strings.IndexByte("$`\\", s[i+1]) >= 0 || inDQ && s[i+1] == '"') {
			i++
		}
		if i < end {
			b.WriteByte(s[i])
		}
	}
	sub := &command{}
	l.subs = append(l.subs, sub)
	inner := lexer{s: b.String(), left: l.left}
	inner.run(sub, 0, 0, depth+1)
	l.left = inner.left
	l.subs = append(l.subs, inner.subs...)
	return min(end+1, len(s))
}

// dquote reads a double-quoted string from s[i:] into cur and returns the
// index of its closing quote. A $(...) or backtick inside is a substitution:
// its own quotes do not end the string.
func (l *lexer) dquote(i int, cur *strings.Builder, depth int) int {
	s := l.s
	for ; i < len(s); i++ {
		switch ch := s[i]; {
		case ch == '"':
			return i
		case ch == '\\' && i+1 < len(s) && strings.IndexByte("\"\\$`\n", s[i+1]) >= 0:
			i++
			if s[i] != '\n' {
				cur.WriteByte(s[i])
			}
		case strings.HasPrefix(s[i:], "$(("):
			i = l.arith(i+3, depth) - 1
			cur.WriteByte('$')
		case ch == '$' && i+1 < len(s) && s[i+1] == '(' && depth < maxDepth:
			i = l.subst(i+2, ')', depth) - 1
			cur.WriteByte('$')
		case ch == '`' && depth < maxDepth:
			i = l.backtick(i+1, depth, true) - 1
			cur.WriteByte('$')
		default:
			cur.WriteByte(ch)
		}
	}
	return len(s)
}

// arith skips an arithmetic expression after its (( and returns the index
// after the matching )). A << in it is a shift, never a heredoc; a $(...) or
// backtick in it is still read as a substitution. Known gap: a subshell
// written right after $( with no space, `$((cd x && cmd) )`, is read as
// arithmetic; bash itself wants `$( (` there.
func (l *lexer) arith(i int, depth int) int {
	s := l.s
	for open := 2; i < len(s); i++ {
		switch ch := s[i]; {
		case ch == '(':
			open++
		case ch == ')':
			if open--; open == 0 {
				return i + 1
			}
		case ch == '$' && i+1 < len(s) && s[i+1] == '(' && !strings.HasPrefix(s[i:], "$((") && depth < maxDepth:
			i = l.subst(i+2, ')', depth) - 1
		case ch == '`' && depth < maxDepth:
			i = l.backtick(i+1, depth, false) - 1
		}
	}
	return len(s)
}

// redirect skips a redirection at s[i:] (>, >>, >|, >&, <, <&, <>, &>, &>>)
// with its target and returns the index after it. A process substitution,
// <(...) or >(...), is read as a substitution. Known gap: a substitution
// inside a quoted target (> "$(cmd)") is not read.
func (l *lexer) redirect(i int, depth int) int {
	s := l.s
	if s[i] == '&' {
		i++
	}
	for i < len(s) && strings.IndexByte("<>&|", s[i]) >= 0 {
		i++
	}
	if i < len(s) && s[i] == '(' && depth < maxDepth {
		return l.subst(i+1, ')', depth)
	}
	for i < len(s) && (s[i] == ' ' || s[i] == '\t') {
		i++
	}
	_, next := redirWord(s, i)
	return next
}

// ansiC reads the rest of a $'...' string from s[i:] into cur, honouring
// backslash escapes, and returns the index of its closing quote.
func ansiC(s string, i int, cur *strings.Builder) int {
	for ; i < len(s); i++ {
		switch {
		case s[i] == '\\' && i+1 < len(s):
			i++
			cur.WriteByte(s[i])
		case s[i] == '\'':
			return i
		default:
			cur.WriteByte(s[i])
		}
	}
	return len(s)
}

func isDigits(w string) bool {
	if w == "" {
		return false
	}
	for i := range len(w) {
		if w[i] < '0' || w[i] > '9' {
			return false
		}
	}
	return true
}

// redirWord reads the word at s[i:] that a heredoc or a redirection names,
// quoted or not, and returns it without quotes with the index after it.
func redirWord(s string, i int) (string, int) {
	var b strings.Builder
	for ; i < len(s); i++ {
		switch ch := s[i]; {
		case ch == '\'' || ch == '"':
			end := strings.IndexByte(s[i+1:], ch)
			if end < 0 {
				end = len(s) - i - 1
			}
			b.WriteString(s[i+1 : i+1+end])
			i += end + 1
		case ch == '\\' && i+1 < len(s):
			i++
			b.WriteByte(s[i])
		case strings.IndexByte(" \t\n;&|()<>", ch) >= 0:
			return b.String(), i
		default:
			b.WriteByte(ch)
		}
	}
	return b.String(), len(s)
}

// skipBodies skips the bodies of docs, in order, from s[start:], and returns
// the index after the last delimiter line (or len(s) when one is missing).
func skipBodies(s string, start int, docs []heredoc) int {
	pos := start
	for _, d := range docs {
		for pos < len(s) {
			end := strings.IndexByte(s[pos:], '\n')
			next := pos + end + 1
			if end < 0 {
				end, next = len(s)-pos, len(s)
			}
			line := s[pos : pos+end]
			pos = next
			if d.tabs {
				line = strings.TrimLeft(line, "\t")
			}
			if line == d.delim {
				break
			}
		}
	}
	return pos
}

var (
	uuidRe   = regexp.MustCompile(`^[0-9A-Fa-f]{8}-[0-9A-Fa-f]{4}-[0-9A-Fa-f]{4}-[0-9A-Fa-f]{4}-[0-9A-Fa-f]{12}$`)
	idJSONRe = regexp.MustCompile(`"id"\s*:\s*"([0-9A-Fa-f]{8}-[0-9A-Fa-f]{4}-[0-9A-Fa-f]{4}-[0-9A-Fa-f]{4}-[0-9A-Fa-f]{12})"`)
)

// maxIDLineWords is the longest line, in words, whose last word is taken as a
// spawned session's id: "<id>", "<label>: <id>", "session <name> <id>".
const maxIDLineWords = 4

// maxJSONResult is the largest output searched for an "id" field. The answer
// of one `agtermctl session new --json` is small; a tree dump is not.
const maxJSONResult = 4 << 10

// terminalIDs returns the agterm session ids an `agtermctl session new`
// printed: the last word of a short line, or an "id" in a small JSON answer.
// Ids inside longer lines, quoted ids in dumps, and agterm's human tree output
// (which shows only short id prefixes) are ignored.
func terminalIDs(out string) []string {
	var ids []string
	add := func(id string) {
		id = strings.ToUpper(id)
		if len(ids) < maxIDsCall && !slices.Contains(ids, id) {
			ids = append(ids, id)
		}
	}
	for line := range strings.SplitSeq(out, "\n") {
		f := strings.Fields(line)
		if n := len(f); n > 0 && n <= maxIDLineWords && uuidRe.MatchString(f[n-1]) {
			add(f[n-1])
		}
	}
	if len(out) <= maxJSONResult {
		for _, m := range idJSONRe.FindAllStringSubmatch(out, maxIDsCall) {
			add(m[1])
		}
	}
	return ids
}
