package main

import (
	"fmt"
	"io"
	"strings"

	"github.com/IZZY-Agency/sitboard/internal/source"
)

// colour is how the start lines are written: plain, or the start-up block in
// sitboard's colours with 256 or 24-bit colour codes.
type colour int

const (
	colourNone colour = iota // plain lines, as before the block existed
	colour256
	colourTrue
)

// colourFor picks the start lines' look: the block only on a terminal, and
// never with NO_COLOR set (to anything but "") or TERM=dumb. 24-bit colour
// only when COLORTERM says the terminal has it.
func colourFor(terminal bool, getenv func(string) string) colour {
	if !terminal || getenv("NO_COLOR") != "" || getenv("TERM") == "dumb" {
		return colourNone
	}
	switch getenv("COLORTERM") {
	case "truecolor", "24bit":
		return colourTrue
	}
	return colour256
}

// lightTerminal says whether COLORFGBG (set by some terminals) names a light
// background, so the block takes the darker accent. Unset or unknown is dark.
func lightTerminal(getenv func(string) string) bool {
	v := getenv("COLORFGBG")
	bg := v[strings.LastIndex(v, ";")+1:]
	return bg == "7" || bg == "15"
}

// startInfo is what the start lines say. Notes are the state notes and the
// port note, in that order. Statuses are the probes run before listening;
// gitlabLater is true when glab is probed only once sitboard listens.
type startInfo struct {
	version     string
	commit      string
	link        string
	notes       []string
	statuses    []source.Status
	gitlabLater bool
}

// printStart writes the start lines once. Plain, it writes them exactly as
// sitboard always has; with colour, the start-up block. Either way the link is
// a line of its own and the only place the token is printed. Nothing is
// redrawn later: later lines go below it.
func printStart(w io.Writer, c colour, light bool, info startInfo) {
	if c == colourNone {
		for _, n := range info.notes {
			_, _ = fmt.Fprintln(w, "sitboard:", n)
		}
		_, _ = fmt.Fprintln(w, "sitboard:", info.link)
		_, _ = fmt.Fprintln(w, "sitboard: press Ctrl-C to stop")
		return
	}
	p := newPalette(c, light)
	var b strings.Builder
	line := func(parts ...string) {
		b.WriteString(strings.Join(parts, ""))
		b.WriteString("\n")
	}
	ver := p.muted(clip(info.version))
	if info.commit != "" && info.commit != "unknown" {
		ver += p.muted(" (" + clip(info.commit) + ")")
	}
	line(p.amber("▸"), " ", p.bold("sitboard"), " ", ver)
	line()
	// The link alone on its line, from the first column, with no other
	// character a double-click or cmd-click could take with it.
	line(p.amber(info.link))
	line()
	for _, r := range sourceRows(info) {
		line("  ", fmt.Sprintf("%-12s", r.label), p.tone(r.tone, r.word), p.muted(r.rest))
	}
	if len(info.notes) > 0 {
		line()
		for _, n := range info.notes {
			n = clip(n)
			if rest, ok := strings.CutPrefix(n, "warning: "); ok {
				line("  ", p.amber("warning:"), " ", rest)
				continue
			}
			line("  ", p.muted(n))
		}
	}
	line()
	line(p.muted("press "), p.bold("Ctrl-C"), p.muted(" to stop"))
	_, _ = io.WriteString(w, b.String())
}

type row struct {
	label, word, rest string
	tone              string // "ok", "muted", "failing"
}

var sourceLabel = map[source.Kind]string{
	source.KindClaude: "claude code",
	source.KindCodex:  "codex",
	source.KindAgterm: "agterm",
	source.KindGit:    "git",
	source.KindGitHub: "github",
	source.KindGitLab: "gitlab",
}

// sourceRows is one row per source, in the page's order, from what the start
// probes found, with the page's words for each state. GitLab, when glab is
// asked only once sitboard listens, says so; tmux has no probe and says when
// it is checked.
func sourceRows(info startInfo) []row {
	by := map[source.Kind]source.Status{}
	for _, s := range info.statuses {
		by[s.Kind] = s
	}
	var rows []row
	for _, k := range source.Kinds {
		s, ok := by[k]
		label := sourceLabel[k]
		if !ok {
			if k == source.KindGitLab && info.gitlabLater {
				rows = append(rows, row{label: label, word: "asked once sitboard listens", tone: "muted", rest: "; the page shows what glab says"})
			}
			continue
		}
		rows = append(rows, statusRow(label, s))
	}
	return append(rows, row{label: "tmux", word: "checked when you jump to a pane", tone: "muted"})
}

func statusRow(label string, s source.Status) row {
	r := row{label: label, tone: "muted"}
	switch s.State {
	case source.StateOK:
		r.word, r.tone = "found", "ok"
		switch s.Kind {
		case source.KindGitHub:
			r.word = "through gh"
		case source.KindGitLab:
			r.word = "through glab"
		}
	case source.StateMissing:
		r.word = "not found"
	case source.StateOff:
		r.word = "off"
	case source.StateFailing:
		r.word, r.tone = "failing", "failing"
	default:
		r.word = "unknown"
	}
	if v := clip(s.Version); v != "" {
		r.rest += " " + v
		if !s.Tested {
			r.rest += ", not a tested version"
		}
	}
	// A detail that only repeats the word ("agterm not found") adds nothing.
	if d := clip(s.Detail); d != "" && !strings.Contains(d, r.word) {
		r.rest += " · " + d
	}
	return r
}

// clip drops control characters, so nothing a source or a path holds can
// steer the terminal.
func clip(s string) string {
	return strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f || (r >= 0x80 && r < 0xa0) {
			return -1
		}
		return r
	}, s)
}

// palette writes sitboard's colours (the page's tokens) as ANSI codes. Text
// with no colour of its own keeps the terminal's foreground, so it reads on a
// light theme as on a dark one.
type palette struct {
	accent, dim, ok, bad string
}

func newPalette(c colour, light bool) palette {
	type rgb struct {
		r, g, b int
		n       int // the nearest of the 256 colours
	}
	amber := rgb{0xf5, 0xc5, 0x42, 221}   // --amber
	muted := rgb{0x8b, 0x92, 0xa5, 103}   // --muted
	green := rgb{0x8f, 0xc7, 0x9a, 114}   // --state-working
	failing := rgb{0xec, 0x7c, 0x7c, 210} // --state-failing
	if light {
		// The page is dark only; on a light ground its amber and greens
		// fade, so a light terminal gets deeper shades of the same hues.
		amber = rgb{0x9a, 0x6a, 0x00, 136}
		muted = rgb{0x5f, 0x65, 0x78, 60}
		green = rgb{0x2f, 0x7a, 0x3e, 28}
		failing = rgb{0xb0, 0x30, 0x30, 124}
	}
	code := func(x rgb) string {
		if c == colourTrue {
			return fmt.Sprintf("\x1b[38;2;%d;%d;%dm", x.r, x.g, x.b)
		}
		return fmt.Sprintf("\x1b[38;5;%dm", x.n)
	}
	return palette{accent: code(amber), dim: code(muted), ok: code(green), bad: code(failing)}
}

const reset = "\x1b[0m"

func paint(code, s string) string {
	if s == "" {
		return ""
	}
	return code + s + reset
}

func (p palette) amber(s string) string { return paint(p.accent, s) }
func (p palette) muted(s string) string { return paint(p.dim, s) }
func (p palette) bold(s string) string  { return paint("\x1b[1m", s) }

func (p palette) tone(t, s string) string {
	switch t {
	case "ok":
		return paint(p.ok, s)
	case "failing":
		return paint(p.bad, s)
	}
	return p.muted(s)
}
