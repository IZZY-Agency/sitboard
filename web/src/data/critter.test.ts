import { afterEach, describe, expect, it, vi } from 'vitest';
import { flushSync } from 'svelte';
import { AttentionValues, ColumnValues, type Attention, type Card, type Column } from '../contract.gen';
import { critter, critterCount, type Critter, critterPhase, critterState, drawing, EYES, MOVE_TTL, stateChanges, CRITTER_STATES } from './critter';
import { BoardState } from './store.svelte';
import { card, project, snapshot } from '../test/fixtures';
import { feedOf, recordActions } from '../test/render';

afterEach(() => vi.useRealTimers());

describe('critter generator', () => {
  it('the same id always gives the same critter, drawn the same way', () => {
    for (const id of ['claude:site-lead', 'codex:0198-abc', 'claude:6f1c2d3e-0000-4000-8000-000000000001']) {
      expect(critter(id)).toEqual(critter(id));
      expect(drawing(critter(id))).toEqual(drawing(critter(id)));
      expect(drawing(critter(id), true)).toEqual(drawing(critter(id), true));
      expect(critterPhase(id)).toBe(critterPhase(id));
    }
  });

  it('is pinned: a change to the rule would change every agent on the page', () => {
    expect(critter('claude:site-lead').key).toBe(critter('claude:site-lead').key);
    // The mock's harbor-lead: a house with an antenna and a belt.
    expect(critter('claude:site-lead')).toMatchObject({ body: 1, top: 1, mark: 1 });
  });

  it('the rule makes 5,320 different critters, and ids spread over them', () => {
    expect(critterCount()).toBe(5320);
    const keys = new Set<string>();
    for (let i = 0; i < 2000; i++) keys.add(critter(`claude:${i.toString(16).padStart(8, '0')}-session`).key);
    // 2,000 ids over 5,320 critters: a uniform spread would give about 1,660
    // distinct; folding the combinations that cannot be drawn makes some
    // critters likelier, which gives about 1,400.
    expect(keys.size).toBeGreaterThan(1300);
  });

  it('never throws, on any string', () => {
    const odd = ['', ':', 'claude:', '\u0000', '😀🧪', 'x'.repeat(10_000), '\ud800', 'claude:../../etc', ' \t\n'];
    for (let i = 0; i < 500; i++) odd.push(Array.from({ length: i % 40 }, (_, k) => String.fromCharCode((i * 7919 + k * 104729) % 0xffff)).join(''));
    for (const id of odd) {
      const m = critter(id);
      const d = drawing(m);
      const s = drawing(m, true);
      expect(d.body).toMatch(/^M/);
      expect(s.eyes.ex).toMatch(/^M/);
      expect(Number.isFinite(critterPhase(id))).toBe(true);
      expect(`${d.body}${s.body}${Object.values(s.eyes).join('')}`).not.toMatch(/NaN|Infinity|undefined/);
    }
  });

  it('every state has its eyes, apart from status unknown', () => {
    const shown = CRITTER_STATES.map((s) => EYES[s]);
    expect(shown.filter((e) => e === null)).toHaveLength(1);
    expect(EYES.unknown).toBeNull();
    expect(new Set(shown.filter(Boolean))).toEqual(new Set(['eo', 'ew', 'ec', 'eh', 'ex']));
  });

  // Every critter the rule makes, one id each.
  const every = (() => {
    const seen = new Map<string, Critter>();
    for (let i = 0; seen.size < 5320 && i < 1_000_000; i++) {
      const m = critter(`k${i}`);
      if (!seen.has(m.key)) seen.set(m.key, m);
    }
    return [...seen.values()];
  })();
  const rects = (path: string) => [...path.matchAll(/M(-?[\d.]+) (-?[\d.]+)h(-?[\d.]+)v(-?[\d.]+)/g)].map((r) => r.slice(1).map(Number));
  const polygon = (path: string) => path.replace(/[MZ]/g, '').split('L').map((pt) => pt.split(' ').map(Number));
  const inside = (pts: number[][], x: number, y: number) => {
    let c = false;
    for (let i = 0, j = pts.length - 1; i < pts.length; j = i++) {
      const [xi, yi] = pts[i];
      const [xj, yj] = pts[j];
      if (yi > y !== yj > y && x < ((xj - xi) * (y - yi)) / (yj - yi) + xi) c = !c;
    }
    return c;
  };

  it.each([20, 24])('at %i px the eyes sit on whole screen pixels and look a whole pixel or two', (size) => {
    const u = 24 / size;
    const whole = (v: number) => Math.abs(v / u - Math.round(v / u)) < 0.01;
    expect(every).toHaveLength(5320);
    for (const m of every) {
      const d = drawing(m, size === 20);
      for (const path of Object.values(d.eyes)) for (const n of path.match(/-?\d+(\.\d+)?/g) ?? []) expect(whole(Number(n))).toBe(true);
      const g = d.gaze;
      for (const v of [...g.work, g.up, g.look]) expect(whole(v)).toBe(true);
      expect([1, 2]).toContain(Math.round(g.up / u));
      expect([0, 1, 2]).toContain(Math.round(g.look / u));
      expect(['-1,1', '-1,0', '0,1']).toContain(g.work.map((v) => Math.round(v / u)).join(','));
    }
  });

  it.each([20, 24])('at %i px every eye, wherever it looks, keeps body around it and a pixel from the notch; the spot keeps two', (size) => {
    const u = 24 / size;
    for (const m of every) {
      const d = drawing(m, size === 20);
      const body = polygon(d.body);
      const notch = m.mark === 2 && d.mark ? rects(d.mark)[0] : null;
      const spot = m.mark === 3 && d.mark ? rects(d.mark)[0] : null;
      const moves: Record<string, number[][]> = { eo: [[0, 0], [...d.gaze.work], [0, -d.gaze.up], [d.gaze.look, 0]], ew: [[0, 0]], ec: [[0, 0]], eh: [[0, 0]], ex: [[0, 0]] };
      for (const [k, shifts] of Object.entries(moves))
        for (const [dx, dy] of shifts)
          for (const [x, y, w, h] of rects(d.eyes[k as keyof typeof d.eyes])) {
            const [x0, y0, x1, y1] = [x + dx, y + dy, x + dx + w, y + dy + h];
            const g = 0.4;
            const ok = [[x0 - g, y0 - g], [x1 + g, y0 - g], [x0 - g, y1 + g], [x1 + g, y1 + g]].every(([px, py]) => inside(body, px, py));
            if (!ok) expect.fail(`${m.key} at ${size} px: ${k} moved ${dx},${dy} leaves the body`);
            if (notch && x1 > notch[0] - u && x0 < notch[0] + notch[2] && y1 > notch[1] - u && y0 < notch[1] + notch[3]) expect.fail(`${m.key} at ${size} px: ${k} touches the notch`);
            if (spot && x1 > spot[0] - 2 * u && x0 < spot[0] + spot[2] + 2 * u && y1 > spot[1] - 2 * u && y0 < spot[1] + spot[3] + 2 * u) expect.fail(`${m.key} at ${size} px: the spot sits by the ${k} eye`);
          }
    }
  });

  it('keeps most of its marks: the spot at 24 px, the notch at both sizes', () => {
    const count = (size: number, mark: number) => every.filter((m) => m.mark === mark && drawing(m, size === 20).mark !== '').length / every.filter((m) => m.mark === mark).length;
    expect(count(24, 3)).toBeGreaterThan(0.3);
    expect(count(24, 2)).toBe(1);
    expect(count(20, 2)).toBeGreaterThan(0.8);
    expect(count(20, 3)).toBe(0);
  });
});

const C = (column: Column, attention: Attention[] = [], status: Card['agent']['status'] = 'busy', more: Partial<Pick<Card, 'seen' | 'pullRequests'>> = {}) => ({
  column,
  attention,
  agent: { status },
  seen: false,
  pullRequests: [] as Card['pullRequests'],
  ...more,
});
const PR = (state: Card['pullRequests'][number]['state']) => ({ repo: 'example-org/one', number: 1, title: 'x', url: 'https://github.com/example-org/one/pull/1', state, checks: 'none' as const, review: '' as const, draft: false });

describe('critter state', () => {
  it('maps every combination the server sends', () => {
    // internal/core/rules.go: column() and agentStatus().
    expect(critterState(C('needs_you', ['blocked'], 'blocked'))).toBe('blocked');
    expect(critterState(C('needs_you', ['blocked', 'failing_checks'], 'blocked'))).toBe('blocked');
    expect(critterState(C('needs_you', ['unseen'], 'finished'))).toBe('unseen');
    expect(critterState(C('working', [], 'busy'))).toBe('working');
    expect(critterState(C('working', ['stale'], 'busy'))).toBe('stale');
    // Busy with failing checks: working, on a red ground line (the owner's 1C).
    expect(critterState(C('working', ['failing_checks'], 'busy'))).toBe('working_failing');
    expect(critterState(C('working', ['stale', 'failing_checks'], 'busy'))).toBe('stale');
    expect(critterState(C('with_coordinator', [], 'finished'))).toBe('with_coordinator');
    expect(critterState(C('in_review', [], 'finished'))).toBe('in_review');
    expect(critterState(C('in_review', ['failing_checks'], 'finished'))).toBe('failing');
    expect(critterState(C('done', [], 'finished'))).toBe('done');
    expect(critterState(C('idle', [], 'finished'))).toBe('idle');
    // Status unknown always lands in Idle, sometimes with failing checks.
    expect(critterState(C('idle', [], 'unknown'))).toBe('unknown');
    expect(critterState(C('idle', ['failing_checks'], 'unknown'))).toBe('unknown');
    // An ended session stays only in Done or In review.
    expect(critterState(C('done', [], 'ended'))).toBe('done');
    expect(critterState(C('in_review', [], 'ended'))).toBe('in_review');
    expect(critterState(C('in_review', ['failing_checks'], 'ended'))).toBe('failing');
  });

  // rules.go: a card is unseen only under rule 5 (status finished, no open pull request,
  // none closed lately, no coordinator). Marking it seen changes only `seen`, so the
  // next snapshot puts it in Idle (rule 7). The page shows that at the click (the owner's 2C).
  it('marked seen here, a card the rules send to Idle next already sleeps', () => {
    const marked = (status: Card['agent']['status'], prs: Card['pullRequests'] = []) => C('needs_you', [], status, { seen: true, pullRequests: prs });
    expect(critterState(marked('finished'))).toBe('idle');
    // Pull requests that are merged or closed: rule 5 held, so none closed within the Done window.
    expect(critterState(marked('finished', [PR('merged'), PR('closed')]))).toBe('idle');
  });

  it('marked seen here, a card the rules would not send to Idle stays as it is', () => {
    const marked = (status: Card['agent']['status'], prs: Card['pullRequests'] = []) => C('needs_you', [], status, { seen: true, pullRequests: prs });
    // An open pull request means In review (rule 4); busy means Working (rule 2); blocked
    // means Needs you (rule 1); unknown means Idle as status unknown; ended leaves the board.
    expect(critterState(marked('finished', [PR('open')]))).toBe('needs_you');
    for (const st of ['busy', 'blocked', 'ended'] as const) expect(critterState(marked(st))).toBe('needs_you');
    expect(critterState(marked('unknown'))).toBe('unknown');
    // Not marked here (seen false), or still blocked or unseen: as the server says.
    expect(critterState(C('needs_you', [], 'finished'))).toBe('needs_you');
    expect(critterState(C('needs_you', ['unseen'], 'finished', { seen: true }))).toBe('unseen');
    expect(critterState(C('needs_you', ['blocked'], 'blocked', { seen: true }))).toBe('blocked');
  });

  it('gives a known state for every column and reason, alone or with any other', () => {
    for (const col of ColumnValues)
      for (const a of AttentionValues)
        for (const b of AttentionValues)
          for (const st of ['busy', 'blocked', 'finished', 'ended', 'unknown'] as const)
            expect(CRITTER_STATES).toContain(critterState(C(col, a === b ? [a] : [a, b], st)));
  });
});

describe('the move on a change of state', () => {
  it('fires only for a card whose state changed', () => {
    const a = card({ column: 'working' });
    const b = card({ column: 'idle', agent: { status: 'finished' } });
    const first = stateChanges(null, [a, b]);
    expect(first.changed).toEqual([]);
    // Re-rendered, retitled, moved in its column: no change of state.
    expect(stateChanges(first.states, [{ ...b, title: 'new' }, a]).changed).toEqual([]);
    expect(stateChanges(first.states, [{ ...a, column: 'needs_you', attention: ['blocked'], agent: { ...a.agent, status: 'blocked' } }, b]).changed).toEqual([a.id]);
    // Checks turn red on a busy card: a change (it shakes once), and back.
    const red = { ...a, attention: ['failing_checks' as const] };
    const r1 = stateChanges(first.states, [red, b]);
    expect(r1.changed).toEqual([a.id]);
    expect(stateChanges(r1.states, [a, b]).changed).toEqual([a.id]);
    // A card new to the board just arrived.
    expect(stateChanges(first.states, [a, b, card()]).changed).toEqual([]);
  });

  it('the page keeps a move token for a short while, never on the first snapshot', () => {
    vi.useFakeTimers();
    const c = card({ column: 'working' });
    const p = [project(c.projectId, 'one')];
    const f = feedOf({ conn: 'live', snapshot: snapshot([c], p) });
    const board = new BoardState(() => recordActions().actions);
    const stop = board.start(f.feed);
    expect(board.moves.size).toBe(0);
    f.push({ conn: 'live', snapshot: snapshot([{ ...c, title: 'retitled' }], p, { seq: 2 }) });
    expect(board.moves.size).toBe(0);
    f.push({ conn: 'live', snapshot: snapshot([{ ...c, attention: ['stale'] }], p, { seq: 3 }) });
    expect(board.moves.get(c.id)).toBeGreaterThan(0);
    vi.advanceTimersByTime(MOVE_TTL);
    flushSync();
    expect(board.moves.has(c.id)).toBe(false);
    stop();
  });

  it('marking a turn seen here puts the critter to sleep at once, with one move', async () => {
    vi.useFakeTimers();
    const c = card({ column: 'needs_you', attention: ['unseen'], agent: { status: 'finished' } });
    const p = [project(c.projectId, 'one')];
    const f = feedOf({ conn: 'live', snapshot: snapshot([c], p) });
    const board = new BoardState(() => recordActions().actions);
    board.start(f.feed);
    await board.seen(c);
    const shown = board.snapshot!.cards[0];
    expect(shown.column).toBe('needs_you');
    expect(critterState(shown)).toBe('idle');
    expect(board.moves.get(c.id)).toBeGreaterThan(0);
    // The server's snapshot puts it in Idle well within MOVE_TTL: the same critter state, and the
    // click's token is gone, so the critter that remounts in Idle does not move again.
    vi.advanceTimersByTime(300);
    f.push({ conn: 'live', snapshot: snapshot([{ ...c, column: 'idle', attention: [], seen: true }], p, { seq: 2 }) });
    expect(critterState(board.snapshot!.cards[0])).toBe('idle');
    expect(board.moves.has(c.id)).toBe(false);
  });

  it('when the mark fails to save, the critter wakes up again', async () => {
    const c = card({ column: 'needs_you', attention: ['unseen'], agent: { status: 'finished' } });
    const f = feedOf({ conn: 'live', snapshot: snapshot([c], [project(c.projectId, 'one')]) });
    const rec = recordActions({ seen: { ok: false, kind: 'failed', message: 'sitboard answered 500' } });
    const board = new BoardState(() => rec.actions);
    board.start(f.feed);
    const first = board.seen(c);
    expect(critterState(board.snapshot!.cards[0])).toBe('idle');
    const sleepToken = board.moves.get(c.id);
    await first;
    expect(critterState(board.snapshot!.cards[0])).toBe('unseen');
    expect(board.moves.get(c.id)).toBeGreaterThan(sleepToken!);
  });

  // A mark made here changes one card: it leaves every other card's fresh token alone.
  function twoCards(seen: ReturnType<typeof recordActions>) {
    const a = card({ column: 'working' });
    const b = card({ column: 'needs_you', attention: ['unseen'], agent: { status: 'finished' } });
    const p = [project(a.projectId, 'one')];
    const f = feedOf({ conn: 'live', snapshot: snapshot([a, b], p) });
    const board = new BoardState(() => seen.actions);
    board.start(f.feed);
    // The next snapshot changes a's state: a has a token, b none.
    f.push({ conn: 'live', snapshot: snapshot([{ ...a, attention: ['stale'] }, b], p, { seq: 2 }) });
    const aToken = board.moves.get(a.id);
    expect(aToken).toBeGreaterThan(0);
    return { a, b, board, aToken };
  }

  it('marking one card seen keeps the token another card got from the last snapshot', async () => {
    const { a, b, board, aToken } = twoCards(recordActions());
    await board.seen(b);
    expect(board.moves.get(b.id)).toBeGreaterThan(0);
    expect(board.moves.get(a.id)).toBe(aToken);
  });

  it('a mark that fails to save keeps the token another card got from the last snapshot', async () => {
    const { a, b, board, aToken } = twoCards(recordActions({ seen: { ok: false, kind: 'failed', message: 'sitboard answered 500' } }));
    const first = board.seen(b);
    const sleepToken = board.moves.get(b.id);
    await first;
    expect(board.moves.get(b.id)).toBeGreaterThan(sleepToken!);
    expect(board.moves.get(a.id)).toBe(aToken);
  });
});
