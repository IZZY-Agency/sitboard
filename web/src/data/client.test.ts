import { describe, expect, it } from 'vitest';
import { BACKOFF, createFeed, type FeedDeps, type FeedState } from './client';
import { CONTRACT_VERSION, type Snapshot } from '../contract.gen';

function snap(seq: number, version = CONTRACT_VERSION): Snapshot {
  return {
    version,
    seq,
    generatedAt: '2026-11-17T10:00:00Z',
    sources: [],
    projects: [],
    cards: [],
    groupingSettings: [],
    leftovers: { items: 0, held: 0, partial: false },
    plainShells: 0,
    counts: { working: 0, needsYou: 0, inReview: 0, stale: 0 },
    agtermPresent: false,
    build: { version: 'dev', commit: 'unknown', os: 'darwin', arch: 'arm64' },
    spend: { days: [], pending: false },
    gitlabHosts: [],
    issues: { state: 'off', open: 0, rev: 0 },
  };
}

class FakeES {
  static all: FakeES[] = [];
  listeners = new Map<string, (e: MessageEvent<string>) => void>();
  onerror: ((e: Event) => void) | null = null;
  onopen: ((e: Event) => void) | null = null;
  closed = false;
  constructor(public url: string) {
    FakeES.all.push(this);
  }
  addEventListener(t: string, fn: (e: MessageEvent<string>) => void) {
    this.listeners.set(t, fn);
  }
  close() {
    this.closed = true;
  }
  send(s: Snapshot) {
    this.listeners.get('snapshot')?.({ data: JSON.stringify(s) } as MessageEvent<string>);
  }
  fail() {
    this.onerror?.(new Event('error'));
  }
}

function harness(responses: Array<() => Promise<Response>>) {
  FakeES.all = [];
  const timers: Array<{ fn: () => void; ms: number }> = [];
  const states: FeedState[] = [];
  let call = 0;
  const deps: FeedDeps = {
    fetch: (() => {
      const r = responses[Math.min(call, responses.length - 1)];
      call++;
      return r();
    }) as unknown as typeof fetch,
    EventSource: FakeES,
    setTimeout: (fn, ms) => {
      timers.push({ fn, ms });
      return timers.length;
    },
    clearTimeout: () => {},
    now: () => 0,
  };
  const stop = createFeed(deps).start((s) => states.push(s));
  return { states, timers, stop, calls: () => call };
}

const ok = (s: Snapshot) => () => Promise.resolve(new Response(JSON.stringify(s), { status: 200 }));
const status = (code: number) => () => Promise.resolve(new Response('', { status: code }));
const down = () => Promise.reject(new TypeError('network'));
const tick = () => new Promise((r) => setTimeout(r, 0));
const last = (states: FeedState[]) => states[states.length - 1];

describe('feed', () => {
  it('loads the snapshot then follows events', async () => {
    const h = harness([ok(snap(1))]);
    expect(h.states[0].conn).toBe('connecting');
    await tick();
    expect(last(h.states)).toMatchObject({ conn: 'live', snapshot: { seq: 1 } });
    expect(FakeES.all).toHaveLength(1);
    expect(FakeES.all[0].url).toBe('/api/v1/events');
    FakeES.all[0].send(snap(2));
    expect(last(h.states).snapshot?.seq).toBe(2);
    h.stop();
    expect(FakeES.all[0].closed).toBe(true);
  });

  it('reconnects with backoff after the stream drops and shows "reconnecting"', async () => {
    const h = harness([ok(snap(1))]);
    await tick();
    FakeES.all[0].fail();
    expect(FakeES.all[0].closed).toBe(true);
    expect(last(h.states).conn).toBe('reconnecting');
    expect(last(h.states).snapshot?.seq).toBe(1); // the last board stays visible
    expect(h.timers[0].ms).toBe(BACKOFF[0]);
    h.timers[0].fn();
    await tick();
    expect(last(h.states).conn).toBe('live');
    expect(FakeES.all).toHaveLength(2);
  });

  it('backs off further on each failed attempt, and resets once live', async () => {
    const h = harness([down, down, down, ok(snap(1))]);
    await tick();
    expect(last(h.states).conn).toBe('unreachable');
    h.timers[0].fn();
    await tick();
    h.timers[1].fn();
    await tick();
    expect(h.timers.map((t) => t.ms)).toEqual([BACKOFF[0], BACKOFF[1], BACKOFF[2]]);
    h.timers[2].fn();
    await tick();
    expect(last(h.states).conn).toBe('live');
    FakeES.all[0].send(snap(2));
    FakeES.all[0].fail();
    expect(h.timers[3].ms).toBe(BACKOFF[0]);
  });

  it('keeps backing off when the snapshot loads but the stream keeps failing', async () => {
    const h = harness([ok(snap(1))]);
    await tick();
    for (let i = 0; i < 4; i++) {
      FakeES.all[i].fail();
      h.timers[i].fn();
      await tick();
    }
    expect(h.timers.map((t) => t.ms)).toEqual(BACKOFF.slice(0, 4));
  });

  it('an opened stream resets the backoff', async () => {
    const h = harness([ok(snap(1))]);
    await tick();
    FakeES.all[0].fail();
    h.timers[0].fn();
    await tick();
    FakeES.all[1].onopen?.(new Event('open'));
    FakeES.all[1].fail();
    expect(h.timers[1].ms).toBe(BACKOFF[0]);
  });

  it('a 401 shows the re-entry message, not an empty board', async () => {
    const h = harness([status(401)]);
    await tick();
    expect(last(h.states)).toMatchObject({ conn: 'unauthorized', snapshot: null });
    expect(h.timers).toHaveLength(0);
    expect(FakeES.all).toHaveLength(0);
  });

  it('a 401 after the stream drops stops retrying', async () => {
    const h = harness([ok(snap(1)), status(401)]);
    await tick();
    FakeES.all[0].fail();
    h.timers[0].fn();
    await tick();
    expect(last(h.states).conn).toBe('unauthorized');
    expect(h.timers).toHaveLength(1);
  });

  it('a snapshot with a newer Version shows "reload the page"', async () => {
    const h = harness([ok(snap(1, CONTRACT_VERSION + 1))]);
    await tick();
    expect(last(h.states).conn).toBe('outdated');
    expect(FakeES.all).toHaveLength(0);
  });

  it('a snapshot of an older contract (before forges) shows "reload the page" too', async () => {
    const old = { ...snap(1, CONTRACT_VERSION - 1) } as Partial<Snapshot>;
    delete old.gitlabHosts;
    const h = harness([ok(old as Snapshot)]);
    await tick();
    expect(last(h.states).conn).toBe('outdated');
    expect(FakeES.all).toHaveLength(0);
  });

  it('a gitlabHosts that is not an array of strings reaches the page as none', async () => {
    const h = harness([ok({ ...snap(1), gitlabHosts: 'gitlab.example.com' } as unknown as Snapshot)]);
    await tick();
    expect(last(h.states).snapshot?.gitlabHosts).toEqual([]);
    FakeES.all[0].send({ ...snap(2), gitlabHosts: [7, 'gitlab.example.com'] } as unknown as Snapshot);
    expect(last(h.states).snapshot?.gitlabHosts).toEqual(['gitlab.example.com']);
    FakeES.all[0].send({ ...snap(3), gitlabHosts: { a: 1 } } as unknown as Snapshot);
    expect(last(h.states).snapshot?.gitlabHosts).toEqual([]);
  });

  it('a newer Version arriving over the stream also stops it', async () => {
    const h = harness([ok(snap(1))]);
    await tick();
    FakeES.all[0].send(snap(2, CONTRACT_VERSION + 1));
    expect(last(h.states).conn).toBe('outdated');
    expect(FakeES.all[0].closed).toBe(true);
  });

  it('a server error is retried', async () => {
    const h = harness([status(500), ok(snap(3))]);
    await tick();
    expect(last(h.states).conn).toBe('unreachable');
    h.timers[0].fn();
    await tick();
    expect(last(h.states).conn).toBe('live');
  });

  it('a broken event is skipped', async () => {
    const h = harness([ok(snap(1))]);
    await tick();
    FakeES.all[0].listeners.get('snapshot')?.({ data: '{nope' } as MessageEvent<string>);
    expect(last(h.states)).toMatchObject({ conn: 'live', snapshot: { seq: 1 } });
  });
});
