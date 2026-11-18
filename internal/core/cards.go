package core

import (
	"cmp"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/IZZY-Agency/sitboard/internal/contract"
	"github.com/IZZY-Agency/sitboard/internal/model"
)

// keepEnded is the one place that decides whether ended sessions stay on the
// board. Proof of concept: they leave at once. The full rule (an ended session
// with pull requests stays in In review up to 7 days, or in Done for 24 hours
// after the last one closed) is already in column(); setting this to true
// switches it on.
var keepEnded = false

// maxText caps every free-text string the page receives. The page clips for
// layout; this keeps a hostile 10,000-character name out of every snapshot.
const maxText = 300

// deriveLocked builds the whole board from the stored facts. It returns the
// view and the card id -> session map. Callers hold b.mu.
func (b *Board) deriveLocked(now time.Time) (view, map[string]model.SessionKey) {
	v := view{
		cards:    []contract.Card{},
		projects: []contract.Project{},
		sources:  b.sources,
	}
	for _, s := range b.sources {
		if s.Kind == contract.SourceAgterm && s.State == contract.SourceOK {
			v.agterm = true
		}
	}
	for _, t := range b.terminals {
		if !t.HasAgent {
			v.plainShells++
		}
	}

	keys := map[string]model.SessionKey{}
	projects := map[string]*contract.Project{}
	mainRoots := map[string]string{} // project id -> main root
	plainLive := map[string]int{}    // plain folder -> agents sitting in it now
	var shown []model.SessionFact
	parents := b.parentsLocked()
	cyclic := cycles(parents)
	hints := b.workerHintsLocked(parents, cyclic)
	for k := range cyclic {
		parents[k] = model.Unknown[model.SessionKey]()
	}
	for _, s := range b.sessions {
		if !b.mayShowLocked(s) {
			continue
		}
		// A worker whose coordinator is not a live session on the board is
		// treated as top-level for the rules; its parent still shows as known.
		parent := parents[s.Key]
		ps, ok := b.sessions[parent.V]
		coordinated := parent.OK && ok && !ps.Ended && b.mayShowLocked(ps)
		card, project, ok := b.cardLocked(s, parent, coordinated, hints[s.Key], now)
		if !ok {
			continue
		}
		keys[card.ID] = s.Key
		v.cards = append(v.cards, card)
		if _, seen := projects[project.ID]; !seen {
			projects[project.ID] = &project
			mainRoots[project.ID] = b.repoLocked(s.Folder).MainRoot
		}
		shown = append(shown, s)
		if b.liveUmbrellaLocked(s) {
			plainLive[s.Folder]++
		}
	}

	// Coordinators: a parent link becomes a card id only when the parent has a card.
	byKey := map[model.SessionKey]string{}
	for id, k := range keys {
		byKey[k] = id
	}
	for i := range v.cards {
		if p := parents[keys[v.cards[i].ID]]; p.OK {
			v.cards[i].Agent.ParentCardID = byKey[p.V]
		}
	}

	v.umbrellas, v.groupings = b.groupLocked(projects, mainRoots, plainLive, b.qualifiedLocked(shown, parents, plainLive), now)

	for _, c := range v.cards {
		p := projects[c.ProjectID]
		count(&p.Counts, c)
		count(&v.counts, c)
	}
	for _, p := range projects {
		v.projects = append(v.projects, *p)
	}
	slices.SortFunc(v.projects, func(a, c contract.Project) int {
		return cmp.Or(cmp.Compare(a.Name, c.Name), cmp.Compare(a.ID, c.ID))
	})
	slices.SortFunc(v.cards, func(a, c contract.Card) int {
		return cmp.Or(
			cmp.Compare(slices.Index(contract.Columns, a.Column), slices.Index(contract.Columns, c.Column)),
			a.Agent.StartedAt.Compare(c.Agent.StartedAt),
			cmp.Compare(a.ID, c.ID),
		)
	})
	return v, keys
}

// mayShowLocked: a session can have a card when its id is valid, its folder
// is within the roots and git has answered for it, and it has not ended (see
// keepEnded). The column rules then decide. Callers hold b.mu.
func (b *Board) mayShowLocked(s model.SessionFact) bool {
	if _, err := contract.CardID(string(s.Key.Tool), s.Key.ID); err != nil {
		return false // an id outside the safe form never becomes a card
	}
	if !b.inRoots(s.Folder) || (s.Ended && !keepEnded) {
		return false
	}
	if s.Folder == "" {
		return true
	}
	_, resolved := b.repos[s.Folder]
	return resolved
}

func count(c *contract.Counts, card contract.Card) {
	switch card.Column {
	case contract.ColumnWorking:
		c.Working++
	case contract.ColumnNeedsYou:
		c.NeedsYou++
	case contract.ColumnInReview:
		c.InReview++
	}
	if slices.Contains(card.Attention, contract.AttentionStale) {
		c.Stale++
	}
}

// cardLocked builds one session's card and its project. ok is false when the
// session has no card. Callers hold b.mu.
func (b *Board) cardLocked(s model.SessionFact, parent model.Known[model.SessionKey], coordinated, workerHint bool, now time.Time) (contract.Card, contract.Project, bool) {
	term, hasTerm := b.terminals[s.TerminalID]
	hasTerm = hasTerm && s.TerminalID != ""
	termStatus := model.Unknown[model.Status]()
	if hasTerm {
		termStatus = term.Status
	}
	status := mergeStatus(s.Status, termStatus)
	repo := b.repoLocked(s.Folder)
	prs := b.linkedPRsLocked(s, repo)
	id, err := contract.CardID(string(s.Key.Tool), s.Key.ID)
	if err != nil {
		return contract.Card{}, contract.Project{}, false
	}

	seen := b.isSeenLocked(id, s, now)
	endedAt := s.EndedAt
	if endedAt.IsZero() {
		endedAt = s.LastActivity
	}
	in := cardInput{
		status:       status,
		ended:        s.Ended,
		endedAt:      endedAt,
		lastActivity: latest(s.LastActivity, s.StatusSince),
		resumedAt:    b.resumedAt,
		staleAfter:   b.opts.StaleAfter,
		prs:          prs,
		hasParent:    coordinated,
		seen:         seen,
	}
	col, attention, ok := column(in, now)
	if !ok {
		return contract.Card{}, contract.Project{}, false
	}
	if attention == nil {
		attention = []contract.Attention{}
	}

	mainRoot := repo.MainRoot
	project := contract.Project{
		ID:   contract.ProjectID(mainRoot),
		Name: clean(filepath.Base(mainRoot)),
		Path: b.homeRel(mainRoot),
		Repo: clean(repo.Slug),
	}
	if mainRoot == "" {
		project.Name = "unknown folder"
	}

	card := contract.Card{
		ID:           id,
		ProjectID:    project.ID,
		Column:       col,
		Attention:    attention,
		Seen:         seen,
		PullRequests: []contract.PullRequest{},
		Issues:       []contract.Issue{},
		Artifacts:    cardArtifacts(s.Artifacts),
		Subagents:    max(s.Subagents, 0),
		Agent: contract.Agent{
			Tool:         contract.Tool(s.Key.Tool),
			Name:         clean(s.Name),
			Model:        clean(s.Model.V),
			Status:       agentStatus(status, s.Ended),
			StatusSince:  clampPast(s.StatusSince, now).UTC(),
			StartedAt:    clampPast(s.StartedAt, now).UTC(),
			LastActivity: clampPast(s.LastActivity, now).UTC(),
			Live:         tri(s.Live),
			Folder:       b.homeRel(s.Folder),
			Parent:       parentState(parent, workerHint),
		},
	}
	if !s.Model.OK {
		card.Agent.Model = ""
	}
	if hasTerm {
		card.Agent.TerminalID = s.TerminalID
		card.Agent.Workspace = clean(term.Workspace)
		if card.Agent.Name == "" {
			card.Agent.Name = clean(term.Name)
		}
	}
	issues := map[contract.Issue]bool{}
	for _, pr := range prs {
		card.PullRequests = append(card.PullRequests, contract.PullRequest{
			Repo:   clean(pr.Repo),
			Number: pr.Number,
			Title:  clean(pr.Title),
			URL:    safeURL(pr.URL),
			State:  contract.PRState(pr.State),
			Checks: contract.Checks(pr.Checks),
			Review: contract.Review(pr.Review),
			Draft:  pr.Draft,
		})
		if pr.State != model.PRStateOpen {
			continue // a closed one never will close them; a merged one already did
		}
		for _, n := range pr.ClosingIssues {
			is := contract.Issue{Repo: clean(pr.Repo), Number: n, URL: issueURL(pr.Repo, n)}
			if !issues[is] {
				issues[is] = true
				card.Issues = append(card.Issues, is)
			}
		}
	}
	card.Title, card.TitleSource = title(prs, s, term, hasTerm)
	return card, project, true
}

// title picks the card's title. Never prompt text. In order:
//  1. the title of the session's most recently opened open pull request;
//  2. the terminal's title (the agent tool's own summary of the work, without
//     its leading status glyph), when it says more than the session name and
//     is not just a folder path;
//  3. the session's name, then its terminal's name;
//  4. the folder name.
//
// A session whose pull requests are all merged or closed shows its own title.
func title(prs []model.PullRequest, s model.SessionFact, term model.TerminalFact, hasTerm bool) (string, contract.TitleSource) {
	if pr, ok := newestOpenPR(prs); ok && clean(pr.Title) != "" {
		return clean(pr.Title), contract.TitleFromPR
	}
	name := clean(s.Name)
	if hasTerm {
		if t := terminalTitle(term.Title); t != "" && t != name && !isBarePath(t) {
			return t, contract.TitleFromSession
		}
	}
	if name != "" {
		return name, contract.TitleFromSession
	}
	if hasTerm {
		if n := clean(term.Name); n != "" {
			return n, contract.TitleFromSession
		}
	}
	if s.Folder == "" {
		return "", contract.TitleFromFolder
	}
	return clean(filepath.Base(s.Folder)), contract.TitleFromFolder
}

// terminalTitle drops the status glyph an agent tool puts in front of its
// terminal title, and the spaces after it. Only known status marks are
// removed, so a real title that starts with an emoji or a currency sign stays.
func terminalTitle(raw string) string {
	return strings.TrimLeftFunc(clean(raw), func(r rune) bool {
		return unicode.IsSpace(r) || isStatusGlyph(r)
	})
}

// statusGlyphs are the marks agent tools and the terminal app show in front of
// a title: stars and spinner stars, half-circle marks, bullets, dots, ellipsis.
const statusGlyphs = "\u2733\u2736\u273B\u273D\u2722\u2726\u2727\u2605\u2606" +
	"\u25D0\u25D1\u25D2\u25D3\u25D6\u25D7\u25CF\u25CB" +
	"\u2022\u00B7\u2026"

func isStatusGlyph(r rune) bool {
	return strings.ContainsRune(statusGlyphs, r) || (r >= '\u2800' && r <= '\u28FF') // braille spinner frames
}

// isBarePath: a title that is only a folder path, as a shell sets it.
func isBarePath(t string) bool {
	return (strings.HasPrefix(t, "/") || strings.HasPrefix(t, "~")) && !strings.ContainsAny(t, " \t")
}

// newestOpenPR is the most recently opened open pull request.
func newestOpenPR(prs []model.PullRequest) (model.PullRequest, bool) {
	var best model.PullRequest
	found := false
	for _, pr := range prs {
		if pr.State != model.PRStateOpen {
			continue
		}
		if !found || newerOpened(pr, best) {
			best, found = pr, true
		}
	}
	return best, found
}

// newerOpened orders by opening time, then by number when times are equal or unknown.
func newerOpened(a, c model.PullRequest) bool {
	if !a.OpenedAt.Equal(c.OpenedAt) {
		return a.OpenedAt.After(c.OpenedAt)
	}
	return a.Number > c.Number
}

// prOrder puts open pull requests first, newest opened first, then merged and
// closed ones, most recently closed first, so the page can show the first few
// and count the rest.
func prOrder(a, c model.PullRequest) int {
	ao, co := a.State == model.PRStateOpen, c.State == model.PRStateOpen
	switch {
	case ao && !co:
		return -1
	case !ao && co:
		return 1
	case ao:
		if newerOpened(a, c) {
			return -1
		}
		if newerOpened(c, a) {
			return 1
		}
	default:
		if r := c.ClosedAt.Compare(a.ClosedAt); r != 0 {
			return r
		}
		if r := cmp.Compare(c.Number, a.Number); r != 0 {
			return r
		}
	}
	return cmp.Compare(a.Repo, c.Repo)
}

// cardArtifacts are the session's artifacts for the page: only addresses that
// pass the check, most recently published first (an unknown time last), with
// clean titles.
func cardArtifacts(refs []model.ArtifactRef) []contract.Artifact {
	refs = slices.Clone(refs)
	slices.SortStableFunc(refs, func(a, c model.ArtifactRef) int {
		if a.PublishedAt.IsZero() != c.PublishedAt.IsZero() {
			if a.PublishedAt.IsZero() {
				return 1
			}
			return -1
		}
		return c.PublishedAt.Compare(a.PublishedAt)
	})
	out := []contract.Artifact{}
	for _, r := range refs {
		if model.ValidArtifactURL(r.URL) {
			out = append(out, contract.Artifact{Title: clean(r.Title), URL: r.URL})
		}
	}
	return out
}

// repoLocked is what git said about a folder, or the folder itself.
func (b *Board) repoLocked(folder string) model.Repo {
	if r, ok := b.repos[folder]; ok {
		return r
	}
	return model.Repo{Root: folder, MainRoot: folder}
}

// linkedPRsLocked returns the pull requests GitHub answered for: those the
// session created, and for Codex those whose head branch is the session's
// branch. Refs GitHub has not answered for are left out, never guessed.
func (b *Board) linkedPRsLocked(s model.SessionFact, repo model.Repo) []model.PullRequest {
	seen := map[model.PRRef]bool{}
	var out []model.PullRequest
	add := func(pr model.PullRequest) {
		ref := model.PRRef{Repo: pr.Repo, Number: pr.Number}
		if !seen[ref] {
			seen[ref] = true
			out = append(out, pr)
		}
	}
	for _, ref := range s.CreatedPRs {
		if pr, ok := b.prs[ref]; ok {
			add(pr)
		}
	}
	if s.Key.Tool == model.ToolCodex && repo.Slug != "" && repo.Branch.OK {
		for _, pr := range b.branchPRs[repo.Slug][repo.Branch.V] {
			add(pr)
		}
	}
	slices.SortStableFunc(out, prOrder)
	return out
}

func (b *Board) inRoots(folder string) bool {
	if len(b.roots) == 0 {
		return true
	}
	if folder == "" {
		return false
	}
	for _, r := range b.roots {
		if under(filepath.Clean(folder), r) {
			return true
		}
	}
	return false
}

func under(p, root string) bool {
	rel, err := filepath.Rel(root, p)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// homeRel shows a path relative to the home folder ("~/..."). A path outside
// the home folder is shown by its base name only, never as an absolute path.
func (b *Board) homeRel(p string) string {
	if p == "" {
		return ""
	}
	p = filepath.Clean(p)
	if b.home == "" || b.home == "." || b.home == "/" || !under(p, b.home) {
		if base := filepath.Base(p); base != "/" && base != "." {
			return clean(base)
		}
		return ""
	}
	if p == b.home {
		return "~"
	}
	rel, err := filepath.Rel(b.home, p)
	if err != nil {
		return ""
	}
	return clean("~/" + filepath.ToSlash(rel))
}

func tri(k model.Known[bool]) contract.Tri {
	switch {
	case !k.OK:
		return contract.TriUnknown
	case k.V:
		return contract.Yes
	default:
		return contract.No
	}
}

// parentState says whether the card has a coordinator: "unknown" only when
// none was matched and there is a sign it is a worker (workerHintsLocked).
func parentState(parent model.Known[model.SessionKey], workerHint bool) contract.ParentState {
	switch {
	case parent.OK:
		return contract.ParentKnown
	case workerHint:
		return contract.ParentUnknown
	default:
		return contract.ParentNone
	}
}

func latest(a, c time.Time) time.Time {
	if c.After(a) {
		return c
	}
	return a
}

// clean makes an untrusted string safe to carry: valid UTF-8, no control,
// bidirectional-override, zero-width-space or byte-order-mark characters
// (line and paragraph separators become spaces, like a newline), at most
// maxText runes. The zero-width joiner stays: emoji sequences need it.
func clean(s string) string {
	if s == "" {
		return ""
	}
	var sb strings.Builder
	n := 0
	for _, r := range strings.ToValidUTF8(s, "�") {
		if unicode.IsControl(r) || isBidiControl(r) || r == '\u200B' || r == '\uFEFF' || r == '\u2028' || r == '\u2029' {
			if r == '\t' || r == '\n' || r == '\r' || r == '\u2028' || r == '\u2029' {
				r = ' '
			} else {
				continue
			}
		}
		if n == maxText {
			sb.WriteString("…")
			break
		}
		sb.WriteRune(r)
		n++
	}
	return strings.TrimSpace(sb.String())
}

func isBidiControl(r rune) bool {
	return r == '\u061C' || r == '\u200E' || r == '\u200F' ||
		(r >= '\u202A' && r <= '\u202E') || (r >= '\u2066' && r <= '\u2069')
}

// safeURL passes only https links to the page.
func safeURL(u string) string {
	if !strings.HasPrefix(u, "https://") || !utf8.ValidString(u) || strings.ContainsAny(u, " \t\r\n\"'<>\\") {
		return ""
	}
	return u
}

func issueURL(repo string, n int) string {
	if strings.Count(repo, "/") != 1 || strings.ContainsAny(repo, " \t\r\n\"'<>\\?#") {
		return ""
	}
	return fmt.Sprintf("https://github.com/%s/issues/%d", repo, n)
}
