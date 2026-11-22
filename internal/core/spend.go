package core

import (
	"cmp"
	"slices"
	"time"

	"github.com/IZZY-Agency/sitboard/internal/contract"
	"github.com/IZZY-Agency/sitboard/internal/model"
)

// spendDays is how many days, today included, the spend summary covers: the
// 7 days whose transcripts the adapters read.
const spendDays = 7

func tokensOf(t model.Tokens) contract.Tokens {
	return contract.Tokens{Input: max(t.Input, 0), Output: max(t.Output, 0), CacheRead: max(t.CacheRead, 0), CacheWrite: max(t.CacheWrite, 0)}
}

// cardSpend is a session's spend for its card: the amount of the use that has
// a price, and whether all of it has.
func cardSpend(s model.Spend) contract.Spend {
	out := contract.Spend{Tokens: tokensOf(s.Tokens), Priced: s.Priced, Recorded: s.Recorded}
	if finiteUSD(s.USD) {
		out.USD = s.USD
	} else {
		out.Priced = false // not an amount: nothing of it is known
	}
	return out
}

func finiteUSD(f float64) bool { return f >= 0 && f < 1e12 } // false for NaN too

// addSpend adds b to a: the amounts add up, and a sum is priced only when
// every part of it is. A sum starts out priced (none of nothing lacks a price).
func addSpend(a *contract.Spend, b contract.Spend) {
	addTokens(&a.Tokens, b.Tokens)
	a.Priced = a.Priced && b.Priced
	a.USD += b.USD
}

func addTokens(t *contract.Tokens, u contract.Tokens) {
	t.Input += u.Input
	t.Output += u.Output
	t.CacheRead += u.CacheRead
	t.CacheWrite += u.CacheWrite
}

// spendLocked sums every known session's spend, ended and backfilled ones
// included, by local day and project. Codex's use goes in Codex, beside
// Claude Code's spend and never in it (Codex keeps no price). Callers hold b.mu.
func (b *Board) spendLocked(now time.Time) contract.SpendSummary {
	y, m, d := now.Date()
	from := time.Date(y, m, d-(spendDays-1), 0, 0, 0, 0, now.Location())
	keys := make([]model.SessionKey, 0, len(b.sessions))
	for k := range b.sessions {
		keys = append(keys, k)
	}
	// A fixed order keeps the sums, and so the snapshot, the same.
	slices.SortFunc(keys, func(a, c model.SessionKey) int { return cmp.Or(cmp.Compare(a.Tool, c.Tool), cmp.Compare(a.ID, c.ID)) })
	type dayProject struct{ day, project string }
	days := map[string]*contract.SpendDay{}
	projects := map[dayProject]*contract.ProjectSpend{}
	for _, k := range keys {
		s := b.sessions[k]
		if len(s.Spend.Spans) == 0 || !b.inRoots(s.Folder) {
			continue
		}
		codex := k.Tool == model.ToolCodex
		p := contract.ProjectSpend{Spend: contract.Spend{Priced: true}}
		if s.Folder != "" {
			root := cmp.Or(b.repoLocked(s.Folder).MainRoot, s.Folder)
			p.ProjectID, p.Name, p.Path = contract.ProjectID(root), projectName(root), b.homeRel(root)
		}
		for _, h := range s.Spend.Spans {
			at := h.At.In(now.Location())
			if at.Before(from) || at.After(now) {
				continue
			}
			hs := cardSpend(model.Spend{Tokens: h.Tokens, USD: h.USD, Priced: h.Priced})
			day := at.Format(time.DateOnly)
			dd := days[day]
			if dd == nil {
				dd = &contract.SpendDay{Day: day, Spend: contract.Spend{Priced: true}, Projects: []contract.ProjectSpend{}}
				days[day] = dd
			}
			dk := dayProject{day, p.ProjectID}
			ps := projects[dk]
			if ps == nil {
				np := p
				ps = &np
				projects[dk] = ps
			}
			if codex {
				addTokens(&dd.Codex, hs.Tokens)
				addTokens(&ps.Codex, hs.Tokens)
			} else {
				addSpend(&dd.Spend, hs)
				addSpend(&ps.Spend, hs)
			}
		}
	}
	for dk, ps := range projects {
		days[dk.day].Projects = append(days[dk.day].Projects, *ps)
	}
	out := contract.SpendSummary{Days: []contract.SpendDay{}, Pending: b.spendPendingLocked()}
	for _, dd := range days {
		slices.SortFunc(dd.Projects, func(a, c contract.ProjectSpend) int {
			return cmp.Or(cmp.Compare(a.Name, c.Name), cmp.Compare(a.ProjectID, c.ProjectID))
		})
		out.Days = append(out.Days, *dd)
	}
	slices.SortFunc(out.Days, func(a, c contract.SpendDay) int { return cmp.Compare(a.Day, c.Day) })
	return out
}
