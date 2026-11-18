package core

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/IZZY-Agency/sitboard/internal/contract"
	"github.com/IZZY-Agency/sitboard/internal/model"
)

func artifactURL(n int) string {
	return fmt.Sprintf("https://claude.ai/artifact/exampleArtifact%04d", n)
}

func TestCardShowsArtifactsNewestFirst(t *testing.T) {
	b := newTestBoard(t)
	s := busySession("s1", tNow)
	s.Artifacts = []model.ArtifactRef{
		{URL: artifactURL(1), Title: "Placeholder old", PublishedAt: tNow.Add(-2 * time.Hour)},
		{URL: artifactURL(2), Title: "Placeholder new", PublishedAt: tNow.Add(-time.Minute)},
		{URL: artifactURL(3), Title: "Placeholder no time"},
		{URL: artifactURL(4), Title: "Placeholder middle", PublishedAt: tNow.Add(-time.Hour)},
	}
	b.applySync(s)
	got := onlyCard(t, b).Artifacts
	want := []contract.Artifact{
		{Title: "Placeholder new", URL: artifactURL(2)},
		{Title: "Placeholder middle", URL: artifactURL(4)},
		{Title: "Placeholder old", URL: artifactURL(1)},
		{Title: "Placeholder no time", URL: artifactURL(3)},
	}
	if !slices.Equal(got, want) {
		t.Fatalf("artifacts = %+v\nwant %+v", got, want)
	}
}

func TestCardArtifactsNeverNull(t *testing.T) {
	b := newTestBoard(t)
	b.applySync(busySession("s1", tNow))
	data, err := json.Marshal(onlyCard(t, b))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"artifacts":[]`) {
		t.Fatalf("a card without artifacts must carry an empty list: %s", data)
	}
}

// The adapter checks the address; the core checks it again, so nothing else
// ever reaches the page.
func TestArtifactWithAForeignAddressIsDropped(t *testing.T) {
	b := newTestBoard(t)
	s := busySession("s1", tNow)
	s.Artifacts = []model.ArtifactRef{
		{URL: "javascript:alert(1)", Title: "Placeholder"},
		{URL: "https://example.com/artifact/exampleArtifact0001", Title: "Placeholder"},
		{URL: artifactURL(1) + "#x", Title: "Placeholder"},
		{URL: artifactURL(2), Title: "Placeholder kept"},
	}
	b.applySync(s)
	got := onlyCard(t, b).Artifacts
	if len(got) != 1 || got[0].URL != artifactURL(2) {
		t.Fatalf("artifacts = %+v", got)
	}
}

func TestArtifactTitleIsCleaned(t *testing.T) {
	b := newTestBoard(t)
	s := busySession("s1", tNow)
	s.Artifacts = []model.ArtifactRef{
		{URL: artifactURL(1), Title: "\u202Eevil\x1b[31m<b>x</b>" + strings.Repeat("y", 1000)},
		{URL: artifactURL(2), Title: " \t "},
	}
	b.applySync(s)
	got := onlyCard(t, b).Artifacts
	if strings.ContainsAny(got[0].Title, "\u202E\x1b") {
		t.Fatalf("control characters kept: %q", got[0].Title[:20])
	}
	if n := len([]rune(got[0].Title)); n > maxText+1 {
		t.Fatalf("title has %d runes, want at most %d", n, maxText+1)
	}
	if got[1].Title != "" {
		t.Fatalf("a blank title is none: %q", got[1].Title)
	}
}

// Artifacts are shown, nothing more: they never move a card, retitle it or
// keep an ended session on the board.
func TestArtifactsDoNotChangeColumnOrTitle(t *testing.T) {
	b := newTestBoard(t)
	s := session("s1", model.StatusFinished, tNow)
	b.applySync(s)
	before := onlyCard(t, b)
	s.Artifacts = []model.ArtifactRef{{URL: artifactURL(1), Title: "Placeholder", PublishedAt: tNow}}
	b.applySync(s)
	after := onlyCard(t, b)
	if after.Column != before.Column || after.Title != before.Title || after.TitleSource != before.TitleSource {
		t.Fatalf("artifacts changed the card: %+v -> %+v", before, after)
	}
	s.Ended, s.Live, s.Status = true, model.Of(false), model.Unknown[model.Status]()
	b.applySync(s)
	if n := len(b.Snapshot().Cards); n != 0 {
		t.Fatalf("an ended session with only artifacts stayed on the board")
	}
}

// The fact the core keeps is its own copy.
func TestArtifactsAreCopiedOnApply(t *testing.T) {
	b := newTestBoard(t)
	s := busySession("s1", tNow)
	s.Artifacts = []model.ArtifactRef{{URL: artifactURL(1), Title: "Placeholder"}}
	b.applySync(s)
	s.Artifacts[0].URL = "javascript:alert(1)"
	if got := onlyCard(t, b).Artifacts; len(got) != 1 || got[0].URL != artifactURL(1) {
		t.Fatalf("artifacts = %+v", got)
	}
}

// clean drops zero-width spaces and byte-order marks, turns line and paragraph
// separators into spaces, and keeps the zero-width joiner of emoji sequences.
// It runs on every title, not only artifacts'.
func TestCleanDropsInvisibleCharacters(t *testing.T) {
	cases := map[string]string{
		"a\u200Bb":                 "ab",
		"\uFEFFa":                  "a",
		"a\u2028b":                 "a b",
		"a\u2029b":                 "a b",
		"\u2028\u200B\uFEFF\u2029": "",
		"family \U0001F468\u200D\U0001F469\u200D\U0001F467": "family \U0001F468\u200D\U0001F469\u200D\U0001F467",
	}
	for in, want := range cases {
		if got := clean(in); got != want {
			t.Errorf("clean(%q) = %q, want %q", in, got, want)
		}
	}
	b := newTestBoard(t)
	s := busySession("s1", tNow)
	s.Name = "worker\u200B-\uFEFFa"
	s.Artifacts = []model.ArtifactRef{{URL: artifactURL(1), Title: "Placeholder\u2028plan\u200B"}}
	b.applySync(s)
	c := onlyCard(t, b)
	if c.Agent.Name != "worker-a" || c.Artifacts[0].Title != "Placeholder plan" {
		t.Fatalf("name %q, artifact title %q", c.Agent.Name, c.Artifacts[0].Title)
	}
}
