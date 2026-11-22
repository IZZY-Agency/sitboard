package core

import (
	"cmp"
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

	v.settings = b.settingsLocked()
	if b.leftovers.State == contract.LeftoversReady {
		v.leftovers = contract.LeftoversSummary{Items: len(b.leftovers.Items), Partial: b.leftovers.Partial}
		for _, h := range b.leftovers.Held {
			v.leftovers.Held += h.Count
		}
	}
	v.spend = b.spendLocked(now)
	v.gitlabHosts = b.gitlabHostsLocked()
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
	prs := b.linkedPRsLocked(s)
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
		Name: projectName(mainRoot),
		Path: b.homeRel(mainRoot),
	}
	// A path too long for the page is left out with its forge and host: a
	// project names its repository whole or not at all.
	if remote := b.shownRemoteLocked(repo.Remote); remote.Valid() && pagePath(remote.Path) != "" {
		path := pagePath(remote.Path)
		project.Repo = path
		project.RepoForge = contract.Forge(repo.Remote.Forge)
		project.RepoHost = repo.Remote.Host
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
		Spend:        cardSpend(s.Spend),
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
	card.Agent.Effort, card.Agent.EffortSource = agentEffort(s)
	card.Agent.EffortWord = effortWord(s)
	card.Agent.Jump = jumpKind(s, hasTerm)
	if hasTerm {
		card.Agent.TerminalID = s.TerminalID
		card.Agent.Workspace = clean(term.Workspace)
		if card.Agent.Name == "" {
			card.Agent.Name = clean(term.Name)
		}
	}
	issues := map[string]bool{} // by the full identity, so two paths too long to show are still two issues
	for _, pr := range prs {
		card.PullRequests = append(card.PullRequests, pagePR(pr))
		if pr.State != model.PRStateOpen {
			continue // a closed one never will close them; a merged one already did
		}
		for _, n := range pr.ClosingIssues {
			id := model.PRRef{Repo: pr.Repo, Number: n}.Key()
			if issues[id] {
				continue
			}
			issues[id] = true
			card.Issues = append(card.Issues, pageIssue(pr.Repo, n))
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
	return cmp.Compare(a.Repo.Key(), c.Repo.Key())
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

// projectName is how a project is named on the page: its main root's base
// name, cleaned.
func projectName(mainRoot string) string {
	if mainRoot == "" {
		return "unknown folder"
	}
	return clean(filepath.Base(mainRoot))
}

// repoLocked is what git said about a folder, or the folder itself.
func (b *Board) repoLocked(folder string) model.Repo {
	if r, ok := b.repos[folder]; ok {
		return r
	}
	return model.Repo{Root: folder, MainRoot: folder}
}

// linkedPRsLocked returns the pull requests GitHub answered for that the
// session created (Claude and Codex alike: Codex's come from gh pr create).
// A branch alone links nothing: a reviewer working in the author's worktree
// is on the author's branch. Refs GitHub has not answered for are left out,
// never guessed.
func (b *Board) linkedPRsLocked(s model.SessionFact) []model.PullRequest {
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

// pagePR is a pull request as the page receives it, on a card or in
// catch-up: cleaned text, and an address built for its own repo and number.
// pagePath is a repository path as the page gets it: whole, or "" when it is
// longer than the page's bound. A path cut at the bound could name another
// repository, so it is never cut, and its links go with it (prURL, pageIssue).
func pagePath(p string) string {
	if utf8.RuneCountInString(p) > maxText {
		return ""
	}
	return clean(p)
}

// pageIssue is issue n of r for the page: the forge and host, the path whole
// (or none), and the address built for it (none when the path is none).
func pageIssue(r model.ForgeRepo, n int) contract.Issue {
	is := contract.Issue{Forge: contract.Forge(r.Forge), Host: clean(r.Host), Repo: pagePath(r.Path), Number: n}
	if is.Repo != "" {
		is.URL = model.ForgeIssueURL(r, n)
	}
	return is
}

func pagePR(pr model.PullRequest) contract.PullRequest {
	return contract.PullRequest{
		Forge:  contract.Forge(pr.Repo.Forge),
		Host:   clean(pr.Repo.Host),
		Repo:   pagePath(pr.Repo.Path),
		Number: pr.Number,
		Title:  clean(pr.Title),
		URL:    prURL(pr),
		State:  contract.PRState(pr.State),
		Checks: contract.Checks(pr.Checks),
		Review: contract.Review(pr.Review),
		Draft:  pr.Draft,
	}
}

// prURL passes a pull request's address to the page only when it is the one
// model.ChangeRequestURL builds for that pull request's own forge, host, repo
// and number: the pattern holds, and the link cannot point at another pull
// request.
func prURL(pr model.PullRequest) string {
	if u := model.ChangeRequestURL(pr.Repo, pr.Number); u != "" && u == pr.URL && pagePath(pr.Repo.Path) != "" {
		return u
	}
	return ""
}

// showEffortWords: effort words that are not on the ramp (Codex's "ultra")
// are shown as written. The owner has not yet ruled whether they show at all
// (#93); false hides them, and the card says effort unknown.
const showEffortWords = true

// effortWord is the effort word the session recorded when it is not one of
// the levels: lowercase letters only, at most 16; "" otherwise.
func effortWord(s model.SessionFact) string {
	w := s.Effort.V
	if !showEffortWords || !s.Effort.OK || model.KnownEffort(w) != "" || w == "" || len(w) > 16 {
		return ""
	}
	for _, c := range []byte(w) {
		if c < 'a' || c > 'z' {
			return ""
		}
	}
	return w
}

// agentEffort is the card's effort level and where it came from. Only the
// known levels pass; anything else is no level.
func agentEffort(s model.SessionFact) (contract.Effort, contract.EffortSource) {
	e := model.KnownEffort(s.Effort.V)
	if !s.Effort.OK || e == "" {
		return contract.EffortUnknown, contract.EffortSourceNone
	}
	if s.EffortAtLaunch {
		return contract.Effort(e), contract.EffortSourceLaunch
	}
	return contract.Effort(e), contract.EffortSourceRecorded
}

// jumpKind is where a jump to s goes: its tmux pane when it runs in tmux
// (even inside agterm: tmux knows the pane, and raises the agterm session
// showing it), else its agterm session when that is in the current tree.
func jumpKind(s model.SessionFact, hasTerm bool) contract.JumpKind {
	switch {
	case s.Tmux.Valid():
		return contract.JumpTmux
	case hasTerm:
		return contract.JumpAgterm
	}
	return contract.JumpNone
}

// gitlabHostsLocked lists the hosts of every GitLab merge request the board
// knows (on a card or not, so a catch-up item's link passes too), sorted;
// never nil. The page links a GitLab address only on one of these hosts.
func (b *Board) gitlabHostsLocked() []string {
	hosts := []string{}
	for ref := range b.prs {
		if ref.Repo.Forge == model.ForgeGitLab && ref.Repo.Valid() && !slices.Contains(hosts, ref.Repo.Host) {
			hosts = append(hosts, ref.Repo.Host)
		}
	}
	slices.Sort(hosts)
	return hosts
}
