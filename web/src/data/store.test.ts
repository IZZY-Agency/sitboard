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

  it('one setting request per folder and mode at a time; another mode is its own request', async () => {
    const calls: string[] = [];
    const answers: Array<(v: ActionResult) => void> = [];
    const actions: Actions = {
      ...recordActions().actions,
      setGrouping: (id, mode) => {
        calls.push(`${id} ${mode}`);
        return new Promise<ActionResult>((res) => answers.push(res));
      },
    };
    const b = new BoardState(() => actions);
    const p = { id: 'p_main', name: 'mainapp' };
    const first = b.setFolderGrouping(p, 'never');
    const second = b.setFolderGrouping(p, 'never');
    void b.setFolderGrouping(p, 'always');
    expect(calls).toEqual(['p_main never', 'p_main always']);
    answers[0]({ ok: true });
    expect(await first).toBe(true);
    expect(await second).toBe(true);
    void b.setFolderGrouping(p, 'never');
    expect(calls).toEqual(['p_main never', 'p_main always', 'p_main never']);
  });

  it('a setting the server answers after its snapshot already showed it is said at once', async () => {
    vi.useFakeTimers();
    const base = snapshot([card({ projectId: 'p_main' })], [project('p_main', 'mainapp')]);
    const f = feedOf({ conn: 'live', snapshot: base });
    const actions: Actions = {
      ...recordActions().actions,
      setGrouping: async (id, mode) => {
        // The server publishes before it answers.
        f.push({ conn: 'live', snapshot: { ...base, seq: 2, groupingSettings: [{ projectId: id, name: 'mainapp', path: '~/projects/mainapp', mode }] } });
        return { ok: true };
      },
    };
    const b = new BoardState(() => actions);
    b.start(f.feed);
    expect(await b.setFolderGrouping({ id: 'p_main', name: 'mainapp' }, 'never')).toBe(true);
    vi.runAllTimers();
    expect(b.said).toBe('mainapp never groups now. Its parts are no longer grouped under it.');
  });

  it('a snapshot from before the change, still on its way when the answer comes, is not announced', async () => {
    vi.useFakeTimers();
    const base = snapshot([card({ projectId: 'p_main' })], [project('p_main', 'mainapp')]);
    const f = feedOf({ conn: 'live', snapshot: base });
    const b = new BoardState(() => recordActions().actions);
    b.start(f.feed);
    expect(await b.setFolderGrouping({ id: 'p_main', name: 'mainapp' }, 'never')).toBe(true);
    // Published before the change (still auto), arriving after the 204.
    f.push({ conn: 'live', snapshot: { ...base, seq: 2 } });
    vi.advanceTimersByTime(1);
    expect(b.said).toBe('');
    f.push({ conn: 'live', snapshot: { ...base, seq: 3, groupingSettings: [{ projectId: 'p_main', name: 'mainapp', path: '~/projects/mainapp', mode: 'never' }] } });
    vi.runAllTimers();
    expect(b.said).toBe('mainapp never groups now. Its parts are no longer grouped under it.');
  });

  it('a setting no snapshot shows within a few seconds is not said later', async () => {
    vi.useFakeTimers();
    const base = snapshot([card({ projectId: 'p_main' })], [project('p_main', 'mainapp')]);
    const f = feedOf({ conn: 'live', snapshot: base });
    const b = new BoardState(() => recordActions().actions);
    b.start(f.feed);
    expect(await b.setFolderGrouping({ id: 'p_main', name: 'mainapp' }, 'never')).toBe(true);
    vi.advanceTimersByTime(4999);
    f.push({ conn: 'live', snapshot: { ...base, seq: 2 } }); // still the old mode: keeps waiting
    vi.advanceTimersByTime(1);
    // Much later, another tab sets it to always: this tab's never is not announced as anything.
    f.push({ conn: 'live', snapshot: { ...base, seq: 3, groupingSettings: [{ projectId: 'p_main', name: 'mainapp', path: '~/projects/mainapp', mode: 'always' }] } });
    vi.runAllTimers();
    expect(b.said).toBe('');
  });

  it('a failed setting names it with a retry; a final refusal has none; a later success clears it', async () => {
    let answer: ActionResult = { ok: false, kind: 'failed', message: 'sitboard could not save it' };
    const actions: Actions = { ...recordActions().actions, setGrouping: async () => answer };
    const b = new BoardState(() => actions);
    const p = { id: 'p_main', name: 'mainapp' };
    expect(await b.setFolderGrouping(p, 'always')).toBe(false);
    expect(b.notice).toMatchObject({ kind: 'failed', what: 'Set mainapp to always group', message: 'sitboard could not save it' });
    expect(b.notice?.kind === 'failed' && b.notice.retry).toBeTypeOf('function');
    answer = { ok: false, kind: 'failed', message: 'it is no longer grouped', final: true };
    expect(await b.setFolderGrouping(p, 'auto')).toBe(false);
    expect(b.notice).toMatchObject({ what: 'Set mainapp back to auto grouping', retry: undefined });
    answer = { ok: true };
    expect(await b.setFolderGrouping(p, 'never')).toBe(true);
    expect(b.notice).toBeNull();
  });
});
