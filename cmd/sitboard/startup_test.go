package main

import (
	"bytes"
	"flag"
	"fmt"
	"os"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/IZZY-Agency/sitboard/internal/source"
)

var updateStartup = flag.Bool("update-startup", false, "rewrite the start-up block's golden files")

const sampleLink = "http://127.0.0.1:4317/?token=" + "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

func sampleInfo() startInfo {
	return startInfo{
		version: "0.5.0",
		commit:  "a122d90d",
		link:    sampleLink,
		notes: []string{
			"state file /srv/boards/board.json",
			"warning: could not lock the state file (its folder does not support locks); another sitboard on the same file would overwrite it",
			"port 4317 is taken (another sitboard?), using 50123",
		},
		statuses: []source.Status{
			{Kind: source.KindClaude, State: source.StateOK, Version: "2.1.3", Tested: true},
			{Kind: source.KindCodex, State: source.StateMissing, Detail: "codex not found"},
			{Kind: source.KindAgterm, State: source.StateFailing, Detail: "agterm is not running"},
			{Kind: source.KindGit, State: source.StateOK, Version: "2.47.0", Tested: false},
			{Kind: source.KindGitHub, State: source.StateOff, Detail: "--no-github"},
		},
		gitlabLater: true,
	}
}

// The plain lines are byte for byte what sitboard printed before the block.
func TestPlainStartLinesAsBefore(t *testing.T) {
	var b bytes.Buffer
	printStart(&b, colourNone, false, sampleInfo())
	want := "sitboard: state file /srv/boards/board.json\n" +
		"sitboard: warning: could not lock the state file (its folder does not support locks); another sitboard on the same file would overwrite it\n" +
		"sitboard: port 4317 is taken (another sitboard?), using 50123\n" +
		"sitboard: " + sampleLink + "\n" +
		"sitboard: press Ctrl-C to stop\n"
	if b.String() != want {
		t.Fatalf("plain start lines:\n%q\nwant\n%q", b.String(), want)
	}
	b.Reset()
	printStart(&b, colourNone, true, startInfo{link: sampleLink})
	if want := "sitboard: " + sampleLink + "\nsitboard: press Ctrl-C to stop\n"; b.String() != want {
		t.Fatalf("plain start lines with no notes:\n%q\nwant\n%q", b.String(), want)
	}
}

func TestStartBlockGolden(t *testing.T) {
	for _, tc := range []struct {
		name  string
		c     colour
		light bool
	}{
		{"truecolor-dark", colourTrue, false},
		{"truecolor-light", colourTrue, true},
		{"256-dark", colour256, false},
		{"256-light", colour256, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var b bytes.Buffer
			printStart(&b, tc.c, tc.light, sampleInfo())
			if *updateStartup {
				writeStartupGolden(t, tc.name, b.String())
				return
			}
			want := []byte(startupGolden[tc.name])
			if !bytes.Equal(b.Bytes(), want) {
				t.Fatalf("start-up block %s differs:\n%q\nwant\n%q", tc.name, b.String(), want)
			}
		})
	}
}

var ansiRe = regexp.MustCompile("\x1b\\[[0-9;]*m")

// The link is a line of its own: once the colour codes are taken away the
// line is the link and nothing else, and the token is on no other line.
func TestStartBlockLinkStandsAlone(t *testing.T) {
	for _, c := range []colour{colour256, colourTrue} {
		var b bytes.Buffer
		printStart(&b, c, false, sampleInfo())
		token := sampleLink[strings.Index(sampleLink, "=")+1:]
		found := 0
		for _, l := range strings.Split(b.String(), "\n") {
			if !strings.Contains(l, token) {
				continue
			}
			found++
			if plain := ansiRe.ReplaceAllString(l, ""); plain != sampleLink {
				t.Errorf("colour %d: link line is %q, want the link alone", c, plain)
			}
		}
		if found != 1 {
			t.Errorf("colour %d: token on %d lines, want 1", c, found)
		}
	}
}

// A source's text cannot write control codes to the terminal.
func TestStartBlockDropsControlCharacters(t *testing.T) {
	info := startInfo{link: sampleLink, notes: []string{"state file /tmp/a\x1b]0;x\x07b"},
		statuses: []source.Status{{Kind: source.KindClaude, State: source.StateFailing, Detail: "bad\x1b[2Jline"}}}
	var b bytes.Buffer
	printStart(&b, colourTrue, false, info)
	out := ansiRe.ReplaceAllString(b.String(), "")
	if strings.ContainsAny(out, "\x1b\x07") {
		t.Fatalf("control characters reached the terminal: %q", out)
	}
	if !strings.Contains(out, "bad[2Jline") || !strings.Contains(out, "/tmp/a]0;xb") {
		t.Fatalf("text around the control characters lost: %q", out)
	}
}

func TestColourFor(t *testing.T) {
	for _, tc := range []struct {
		terminal bool
		env      map[string]string
		want     colour
	}{
		{false, nil, colourNone},
		{false, map[string]string{"COLORTERM": "truecolor"}, colourNone},
		{true, nil, colour256},
		{true, map[string]string{"COLORTERM": "truecolor"}, colourTrue},
		{true, map[string]string{"COLORTERM": "24bit"}, colourTrue},
		{true, map[string]string{"NO_COLOR": "1", "COLORTERM": "truecolor"}, colourNone},
		{true, map[string]string{"NO_COLOR": ""}, colour256},
		{true, map[string]string{"TERM": "dumb"}, colourNone},
		{true, map[string]string{"TERM": "xterm-256color"}, colour256},
	} {
		got := colourFor(tc.terminal, func(k string) string { return tc.env[k] })
		if got != tc.want {
			t.Errorf("terminal=%v env=%v: %d, want %d", tc.terminal, tc.env, got, tc.want)
		}
	}
}

func TestLightTerminal(t *testing.T) {
	for v, want := range map[string]bool{"": false, "15;0": false, "0;15": true, "0;7": true, "0;default;15": true, "7;0": false, "x": false} {
		if got := lightTerminal(func(string) string { return v }); got != want {
			t.Errorf("COLORFGBG=%q: %v, want %v", v, got, want)
		}
	}
}

func TestIsTerminalFalseForFileAndPipe(t *testing.T) {
	f, err := os.CreateTemp(t.TempDir(), "out")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = r.Close(); _ = w.Close() }()
	for _, x := range []*os.File{f, w} {
		if isTerminal(x) {
			t.Errorf("%s taken for a terminal", x.Name())
		}
	}
}

// Through run: plain by default, every line as before; with colour, the
// block, with the link alone on its line and printed once.
func TestRunPrintsPlainOrBlock(t *testing.T) {
	r := start(t, cfgWith(0, true))
	r.stop(t)
	out := r.out.String()
	if strings.Contains(out, "\x1b") {
		t.Fatalf("plain run wrote colour codes:\n%q", out)
	}
	if !strings.HasSuffix(out, "sitboard: "+r.link+"\nsitboard: press Ctrl-C to stop\n") {
		t.Fatalf("plain run's start lines changed:\n%s", out)
	}
	for _, l := range strings.Split(strings.TrimSuffix(out, "\n"), "\n") {
		if !strings.HasPrefix(l, "sitboard: ") {
			t.Fatalf("plain line without the prefix: %q", l)
		}
	}

	c := startIn(t, cfgWith(0, true), t.TempDir(), func(e *runEnv) { e.colour = colourTrue })
	c.stop(t)
	out = c.out.String()
	if n := strings.Count(out, c.token); n != 1 {
		t.Fatalf("token printed %d times:\n%s", n, out)
	}
	if !strings.Contains(out, "\n"+paint(newPalette(colourTrue, false).accent, c.link)+"\n") {
		t.Fatalf("block has no link line of its own:\n%q", out)
	}
	if !strings.Contains(ansiRe.ReplaceAllString(out, ""), "press Ctrl-C to stop\n") {
		t.Fatalf("block has no stop line:\n%q", out)
	}
}

// writeStartupGolden replaces one entry of startup_golden_test.go.
func writeStartupGolden(t *testing.T, name, got string) {
	t.Helper()
	const file = "startup_golden_test.go"
	src, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.SplitAfter(got, "\n")
	var b strings.Builder
	fmt.Fprintf(&b, "\t%q: \"\"", name)
	for _, l := range lines {
		if l != "" {
			fmt.Fprintf(&b, " +\n\t\t%q", l)
		}
	}
	b.WriteString(",\n")
	re := regexp.MustCompile(`(?s)\t` + regexp.QuoteMeta(strconv.Quote(name)) + `: "".*?,\n(\t"|\})`)
	m := re.FindSubmatchIndex(src)
	if m == nil {
		t.Fatalf("no entry %s in %s", name, file)
	}
	out := string(src[:m[0]]) + b.String() + string(src[m[2]:])
	if err := os.WriteFile(file, []byte(out), 0o644); err != nil {
		t.Fatal(err)
	}
}
