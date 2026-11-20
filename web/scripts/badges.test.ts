import { describe, expect, it } from 'vitest';
import { existsSync, readdirSync, readFileSync } from 'node:fs';
import { join } from 'node:path';
// @ts-expect-error plain JavaScript module without types
import { BADGES, badge, iconFiles, tokenColours } from './badges.mjs';
import { tabIcon } from '../src/data/nudge';

const WEB = process.cwd(); // tests run from web/
const css = readFileSync(join(WEB, 'src/styles/tokens.css'), 'utf8');
const colours = tokenColours(css) as Record<'bg' | 'fg' | 'amber' | 'muted', string>;

/** The lit pixels of an icon, as a 16x16 grid of fill colours ('' for the ground). */
function grid(svg: string): string[][] {
  const g = Array.from({ length: 16 }, () => Array<string>(16).fill(''));
  for (const m of svg.matchAll(/<rect( x="(\d+)" y="(\d+)")? width="(\d+)" height="(\d+)" fill="(#[0-9a-f]{6})"\/>/g)) {
    const [x, y, w, h] = [Number(m[2] ?? 0), Number(m[3] ?? 0), Number(m[4]), Number(m[5])];
    for (let j = y; j < y + h; j++) for (let i = x; i < x + w; i++) g[j][i] = m[6] === colours.bg ? '' : m[6];
  }
  return g;
}

describe('tab icons', () => {
  it('the files in public/ are the ones scripts/badges.mjs writes (run it after a token change)', () => {
    for (const [rel, body] of Object.entries(iconFiles(css) as Record<string, string>)) {
      expect(existsSync(join(WEB, rel)), rel).toBe(true);
      expect(readFileSync(join(WEB, rel), 'utf8'), rel).toBe(body);
    }
    expect(readdirSync(join(WEB, 'public/badge')).sort()).toEqual(BADGES.map((b: string) => `${b}.svg`).sort());
  });

  it('every icon the page can ask for exists', () => {
    const asked = new Set([tabIcon({ kind: 'plain' }), tabIcon({ kind: 'lost', word: 'x' })]);
    for (let n = 1; n <= 12; n++) asked.add(tabIcon({ kind: 'count', n }));
    for (const href of asked) expect(existsSync(join(WEB, 'public', href)), href).toBe(true);
  });

  it('holds only whole-pixel rectangles in the token colours: no script, link, text or style', () => {
    const allowed = new Set(Object.values(colours));
    for (const b of BADGES) {
      const svg = badge(colours, b) as string;
      const body = svg.replace(/^<svg xmlns="http:\/\/www.w3.org\/2000\/svg" viewBox="0 0 16 16">/, '').replace(/<\/svg>\n$/, '');
      expect(body.replace(/<rect( x="\d+" y="\d+")? width="\d+" height="\d+" fill="#[0-9a-f]{6}"\/>/g, ''), b).toBe('');
      for (const m of body.matchAll(/fill="(#[0-9a-f]{6})"/g)) expect(allowed.has(m[1]), b).toBe(true);
    }
  });

  it('keeps the mark’s amber square and its L of squares, and puts the count in the corner', () => {
    for (const b of BADGES) {
      const g = grid(badge(colours, b));
      expect(g[1][1], b).toBe(colours.amber);
      for (const [x, y] of [[1, 6], [1, 11], [6, 1], [11, 1]]) expect(g[y][x], `${b} ${x},${y}`).toBe(colours.fg);
      // The block's edge: a count is amber, "not live" is muted.
      expect(g[7][7], b).toBe(b === 'lost' ? colours.muted : colours.amber);
      expect(g[15][15], b).toBe(b === 'lost' ? colours.muted : colours.amber);
      // A one-pixel gap of ground between the block and the squares around it.
      for (let i = 0; i < 16; i++) {
        expect(g[6][i] === '' || i < 6, `${b} row 6`).toBe(true);
        expect(g[i][6] === '' || i < 6, `${b} column 6`).toBe(true);
      }
    }
  });

  it('every badge looks different from every other', () => {
    const seen = new Map<string, string>();
    for (const b of BADGES) {
      const key = JSON.stringify(grid(badge(colours, b)));
      expect(seen.get(key), b).toBeUndefined();
      seen.set(key, b);
    }
  });
});
