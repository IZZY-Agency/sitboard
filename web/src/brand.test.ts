import { afterEach, describe, expect, it } from 'vitest';
import { existsSync, readFileSync } from 'node:fs';
import { join } from 'node:path';
import Header from './components/Header.svelte';
import { render } from './test/render';

// The mark: six squares of a small board, the one at the top left amber.
const SQUARES = [
  [6, 6, 'a'],
  [6, 13.5, 'c'],
  [6, 21, 'c'],
  [13.5, 6, 'c'],
  [13.5, 13.5, 'c'],
  [21, 6, 'c'],
] as const;

let r: ReturnType<typeof render> | null = null;
afterEach(() => {
  r?.destroy();
  r = null;
});

describe('brand', () => {
  it('the header shows the mark, hidden from screen readers, beside the wordmark', () => {
    r = render(Header, { view: 'board', onview: () => {}, agents: null, projects: null, conn: 'connecting', generatedAt: '' });
    const svg = r.target.querySelector('.brand svg.mark')!;
    expect(svg.getAttribute('aria-hidden')).toBe('true');
    const rects = [...svg.querySelectorAll('rect')];
    expect(rects.map((e) => [Number(e.getAttribute('x')), Number(e.getAttribute('y')), e.classList.contains('a') ? 'a' : e.classList.contains('c') ? 'c' : ''])).toEqual(
      SQUARES.map(([x, y, c]) => [x, y, c]),
    );
    expect(rects.every((e) => e.getAttribute('width') === '5' && e.getAttribute('height') === '5')).toBe(true);
    // Colours come from the tokens through CSS, not from fill attributes.
    expect(rects.some((e) => e.hasAttribute('fill'))).toBe(false);
    expect(r.target.querySelector('.brand .word')!.textContent).toBe('sitboard');
    expect(r.target.querySelector('.brand .word .accent')!.textContent).toBe('board');
  });

  it('every square edge of the header mark falls on a whole pixel', () => {
    r = render(Header, { view: 'board', onview: () => {}, agents: null, projects: null, conn: 'connecting', generatedAt: '' });
    const svg = r.target.querySelector('.brand svg.mark')!;
    const px = Number(svg.getAttribute('width'));
    expect(Number(svg.getAttribute('height'))).toBe(px);
    const [vx, vy, vw, vh] = svg.getAttribute('viewBox')!.split(' ').map(Number);
    expect(vw).toBe(vh);
    const k = px / vw;
    for (const [x, y] of SQUARES) {
      for (const edge of [(x - vx) * k, (x + 5 - vx) * k, (y - vy) * k, (y + 5 - vy) * k]) {
        expect(Number.isInteger(Math.round(edge * 1e9) / 1e9)).toBe(true);
      }
    }
  });

  it('the page links an SVG favicon: the mark on the dark tile, on a 16-unit grid, in the token colours, nothing else', () => {
    const html = readFileSync(join(process.cwd(), 'index.html'), 'utf8');
    expect(html).toContain('<link rel="icon" type="image/svg+xml" href="/favicon.svg" />');
    const file = join(process.cwd(), 'public/favicon.svg');
    expect(existsSync(file)).toBe(true);
    // The colours come from the tokens, so a token change cannot leave the icon behind.
    const css = readFileSync(join(process.cwd(), 'src/styles/tokens.css'), 'utf8');
    const token = (name: string) => css.match(new RegExp(`^\\s*--${name}:\\s*(#[0-9a-f]{6});`, 'm'))?.[1];
    const [bg, amber, fg] = [token('bg'), token('amber'), token('fg')];
    expect([bg, amber, fg].every(Boolean)).toBe(true);
    // 4-unit squares, 1-unit gaps and padding: whole pixels at 16px and 32px.
    // The whole file is compared, so no script, style, link, image or handler can slip in.
    const sq = (x: number, y: number, fill: string) => `<rect x="${x}" y="${y}" width="4" height="4" fill="${fill}"/>`;
    const expected =
      '<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 16 16">' +
      `<rect width="16" height="16" fill="${bg}"/>` +
      sq(1, 1, amber!) +
      sq(1, 6, fg!) +
      sq(1, 11, fg!) +
      sq(6, 1, fg!) +
      sq(6, 6, fg!) +
      sq(11, 1, fg!) +
      '</svg>\n';
    expect(readFileSync(file, 'utf8')).toBe(expected);
  });
});
