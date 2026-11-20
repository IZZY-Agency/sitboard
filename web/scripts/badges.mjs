#!/usr/bin/env node
// Writes the tab icons: public/favicon.svg (the mark) and public/badge/*.svg
// (the mark with how many agents need you, or "?" while the board is not
// live). The page's policy allows images from sitboard only (img-src 'self'),
// and browsers apply it to the tab icon, so the icons are files served with
// the page, not generated in the browser. Colours come from the tokens.
// `node scripts/badges.mjs` rewrites them; a test fails when they are stale.
import { mkdirSync, readFileSync, writeFileSync } from 'node:fs';
import { dirname, join } from 'node:path';
import { fileURLToPath } from 'node:url';

const WEB = dirname(dirname(fileURLToPath(import.meta.url)));

/** Every badge file, without the extension: 1 to 9, "9plus", and "lost". */
export const BADGES = ['1', '2', '3', '4', '5', '6', '7', '8', '9', '9plus', 'lost'];

// 5x7 pixel digits, and 3x5 ones for "9+" where two must fit.
const BIG = {
  1: ['00100', '01100', '00100', '00100', '00100', '00100', '01110'],
  2: ['01110', '10001', '00001', '00010', '00100', '01000', '11111'],
  3: ['11110', '00001', '00001', '01110', '00001', '00001', '11110'],
  4: ['00010', '00110', '01010', '10010', '11111', '00010', '00010'],
  5: ['11111', '10000', '11110', '00001', '00001', '10001', '01110'],
  6: ['00110', '01000', '10000', '11110', '10001', '10001', '01110'],
  7: ['11111', '00001', '00010', '00100', '01000', '01000', '01000'],
  8: ['01110', '10001', '10001', '01110', '10001', '10001', '01110'],
  9: ['01110', '10001', '10001', '01111', '00001', '00010', '01100'],
  '?': ['01110', '10001', '00001', '00010', '00100', '00000', '00100'],
};
const SMALL = {
  9: ['111', '101', '111', '001', '111'],
  '+': ['000', '010', '111', '010', '000'],
};

/** Reads the four colours the icons use from tokens.css. */
export function tokenColours(css) {
  const token = (name) => {
    const v = css.match(new RegExp(`^\\s*--${name}:\\s*(#[0-9a-f]{6});`, 'm'))?.[1];
    if (!v) throw new Error(`tokens.css has no --${name}`);
    return v;
  };
  return { bg: token('bg'), fg: token('fg'), amber: token('amber'), muted: token('muted') };
}

const sq = (x, y, fill) => `<rect x="${x}" y="${y}" width="4" height="4" fill="${fill}"/>`;

// One run of lit pixels per row keeps the files small.
function glyph(rows, x0, y0, fill) {
  let out = '';
  rows.forEach((row, y) => {
    for (const m of row.matchAll(/1+/g)) {
      out += `<rect x="${x0 + m.index}" y="${y0 + y}" width="${m[0].length}" height="1" fill="${fill}"/>`;
    }
  });
  return out;
}

function svg(c, body) {
  return `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 16 16"><rect width="16" height="16" fill="${c.bg}"/>${body}</svg>\n`;
}

// The mark: six squares of a small board, the one at the top left amber.
// A badge takes the square at the centre and the free corner beside it.
const ell = (c) => sq(1, 1, c.amber) + sq(1, 6, c.fg) + sq(1, 11, c.fg) + sq(6, 1, c.fg);

export function favicon(c) {
  return svg(c, ell(c) + sq(6, 6, c.fg) + sq(11, 1, c.fg));
}

/** A badge icon: the mark with a 9x9 block in its lower right corner. */
export function badge(c, name) {
  if (!BADGES.includes(name)) throw new Error(`no badge ${name}`);
  const lost = name === 'lost';
  let block = `<rect x="7" y="7" width="9" height="9" fill="${lost ? c.muted : c.amber}"/>`;
  if (name === '9plus') block += glyph(SMALL[9], 8, 9, c.bg) + glyph(SMALL['+'], 12, 9, c.bg);
  else block += glyph(BIG[lost ? '?' : name], 9, 8, c.bg);
  return svg(c, ell(c) + sq(11, 1, c.fg) + block);
}

/** Every icon file, path relative to web/, and its content. */
export function iconFiles(css) {
  const c = tokenColours(css);
  const files = { 'public/favicon.svg': favicon(c) };
  for (const b of BADGES) files[`public/badge/${b}.svg`] = badge(c, b);
  return files;
}

if (process.argv[1] && fileURLToPath(import.meta.url) === process.argv[1]) {
  const files = iconFiles(readFileSync(join(WEB, 'src/styles/tokens.css'), 'utf8'));
  for (const [rel, body] of Object.entries(files)) {
    mkdirSync(dirname(join(WEB, rel)), { recursive: true });
    writeFileSync(join(WEB, rel), body);
  }
  console.log(`badges: wrote ${Object.keys(files).length} icons`);
}
