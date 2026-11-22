// Every string from the API is untrusted. Svelte renders it as text, never as
// markup; this also removes characters that could disturb the layout.

// C0 and C1 controls (newline and tab are folded into spaces first).
const CONTROLS = /[\u0000-\u001f\u007f-\u009f]/g;
// Bidi marks, embeddings, overrides and isolates.
const BIDI = /[\u200e\u200f\u202a-\u202e\u2066-\u2069\u061c]/g;
// Zero-width space, non-joiner, joiner, word joiner, byte-order mark. Removed
// before whitespace is folded, because \s matches the byte-order mark.
const ZERO_WIDTH = /[\u200b-\u200d\u2060\ufeff]/g;
const MAX = 500;

export function clean(s: string): string {
  const out = s.replace(ZERO_WIDTH, '').replace(/\s+/g, ' ').replace(CONTROLS, '').replace(BIDI, '').trim();
  // Cut by code point, so a surrogate pair is never split.
  const points = Array.from(out);
  return points.length > MAX ? points.slice(0, MAX).join('') + '…' : out;
}

// What exact() shows escaped: controls, format characters (bidi, zero-width,
// byte-order mark, soft hyphen), every space but the plain one, line and
// paragraph separators, lone surrogates, private use, and the other
// characters that draw nothing; the backslash, so an escape is never ambiguous.
const UNSEEN = /[\p{Cc}\p{Cf}\p{Zs}\p{Zl}\p{Zp}\p{Cs}\p{Co}\p{Default_Ignorable_Code_Point}\\]/gu;

function escape(ch: string): string {
  if (ch === '\\') return '\\\\';
  const cp = ch.codePointAt(0)!;
  return cp <= 0xff ? `\\x${cp.toString(16).padStart(2, '0')}` : `\\u{${cp.toString(16)}}`;
}

/**
 * A name shown exactly, for paths and branch names: nothing removed, folded
 * or cut, so two different names never look the same. What would not show is
 * written as an escape (\x09, \u{200b}, \\ for a backslash), and so is a
 * plain space at either end. Repeated spaces stay; show it with
 * white-space: pre-wrap.
 */
export function exact(s: string): string {
  const out = s.replace(UNSEEN, (c) => (c === ' ' ? c : escape(c)));
  const lead = out.length - out.trimStart().length;
  const trail = out.length - out.trimEnd().length;
  if (lead === out.length) return '\\x20'.repeat(out.length);
  return '\\x20'.repeat(lead) + out.slice(lead, out.length - trail) + '\\x20'.repeat(trail);
}
