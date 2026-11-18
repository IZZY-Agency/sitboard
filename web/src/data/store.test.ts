import { afterEach, describe, expect, it, vi } from 'vitest';
import { flushSync } from 'svelte';
import { BoardState, dedupe, FLASH_TTL } from './store.svelte';
import { card, project, snapshot } from '../test/fixtures';
import { feedOf, recordActions } from '../test/render';
import type { ActionResult, Actions } from './actions';

afterEach(() => vi.useRealTimers());

describe('store', () => {
  it('an arrival in Needs you can flash only for a short while, so a remount does not replay it', () => {
    vi.useFakeTimers();
    const c = card({ column: 'working' });
    const f = feedOf({ conn: 'live', snapshot: snapshot([c], [project(c.projectId, 'one')]) });
    const b = new BoardState(() => recordActions().actions);
    const stop = b.start(f.feed);
    f.push({ conn: 'live', snapshot: snapshot([{ ...c, column: 'needs_you' }], [project(c.projectId, 'one')]) });
    expect(b.arrivals.get(c.id)).toBeGreaterThan(0);
    vi.advanceTimersByTime(FLASH_TTL);
    flushSync();
    expect(b.arrivals.has(c.id)).toBe(false);
    stop();
  });

  it('records when the connection was lost, and clears it once live again', () => {
    let t = 1000;
    const s = snapshot([card()], []);
    const f = feedOf({ conn: 'live', snapshot: s });
    const b = new BoardState(() => recordActions().actions, () => t);
    b.start(f.feed);
    expect(b.lostAt).toBeNull();
    t = 5000;
    f.push({ conn: 'reconnecting', snapshot: s });
    t = 9000;
    f.push({ conn: 'reconnecting', snapshot: s });
    expect(b.lostAt).toBe(5000);
    f.push({ conn: 'live', snapshot: s });
    expect(b.lostAt).toBeNull();
  });

  it('drops duplicate ids so a keyed list never throws', () => {
    const c = card();
    const s = snapshot([c, { ...c, title: 'duplicate' }], [project('p_a', 'a'), project('p_a', 'a')]);
    const d = dedupe(s);
    expect(d.cards).toHaveLength(1);
    expect(d.cards[0].title).toBe(c.title);
    expect(d.projects).toHaveLength(1);
    const clean = snapshot([card()], []);
    expect(dedupe(clean)).toBe(clean);
  });

  it('one forget per project at a time: a second press while one is out gets the same answer', async () => {
    const calls: string[] = [];
    let answer: (v: ActionResult) => void = () => {};
    const actions: Actions = {
      ...recordActions().actions,
      forgetGrouping: (id) => {
        calls.push(id);
        return new Promise<ActionResult>((res) => (answer = res));
      },
    };
    const b = new BoardState(() => actions);
    const p = { id: 'p_main', name: 'mainapp' };
    const first = b.forgetGrouping(p); // the tiles' control
    const second = b.forgetGrouping(p); // the rail's, or a second click
    expect(calls).toEqual(['p_main']);
    answer({ ok: true });
    expect(await first).toBe(true);
    expect(await second).toBe(true);
    expect(b.notice).toBeNull();
    // Once it is answered, a new press sends a new request.
    void b.forgetGrouping(p);
    expect(calls).toEqual(['p_main', 'p_main']);
  });
});
