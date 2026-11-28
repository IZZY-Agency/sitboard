package core

import (
	"github.com/IZZY-Agency/sitboard/internal/model"
)

// Who spawned whom is matched here, across sessions (model.SessionFact). In
// order:
//  1. the parent the agent tool recorded natively (SessionFact.Parent) wins;
//  2. the session whose spawned terminal ids contain this session's terminal id;
//  3. the session whose spawned names contain this session's name or its
//     terminal's name. Ambiguity gives no match: two sessions that listed the
//     name, or two sessions that carry the name.
// A session is never its own parent.

// spawnRecord is what one session says it spawned.
type spawnRecord struct {
	key         model.SessionKey
	terminalIDs []string
	names       []string
}

// parentsLocked resolves every session's parent. Callers hold b.mu.
func (b *Board) parentsLocked() map[model.SessionKey]model.Known[model.SessionKey] {
	records := make([]spawnRecord, 0, len(b.sessions))
	carriers := map[string]int{} // name -> sessions carrying it (own or terminal name)
	for _, s := range b.sessions {
		// A session known only from catch-up's backfill is neither a spawn
		// record nor a name carrier, so the board is not changed by it. It
		// then has no part in the rest either: it is never anyone's parent,
		// has no terminal, and its own parent and hint are never shown.
		if s.Backfill {
			continue
		}
		if len(s.SpawnedTerminalIDs) > 0 || len(s.SpawnedNames) > 0 {
			records = append(records, spawnRecord{key: s.Key, terminalIDs: s.SpawnedTerminalIDs, names: s.SpawnedNames})
		}
		for _, n := range b.namesLocked(s) {
			carriers[n]++
		}
	}
	out := make(map[model.SessionKey]model.Known[model.SessionKey], len(b.sessions))
	for _, s := range b.sessions {
		var names []string
		for _, n := range b.namesLocked(s) {
			if carriers[n] == 1 {
				names = append(names, n) // a name two sessions carry identifies neither
			}
		}
		out[s.Key] = matchParent(s, names, records)
	}
	return out
}

// cycles finds sessions whose parent chain leads back to themselves (a parent
// and child that name each other, or a longer loop). Neither side can be
// waiting for the other, so each is treated as top-level and shown with
// "coordinator: unknown".
func cycles(parents map[model.SessionKey]model.Known[model.SessionKey]) map[model.SessionKey]bool {
	in := map[model.SessionKey]bool{}
	for start := range parents {
		k := start
		for range len(parents) {
			p := parents[k]
			if !p.OK {
				break
			}
			if k = p.V; k == start {
				in[start] = true
				break
			}
		}
	}
	return in
}

// namesLocked lists the distinct names a session goes by: its own and its
// terminal's. Callers hold b.mu.
func (b *Board) namesLocked(s model.SessionFact) []string {
	var out []string
	if s.Name != "" {
		out = append(out, s.Name)
	}
	if t, ok := b.terminals[s.TerminalID]; ok && s.TerminalID != "" && t.Name != "" && t.Name != s.Name {
		out = append(out, t.Name)
	}
	return out
}

// matchParent applies the rules above to one session. names are the names
// that identify only this session.
func matchParent(s model.SessionFact, names []string, records []spawnRecord) model.Known[model.SessionKey] {
	if s.Parent.OK && s.Parent.V != s.Key {
		return s.Parent
	}
	if s.TerminalID != "" {
		if k, ok := unique(s.Key, records, func(r spawnRecord) bool { return contains(r.terminalIDs, s.TerminalID) }); ok {
			return model.Of(k)
		}
	}
	if len(names) > 0 {
		match := func(r spawnRecord) bool {
			for _, n := range names {
				if contains(r.names, n) {
					return true
				}
			}
			return false
		}
		if k, ok := unique(s.Key, records, match); ok {
			return model.Of(k)
		}
	}
	return model.Unknown[model.SessionKey]()
}

// workerHintsLocked derives the worker hint in the core, after matching (the
// adapters' SessionFact.WorkerHint is not read). A session is a worker of an
// unknown coordinator only when it has no matched parent and a session it did
// not spawn sent it a message. A coordinator hears from its own workers all
// the time, including ones that are not matched right now (records gone,
// terminal closed, a name two sessions share, not read yet), so a sender is
// skipped when it is one of: the session's own names, the names it gave the
// sessions it spawned, the names of the terminals it spawned, and the names of
// its matched children. A coordinator (it spawned sessions, or has matched
// children) also hears from old workers under names it never recorded, so for
// it only a sender that is a live session counts. A session that spawned
// nothing keeps every unknown sender as a sign: that is a worker whose
// coordinator has ended. The trade-off, chosen for less noise: a worker that
// spawned helpers of its own and whose coordinator has ended counts as a
// coordinator here, so it shows no coordinator line rather than "unknown".
// Callers hold b.mu.
func (b *Board) workerHintsLocked(parents map[model.SessionKey]model.Known[model.SessionKey], cyclic map[model.SessionKey]bool) map[model.SessionKey]bool {
	own := map[model.SessionKey]map[string]bool{}
	live := map[string]bool{} // names carried by a session that has not ended
	coordinator := b.coordinatorsLocked(parents)
	add := func(k model.SessionKey, names ...string) {
		if own[k] == nil {
			own[k] = map[string]bool{}
		}
		for _, n := range names {
			own[k][n] = true
		}
	}
	for k, s := range b.sessions {
		if !s.Ended {
			for _, n := range b.namesLocked(s) {
				live[n] = true
			}
		}
		add(k, b.namesLocked(s)...)
		add(k, s.SpawnedNames...)
		for _, id := range s.SpawnedTerminalIDs {
			if t, ok := b.terminals[id]; ok && t.Name != "" {
				add(k, t.Name)
			}
		}
		if p := parents[k]; p.OK {
			add(p.V, b.namesLocked(s)...)
		}
	}
	hints := map[model.SessionKey]bool{}
	for k, s := range b.sessions {
		if cyclic[k] {
			hints[k] = true
			continue
		}
		if parents[k].OK {
			continue
		}
		for _, sender := range s.PeerSenders {
			if sender != "" && !own[k][sender] && (live[sender] || !coordinator[k]) {
				hints[k] = true
				break
			}
		}
	}
	return hints
}

// coordinatorsLocked is the sessions that are coordinators: they spawned
// sessions or terminals, or have a matched child (a session whose parent is
// them in parents). Callers hold b.mu.
func (b *Board) coordinatorsLocked(parents map[model.SessionKey]model.Known[model.SessionKey]) map[model.SessionKey]bool {
	coordinator := map[model.SessionKey]bool{}
	for k, s := range b.sessions {
		if len(s.SpawnedNames) > 0 || len(s.SpawnedTerminalIDs) > 0 {
			coordinator[k] = true
		}
		if p := parents[k]; p.OK {
			coordinator[p.V] = true
		}
	}
	return coordinator
}

// unique returns the one record, other than self, that matches.
func unique(self model.SessionKey, records []spawnRecord, match func(spawnRecord) bool) (model.SessionKey, bool) {
	var found model.SessionKey
	n := 0
	for _, r := range records {
		if r.key != self && match(r) {
			found = r.key
			n++
		}
	}
	return found, n == 1
}

func contains(list []string, v string) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}
