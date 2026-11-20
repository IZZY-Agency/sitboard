import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { flushSync, mount, unmount } from 'svelte';
import { SvelteMap } from 'svelte/reactivity';

// A stand-in for the browser's IntersectionObserver and a tab that can be hidden,
// both in place before the page's observer is made.
const env = vi.hoisted(() => {
  class FakeIO {
    static all: FakeIO[] = [];
    els = new Set<Element>();
    observed = 0;
    constructor(
      public cb: (entries: { target: Element; isIntersecting: boolean }[], io: FakeIO) => void,
      public opts: IntersectionObserverInit,
    ) {
      FakeIO.all.push(this);
    }
    observe(el: Element) {
      this.observed++;
      this.els.add(el);
    }
    unobserve(el: Element) {
      this.els.delete(el);
    }
    disconnect() {
      this.els.clear();
    }
    get scrollMargin() {
      return this.opts.scrollMargin ?? '0px';
    }
    report(el: Element, isIntersecting: boolean) {
      this.cb([{ target: el, isIntersecting }], this);
    }
  }
  (globalThis as unknown as { IntersectionObserver: unknown }).IntersectionObserver = FakeIO;
  const tab = { hidden: false };
  Object.defineProperty(document, 'visibilityState', { configurable: true, get: () => (tab.hidden ? 'hidden' : 'visible') });
  return { FakeIO, tab, motion: { reduced: false } };
});
vi.mock('svelte/motion', async (orig) => ({
  ...(await orig<typeof import('svelte/motion')>()),
  prefersReducedMotion: {
    get current() {
      return env.motion.reduced;
    },
  },
}));

const { default: Critter } = await import('./Critter.svelte');
const { MOVES } = await import('../data/critter');
const { SCREEN_MARGIN, observerOptions } = await import('../onscreen.svelte');

const apps: Record<string, unknown>[] = [];
const targets: HTMLElement[] = [];
// Fake timers throughout: the page's observer looks on the next animation frame,
// and a frame left pending across tests would leave it waiting forever.
beforeEach(() => fakeTimers());
afterEach(() => {
  for (const a of apps.splice(0)) unmount(a);
  for (const t of targets.splice(0)) t.remove();
  setTab(false);
  vi.runOnlyPendingTimers();
  vi.useRealTimers();
});

function io() {
  expect(env.FakeIO.all).toHaveLength(1);
  return env.FakeIO.all[0];
}

function setTab(hidden: boolean) {
  env.tab.hidden = hidden;
  document.dispatchEvent(new Event('visibilitychange'));
  flushSync();
}

function show(id: string, mood = 'working', moves = new SvelteMap<string, number>()) {
  const target = document.createElement('div');
  document.body.appendChild(target);
  targets.push(target);
  apps.push(mount(Critter, { target, props: { id, mood } as never, context: new Map([[MOVES, moves]]) }));
  flushSync();
  return { cr: target.querySelector('[data-critter]') as HTMLElement, moves };
}

const still = (cr: HTMLElement) => cr.classList.contains('still');

function tick(ms: number) {
  flushSync();
  vi.advanceTimersByTime(ms);
  flushSync();
}
function fakeTimers() {
  vi.useFakeTimers({ toFake: ['setTimeout', 'clearTimeout', 'requestAnimationFrame', 'cancelAnimationFrame'] });
}

describe('a critter moves only while it can be seen', () => {
  it('one observer for the page watches every critter, with a margin on the viewport and on scrolled columns', () => {
    const a = show('claude:a').cr;
    const b = show('claude:b').cr;
    const c = show('claude:c').cr;
    const o = io();
    expect([...o.els]).toEqual([a, b, c]);
    expect(o.opts).toEqual(observerOptions(true));
  });

  it('the margin counts once: on every scrolled box where the browser can, else on the viewport', () => {
    const m = `${SCREEN_MARGIN}px`;
    expect(observerOptions(true)).toEqual({ scrollMargin: m, threshold: 0 });
    expect(observerOptions(false)).toEqual({ rootMargin: m, threshold: 0 });
  });

  it('is still until the observer reports it on screen, and still again when it leaves', () => {
    const { cr } = show('claude:a');
    expect(still(cr)).toBe(true);
    io().report(cr, true);
    flushSync();
    expect(still(cr)).toBe(false);
    // Scrolled away, or no longer rendered (a collapsed part, display: none): the observer says so.
    io().report(cr, false);
    flushSync();
    expect(still(cr)).toBe(true);
  });

  it('is still while the browser tab is hidden, and moves again when it is shown', () => {
    const { cr } = show('claude:a');
    io().report(cr, true);
    flushSync();
    setTab(true);
    expect(still(cr)).toBe(true);
    setTab(false);
    expect(still(cr)).toBe(false);
  });

  it('stops being watched when it goes away (a view that is not shown unmounts)', () => {
    const { cr } = show('claude:a');
    expect(io().els.has(cr)).toBe(true);
    unmount(apps.pop()!);
    expect(io().els.has(cr)).toBe(false);
    // Nothing that happens later brings it back.
    fakeTimers();
    window.dispatchEvent(new Event('scroll'));
    tick(20);
    expect(io().els.has(cr)).toBe(false);
  });

  // While the observer watches anything, Chrome runs style, layout and paint on every
  // frame a compositor animation draws: it must look, then let go.
  it('lets go of a critter as soon as it has reported on it', () => {
    const { cr } = show('claude:a');
    const b = show('claude:b').cr;
    io().report(cr, true);
    io().report(b, false);
    expect(io().els.size).toBe(0);
  });

  it.each([
    ['a scroll anywhere on the page, a scrolled column too', () => document.body.dispatchEvent(new Event('scroll'))],
    ['a resize', () => window.dispatchEvent(new Event('resize'))],
    ['the tab shown again', () => setTab(false)],
  ])('looks again, once, on the next frame after %s', (_what, happen) => {
    fakeTimers();
    const { cr } = show('claude:a');
    const b = show('claude:b').cr;
    io().report(cr, true);
    io().report(b, true);
    expect(io().els.size).toBe(0);
    const before = io().observed;
    happen();
    happen();
    expect(io().els.size).toBe(0);
    tick(20);
    expect([...io().els]).toEqual([cr, b]);
    expect(io().observed - before).toBe(2);
  });

  it('looks again when the page changes under the critters', async () => {
    fakeTimers();
    const { cr } = show('claude:a');
    io().report(cr, true);
    document.body.appendChild(document.createElement('p')).remove();
    await Promise.resolve();
    tick(20);
    expect(io().els.has(cr)).toBe(true);
  });

  it('the still rule stops every loop of the critter', async () => {
    const { readFileSync } = await import('node:fs');
    const src = readFileSync(`${process.cwd()}/src/components/Critter.svelte`, 'utf8');
    expect(src).toMatch(/\.still :global\(\*\) \{\s*animation: none !important;\s*\}/);
    expect(src).toMatch(/class:still=\{!shown\}/);
  });
});

describe('the move on a change of state', () => {
  it('plays for a critter on screen', () => {
    fakeTimers();
    const { cr, moves } = show('claude:a');
    io().report(cr, true);
    flushSync();
    moves.set('claude:a', 1);
    tick(20);
    expect(cr.classList.contains('tx-working')).toBe(true);
  });

  it('does not play off screen, nor later when the critter scrolls into view', () => {
    fakeTimers();
    const { cr, moves } = show('claude:a');
    io().report(cr, false);
    flushSync();
    moves.set('claude:a', 1);
    tick(20);
    expect(cr.classList.contains('tx')).toBe(false);
    io().report(cr, true);
    tick(20);
    expect(cr.classList.contains('tx')).toBe(false);
    expect(still(cr)).toBe(false);
  });

  it('does not play in a hidden tab, nor when the tab is shown again', () => {
    fakeTimers();
    const { cr, moves } = show('claude:a');
    io().report(cr, true);
    flushSync();
    setTab(true);
    moves.set('claude:a', 1);
    tick(20);
    setTab(false);
    tick(20);
    expect(cr.classList.contains('tx')).toBe(false);
  });

  it('a critter that just mounted with a fresh token plays it once the observer finds it on screen (a card that moved column)', () => {
    fakeTimers();
    const { cr } = show('claude:a', 'done', new SvelteMap([['claude:a', 4]]));
    tick(20);
    expect(cr.classList.contains('tx')).toBe(false);
    io().report(cr, true);
    tick(20);
    expect(cr.classList.contains('tx-done')).toBe(true);
  });

  it('a critter that just mounted off screen with a fresh token never plays it', () => {
    fakeTimers();
    const { cr } = show('claude:a', 'done', new SvelteMap([['claude:a', 4]]));
    io().report(cr, false);
    tick(20);
    io().report(cr, true);
    tick(20);
    expect(cr.classList.contains('tx')).toBe(false);
  });
});
