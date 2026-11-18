// Package state keeps what only the owner knows: which cards were seen, the
// catch-up marker, the umbrella folders that group projects, and the folders
// whose grouping the owner forgot. Seen keys are card ids (contract.CardID),
// so "seen" survives restarts. Folder paths are local only: they never leave
// this file.
//
// A file from before umbrellas existed has no "umbrellas" key, and a file
// written by 0.1.0 has no "forgotten" key; both load as is. The file version
// stays 1 because both fields are optional. A 0.1.0 build reading a newer
// file ignores "forgotten" and drops it on its next write: a forgotten folder
// may then group again, nothing else is lost.
//
// The file is JSON, written atomically (temp file, then rename) with mode 0600.
// It is created on the first write, not on Open.
package state

import (
	"cmp"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"time"
)

// SeenRetention is how long a seen mark is kept. Marks older than this are
// dropped on every write, so the file does not grow forever.
const SeenRetention = 7 * 24 * time.Hour

// UmbrellaRetention is how long an umbrella folder is remembered after an
// agent last sat in it; MaxUmbrellas caps how many are kept (the most recently
// seen win).
const (
	UmbrellaRetention = 30 * 24 * time.Hour
	MaxUmbrellas      = 200
)

// Store is safe for concurrent use.
type Store interface {
	// SeenAt returns when the card was last marked seen. Marks older than
	// SeenRetention (7 days) are pruned on write, so a live card that was
	// seen long ago and has been idle since reads as never seen here. The
	// core therefore treats a turn that finished more than 7 days ago as
	// seen, whatever SeenAt says.
	SeenAt(cardID string) (time.Time, bool)
	SetSeen(cardID string, at time.Time) error
	CatchupMarker() time.Time
	SetCatchupMarker(at time.Time) error

	// Umbrellas returns a copy of the remembered umbrella folders (absolute
	// path -> last seen), without those older than UmbrellaRetention.
	Umbrellas() map[string]time.Time
	// TouchUmbrellas records that an agent sat in each folder at at. A
	// forgotten folder is skipped: it is not remembered while it is held.
	TouchUmbrellas(paths []string, at time.Time) error
	// ForgetUmbrellas drops folders, for example ones that no longer exist,
	// in one write.
	ForgetUmbrellas(paths []string) error

	// Forgotten returns a copy of the folders whose grouping the owner forgot
	// (absolute path -> last time an agent sat in it, or when it was
	// forgotten), without those older than UmbrellaRetention. While a folder
	// is listed here it does not group.
	Forgotten() map[string]time.Time
	// ForgetGrouping drops the folder from the umbrellas and lists it as
	// forgotten, in one write. It stores at, or, when the folder is listed
	// already with that time or a later one, the nanosecond after it, so a
	// new forget is always newer than an old hold; it returns the time
	// stored.
	ForgetGrouping(path string, at time.Time) (time.Time, error)
	// TouchForgotten records, per folder, the last time an agent still sat
	// in it. Folders not listed as forgotten, and folders stored with a
	// newer time (forgotten again since), are left alone.
	TouchForgotten(at map[string]time.Time) error
	// ReleaseForgotten drops folders from the forgotten list, in one write,
	// so they may group again. held gives each folder's time as the caller
	// last knew it; a folder whose stored time is newer (it was forgotten
	// again meanwhile) is kept.
	ReleaseForgotten(held map[string]time.Time) error
}

const fileVersion = 1

type document struct {
	Version       int                  `json:"version"`
	Seen          map[string]time.Time `json:"seen"`
	CatchupMarker time.Time            `json:"catchupMarker,omitzero"`
	Umbrellas     map[string]time.Time `json:"umbrellas,omitempty"`
	Forgotten     map[string]time.Time `json:"forgotten,omitempty"`
}

type fileStore struct {
	path string
	now  func() time.Time // clock for pruning; time.Now outside tests
	mu   sync.Mutex
	doc  document
}

// Open reads the state file. A missing file gives an empty store. A file that
// exists but cannot be read or parsed is an error: the caller decides what to
// do, so seen marks are never dropped silently. A file written by a newer
// sitboard (a higher version) is refused too: reading it as this version would
// drop its newer fields on the next write.
func Open(file string) (Store, error) {
	s := &fileStore{path: file, now: time.Now, doc: document{Version: fileVersion, Seen: map[string]time.Time{}}}
	data, err := os.ReadFile(file)
	if errors.Is(err, fs.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return nil, fmt.Errorf("state: read %s: %w", file, err)
	}
	// "forgotten" is read on its own: the outer field wins over the embedded
	// one, so a bad "forgotten" never fails the whole file.
	var disk struct {
		document
		Forgotten json.RawMessage `json:"forgotten"`
	}
	if err := json.Unmarshal(data, &disk); err != nil {
		return nil, fmt.Errorf("state: %s is not a valid state file (move it aside to start fresh): %w", file, err)
	}
	doc := disk.document
	doc.Forgotten = readForgotten(disk.Forgotten)
	if doc.Version > fileVersion {
		return nil, fmt.Errorf("state: %s was written by a newer sitboard (file version %d, this build reads up to %d); upgrade sitboard, or move the file aside to start fresh", file, doc.Version, fileVersion)
	}
	if doc.Seen == nil {
		doc.Seen = map[string]time.Time{}
	}
	// Umbrellas are cut at load: a time in the future (a wrong clock) counts
	// as now so it still ages out, and only the newest MaxUmbrellas are kept.
	// The next write saves the cut.
	// Forgotten folders are cut the same way.
	now := s.now()
	for _, m := range []map[string]time.Time{doc.Umbrellas, doc.Forgotten} {
		for p, at := range m {
			switch {
			case !validUmbrella(p):
				delete(m, p)
			case at.After(now):
				m[p] = now.UTC()
			}
		}
	}
	doc.Umbrellas = PruneUmbrellas(doc.Umbrellas, now)
	doc.Forgotten = PruneUmbrellas(doc.Forgotten, now)
	doc.Version = fileVersion
	s.doc = doc
	return s, nil
}

// readForgotten decodes the optional "forgotten" key leniently. 0.1.0 does
// not know it and opens any file that has it, so this build must not refuse
// one over it either: what cannot be decoded (not an object, an entry that is
// not a time) is dropped with one log line that names no path or value, and
// the rest of the file is kept. The keys 0.1.0 also reads keep its strict
// behaviour: a bad "seen", "catchupMarker" or "umbrellas" still refuses the
// file, so seen marks are never dropped silently.
func readForgotten(raw json.RawMessage) map[string]time.Time {
	if len(raw) == 0 || string(raw) == "null" {
		return nil
	}
	var entries map[string]json.RawMessage
	if err := json.Unmarshal(raw, &entries); err != nil {
		log.Printf("state: the forgotten folders in the state file are not readable; dropped them")
		return nil
	}
	out := make(map[string]time.Time, len(entries))
	bad := 0
	for p, v := range entries {
		var at time.Time
		if err := json.Unmarshal(v, &at); err != nil {
			bad++
			continue
		}
		out[p] = at
	}
	if bad > 0 {
		log.Printf("state: dropped %d unreadable forgotten folder(s) from the state file", bad)
	}
	return out
}

func (s *fileStore) SeenAt(cardID string) (time.Time, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	at, ok := s.doc.Seen[cardID]
	return at, ok
}

func (s *fileStore) SetSeen(cardID string, at time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	next := s.doc
	next.Seen = maps.Clone(s.doc.Seen)
	next.Seen[cardID] = at.UTC()
	return s.commit(next)
}

func (s *fileStore) CatchupMarker() time.Time {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.doc.CatchupMarker
}

func (s *fileStore) SetCatchupMarker(at time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	next := s.doc
	next.CatchupMarker = at.UTC()
	return s.commit(next)
}

func (s *fileStore) Umbrellas() map[string]time.Time {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.recentLocked(s.doc.Umbrellas)
}

func (s *fileStore) Forgotten() map[string]time.Time {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.recentLocked(s.doc.Forgotten)
}

// recentLocked copies the entries of m not older than UmbrellaRetention.
// Callers hold s.mu.
func (s *fileStore) recentLocked(m map[string]time.Time) map[string]time.Time {
	cutoff := s.now().Add(-UmbrellaRetention)
	out := make(map[string]time.Time, len(m))
	for p, at := range m {
		if !at.Before(cutoff) {
			out[p] = at
		}
	}
	return out
}

func (s *fileStore) ForgetGrouping(path string, at time.Time) (time.Time, error) {
	if !validUmbrella(path) {
		return time.Time{}, fmt.Errorf("state: %d-byte folder is not an absolute, clean path", len(path))
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	at = at.UTC()
	if prev, ok := s.doc.Forgotten[path]; ok && !at.After(prev) {
		at = prev.Add(time.Nanosecond) // the file keeps nanoseconds
	}
	next := s.doc
	next.Umbrellas = maps.Clone(s.doc.Umbrellas)
	delete(next.Umbrellas, path)
	next.Forgotten = maps.Clone(s.doc.Forgotten)
	if next.Forgotten == nil {
		next.Forgotten = map[string]time.Time{}
	}
	next.Forgotten[path] = at
	if err := s.commit(next); err != nil {
		return time.Time{}, err
	}
	return at, nil
}

func (s *fileStore) TouchForgotten(at map[string]time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	next := s.doc
	next.Forgotten = maps.Clone(s.doc.Forgotten)
	changed := false
	for p, t := range at {
		if stored, ok := next.Forgotten[p]; ok && !stored.After(t) {
			next.Forgotten[p] = t.UTC()
			changed = true
		}
	}
	if !changed {
		return nil
	}
	return s.commit(next)
}

func (s *fileStore) ReleaseForgotten(held map[string]time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	next := s.doc
	next.Forgotten = maps.Clone(s.doc.Forgotten)
	for p, t := range held {
		if stored, ok := next.Forgotten[p]; ok && !stored.After(t) {
			delete(next.Forgotten, p)
		}
	}
	if len(next.Forgotten) == len(s.doc.Forgotten) {
		return nil // nothing to release
	}
	return s.commit(next)
}

func (s *fileStore) TouchUmbrellas(paths []string, at time.Time) error {
	if len(paths) == 0 {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	next := s.doc
	next.Umbrellas = maps.Clone(s.doc.Umbrellas)
	if next.Umbrellas == nil {
		next.Umbrellas = map[string]time.Time{}
	}
	for _, p := range paths {
		if _, held := s.doc.Forgotten[p]; validUmbrella(p) && !held {
			next.Umbrellas[p] = at.UTC()
		}
	}
	return s.commit(next)
}

func (s *fileStore) ForgetUmbrellas(paths []string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	next := s.doc
	next.Umbrellas = maps.Clone(s.doc.Umbrellas) // at most MaxUmbrellas entries
	for _, p := range paths {
		delete(next.Umbrellas, p)
	}
	if len(next.Umbrellas) == len(s.doc.Umbrellas) {
		return nil // nothing to forget
	}
	return s.commit(next)
}

// validUmbrella: an umbrella is an absolute, clean folder other than the root.
func validUmbrella(p string) bool {
	return filepath.IsAbs(p) && filepath.Clean(p) == p && p != "/"
}

// PruneUmbrellas drops umbrellas (or forgotten folders) older than
// UmbrellaRetention and keeps the MaxUmbrellas most recently seen. It returns
// a new map when it changes one.
func PruneUmbrellas(in map[string]time.Time, now time.Time) map[string]time.Time {
	cutoff := now.Add(-UmbrellaRetention)
	type entry struct {
		path string
		at   time.Time
	}
	var keep []entry
	for p, at := range in {
		if !at.Before(cutoff) {
			keep = append(keep, entry{p, at})
		}
	}
	if len(keep) == len(in) && len(keep) <= MaxUmbrellas {
		return in
	}
	slices.SortFunc(keep, func(a, b entry) int {
		return cmp.Or(b.at.Compare(a.at), cmp.Compare(a.path, b.path)) // newest first
	})
	out := make(map[string]time.Time, min(len(keep), MaxUmbrellas))
	for _, e := range keep[:min(len(keep), MaxUmbrellas)] {
		out[e.path] = e.at
	}
	return out
}

// commit prunes seen marks older than SeenRetention, writes next to disk and
// only then makes it the in-memory state, so a failed write changes nothing.
// Callers hold s.mu.
func (s *fileStore) commit(next document) error {
	cutoff := s.now().Add(-SeenRetention)
	cloned := false
	for id, at := range next.Seen {
		if !at.Before(cutoff) {
			continue
		}
		if !cloned { // next.Seen may still be the in-memory map
			next.Seen = maps.Clone(next.Seen)
			cloned = true
		}
		delete(next.Seen, id)
	}
	next.Umbrellas = PruneUmbrellas(next.Umbrellas, s.now())
	next.Forgotten = PruneUmbrellas(next.Forgotten, s.now())
	data, err := json.MarshalIndent(next, "", "  ")
	if err != nil {
		return fmt.Errorf("state: encode: %w", err)
	}
	if err := writeAtomic(s.path, data); err != nil {
		return err
	}
	s.doc = next
	return nil
}

func writeAtomic(path string, data []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("state: create folder: %w", err)
	}
	tmp, err := os.CreateTemp(dir, ".state-*.tmp")
	if err != nil {
		return fmt.Errorf("state: temp file: %w", err)
	}
	name := tmp.Name()
	ok := false
	defer func() {
		if !ok {
			_ = tmp.Close()
			_ = os.Remove(name)
		}
	}()
	if err := tmp.Chmod(0o600); err != nil {
		return fmt.Errorf("state: chmod: %w", err)
	}
	if _, err := tmp.Write(data); err != nil {
		return fmt.Errorf("state: write: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		return fmt.Errorf("state: sync: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("state: close: %w", err)
	}
	if err := os.Rename(name, path); err != nil {
		return fmt.Errorf("state: rename: %w", err)
	}
	ok = true
	return nil
}
