import { afterEach, describe, expect, it } from 'vitest';
import { readFileSync } from 'node:fs';
import { join } from 'node:path';
import Header from './Header.svelte';
import SpendButton from './SpendButton.svelte';
import { render } from '../test/render';
import type { Conn } from '../data/client';
import { clock } from '../data/time';

// Tests run from web/.
const SRC = join(process.cwd(), 'src');

// The rules of a component's <style> block, as selector → declarations.
function rules(file: string): Array<{ sel: string; body: string }> {
  const text = readFileSync(join(SRC, file), 'utf8');
  const css = text.match(/<style[^>]*>([\s\S]*?)<\/style>/)?.[1] ?? text;
  // Leaf rules only: a selector list and a body without nested braces.
  return [...css.replace(/\/\*[\s\S]*?\*\//g, '').matchAll(/([^{}]+)\{([^{}]*)\}/g)].map((m) => ({ sel: m[1].trim(), body: m[2] }));
}
function decl(body: string, prop: string): string | undefined {
  return body.match(new RegExp(`(?:^|;|\\s)${prop}:\\s*([^;]+);`))?.[1].trim();
}
const BOX = ['min-height', 'padding', 'font-size', 'font-weight', 'letter-spacing', 'text-transform'] as const;
function box(file: string, sel: string) {
  // Every rule with this selector, the phone layout's included.
  const body = rules(file)
    .filter((r) => r.sel === sel)
    .map((r) => r.body)
    .join(';');
  return Object.fromEntries(BOX.map((p) => [p, decl(body, p)]));
}

let r: ReturnType<typeof render> | null = null;
afterEach(() => {
  r?.destroy();
  r = null;
});

function chip(conn: Conn) {
  r?.destroy();
  r = render(Header, { view: 'board', onview: () => {}, agents: 1, projects: 1, conn, generatedAt: '2026-11-23T10:04:00Z' });
  return r.target.querySelector('.live')!;
}

describe('the Live chip', () => {
  it('has the box and type of the other header chips (Spend, Catch-up, Notify)', () => {
    const live = box('components/Header.svelte', '.live');
    expect(live['min-height']).toBe('44px');
    expect(live).toEqual(box('components/CatchupButton.svelte', '.catchup'));
    expect(live).toEqual(box('components/SpendButton.svelte', '.spend'));
    // Notify keeps the state word bold and the name regular; the box and spacing match.
    const notify = box('components/NotifyToggle.svelte', '.notify');
    for (const p of ['min-height', 'padding', 'font-size', 'letter-spacing', 'text-transform'] as const) expect(live[p], p).toBe(notify[p]);
  });

  it('is green with the working green only while connected; the other states keep their colours', () => {
    expect(chip('live').className).toMatch(/\blive on\b/);
    for (const conn of ['reconnecting', 'stopped', 'unreachable', 'connecting', 'unauthorized', 'outdated'] satisfies Conn[]) {
      expect(chip(conn).classList.contains('on'), conn).toBe(false);
    }
    expect(chip('reconnecting').classList.contains('warn')).toBe(true);
    expect(chip('stopped').classList.contains('warn')).toBe(true);
    expect(chip('connecting').classList.contains('quiet')).toBe(true);
    const on = rules('components/Header.svelte').find((x) => x.sel === '.live.on')!.body;
    expect(decl(on, 'color')).toBe('var(--state-working)');
    expect(decl(on, 'background')).toBe('var(--working-soft)');
    expect(decl(on, 'border-color')).toBe('var(--working-edge)');
  });

  it('says "Stopped" with the time of the last update once the board is stopped', () => {
    r?.destroy();
    const at = new Date('2026-11-23T10:04:00Z').getTime();
    r = render(Header, { view: 'board', onview: () => {}, agents: 1, projects: 1, conn: 'stopped', generatedAt: '2026-11-23T09:00:00Z', lostAt: at });
    const t = r.target.querySelector('.live')!.textContent!.trim();
    expect(t).toBe(`Stopped · last update ${clock(new Date(at).toISOString())}`);
    expect(t).toMatch(/last update \d\d:\d\d$/);
    expect(chip('stopped').textContent!.trim()).toBe('Stopped'); // no time known: just the word
  });

  it('pulses its dot only while connected, and not at all under reduced motion', () => {
    const all = rules('components/Header.svelte');
    const moving = all.filter((x) => /(^|\s)animation:/.test(x.body)).map((x) => x.sel);
    expect(moving.length).toBeGreaterThan(0);
    // Every animated rule is the connected dot or its ring; the base dot and the
    // warn and quiet dots have none.
    for (const sel of moving) expect(sel, sel).toMatch(/^\.live\.on \.dot(::after)?$/);
    expect(moving).toContain('.live.on .dot');
    // About two seconds, and it never changes the layout: only opacity and transform move.
    for (const sel of moving) expect(decl(all.find((x) => x.sel === sel)!.body, 'animation'), sel).toMatch(/^live-(dot|ring) 2s /);
    const css = readFileSync(join(SRC, 'components/Header.svelte'), 'utf8');
    for (const k of css.matchAll(/@keyframes [\w-]+ \{([\s\S]*?)\n {2}\}/g)) {
      const props = [...k[1].matchAll(/([a-z-]+):/g)].map((m) => m[1]);
      expect(props.every((p) => p === 'opacity' || p === 'transform'), props.join()).toBe(true);
    }
    // Reduced motion: base.css stops every animation, the ring's ::after included.
    const base = readFileSync(join(SRC, 'styles/base.css'), 'utf8');
    expect(base).toMatch(/@media \(prefers-reduced-motion: reduce\) \{\s*\*,\s*\*::before,\s*\*::after \{\s*animation: none !important;/);
  });
});

// #143: at 360 px the Spend chip, with its widest figure, was wider than the
// page and lost its first letters off the left edge. jsdom lays nothing out,
// so this guards the rules that keep it in: the button never grows past its
// row, and its figure wraps only between its parts, so the money stays whole.
describe('the Spend chip at phone width', () => {
  const WIDEST = ['≈ $15.48 + unpriced', 'Codex 12 k out …'];

  it('renders each part of the figure on its own unbreakable run, the money first', () => {
    r?.destroy();
    r = render(SpendButton, { figure: WIDEST, spoken: 'about $15.48', note: '', title: 'hover', open: false, controls: 'x', onclick: () => {} });
    const parts = [...r.target.querySelectorAll('.spend .n > .p')].map((p) => p.textContent);
    expect(parts).toEqual(['≈ $15.48 + unpriced ·', 'Codex 12 k out …']);
    // Between the runs only a plain space, where the line may break.
    expect(r.target.querySelector('.spend .n')!.textContent).toBe('≈ $15.48 + unpriced · Codex 12 k out …');
  });

  it('never grows past its row, and wraps between the parts, not inside one', () => {
    const all = rules('components/SpendButton.svelte');
    const body = (sel: string) =>
      all
        .filter((x) => x.sel === sel)
        .map((x) => x.body)
        .join(';');
    expect(decl(body('.spend'), 'max-width')).toBe('100%');
    expect(decl(body('.n'), 'white-space')).toBe('normal');
    expect(decl(body('.n'), 'min-width')).toBe('0');
    expect(decl(body('.p'), 'white-space')).toBe('nowrap');
    // The phone layout gives the controls their own row, which wraps.
    const phone = readFileSync(join(SRC, 'components/Header.svelte'), 'utf8').match(/@media \(max-width: 559px\) \{([\s\S]*?)\n {2}\}\n/)![1];
    const controls = [...phone.matchAll(/([^{}]+)\{([^{}]*)\}/g)].find((m) => m[1].trim() === '.controls')![2];
    expect(decl(controls, 'flex-wrap')).toBe('wrap');
  });
});
