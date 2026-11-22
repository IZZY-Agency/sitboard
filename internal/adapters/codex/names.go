package codex

import (
	"bufio"
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
		if len(l.Name) > maxName {
			cut := maxName
			for cut > 0 && !utf8.RuneStart(l.Name[cut]) {
				cut--
			}
			l.Name = l.Name[:cut]
		}
		if n.byID[l.ID] != l.Name {
			n.byID[l.ID] = l.Name
			changed = true
		}
	}
}
