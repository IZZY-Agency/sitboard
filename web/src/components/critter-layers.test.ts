import { afterEach, describe, expect, it } from 'vitest';
import { flushSync, mount, unmount } from 'svelte';
import { readFileSync } from 'node:fs';
import { CRITTER_STATES } from '../data/critter';
import Critter from './Critter.svelte';

// Chrome animates an HTML box, or an <svg> root's opacity, on the compositor;
// an animation or transition on an element inside an <svg> is laid out and
// styled again on the main thread on every frame. So every rule in the
// critter that animates or transitions must only ever reach HTML boxes and
// <svg> roots, never a shape inside one.

const src = readFileSync(`${process.cwd()}/src/components/Critter.svelte`, 'utf8');
const css = src.slice(src.indexOf('<style>') + '<style>'.length, src.indexOf('</style>'));

/** Every selector of a style rule that starts or keeps an animation or a transition. */
function movingSelectors(): string[] {
  const style = document.createElement('style');
  style.textContent = css;
  document.head.appendChild(style);
  const out: string[] = [];
  const walk = (rules: CSSRuleList) => {
    for (const r of Array.from(rules)) {
      if (r instanceof CSSStyleRule) {
        const s = r.style;
        const anim = [s.getPropertyValue('animation'), s.getPropertyValue('animation-name')].some((v) => v && !/^none\b/.test(v.trim()));
        const trans = [s.getPropertyValue('transition'), s.getPropertyValue('transition-property')].some((v) => v && !/^none\b/.test(v.trim()));
        if (anim || trans) out.push(...r.selectorText.split(/,(?![^(]*\))/).map((x) => x.trim()));
      } else if ('cssRules' in r && !(r instanceof CSSKeyframesRule)) {
        walk((r as CSSGroupingRule).cssRules);
      }
    }
  };
  walk(style.sheet!.cssRules);
  style.remove();
  // Svelte's :global(x) is plain x once compiled.
  return out.map((x) => x.replace(/:global\(([^()]*)\)/g, '$1'));
}

let apps: Record<string, unknown>[] = [];
let target: HTMLElement | null = null;
afterEach(() => {
  for (const a of apps) unmount(a);
  apps = [];
  target?.remove();
  target = null;
});

describe('no critter part inside an SVG moves', () => {
  it('finds the rules that move something (the guard is not empty)', () => {
    const sel = movingSelectors();
    expect(sel.length).toBeGreaterThan(10);
    expect(sel.some((x) => x.includes('.bL'))).toBe(true);
  });

  it('every animation and transition reaches only HTML boxes and <svg> roots, in every state and move', () => {
    target = document.createElement('div');
    document.body.appendChild(target);
    for (const size of [20, 24]) {
      for (const mood of CRITTER_STATES) {
        const box = document.createElement('div');
        target.appendChild(box);
        apps.push(mount(Critter, { target: box, props: { id: `claude:${mood}-${size}`, mood, size } }));
      }
    }
    flushSync();
    // Every move as well: the page adds these while a move plays.
    for (const el of target.querySelectorAll<HTMLElement>('[data-state]')) el.classList.add('tx', `tx-${el.dataset.state}`);
    const roots = target.querySelectorAll('[data-critter]');
    expect(roots.length).toBe(2 * CRITTER_STATES.length);

    const inside: string[] = [];
    for (const sel of movingSelectors()) {
      for (const el of target.querySelectorAll(sel)) {
        const svg = el.parentElement?.closest('svg');
        if (svg) inside.push(`${sel} → <${el.tagName.toLowerCase()} class="${el.getAttribute('class') ?? ''}"> inside <svg class="${svg.getAttribute('class') ?? ''}">`);
      }
    }
    expect([...new Set(inside)]).toEqual([]);
  });
});
