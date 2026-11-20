import { afterEach, describe, expect, it, vi } from 'vitest';
import { flushSync, mount, unmount } from 'svelte';
import { SvelteMap } from 'svelte/reactivity';
import { MOVE_MS, MOVES } from '../data/critter';

// Reduced motion is on in tests (test/setup.ts); one switch here turns it off.
const motion = vi.hoisted(() => ({ reduced: true }));
vi.mock('svelte/motion', async (orig) => ({
  ...(await orig<typeof import('svelte/motion')>()),
  prefersReducedMotion: {
    get current() {
      return motion.reduced;
    },
  },
}));

const { default: Critter } = await import('./Critter.svelte');

let app: Record<string, unknown> | null = null;
let target: HTMLElement | null = null;
afterEach(() => {
  if (app) unmount(app);
  target?.remove();
  app = null;
  motion.reduced = true;
  vi.useRealTimers();
});

function show(props: { id: string; mood: string; size?: number }, moves = new SvelteMap<string, number>()) {
  target = document.createElement('div');
  document.body.appendChild(target);
  const p = $state(props);
  app = mount(Critter, { target, props: p as never, context: new Map([[MOVES, moves]]) });
  flushSync();
  return { cr: target.querySelector('[data-critter]') as HTMLElement, p, moves };
}

describe('Critter', () => {
  it('is hidden from screen readers: the state is said in words next to it', () => {
    const { cr } = show({ id: 'claude:a', mood: 'working' });
    expect(cr.getAttribute('aria-hidden')).toBe('true');
    expect(cr.textContent?.trim()).toBe('');
    expect(cr.classList.contains('st-working')).toBe(true);
  });

  it('draws the same critter for the same id, a 20 px one for Teams and Lanes', () => {
    const a = show({ id: 'claude:a', mood: 'idle', size: 20 }).cr;
    expect(a.style.width).toBe('20px');
    const key = a.dataset.critter;
    unmount(app!);
    app = null;
    expect(show({ id: 'claude:a', mood: 'done' }).cr.dataset.critter).toBe(key);
  });

  it('plays its move only when the page hands it a token, and then stops', async () => {
    motion.reduced = false;
    vi.useFakeTimers({ toFake: ['setTimeout', 'clearTimeout', 'requestAnimationFrame', 'cancelAnimationFrame'] });
    const { cr, p, moves } = show({ id: 'claude:a', mood: 'working' });
    vi.advanceTimersByTime(50);
    flushSync();
    expect(cr.classList.contains('tx')).toBe(false);
    // A re-render with the same state: nothing.
    p.mood = 'working';
    flushSync();
    vi.advanceTimersByTime(50);
    flushSync();
    expect(cr.classList.contains('tx')).toBe(false);
    p.mood = 'blocked';
    moves.set('claude:a', 1);
    flushSync();
    vi.advanceTimersByTime(20);
    flushSync();
    expect(cr.classList.contains('tx-blocked')).toBe(true);
    vi.advanceTimersByTime(MOVE_MS);
    flushSync();
    expect(cr.classList.contains('tx')).toBe(false);
  });

  it('ends its move on time even when the page drops the token while it plays', () => {
    motion.reduced = false;
    vi.useFakeTimers({ toFake: ['setTimeout', 'clearTimeout', 'requestAnimationFrame', 'cancelAnimationFrame'] });
    const moves = new SvelteMap([['claude:a', 4]]);
    const { cr } = show({ id: 'claude:a', mood: 'done' }, moves);
    vi.advanceTimersByTime(20);
    flushSync();
    expect(cr.classList.contains('tx-done')).toBe(true);
    moves.delete('claude:a');
    flushSync();
    vi.advanceTimersByTime(MOVE_MS);
    flushSync();
    expect(cr.classList.contains('tx')).toBe(false);
  });

  it('a critter that mounts with a fresh token plays it: a card that moved column', () => {
    motion.reduced = false;
    vi.useFakeTimers({ toFake: ['setTimeout', 'clearTimeout', 'requestAnimationFrame', 'cancelAnimationFrame'] });
    const moves = new SvelteMap([['claude:a', 4]]);
    const { cr } = show({ id: 'claude:a', mood: 'done' }, moves);
    vi.advanceTimersByTime(20);
    flushSync();
    expect(cr.classList.contains('tx-done')).toBe(true);
  });

  it('with reduced motion nothing plays, and the state still reads from the still critter', () => {
    vi.useFakeTimers({ toFake: ['setTimeout', 'clearTimeout', 'requestAnimationFrame', 'cancelAnimationFrame'] });
    const moves = new SvelteMap([['claude:a', 4]]);
    const { cr } = show({ id: 'claude:a', mood: 'failing' }, moves);
    vi.advanceTimersByTime(100);
    flushSync();
    expect(cr.classList.contains('tx')).toBe(false);
    expect(cr.querySelector('.ex')?.classList.contains('on')).toBe(true);
  });
});

describe('a busy critter whose checks fail (1C, the owner picked it)', () => {
  it('keeps working eyes and its loop, stands on the failing red, and shakes once when the checks turn red', async () => {
    const { readFileSync } = await import('node:fs');
    const src = readFileSync(`${process.cwd()}/src/components/Critter.svelte`, 'utf8');
    expect(src).toMatch(/\.st-working_failing \{\s*--tone: var\(--state-failing\);/);
    expect(src).toMatch(/:is\(\.st-working, \.st-working_failing\) \.look \{/);
    expect(src).toMatch(/:is\(\.st-working, \.st-working_failing\) \.bL \{\s*animation: bob/);
    expect(src).toMatch(/:is\(\.st-working, \.st-working_failing\) \.eyes \{\s*animation: blinkLoop/);
    expect(src).toMatch(/:is\(\.tx-failing, \.tx-working_failing\) \.bT \{\s*animation: shake/);
    const { cr } = show({ id: 'claude:a', mood: 'working_failing' });
    expect(cr.querySelector('.eo')?.classList.contains('on')).toBe(true);
  });
});

describe('reduced motion stops every critter animation', () => {
  it('the page-wide rule covers the critter', async () => {
    const { readFileSync } = await import('node:fs');
    const css = readFileSync(`${process.cwd()}/src/styles/base.css`, 'utf8');
    expect(css).toMatch(/@media \(prefers-reduced-motion: reduce\)[\s\S]*animation: none !important;[\s\S]*transition: none !important;/);
  });
});
