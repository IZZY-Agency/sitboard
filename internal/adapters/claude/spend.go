package claude

import (
	"cmp"
	"encoding/json"
	"hash/fnv"
	"io"
	"math"
	"os"
	"reflect"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/IZZY-Agency/sitboard/internal/model"
)

// Spend. Every assistant line carries its reply's token counts in
// message.usage. Claude Code writes one reply as several lines, one per
// content block, each with the same message.id (replyClaims remembers the ids
// and what was counted of each). The counts can grow from one copy
// to the next (output tokens, web searches: the reply was still streaming),
// so each count is the largest of the copies, which is the last one's: on
// this machine 7,760 of about 260,000 replies grew, and counting the first
// copy left out 4.7% of the output tokens. When a session
// exits Claude Code writes a cost-state line with its own estimate,
// totalCostUSD, which also covers in-process subagents and the background
// calls that leave no transcript line. Seen on 2.1.319 to 2.1.329.
//
// The cost is an estimate at API list price, never a bill. A session whose
// last reply came after its last cost-state line (it is running, or it was
// resumed) is priced here, from the counted tokens and Claude Code's own price
// table, the additionalModelCostsCache key of <Home>/.claude.json. Nothing
// else in that file is kept.

// usageJSON is message.usage on an assistant line.
type usageJSON struct {
	Input         int64 `json:"input_tokens"`
	Output        int64 `json:"output_tokens"`
	CacheRead     int64 `json:"cache_read_input_tokens"`
	CacheCreation int64 `json:"cache_creation_input_tokens"`
	Split         *struct {
		Write5m int64 `json:"ephemeral_5m_input_tokens"`
		Write1h int64 `json:"ephemeral_1h_input_tokens"`
	} `json:"cache_creation"`
	Server *struct {
		WebSearch int64 `json:"web_search_requests"`
	} `json:"server_tool_use"`
	Speed string `json:"speed"`
	// Iterations: one entry per model call the reply took. Seen only with
	// one entry of type "message" whose counts are the top-level ones
	// (492,547 of 492,588 lines on this machine; the rest an early copy of
	// a reply whose entry ran ahead). Any other shape is counted from the
	// top level and marked as not priced whole.
	Iterations []struct {
		Type  string `json:"type"`
		Model string `json:"model"`
	} `json:"iterations"`
}

// costStateLine is the cost-state line Claude Code writes when a session exits.
type costStateLine struct {
	SessionID  string   `json:"sessionId"`
	TotalUSD   *float64 `json:"totalCostUSD"`
	Unknown    bool     `json:"hasUnknownModelCost"`
	ModelUsage map[string]struct {
		Input      int64 `json:"inputTokens"`
		Output     int64 `json:"outputTokens"`
		CacheRead  int64 `json:"cacheReadInputTokens"`
		CacheWrite int64 `json:"cacheCreationInputTokens"`
	} `json:"modelUsage"`
}

// usage is what is counted of one reply, or a sum of them.
type usage struct {
	in, out, read, write5m, write1h, web int64
	// unpriced counts the replies in it that cannot be priced whole: their
	// tokens are counted, and the sum is marked partly priced.
	unpriced int64
}

// most is the larger of each count.
func (u usage) most(v usage) usage {
	return usage{max(u.in, v.in), max(u.out, v.out), max(u.read, v.read), max(u.write5m, v.write5m), max(u.write1h, v.write1h), max(u.web, v.web), max(u.unpriced, v.unpriced)}
}

func (u usage) sub(v usage) usage {
	return usage{u.in - v.in, u.out - v.out, u.read - v.read, u.write5m - v.write5m, u.write1h - v.write1h, u.web - v.web, u.unpriced - v.unpriced}
}

func (u usage) add(v usage) usage {
	return usage{u.in + v.in, u.out + v.out, u.read + v.read, u.write5m + v.write5m, u.write1h + v.write1h, u.web + v.web, u.unpriced + v.unpriced}
}

// prompt is the size of the reply's prompt, as Claude Code counts it for the
// long-prompt rates.
func (u usage) prompt() int64 { return u.in + u.read + u.write5m + u.write1h }

func (u usage) tokens() model.Tokens {
	return model.Tokens{Input: u.in, Output: u.out, CacheRead: u.read, CacheWrite: u.write5m + u.write1h}
}

// maxCount caps one reply's count; anything larger is not a real count.
const maxCount = 1 << 40

func count(n int64) int64 {
	if n < 0 || n > maxCount {
		return 0
	}
	return n
}

// speedKnown reports whether the rates of a reply's speed are known.
func speedKnown(s string) bool { return s == "" || s == "standard" || s == "fast" }

// usage is what is counted of a reply of model m.
func (j *usageJSON) usage(m string) usage {
	u := usage{in: count(j.Input), out: count(j.Output), read: count(j.CacheRead)}
	if !speedKnown(j.Speed) {
		u.unpriced = 1
	}
	if n := len(j.Iterations); n > 1 || n == 1 && (j.Iterations[0].Type != "message" && j.Iterations[0].Type != "" ||
		j.Iterations[0].Model != "" && j.Iterations[0].Model != m) {
		u.unpriced = 1
	}
	if j.Split != nil {
		u.write5m, u.write1h = count(j.Split.Write5m), count(j.Split.Write1h)
	} else {
		u.write5m = count(j.CacheCreation)
	}
	if j.Server != nil {
		u.web = count(j.Server.WebSearch)
	}
	return u
}

// tier is the rate card a reply is priced at. A reply whose prompt (input,
// cache read and cache write) passes a model's abovePromptTokens is priced
// whole at that model's long-prompt rates. The tier is chosen when the reply
// is counted, from the price table of that moment; tierUnknown means there
// was no entry for the model then.
type tier uint8

const (
	tierBase tier = iota
	tierLong
	tierUnknown
)

// priceKey is what a reply's price depends on.
type priceKey struct {
	model string
	fast  bool
	tier  tier
	// noPrice: the use has no rates (a speed that is not known; a subagent's
	// reply of unknown time that may be in the recorded total already): it
	// is counted in tokens and adds no dollars.
	noPrice bool
}

type spanKey struct {
	at time.Time
	priceKey
}

// Caps on what one transcript's spend may hold.
const (
	maxSpendModels = 64
	maxSpendSpans  = 16384
)

// replyClaims remembers which of one session's transcripts counted a reply,
// by message id, and what of it, so a reply that more than one of them holds
// is counted once: a subagent that inherits the conversation gets copies of
// its replies, with the session's id on them. A later copy, wherever it is
// (another reply may have come between, or it is in another transcript of the
// session), that carries more usage raises what was counted, in the log that
// counted it. A transcript counts only the lines that carry its own session's
// id (spendReply), and a fork's copies are marked (forked), so a copy in
// another session's transcript is never counted there and the claims need not
// reach across sessions: what a session counts does not depend on which other
// sessions there are or the order they are read in. Within the session the
// main transcript is read first, then the subagents' by name. Only a hash of
// the id is kept. The claims live as long as the session's tailers, and are
// used by the indexer's one goroutine.
type replyClaims struct {
	by map[uint64]*claim
}

// claim is a reply some transcript of the session counted.
type claim struct {
	owner uint32 // the transcript that counted it
	counted
}

// maxClaims caps the ids one session remembers (a claim is about 250 bytes);
// past it, replies are counted without the check (a session would need a
// quarter of a million replies).
const maxClaims = 1 << 18

func newReplyClaims() *replyClaims {
	return &replyClaims{by: map[uint64]*claim{}}
}

// take returns the claim of a reply and whether it is new: owner may count a
// reply no transcript of the session has, this one included (a reply id seen
// again further down the same transcript is the same reply). The new claim is
// filled by the one that counts the reply.
func (c *replyClaims) take(id string, owner uint32) (*claim, bool) {
	h := fnv.New64a()
	_, _ = h.Write([]byte(id))
	k := h.Sum64()
	if cl, ok := c.by[k]; ok {
		return cl, false
	}
	cl := &claim{owner: owner}
	if len(c.by) < maxClaims {
		c.by[k] = cl
	}
	return cl, true
}

// release forgets what owner counted: its transcript is read again from the
// start, or no longer read.
func (c *replyClaims) release(owner uint32) {
	for k, cl := range c.by {
		if cl.owner == owner {
			delete(c.by, k)
		}
	}
}

// counted is where a reply was counted and what of it, so that a later copy
// adds what it grew by in the same places.
type counted struct {
	log               *spendLog
	on, spanOn, since bool
	gen               uint64 // log.costGen when it was counted
	u                 usage
	k, sinceK         priceKey
	span              spanKey
}

// grow takes a later copy of the reply: what it grew by is added where the
// reply was counted. A copy never lowers a count.
func (c *counted) grow(u usage) {
	if !c.on || c.log.total == nil {
		return
	}
	m := c.u.most(u)
	d := m.sub(c.u)
	if d == (usage{}) {
		return
	}
	c.u = m
	l := c.log
	l.total[c.k] = l.total[c.k].add(d)
	if c.since && l.costGen == c.gen && l.since != nil { // not after another cost-state line
		l.since[c.sinceK] = l.since[c.sinceK].add(d)
	}
	if _, ok := l.spans[c.span]; c.spanOn && ok {
		l.spans[c.span] = l.spans[c.span].add(d)
	}
}

// recordedCost is the last cost-state line of a transcript.
type recordedCost struct {
	usd     float64
	unknown bool
	tokens  usage
}

// spendLog is what one transcript says about spend.
type spendLog struct {
	total    map[priceKey]usage
	spans    map[spanKey]usage
	recorded *recordedCost
	// after: a reply was counted after the last cost-state line, and since
	// is the usage counted after it: what a resumed session adds to its
	// restored total. In the main transcript that is the replies below the
	// line. resumeAt is the time of the first reply below it, when the
	// session was resumed: nothing of the session runs between its exit (the
	// line) and then. costGen tells the cost-state lines apart.
	after    bool
	since    map[priceKey]usage
	resumeAt time.Time
	costGen  uint64
	// sub: a subagent's transcript (or another of the session's, see
	// index), which has no cost-state line of its own: a reply of it is
	// after the main transcript's line when it is not before resumeAt,
	// copied from the main transcript by follow. costOn: the main transcript
	// has a cost-state line.
	sub    bool
	costOn bool
	// cut: spans before it are not kept (they would be pruned at the end of
	// the read, and could fill the cap before the current week is reached).
	cut time.Time
}

// reset empties the log; costGen goes on counting, so that a subagent's log
// that followed the old reading does not take the new one's lines for it.
func (l *spendLog) reset() { *l = spendLog{sub: l.sub, costGen: l.costGen + 1} }

// follow takes the main transcript's last cost-state line, for a subagent's
// log. A new line covers every reply read so far: the session had exited.
func (l *spendLog) follow(m *spendLog) {
	if l.costGen != m.costGen {
		l.costGen, l.after, l.since = m.costGen, false, nil
	}
	l.costOn, l.resumeAt = m.recorded != nil, m.resumeAt
}

// reply counts one assistant line's usage, at the rates of the price table
// of the moment (nil: none was read), and says where it was counted.
func (l *spendLog) reply(m string, j *usageJSON, at time.Time, tab priceTable) counted {
	u := j.usage(m)
	k := priceKey{model: m, fast: j.Speed == "fast", tier: tab.tierOf(m, u.prompt()), noPrice: !speedKnown(j.Speed)}
	if l.total == nil {
		l.total, l.spans = map[priceKey]usage{}, map[spanKey]usage{}
	}
	if _, ok := l.total[k]; !ok && len(l.total) >= maxSpendModels {
		return counted{}
	}
	l.total[k] = l.total[k].add(u)
	c := counted{log: l, on: true, gen: l.costGen, u: u, k: k}
	if l.recorded != nil && l.resumeAt.IsZero() && !at.IsZero() {
		l.resumeAt = at
	}
	if l.afterCost(at) {
		su, sk := u, k
		if at.IsZero() && l.sub {
			su.unpriced++ // when it came is not known: counted, adds no dollars
			sk.noPrice = true
		}
		l.after = true
		if l.since == nil {
			l.since = map[priceKey]usage{}
		}
		l.since[sk] = l.since[sk].add(su)
		c.since, c.sinceK = true, sk
	}
	if at.IsZero() {
		return c
	}
	sp := at.UTC().Truncate(model.SpendSpanLength)
	if sp.Before(l.cut) {
		return c
	}
	hk := spanKey{at: sp, priceKey: k}
	if _, ok := l.spans[hk]; !ok && len(l.spans) >= maxSpendSpans {
		return c
	}
	l.spans[hk] = l.spans[hk].add(u)
	c.spanOn, c.span = true, hk
	return c
}

// afterCost reports whether a reply at this time is after the session's last
// cost-state line: below it, in the main transcript; for a subagent's, not
// before the session was resumed, and also when its time is not known and
// the session was resumed.
func (l *spendLog) afterCost(at time.Time) bool {
	if !l.sub {
		return l.recorded != nil
	}
	return l.costOn && !l.resumeAt.IsZero() && (at.IsZero() || !at.Before(l.resumeAt))
}

// costState keeps a cost-state line.
func (l *spendLog) costState(c *costStateLine) {
	if c.TotalUSD == nil || !finite(*c.TotalUSD) || *c.TotalUSD < 0 {
		return
	}
	r := &recordedCost{usd: *c.TotalUSD, unknown: c.Unknown}
	for _, m := range c.ModelUsage {
		r.tokens = r.tokens.add(usage{in: count(m.Input), out: count(m.Output), read: count(m.CacheRead), write5m: count(m.CacheWrite)})
	}
	l.recorded, l.after, l.since, l.resumeAt = r, false, nil, time.Time{}
	l.costGen++
}

// prune drops the spans before cut.
func (l *spendLog) prune(cut time.Time) bool {
	n := len(l.spans)
	for k := range l.spans {
		if k.at.Before(cut) {
			delete(l.spans, k)
		}
	}
	return len(l.spans) != n
}

func finite(f float64) bool { return !math.IsNaN(f) && !math.IsInf(f, 0) }

// price is one model's list price: US dollars per million tokens, and per
// web search request.
type price struct {
	In      *float64 `json:"inputTokens"`
	Out     *float64 `json:"outputTokens"`
	Write5m *float64 `json:"promptCacheWriteTokens"`
	Write1h *float64 `json:"promptCacheWrite1hTokens"`
	Read    *float64 `json:"promptCacheReadTokens"`
	Web     *float64 `json:"webSearchRequests"`
	Fast    *price   `json:"fast"`
	// Long is the long-prompt rate card: it has the same keys, and
	// abovePromptTokens, the prompt size above which it applies.
	Long  *price `json:"longPrompt"`
	Above *int64 `json:"abovePromptTokens"`
}

// priceTable is Claude Code's price table, by model id.
type priceTable map[string]price

// rate is one price, or false when it is missing or not a plausible price.
func rate(p *float64) (float64, bool) {
	if p == nil || !finite(*p) || *p < 0 || *p > 1e4 {
		return 0, false
	}
	return *p, true
}

// canonModel reduces a model id to the form the price table is keyed by: no
// provider prefix or suffix (a cloud provider's "anthropic." and version
// parts), no "[1m]" and no trailing date.
func canonModel(m string) string {
	if i := strings.LastIndex(m, "/"); i >= 0 {
		m = m[i+1:]
	}
	if i := strings.LastIndex(m, "anthropic."); i >= 0 {
		m = m[i+len("anthropic."):]
	}
	if i := strings.IndexAny(m, "@:"); i >= 0 {
		m = m[:i]
	}
	m = strings.TrimSuffix(m, "[1m]")
	if i := strings.LastIndex(m, "-v"); i > 0 && i+2 < len(m) && allDigits(m[i+2:]) {
		m = m[:i]
	}
	if n := len(m); n > 9 && m[n-9] == '-' && allDigits(m[n-8:]) {
		m = m[:n-9]
	}
	return m
}

func allDigits(s string) bool {
	for _, c := range s {
		if c < '0' || c > '9' {
			return false
		}
	}
	return s != ""
}

// lookup finds a model's entry: by its id, else by its canonical id (the
// table can key a dated id, and a reply can carry an undated one). Entries
// whose canonical ids match and whose prices differ leave the model without
// a price: which one Claude Code meant is not known.
func (t priceTable) lookup(m string) (price, bool) {
	if p, ok := t[m]; ok {
		return p, true
	}
	c := canonModel(m)
	if p, ok := t[c]; ok {
		return p, true
	}
	var found *price
	for k, p := range t {
		if canonModel(k) != c {
			continue
		}
		if found != nil && !reflect.DeepEqual(*found, p) {
			return price{}, false
		}
		found = &p
	}
	if found == nil {
		return price{}, false
	}
	return *found, true
}

// longAbove is the prompt size above which an entry's long-prompt rates
// apply, or false when it has none.
func (p price) longAbove() (int64, bool) {
	if p.Long == nil || p.Long.Above == nil || *p.Long.Above <= 0 {
		return 0, false
	}
	return *p.Long.Above, true
}

// tierOf is the tier of a reply of this model with a prompt of this size.
func (t priceTable) tierOf(m string, prompt int64) tier {
	p, ok := t.lookup(m)
	if !ok {
		return tierUnknown
	}
	if above, ok := p.longAbove(); ok && prompt > above {
		return tierLong
	}
	return tierBase
}

// resolve gives the rate card for a tier: the long-prompt one and the fast
// one are each used whole, with the entry's web search rate when theirs is
// missing.
func (p price) resolve(fast, long bool) (price, bool) {
	if long {
		if p.Long == nil {
			return price{}, false
		}
		l := *p.Long
		if l.Web == nil {
			l.Web = p.Web
		}
		p = l
	}
	if fast {
		if p.Fast == nil {
			return price{}, false
		}
		f := *p.Fast
		if f.Web == nil {
			f.Web = p.Web
		}
		p = f
	}
	return p, true
}

// cost prices the parts of u that have a price, and reports whether every
// part does: a part with no rate leaves the rest priced (shown as "+
// unpriced"), and a part that was not used needs no rate.
func (t priceTable) cost(k priceKey, u usage) (float64, bool) {
	if k.noPrice {
		return 0, false
	}
	p, ok := t.lookup(k.model)
	if !ok {
		return 0, false
	}
	if k.tier == tierUnknown {
		// Counted with no entry for the model: its tier is not known, which
		// matters only for a model that has a long-prompt rate card now.
		if _, long := p.longAbove(); long {
			return 0, false
		}
	}
	p, ok = p.resolve(k.fast, k.tier == tierLong)
	if !ok {
		return 0, false
	}
	var usd float64
	all := u.unpriced == 0
	for _, part := range []struct {
		n   int64
		p   *float64
		per float64
	}{
		{u.in, p.In, 1e6},
		{u.out, p.Out, 1e6},
		{u.read, p.Read, 1e6},
		{u.write5m, p.Write5m, 1e6},
		{u.write1h, p.Write1h, 1e6},
		{u.web, p.Web, 1},
	} {
		if part.n == 0 {
			continue
		}
		r, ok := rate(part.p)
		if !ok {
			all = false
			continue
		}
		usd += float64(part.n) * r / part.per
	}
	return usd, all
}

// maxConfig caps the size of <Home>/.claude.json that is read.
const maxConfig = 32 << 20

// maxPriced caps the number of models kept from the price table.
const maxPriced = 512

// prices reads Claude Code's price table, again only when its file changed,
// and looks at the file at most once per priceCheck.
type prices struct {
	path string
	mu   sync.Mutex
	at   time.Time // last look
	info os.FileInfo
	tab  priceTable
}

const priceCheck = time.Minute

func newPrices(path string) *prices { return &prices{path: path} }

func (p *prices) table(now time.Time) priceTable {
	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.at.IsZero() && now.Sub(p.at) < priceCheck && now.Sub(p.at) >= 0 {
		return p.tab
	}
	p.at = now
	fi, err := os.Lstat(p.path)
	if err != nil || !fi.Mode().IsRegular() || fi.Size() > maxConfig {
		p.info, p.tab = nil, nil
		return nil
	}
	if p.info != nil && os.SameFile(p.info, fi) && fi.Size() == p.info.Size() && fi.ModTime().Equal(p.info.ModTime()) {
		return p.tab
	}
	tab, ok := readPrices(p.path)
	if !ok {
		return p.tab // a half-written file: keep the last table, look again later
	}
	p.info, p.tab = fi, tab
	return tab
}

// readPrices reads only the price table key; the decoder steps over every
// other key without keeping it.
func readPrices(path string) (priceTable, bool) {
	f, err := openNoFollow(path)
	if err != nil {
		return nil, false
	}
	defer func() { _ = f.Close() }()
	var v struct {
		Costs map[string]json.RawMessage `json:"additionalModelCostsCache"`
	}
	if err := json.NewDecoder(io.LimitReader(f, maxConfig)).Decode(&v); err != nil {
		return nil, false
	}
	tab := priceTable{}
	names := make([]string, 0, len(v.Costs))
	for m := range v.Costs {
		names = append(names, m)
	}
	slices.Sort(names)
	for _, m := range names {
		if len(tab) >= maxPriced || m == "" || len(m) > 128 {
			continue
		}
		var p price
		if json.Unmarshal(v.Costs[m], &p) == nil {
			tab[m] = p
		}
	}
	return tab, true
}

// spendData is a session's spend, counted and not yet priced: it is priced
// when a snapshot is built (priceSpend), so a price table that was missing or
// changed reaches sessions that ended long ago too. It is never changed after
// buildSpend returns it.
type spendData struct {
	total    map[priceKey]usage
	spans    map[spanKey]usage
	recorded *recordedCost
	// since: the usage after the recorded total (a resumed session), with
	// sinceOn set when there is some.
	since   map[priceKey]usage
	sinceOn bool
}

// buildSpend merges a session's transcripts (main first, then its
// subagents') into its spend. Spans before cut are left out. It is nil when
// nothing was counted and no cost was recorded.
//
// For a resumed session each transcript says what came after the restored
// cost-state line (spendLog.since).
func buildSpend(main *spendLog, subs []*spendLog, cut time.Time) *spendData {
	d := &spendData{total: map[priceKey]usage{}, spans: map[spanKey]usage{}, recorded: main.recorded}
	for _, l := range append([]*spendLog{main}, subs...) {
		for k, u := range l.total {
			d.total[k] = d.total[k].add(u)
		}
		for k, u := range l.spans {
			if !k.at.Before(cut) {
				d.spans[k] = d.spans[k].add(u)
			}
		}
	}
	if main.recorded != nil {
		since := map[priceKey]usage{}
		for _, l := range append([]*spendLog{main}, subs...) {
			if l.after {
				d.sinceOn = true
				for k, u := range l.since {
					since[k] = since[k].add(u)
				}
			}
		}
		if d.sinceOn {
			d.since = since
		}
	}
	if len(d.total) == 0 && d.recorded == nil {
		return nil
	}
	return d
}

// priced prices a sum: the amount of the parts that have a price, and whether
// every part has.
func (t priceTable) priced(m map[priceKey]usage) (usage, float64, bool) {
	var sum usage
	var usd float64
	all := true
	for _, k := range sortedKeys(m) { // a fixed order keeps the float sum the same
		u := m[k]
		sum = sum.add(u)
		c, ok := t.cost(k, u)
		usd += c
		all = all && ok
	}
	return sum, usd, all
}

// Claude Code's recorded total and the computed one of the same session are
// expected to agree (on this machine 101 of 102 ended sessions came within
// 0.9 to 1.01; the one at 0.17 was resumed from another transcript, which
// holds only part of its use). A table whose figures are further apart than
// this (prices in other units, say) is not trusted for the spans: they are
// shown as having no price, which is safe, never as a wrong amount.
const (
	sanityLow  = 0.05
	sanityHigh = 2
)

// priceSpend prices a session's spend with the price table of now. USD is the
// part that has a price; Priced is false when some of the use has none.
func priceSpend(d *spendData, tab priceTable) model.Spend {
	if d == nil {
		return model.Spend{}
	}
	var s model.Spend
	sum, usd, all := tab.priced(d.total)
	s.Tokens, s.USD, s.Priced = sum.tokens(), usd, len(d.total) > 0 && all
	trusted := true
	if r := d.recorded; r != nil {
		if !d.sinceOn {
			if r.usd > 0 && !r.unknown && all && usd > 0 && (usd/r.usd < sanityLow || usd/r.usd > sanityHigh) {
				trusted = false
			}
			s.Recorded, s.USD, s.Priced = true, r.usd, !r.unknown
			if r.tokens != (usage{}) {
				s.Tokens = r.tokens.tokens()
			}
		} else {
			ss, susd, sok := tab.priced(d.since)
			s.USD, s.Priced = r.usd+susd, !r.unknown && sok
			if r.tokens != (usage{}) {
				s.Tokens = r.tokens.add(ss).tokens()
			}
		}
	}
	bySpan := map[time.Time]*model.SpendSpan{}
	for _, k := range sortedKeys(d.spans) {
		u := d.spans[k]
		h := bySpan[k.at]
		if h == nil {
			h = &model.SpendSpan{At: k.at, Priced: true}
			bySpan[k.at] = h
		}
		h.Tokens = h.Tokens.Add(u.tokens())
		c, ok := tab.cost(k.priceKey, u)
		if !trusted {
			c, ok = 0, false
		}
		h.USD += c
		h.Priced = h.Priced && ok
	}
	for _, h := range bySpan {
		s.Spans = append(s.Spans, *h)
	}
	slices.SortFunc(s.Spans, func(a, b model.SpendSpan) int { return a.At.Compare(b.At) })
	return s
}

func cmpPriceKey(a, b priceKey) int {
	if a.model != b.model {
		if a.model < b.model {
			return -1
		}
		return 1
	}
	if a.fast != b.fast {
		if b.fast {
			return -1
		}
		return 1
	}
	if a.tier != b.tier {
		return cmp.Compare(a.tier, b.tier)
	}
	if a.noPrice != b.noPrice {
		if b.noPrice {
			return -1
		}
		return 1
	}
	return 0
}

func sortedKeys[K priceKey | spanKey](m map[K]usage) []K {
	keys := make([]K, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.SortFunc(keys, func(a, b K) int {
		switch x := any(a).(type) {
		case priceKey:
			return cmpPriceKey(x, any(b).(priceKey))
		case spanKey:
			y := any(b).(spanKey)
			if c := x.at.Compare(y.at); c != 0 {
				return c
			}
			return cmpPriceKey(x.priceKey, y.priceKey)
		}
		return 0
	})
	return keys
}
