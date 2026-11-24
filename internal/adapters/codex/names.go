package codex

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"unicode/utf8"
)

// maxIndexLine: a longer line of the session index is skipped.
const maxIndexLine = 64 << 10

// maxSkipped bounds the ids remembered as passed over.
const maxSkipped = 8192

// maxName caps a name the way a Claude Code session name is capped; the core
// cleans it.
const maxName = 1024

// names reads <CODEX_HOME>/session_index.jsonl, an append-only file of
// {"id", "thread_name", "updated_at"} lines: the title Codex keeps for each
// thread. The terminal client fills it with a title the model writes from the
// conversation, unless the person renamed the thread; the last line for an id
// wins. Only the titles of the threads keep asks about are kept.
//
// Checked live on 0.171.0 (#111): the title is appended about half a second
// after the first turn ends (later turns do not write it again), and /rename
// appends a new line with the new name and nothing is written to the rollout.
type names struct {
	path string
	off  int64
	info os.FileInfo
	byID map[string]string
	// skipped holds ids that a line named while keep said no: a thread whose
	// rollout sitboard had not recognised yet. If one of them is wanted later,
	// the index is read again from its start.
	skipped map[string]bool
}

func newNames(path string) *names {
	return &names{path: path, byID: map[string]string{}, skipped: map[string]bool{}}
}

// update reads the lines appended since the last call. keep says which ids
// matter. It reports whether a kept name changed.
func (n *names) update(keep func(id string) bool) bool {
	fi, err := os.Lstat(n.path)
	if err != nil || !fi.Mode().IsRegular() {
		return false
	}
	changed := false
	if n.info != nil && (!os.SameFile(n.info, fi) || fi.Size() < n.off) {
		n.off = 0
		clear(n.byID)
		clear(n.skipped)
		changed = true
	}
	for id := range n.skipped {
		if keep(id) {
			n.off, n.info = 0, nil // a wanted id was passed over: read it all again
			clear(n.skipped)
			break
		}
	}
	if n.info != nil && n.off == fi.Size() && fi.ModTime().Equal(n.info.ModTime()) {
		return changed
	}
	n.info = fi
	f, err := openRegular(n.path)
	if err != nil {
		return changed
	}
	defer func() { _ = f.Close() }()
	if _, err := f.Seek(n.off, io.SeekStart); err != nil {
		return changed
	}
	r := bufio.NewReaderSize(f, maxIndexLine)
	skipping, lineLen := false, int64(0)
	for {
		chunk, err := r.ReadSlice('\n')
		lineLen += int64(len(chunk))
		if errors.Is(err, bufio.ErrBufferFull) {
			skipping = true
			continue
		}
		if err != nil { // an incomplete last line, however long, waits whole for the next update
			return changed
		}
		n.off += lineLen // the offset moves only past a line that ended
		lineLen = 0
		if skipping {
			skipping = false
			continue
		}
		var l struct {
			ID   string `json:"id"`
			Name string `json:"thread_name"`
		}
		if json.Unmarshal(chunk, &l) != nil || !threadIDRe.MatchString(l.ID) {
			continue
		}
		if !keep(l.ID) {
			if len(n.skipped) < maxSkipped {
				n.skipped[l.ID] = true
			}
			continue
		}
		l.Name = capName(l.Name)
		if n.byID[l.ID] != l.Name {
			n.byID[l.ID] = l.Name
			changed = true
		}
	}
}

// maxLookupBytes bounds one lookup of titles (lookupTitles): the index is a
// line per title and rename, so this is far more than any real one holds.
const maxLookupBytes = 256 << 20

// lookupTitles reads the index once, from its start, for the titles of the
// threads in want (the last line of an id wins), apart from the registry's
// own read and its state: it runs off the registry goroutine, within ctx and
// maxLookupBytes. A title is only what the index holds; an id with no line is
// not in the result.
func lookupTitles(ctx context.Context, path string, want map[string]bool) map[string]string {
	out := map[string]string{}
	if len(want) == 0 {
		return out
	}
	fi, err := os.Lstat(path)
	if err != nil || !fi.Mode().IsRegular() {
		return out
	}
	f, err := openRegular(path)
	if err != nil {
		return out
	}
	defer func() { _ = f.Close() }()
	r := bufio.NewReaderSize(io.LimitReader(f, maxLookupBytes), maxIndexLine)
	skipping := false
	for lines := 0; ; lines++ {
		if lines%1024 == 0 && ctx.Err() != nil {
			return out
		}
		chunk, err := r.ReadSlice('\n')
		if errors.Is(err, bufio.ErrBufferFull) {
			skipping = true
			continue
		}
		if err != nil {
			return out // the end, or an incomplete last line
		}
		if skipping {
			skipping = false
			continue
		}
		var l struct {
			ID   string `json:"id"`
			Name string `json:"thread_name"`
		}
		if json.Unmarshal(chunk, &l) != nil || !want[l.ID] {
			continue
		}
		out[l.ID] = capName(l.Name)
	}
}

// capName cuts a name to maxName bytes at a character boundary.
func capName(name string) string {
	if len(name) <= maxName {
		return name
	}
	cut := maxName
	for cut > 0 && !utf8.RuneStart(name[cut]) {
		cut--
	}
	return name[:cut]
}
