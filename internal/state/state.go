// Package state keeps what only the owner knows: which cards were seen, the
// catch-up marker, and the umbrella folders that group projects. Seen keys
// are card ids (contract.CardID), so "seen" survives restarts. Umbrella paths
// are local only: they never leave this file.
//
// A file from before umbrellas existed has no "umbrellas" key and loads as
// is; the file version stays 1 because the field is optional.
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
	// TouchUmbrellas records that an agent sat in each folder at at.
	TouchUmbrellas(paths []string, at time.Time) error
	// ForgetUmbrellas drops folders, for example ones that no longer exist,
	// in one write.
	ForgetUmbrellas(paths []string) error
}

const fileVersion = 1

type document struct {
	Version       int                  `json:"version"`
	Seen          map[string]time.Time `json:"seen"`
	CatchupMarker time.Time            `json:"catchupMarker,omitzero"`
	Umbrellas     map[string]time.Time `json:"umbrellas,omitempty"`
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
	var doc document
	if err := json.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("state: %s is not a valid state file (move it aside to start fresh): %w", file, err)
	}
	if doc.Version > fileVersion {
		return nil, fmt.Errorf("state: %s was written by a newer sitboard (file version %d, this build reads up to %d); upgrade sitboard, or move the file aside to start fresh", file, doc.Version, fileVersion)
	}
	if doc.Seen == nil {
		doc.Seen = map[string]time.Time{}
	}
	// Umbrellas are cut at load: a time in the future (a wrong clock) counts
	// as now so it still ages out, and only the newest MaxUmbrellas are kept.
	// The next write saves the cut.
	now := s.now()
	for p, at := range doc.Umbrellas {
		switch {
		case !validUmbrella(p):
			delete(doc.Umbrellas, p)
		case at.After(now):
			doc.Umbrellas[p] = now.UTC()
		}
	}
	doc.Umbrellas = PruneUmbrellas(doc.Umbrellas, now)
	doc.Version = fileVersion
	s.doc = doc
	return s, nil
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
	cutoff := s.now().Add(-UmbrellaRetention)
	out := make(map[string]time.Time, len(s.doc.Umbrellas))
	for p, at := range s.doc.Umbrellas {
		if !at.Before(cutoff) {
			out[p] = at
		}
	}
	return out
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
		if validUmbrella(p) {
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

// PruneUmbrellas drops umbrellas older than UmbrellaRetention and keeps the
// MaxUmbrellas most recently seen. It returns a new map when it changes one.
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
