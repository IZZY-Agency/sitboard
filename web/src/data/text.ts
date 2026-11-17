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
