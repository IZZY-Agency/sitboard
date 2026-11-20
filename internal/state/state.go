// Package state keeps what only the owner knows: which cards were seen, the
// catch-up marker, the umbrella folders that group projects, the folders
// whose grouping the owner forgot, and the folders the owner set to always or
// never group. Seen keys are card ids (contract.CardID),
// so "seen" survives restarts. Folder paths are local only: they never leave
// this file.
//
// Catch-up items still unread although they happened before the marker
// ("catchupUnread") are kept as a hash of each item's identity, not the
// identity itself, and the item's own time. Someone who can read the file and
// guesses a repository can tell which of its pull requests are unread; the
// file already names sessions and folders. There is no salt: one kept in this
// same file would protect nothing against whoever reads it. Older builds drop
// the key on their next write; that only makes those items count as read, so
// the file version is not raised for it.
//
// A file from before umbrellas existed has no "umbrellas" key, and a file
// written by 0.1.0 has no "forgotten" key; both load as is. The file version
// stays 1 because both fields are optional. A 0.1.0 build reading a newer
// file ignores "forgotten" and drops it on its next write: a forgotten folder
// may then group again, nothing else is lost.
//
// Grouping settings ("grouping") are the owner's deliberate choice, so an
// older build must not drop them silently: a file that holds at least one is
// written as version 2, which 0.1.0 and 0.2.0 refuse to open ("written by a
// newer sitboard"). A file with no setting is written as version 1, as before,
// so those builds still open it. This build reads both.
//
// The file is JSON, written atomically (temp file, then rename) with mode 0600.
// It is created on the first write, not on Open. Memory gives a store with no
// file at all, for throwaway runs.
package state

import (
	"bytes"
	"cmp"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"maps"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

// MaxCatchupUnread caps the catch-up items kept as unread although they
// happened before the marker. Past it the oldest are dropped: each of those
// counts as read after the next restart, as if it had been in a list marked
// read.
const MaxCatchupUnread = 1000

// CatchupKey is how a catch-up item's identity is kept in the state file: the
// first 16 bytes of its SHA-256, in hex.
func CatchupKey(identity string) string {
	sum := sha256.Sum256([]byte(identity))
	return hex.EncodeToString(sum[:16])
}

func validCatchupKey(k string) bool {
	if len(k) != 32 {
		return false
	}
	_, err := hex.DecodeString(k)
	return err == nil && strings.ToLower(k) == k
}

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
	// CatchupUnread returns a copy of the catch-up items that are still
	// unread although they happened before the marker (learned late): key ->
	// the item's own time. A key is a hash of the item's identity
	// (CatchupKey), not the identity itself.
	CatchupUnread() map[string]time.Time
	// SetCatchup saves the marker and the unread items in one write. Items
	// older than SeenRetention are dropped; past MaxCatchupUnread, the oldest.
	// After Close it changes nothing and returns ErrClosed.
	SetCatchup(marker time.Time, unread map[string]time.Time) error

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

	// Grouping returns a copy of the owner's grouping settings (absolute
	// path -> GroupingAlways or GroupingNever). A folder not listed is on
	// auto. Settings never expire.
	Grouping() map[string]string
	// SetGrouping sets a folder's grouping, in one write:
	//   - GroupingNever: the folder is no longer remembered as an umbrella
	//     nor held as forgotten, and never groups;
	//   - GroupingAlways: a forget hold on it is dropped, and it groups;
	//   - GroupingAuto: the setting is removed. A folder that was set to
	//     always is remembered as an umbrella seen at at, so going back to
	//     auto changes nothing on the board at once.
	// A new setting past MaxGroupingSettings is ErrTooManySettings. A
	// ForgetGrouping removes a folder's setting (back to auto, held).
	SetGrouping(path, mode string, at time.Time) error

	// Close stops saving: a write in progress finishes first, later changes
	// stay in memory only. Call it before the state file's lock is released,
	// so nothing is written once another sitboard may hold the file. The
	// owner's own changes (ForgetGrouping, SetGrouping) are refused after it
	// with ErrClosed instead, so one that cannot be saved is reported, not
	// swallowed.
	Close()
}

// fileVersion is the newest file version this build reads. It writes
// versionPlain when the file holds no grouping setting, and fileVersion when
// it holds one.
//
// This build reads a version 2 file's "grouping" leniently: a mode it does
// not know is dropped with a log line, and the next write saves the file
// without it. So a later build that adds a grouping mode, or another kind of
// setting the owner chose, MUST write version 3 (or higher) while the file
// holds one; otherwise this build, run after it (a downgrade), drops that
// setting silently.
const (
	fileVersion  = 2
	versionPlain = 1
)

// The values of a grouping setting. A folder with no setting is on auto:
// it groups as sitboard sees it (umbrellas, forget).
const (
	GroupingAuto   = "auto"
	GroupingAlways = "always"
	GroupingNever  = "never"
)

// MaxGroupingSettings caps how many folders can have a setting. Settings do
// not expire, so a new one past the cap is refused, never one dropped.
const MaxGroupingSettings = 200

// ErrClosed is what ForgetGrouping and SetGrouping return after Close on a
// store with a file: sitboard is stopping and the change would not be saved.
// Nothing changes, in memory either.
var ErrClosed = errors.New("state: sitboard is stopping; the change was not saved")

// ErrTooManySettings is what SetGrouping returns for a new setting past
// MaxGroupingSettings.
var ErrTooManySettings = errors.New("state: too many folders have a grouping setting")

type document struct {
	Version       int                  `json:"version"`
	Seen          map[string]time.Time `json:"seen"`
	CatchupMarker time.Time            `json:"catchupMarker,omitzero"`
	Umbrellas     map[string]time.Time `json:"umbrellas,omitempty"`
	Forgotten     map[string]time.Time `json:"forgotten,omitempty"`
	Grouping      map[string]string    `json:"grouping,omitempty"`
	// CatchupUnread: older builds do not know it and drop it on their next
	// write. That loses only these marks (those items then count as read),
	// so it is no reason for a version that older builds would refuse.
	CatchupUnread map[string]time.Time `json:"catchupUnread,omitempty"`
}

type fileStore struct {
	path string           // "" keeps everything in memory (see Memory)
	now  func() time.Time // clock for pruning; time.Now outside tests
	mu   sync.Mutex
	doc  document

	// disk is the file as this store last read or wrote it (nil: there was
	// none). A write that finds another file there warns once per store.
	disk   os.FileInfo
	warned bool
	closed bool                             // Close was called: commit keeps changes in memory only
	warnf  func(format string, args ...any) // log.Printf outside tests
}

// Open reads the state file. A missing file gives an empty store. A file that
// exists but cannot be read or parsed is an error: the caller decides what to
// do, so seen marks are never dropped silently. A file written by a newer
// sitboard (a higher version) is refused too: reading it as this version would
// drop its newer fields on the next write.
func Open(file string) (Store, error) { return openAt(file, time.Now) }

// openAt is Open with its clock: groupings are pruned against it at load.
func openAt(file string, clock func() time.Time) (Store, error) {
	s := &fileStore{path: file, now: clock, warnf: log.Printf, doc: document{Version: versionPlain, Seen: map[string]time.Time{}}}
	data, info, err := readFile(file)
	if errors.Is(err, fs.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return nil, fmt.Errorf("state: read %s: %w", file, err)
	}
	s.disk = info
	if len(bytes.TrimSpace(data)) == 0 {
		return s, nil // an empty file is a new state file
	}
	// "forgotten" and "version" are read on their own: the outer field wins
	// over the embedded one, so a bad "forgotten" never fails the whole file,
	// and a missing version can be told from version 0.
	var disk struct {
		document
		Version   json.RawMessage `json:"version"`
		Forgotten json.RawMessage `json:"forgotten"`
		Grouping  json.RawMessage `json:"grouping"`
		Unread    json.RawMessage `json:"catchupUnread"`
	}
	if err := json.Unmarshal(data, &disk); err != nil {
		return nil, fmt.Errorf("state: %s is not a valid state file (move it aside to start fresh): %w", file, err)
	}
	doc := disk.document
	// Only a document with a version this build reads is a state file: any
	// other JSON (a package.json, a settings file) would be replaced by the
	// first write. sitboard 0.1.0 and 0.2.0 always write "version": 1; this
	// build writes 1, or 2 while the file holds a grouping setting.
	version, err := readVersion(disk.Version)
	if err != nil {
		return nil, fmt.Errorf("state: %s is not a sitboard state file (%v); give sitboard another file, or move this one aside", file, err)
	}
	doc.Version = version
	if doc.Version > fileVersion {
		return nil, fmt.Errorf("state: %s was written by a newer sitboard (file version %s, this build reads up to %d); upgrade sitboard, or move the file aside to start fresh", file, disk.Version, fileVersion)
	}
	// Read only once the file is known to be one this build reads, so a
	// refused file logs nothing about its contents.
	doc.Forgotten = readForgotten(disk.Forgotten)
	doc.Grouping = readGrouping(disk.Grouping)
	doc.CatchupUnread = readCatchupUnread(disk.Unread, s.now())
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
	// A folder set to never is not an umbrella, and a folder with a setting
	// holds no forget (a file edited by hand).
	for p, mode := range doc.Grouping {
		delete(doc.Forgotten, p)
		if mode == GroupingNever {
			delete(doc.Umbrellas, p)
		}
	}
	doc.Version = versionOf(doc)
	s.doc = doc
	return s, nil
}

// readVersion decodes the "version" key: a whole number from 1 up, written
// plainly, as every sitboard writes it. A version above fileVersion is left
// for the caller to refuse as newer; one too large for an int is returned as
// math.MaxInt, so it is refused as newer too. A number written with a
// fraction or an exponent ("1.0", "1e0") is refused: no sitboard writes one.
func readVersion(raw json.RawMessage) (int, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return 0, errors.New("it has no version")
	}
	text := string(raw)
	if !plainInteger.MatchString(text) {
		var n json.Number
		if json.Unmarshal(raw, &n) == nil && raw[0] != '"' {
			return 0, fmt.Errorf("its version %s is not written as a whole number", text)
		}
		return 0, errors.New("its version is not a number")
	}
	v, err := strconv.Atoi(text)
	switch {
	case err != nil && text[0] != '-':
		return math.MaxInt, nil // out of range: a newer version
	case v < 1: // a negative out of range comes back as math.MinInt
		return 0, fmt.Errorf("its version %s is not one sitboard writes", text)
	}
	return v, nil
}

// plainInteger is a JSON number with no fraction and no exponent.
var plainInteger = regexp.MustCompile(`^-?[0-9]+$`)

// readFile reads file and stats the same open file, so the two agree.
func readFile(file string) ([]byte, os.FileInfo, error) {
	f, err := os.Open(file)
	if err != nil {
		return nil, nil, err
	}
	defer func() { _ = f.Close() }()
	info, err := f.Stat()
	if err != nil {
		return nil, nil, err
	}
	data, err := io.ReadAll(f)
	if err != nil {
		return nil, nil, err
	}
	return data, info, nil
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

// Memory returns a store that never reads or writes a file: it starts empty and
// keeps everything for the life of the process only.
func Memory() Store {
	return &fileStore{now: time.Now, warnf: log.Printf, doc: document{Version: versionPlain, Seen: map[string]time.Time{}}}
}

// Resolve gives the file that is really read and written: symbolic links in
// file and in its folders are followed, so the atomic write replaces the link's
// target, not the link, and two spellings of one file name one file. A link to
// a missing file is refused: the first write would replace the link itself.
// Folders that do not exist yet are kept as given. A folder in the path that
// is a link to a missing folder, a link into a folder sitboard has no
// permission to enter, or a loop of links, is refused and called what it is.
func Resolve(file string) (string, error) {
	if fi, err := os.Lstat(file); err == nil && fi.Mode()&os.ModeSymlink != 0 {
		target, err := filepath.EvalSymlinks(file)
		if err != nil {
			if _, serr := os.Stat(file); errors.Is(serr, syscall.ELOOP) {
				return "", fmt.Errorf("state: %s is a loop of links; give the file itself", file)
			}
			return "", fmt.Errorf("state: %s is a link to a missing file; give the file itself", file)
		}
		return target, nil
	}
	dir, rest := filepath.Dir(file), filepath.Base(file)
	for {
		real, err := filepath.EvalSymlinks(dir)
		if err == nil {
			return filepath.Join(real, rest), nil
		}
		// dir is there but cannot be followed: it is a link that leads
		// nowhere. Creating the folder would fail with "file exists".
		if fi, lerr := os.Lstat(dir); lerr == nil && fi.Mode()&os.ModeSymlink != 0 {
			switch _, serr := os.Stat(dir); {
			case errors.Is(serr, syscall.ELOOP):
				return "", fmt.Errorf("state: the folder %s in %s is a loop of links; give a path through real folders", dir, file)
			case errors.Is(serr, fs.ErrPermission):
				return "", fmt.Errorf("state: the folder %s in %s is a link into a folder sitboard has no permission to enter; give a path through folders it can enter", dir, file)
			}
			return "", fmt.Errorf("state: the folder %s in %s is a link to a missing folder; give a path through real folders", dir, file)
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return file, nil
		}
		rest = filepath.Join(filepath.Base(dir), rest)
		dir = parent
	}
}

// createTemp is os.CreateTemp; tests replace it.
var createTemp = os.CreateTemp

// CheckWritable fails when file cannot be used as a state file: it is a
// folder or another file that is not a regular one (a named pipe, a device),
// or no file can be created next to it. Like the first write, it
// creates the missing folders (mode 0700); when the check fails, it removes
// the folders it created. It leaves nothing behind and never opens file
// itself, so an existing state file keeps its contents, times and
// permissions, and a read-only one passes (the write replaces it by a rename).
func CheckWritable(file string) error {
	if fi, err := os.Stat(file); err == nil {
		if fi.IsDir() {
			return fmt.Errorf("state: %s is a folder; give a file path", file)
		}
		// A named pipe or a device would hang the start or never hold the
		// state: only a regular file is one.
		if !fi.Mode().IsRegular() {
			return fmt.Errorf("state: %s is not a regular file; give a file path", file)
		}
	}
	dir := filepath.Dir(file)
	var missing []string // deepest first
	for d := dir; ; d = filepath.Dir(d) {
		if _, err := os.Lstat(d); err == nil || filepath.Dir(d) == d {
			break
		}
		missing = append(missing, d)
	}
	removeCreated := func() {
		for _, d := range missing {
			_ = os.Remove(d) // only empty folders go; one that was not created stays
		}
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		removeCreated()
		return fmt.Errorf("state: cannot create the folder for %s: %w", file, err)
	}
	tmp, err := createTemp(dir, ".state-*.tmp")
	if err != nil {
		removeCreated()
		return fmt.Errorf("state: cannot write next to %s: %w", file, err)
	}
	name := tmp.Name()
	_ = tmp.Close()
	if err := os.Remove(name); err != nil {
		return fmt.Errorf("state: cannot remove a probe file next to %s: %w", file, err)
	}
	return nil
}

func (s *fileStore) Close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closed = true
}

// readGrouping decodes the optional "grouping" key leniently, like
// "forgotten": an entry this build cannot read (not a string, a value other
// than always or never, a path that is not absolute and clean) is dropped
// with one log line that names no path or value, and the rest of the file is
// kept. Past MaxGroupingSettings, the paths that sort last are dropped.
func readGrouping(raw json.RawMessage) map[string]string {
	if len(raw) == 0 || string(raw) == "null" {
		return nil
	}
	var entries map[string]json.RawMessage
	if err := json.Unmarshal(raw, &entries); err != nil {
		log.Printf("state: the grouping settings in the state file are not readable; dropped them")
		return nil
	}
	out := make(map[string]string, len(entries))
	bad := 0
	for p, v := range entries {
		var mode string
		if err := json.Unmarshal(v, &mode); err != nil || (mode != GroupingAlways && mode != GroupingNever) || !validUmbrella(p) {
			bad++
			continue
		}
		out[p] = mode
	}
	if len(out) > MaxGroupingSettings {
		for _, p := range slices.Sorted(maps.Keys(out))[MaxGroupingSettings:] {
			delete(out, p)
			bad++
		}
	}
	if bad > 0 {
		log.Printf("state: dropped %d unreadable grouping setting(s) from the state file", bad)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// readCatchupUnread decodes the optional "catchupUnread" key leniently, like
// "forgotten": an entry that is not a key and a time is dropped with one log
// line that names no key, and the rest of the file is kept.
func readCatchupUnread(raw json.RawMessage, now time.Time) map[string]time.Time {
	if len(raw) == 0 || string(raw) == "null" {
		return nil
	}
	var entries map[string]json.RawMessage
	if err := json.Unmarshal(raw, &entries); err != nil {
		log.Printf("state: the unread catch-up items in the state file are not readable; dropped them")
		return nil
	}
	out := make(map[string]time.Time, len(entries))
	bad := 0
	for k, v := range entries {
		var at time.Time
		if err := json.Unmarshal(v, &at); err != nil || !validCatchupKey(k) {
			bad++
			continue
		}
		out[k] = at
	}
	if bad > 0 {
		log.Printf("state: dropped %d unreadable unread catch-up item(s) from the state file", bad)
	}
	return pruneUnread(out, now)
}

// pruneUnread drops items older than SeenRetention and, past
// MaxCatchupUnread, the oldest. It returns a new map (nil when empty).
func pruneUnread(m map[string]time.Time, now time.Time) map[string]time.Time {
	cutoff := now.Add(-SeenRetention)
	keys := make([]string, 0, len(m))
	for k, at := range m {
		if at.After(cutoff) {
			keys = append(keys, k)
		}
	}
	slices.SortFunc(keys, func(a, b string) int { return cmp.Or(m[b].Compare(m[a]), cmp.Compare(a, b)) })
	if len(keys) > MaxCatchupUnread {
		keys = keys[:MaxCatchupUnread]
	}
	if len(keys) == 0 {
		return nil
	}
	out := make(map[string]time.Time, len(keys))
	for _, k := range keys {
		out[k] = m[k].UTC()
	}
	return out
}

// versionOf is the version a document is written as: 2 while it holds a
// grouping setting, so older builds refuse it instead of dropping the
// setting; 1 otherwise.
func versionOf(doc document) int {
	if len(doc.Grouping) > 0 {
		return fileVersion
	}
	return versionPlain
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

func (s *fileStore) CatchupUnread() map[string]time.Time {
	s.mu.Lock()
	defer s.mu.Unlock()
	return pruneUnread(s.doc.CatchupUnread, s.now())
}

func (s *fileStore) SetCatchup(marker time.Time, unread map[string]time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed && s.path != "" {
		return ErrClosed // not saved: the caller must not take it as saved
	}
	next := s.doc
	next.CatchupMarker = marker.UTC()
	next.CatchupUnread = maps.Clone(unread)
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
	if s.closed && s.path != "" {
		return time.Time{}, ErrClosed
	}
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
	if _, set := s.doc.Grouping[path]; set {
		next.Grouping = maps.Clone(s.doc.Grouping)
		delete(next.Grouping, path) // forgetting is auto, held
	}
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
		_, held := s.doc.Forgotten[p]
		if validUmbrella(p) && !held && s.doc.Grouping[p] != GroupingNever {
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

func (s *fileStore) Grouping() map[string]string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return maps.Clone(s.doc.Grouping)
}

func (s *fileStore) SetGrouping(path, mode string, at time.Time) error {
	if !validUmbrella(path) {
		return fmt.Errorf("state: %d-byte folder is not an absolute, clean path", len(path))
	}
	if mode != GroupingAuto && mode != GroupingAlways && mode != GroupingNever {
		return fmt.Errorf("state: %d-byte grouping mode is not auto, always or never", len(mode))
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed && s.path != "" {
		return ErrClosed
	}
	prev, had := s.doc.Grouping[path]
	if mode != GroupingAuto && !had && len(s.doc.Grouping) >= MaxGroupingSettings {
		return ErrTooManySettings
	}
	next := s.doc
	next.Grouping = maps.Clone(s.doc.Grouping)
	next.Umbrellas = maps.Clone(s.doc.Umbrellas)
	next.Forgotten = maps.Clone(s.doc.Forgotten)
	switch mode {
	case GroupingAuto:
		delete(next.Grouping, path)
		if prev == GroupingAlways {
			if next.Umbrellas == nil {
				next.Umbrellas = map[string]time.Time{}
			}
			next.Umbrellas[path] = at.UTC()
		}
	case GroupingNever:
		delete(next.Umbrellas, path)
		fallthrough
	default:
		if next.Grouping == nil {
			next.Grouping = map[string]string{}
		}
		next.Grouping[path] = mode
		delete(next.Forgotten, path)
	}
	cur, has := next.Grouping[path]
	if cur == prev && has == had && len(next.Umbrellas) == len(s.doc.Umbrellas) && len(next.Forgotten) == len(s.doc.Forgotten) {
		return nil // nothing changes
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
	next.CatchupUnread = pruneUnread(next.CatchupUnread, s.now())
	next.Version = versionOf(next) // an empty "grouping" is left out (omitempty)
	data, err := json.MarshalIndent(next, "", "  ")
	if err != nil {
		return fmt.Errorf("state: encode: %w", err)
	}
	if s.path != "" && !s.closed {
		s.checkForeignLocked()
		info, err := writeAtomic(s.path, data)
		if err != nil {
			return err
		}
		s.disk = info
	}
	s.doc = next
	return nil
}

// checkForeignLocked warns, once per store, when the file on disk is not the
// one this store last read or wrote: another program (most likely another
// sitboard that cannot take the lock, such as 0.1.0) wrote it, and the write
// about to happen replaces its changes. Nothing is merged. A write by another
// program between this check and the rename is not seen. Callers hold s.mu.
func (s *fileStore) checkForeignLocked() {
	if s.warned {
		return
	}
	now, err := os.Stat(s.path)
	switch {
	case errors.Is(err, fs.ErrNotExist) && s.disk == nil:
		return // still no file: the first write creates it
	case err != nil && !errors.Is(err, fs.ErrNotExist):
		return // cannot tell; the write reports what is wrong
	case err == nil && s.disk != nil && sameFile(s.disk, now):
		return
	}
	s.warned = true
	s.warnf("state: another program changed the state file since sitboard last read or wrote it; its changes will be overwritten")
}

// sameFile: the same file (device and inode), not changed since (size and
// modification time).
func sameFile(a, b os.FileInfo) bool {
	return os.SameFile(a, b) && a.Size() == b.Size() && a.ModTime().Equal(b.ModTime())
}

// writeAtomic writes path through a temp file and a rename, and returns what
// the new file looks like on disk.
func writeAtomic(path string, data []byte) (os.FileInfo, error) {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("state: create folder: %w", err)
	}
	tmp, err := os.CreateTemp(dir, ".state-*.tmp")
	if err != nil {
		return nil, fmt.Errorf("state: temp file: %w", err)
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
		return nil, fmt.Errorf("state: chmod: %w", err)
	}
	if _, err := tmp.Write(data); err != nil {
		return nil, fmt.Errorf("state: write: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		return nil, fmt.Errorf("state: sync: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return nil, fmt.Errorf("state: close: %w", err)
	}
	// What the file looks like is taken once it is closed (a network folder
	// may set the time on close) and renamed: from the new name when that is
	// still this file, else from the closed file, so another program's file
	// that replaced it at once is still told apart at the next write.
	closed, err := os.Stat(name)
	if err != nil {
		return nil, fmt.Errorf("state: stat: %w", err)
	}
	if err := os.Rename(name, path); err != nil {
		return nil, fmt.Errorf("state: rename: %w", err)
	}
	ok = true
	if renamed, err := os.Stat(path); err == nil && os.SameFile(renamed, closed) {
		return renamed, nil
	}
	return closed, nil
}
