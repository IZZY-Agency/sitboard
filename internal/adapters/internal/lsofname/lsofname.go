// Package lsofname reads file names as lsof prints them. lsof prints a byte
// it would not print as \xHH (all the bytes of a non-ASCII letter in the C
// locale), a few control characters as \n, \t and the like, the others as ^
// and a letter, and everything else as itself: a backslash or a caret in a
// name is printed as it is. So a printed name can stand for more than one
// real one.
package lsofname

import (
	"path"
	"slices"
	"strconv"
	"strings"
)

// piece is one part of a printed name: text printed as it is, or an escape,
// which stands either for the byte it names (val) or for its own characters
// (lit).
type piece struct {
	lit, val string
	esc      bool
}

// pieces splits s; carets: ^ and a letter is an escape too.
func pieces(s string, carets bool) []piece {
	var out []piece
	plain := func(t string) {
		if n := len(out); n > 0 && !out[n-1].esc {
			out[n-1].lit += t
			out[n-1].val += t
			return
		}
		out = append(out, piece{lit: t, val: t})
	}
	for i := 0; i < len(s); i++ {
		switch {
		case s[i] == '\\' && i+1 < len(s):
			if c := s[i+1]; c == 'x' {
				if i+3 < len(s) {
					if v, err := strconv.ParseUint(s[i+2:i+4], 16, 8); err == nil {
						out = append(out, piece{lit: s[i : i+4], val: string([]byte{byte(v)}), esc: true})
						i += 3
						continue
					}
				}
			} else if v, ok := backslashed[c]; ok {
				out = append(out, piece{lit: s[i : i+2], val: v, esc: true})
				i++
				continue
			}
		case carets && s[i] == '^' && i+1 < len(s) && (s[i+1] >= '@' && s[i+1] <= '_' || s[i+1] == '?'):
			v := s[i+1] - 0x40
			if s[i+1] == '?' {
				v = 0x7f
			}
			out = append(out, piece{lit: s[i : i+2], val: string([]byte{v}), esc: true})
			i++
			continue
		}
		plain(s[i : i+1])
	}
	return out
}

var backslashed = map[byte]string{'\\': "\\", 'n': "\n", 't': "\t", 'r': "\r", 'f': "\f", 'b': "\b", 'a': "\a", 'v': "\v"}

// Unescape undoes lsof's escaping of a name, reading every \ escape as one:
// the likeliest reading. Resolve gives those that exist.
func Unescape(s string) string {
	if !strings.Contains(s, `\`) {
		return s
	}
	var b strings.Builder
	for _, p := range pieces(s, false) {
		b.WriteString(p.val)
	}
	return b.String()
}

// Resolve returns the readings of s that name an existing path, s being an
// absolute name. It builds them one piece at a time and drops a reading as
// soon as a folder on its way is not there, or the name it is spelling
// starts no entry of its folder, so a name with many escapes (every byte of
// a non-ASCII letter is one) is followed along the paths that exist only.
// entries lists the names in a folder (nil when it cannot be read). A name
// with no escape is returned as it is, unchecked. ok is false when more
// than max readings are alive at once.
func Resolve(s string, max int, entries func(dir string) []string) (names []string, ok bool) {
	ps := pieces(s, true)
	if !slices.ContainsFunc(ps, func(p piece) bool { return p.esc }) {
		return []string{s}, true
	}
	listed := map[string][]string{}
	list := func(dir string) []string {
		e, seen := listed[dir]
		if !seen {
			e = entries(dir)
			listed[dir] = e
		}
		return e
	}
	// alive: every whole name on the way is an entry of its folder, and the
	// last one, while it is still being spelled, starts one.
	alive := func(c string, whole bool) bool {
		if !strings.HasPrefix(c, "/") {
			return false
		}
		parts := strings.Split(c[1:], "/")
		dir := "/"
		for i, p := range parts {
			last := i == len(parts)-1
			if p == "" {
				continue
			}
			es := list(dir)
			if last && !whole {
				return slices.ContainsFunc(es, func(e string) bool { return strings.HasPrefix(e, p) })
			}
			if !slices.Contains(es, p) {
				return false
			}
			dir = path.Join(dir, p)
		}
		return true
	}
	names = []string{""}
	for _, p := range ps {
		var next []string
		for _, nm := range names {
			if !p.esc {
				next = append(next, nm+p.lit)
				continue
			}
			next = append(next, nm+p.val, nm+p.lit)
		}
		names = names[:0]
		for _, nm := range next {
			if alive(nm, false) {
				names = append(names, nm)
			}
		}
		if len(names) > max {
			return nil, false
		}
	}
	return slices.DeleteFunc(names, func(nm string) bool { return !alive(nm, true) }), true
}
