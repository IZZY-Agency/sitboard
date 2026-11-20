import { beforeEach, describe, expect, it } from 'vitest';
import type { Card, Snapshot } from '../contract.gen';
import { card, project, snapshot } from '../test/fixtures';
import {
  ASK_MS,
  BASELINE_MS,
  BURST_MS,
  Notifier,
  Presence,
  SENT_KEY,
  SENT_MAX,
  SWITCH_KEY,
  exclusive,
  hits,
  loadSwitch,
  message,
  saveSwitch,
  sentStore,
  switchState,
  type Channel,
  type Permission,
  type SentStore,
} from './notify';

// A clock the test moves by hand: no wall clock.
class Clock {
  t = 0;
  #jobs: Array<{ at: number; fn: () => void; id: number }> = [];
  #n = 0;
  setTimeout = (fn: () => void, ms: number) => {
    const id = ++this.#n;
    this.#jobs.push({ at: this.t + ms, fn, id });
    return id;
  };
  clearTimeout = (id: unknown) => {
    this.#jobs = this.#jobs.filter((j) => j.id !== id);
  };
  async advance(ms: number) {
    this.t += ms;
    for (;;) {
      const due = this.#jobs.filter((j) => j.at <= this.t).sort((a, b) => a.at - b.at)[0];
      if (!due) break;
      this.#jobs = this.#jobs.filter((j) => j !== due);
      due.fn();
      for (let i = 0; i < 5; i++) await Promise.resolve();
    }
    for (let i = 0; i < 5; i++) await Promise.resolve();
  }
}

function memoryStore(): SentStore & { keys: string[] } {
  const s = {
    keys: [] as string[],
    read: () => [...s.keys],
    write: (k: string[]) => {
      s.keys = [...k];
    },
  };
  return s;
}

// One lock shared by the "tabs" of a test, like the browser's lock per address.
function sharedLock() {
  let chain = Promise.resolve();
  return (fn: () => Promise<void>) => {
    const run = chain.then(fn);
    chain = run.catch(() => {});
    return run;
  };
}

interface Tab {
  n: Notifier;
  shown: string[];
  on: boolean;
  looking: boolean;
}

function tab(clock: Clock, sent: SentStore, lock = sharedLock(), others: () => boolean = () => false): Tab {
  const t: Tab = { n: null as unknown as Notifier, shown: [], on: true, looking: false };
  t.n = new Notifier({
    enabled: () => t.on,
    looking: async () => t.looking || others(),
    show: (b) => t.shown.push(b),
    sent,
    exclusive: lock,
    now: () => clock.t,
    setTimeout: clock.setTimeout,
    clearTimeout: clock.clearTimeout,
  });
  return t;
}

const P = [project('p_000000000001', 'one')];
let seq = 0;
const ready = (cards: Card[]) => snapshot(cards, P, { seq: ++seq });
const scanning = (cards: Card[]): Snapshot => ({
  ...ready(cards),
  sources: [{ kind: 'claude', state: 'ok', version: '', detail: '', tested: true, ready: false }],
});
const at = (i: number) => `2026-11-17T09:${String(i).padStart(2, '0')}:00Z`;
// A turn is its statusSince; `null` leaves it out, as an unknown time does.
const since = (turn: number | null) => (turn === null ? undefined : at(turn));
const blocked = (id: string, turn: number | null = 1) =>
  card({ id, column: 'needs_you', attention: ['blocked'], agent: { status: 'blocked', statusSince: since(turn), name: `secret-${id}` } });
const unseen = (id: string, turn: number | null = 1) =>
  card({ id, column: 'needs_you', attention: ['unseen'], seen: false, agent: { status: 'finished', statusSince: since(turn), name: `secret-${id}` } });
const working = (id: string) => card({ id, column: 'working', agent: { status: 'busy', name: `secret-${id}` } });

let clock: Clock;
let sent: ReturnType<typeof memoryStore>;
beforeEach(() => {
  clock = new Clock();
  sent = memoryStore();
});

const BLOCKED = 'An agent needs you · blocked on a question or a permission';
const FINISHED = 'An agent needs you · finished a turn';

describe('hits', () => {
  it('one per card in Needs you and reason, keyed by the turn; failing checks and stale never', () => {
    const s = snapshot(
      [blocked('a'), unseen('b', 2), unseen('n', null), card({ id: 'c', column: 'in_review', attention: ['failing_checks'] }), card({ id: 'd', column: 'working', attention: ['stale'] })],
      P,
      { seq: 7 },
    );
    expect(hits(s).map((h) => [h.pair, h.key])).toEqual([
      ['a|blocked', `a|blocked|${at(1)}`],
      ['b|unseen', `b|unseen|${at(2)}`],
      ['n|unseen', 'n|unseen|seq 7'],
    ]);
  });
});

describe('message', () => {
  it('says why when one agent waits, how many wait when more do, and never a name', () => {
    expect(message(hits(ready([blocked('a')])), 1)).toBe(BLOCKED);
    expect(message(hits(ready([unseen('b')])), 1)).toBe(FINISHED);
    // One new agent among three waiting: the count, like the tab, so a notification that replaces an unread one keeps it all.
    expect(message(hits(ready([unseen('b')])), 3)).toBe('3 agents need you');
  });
});

describe('Notifier', () => {
  it('fires for nothing that was waiting at load', async () => {
    const t = tab(clock, sent);
    t.n.observe(ready([blocked('a'), unseen('b')]));
    t.n.observe(ready([blocked('a'), unseen('b')]));
    await clock.advance(BURST_MS * 2);
    expect(t.shown).toEqual([]);
  });

  it('fires for nothing the first scan finds while it is still reading', async () => {
    const t = tab(clock, sent);
    t.n.observe(scanning([]));
    t.n.observe(scanning([blocked('a')]));
    t.n.observe(ready([blocked('a'), unseen('b')]));
    await clock.advance(BURST_MS * 2);
    expect(t.shown).toEqual([]);
    t.n.observe(ready([blocked('a'), unseen('b'), blocked('c')]));
    await clock.advance(BURST_MS);
    expect(t.shown).toEqual(['3 agents need you']);
  });

  it('a watcher that never finishes its first pass holds the baseline BASELINE_MS from the first snapshot, no longer', async () => {
    const t = tab(clock, sent);
    t.n.observe(scanning([]));
    await clock.advance(BASELINE_MS - 1);
    t.n.observe(scanning([blocked('a')]));
    await clock.advance(BURST_MS);
    expect(t.shown).toEqual([]);
    await clock.advance(BASELINE_MS); // well past the limit, still scanning
    t.n.observe(scanning([blocked('a'), unseen('b')]));
    await clock.advance(BURST_MS);
    expect(t.shown).toEqual(['2 agents need you']);
  });

  it('fires once a burst is over, and makes one notification of it', async () => {
    const t = tab(clock, sent);
    t.n.observe(ready([working('a'), working('b')]));
    t.n.observe(ready([blocked('a'), working('b')]));
    await clock.advance(BURST_MS - 1);
    expect(t.shown).toEqual([]);
    t.n.observe(ready([blocked('a'), unseen('b')]));
    await clock.advance(1);
    expect(t.shown).toEqual(['2 agents need you']);
  });

  it('a burst starts at its first change: staggered changes give one notification per burst', async () => {
    const t = tab(clock, sent);
    const ids = ['a', 'b', 'c', 'd'];
    t.n.observe(ready(ids.map(working)));
    const cards = ids.map(working);
    for (const [i, gap] of [0, 1900, 200, 1850].entries()) {
      await clock.advance(gap);
      cards[i] = blocked(ids[i]);
      t.n.observe(ready([...cards]));
    }
    await clock.advance(BURST_MS);
    expect(t.shown).toEqual(['2 agents need you', '4 agents need you']);
  });

  it('never fires twice for the same reason on the same turn, also after the card left and came back, or a reconnect', async () => {
    const t = tab(clock, sent);
    t.n.observe(ready([working('a')]));
    t.n.observe(ready([blocked('a')]));
    await clock.advance(BURST_MS);
    t.n.observe(ready([working('a')]));
    t.n.observe(ready([blocked('a')])); // same turn again: a snapshot replayed after a reconnect
    await clock.advance(BURST_MS);
    expect(t.shown).toHaveLength(1);
  });

  it('a card that keeps its reason does not fire again when only its time moves (a future time the server holds at now)', async () => {
    const t = tab(clock, sent);
    t.n.observe(ready([working('a')]));
    t.n.observe(ready([blocked('a', 1)]));
    await clock.advance(BURST_MS);
    for (let turn = 2; turn < 6; turn++) {
      t.n.observe(ready([blocked('a', turn)]));
      await clock.advance(BURST_MS);
    }
    expect(t.shown).toEqual([BLOCKED]);
  });

  it('without a time: fires when the reason appears, not while it stays, and again when it comes back', async () => {
    const t = tab(clock, sent);
    t.n.observe(ready([working('a')]));
    t.n.observe(ready([blocked('a', null)]));
    t.n.observe(ready([blocked('a', null)]));
    await clock.advance(BURST_MS);
    t.n.observe(ready([working('a')]));
    t.n.observe(ready([blocked('a', null)])); // the turn cannot be told apart: a tap rather than a miss
    await clock.advance(BURST_MS);
    expect(t.shown).toEqual([BLOCKED, BLOCKED]);
  });

  it('fires for a new reason on a card already in Needs you', async () => {
    const t = tab(clock, sent);
    t.n.observe(ready([unseen('a', 1)]));
    t.n.observe(ready([blocked('a', 2)]));
    await clock.advance(BURST_MS);
    expect(t.shown).toEqual([BLOCKED]);
  });

  it('a reason gone again before the burst ends is dropped', async () => {
    const t = tab(clock, sent);
    t.n.observe(ready([working('a'), working('b')]));
    t.n.observe(ready([blocked('a'), working('b')]));
    t.n.observe(ready([working('a'), unseen('b')]));
    await clock.advance(BURST_MS);
    expect(t.shown).toEqual([FINISHED]);
    expect(sent.keys).toEqual([`b|unseen|${at(1)}`]);
  });

  it('while off, a change passes and is not kept for later', async () => {
    const t = tab(clock, sent);
    t.on = false;
    t.n.observe(ready([]));
    t.n.observe(ready([blocked('a')]));
    await clock.advance(BURST_MS);
    t.on = true;
    t.n.observe(ready([blocked('a')]));
    await clock.advance(BURST_MS);
    expect(t.shown).toEqual([]);
    expect(sent.keys).toEqual([]);
  });

  it('a change while off is not shown when the switch goes on before the burst ends', async () => {
    const t = tab(clock, sent);
    t.on = false;
    t.n.observe(ready([]));
    t.n.observe(ready([blocked('a')]));
    t.on = true;
    t.n.observe(ready([blocked('a'), unseen('b')]));
    await clock.advance(BURST_MS);
    expect(t.shown).toEqual(['2 agents need you']);
    expect(sent.keys).toEqual([`b|unseen|${at(1)}`]);
  });

  it('turned off during a burst, shows nothing', async () => {
    const t = tab(clock, sent);
    t.n.observe(ready([]));
    t.n.observe(ready([blocked('a')]));
    t.on = false;
    await clock.advance(BURST_MS);
    expect(t.shown).toEqual([]);
  });

  it('shows nothing while the owner looks at a board tab, and does not show it later either', async () => {
    const t = tab(clock, sent);
    t.looking = true;
    t.n.observe(ready([]));
    t.n.observe(ready([blocked('a')]));
    await clock.advance(BURST_MS);
    expect(t.shown).toEqual([]);
    expect(sent.keys).toEqual([`a|blocked|${at(1)}`]);
    const other = tab(clock, sent); // a second tab of the same address
    other.n.observe(ready([]));
    other.n.observe(ready([blocked('a')]));
    await clock.advance(BURST_MS);
    expect(other.shown).toEqual([]);
  });

  it('several tabs of one address give one notification', async () => {
    const lock = sharedLock();
    const tabs = [tab(clock, sent, lock), tab(clock, sent, lock), tab(clock, sent, lock)];
    const one = ready([working('a')]);
    const two = ready([blocked('a')]);
    for (const t of tabs) t.n.observe(one);
    for (const t of tabs) t.n.observe(two);
    await clock.advance(BURST_MS);
    expect(tabs.flatMap((t) => t.shown)).toEqual([BLOCKED]);
  });

  it('with the shared list unusable, a tab still never repeats itself (each open tab shows its own, under one tag)', async () => {
    const broken: SentStore = { read: () => [], write: () => {} };
    const t = tab(clock, broken);
    t.n.observe(ready([working('a')]));
    t.n.observe(ready([blocked('a')]));
    await clock.advance(BURST_MS);
    t.n.observe(ready([working('a')]));
    t.n.observe(ready([blocked('a')]));
    await clock.advance(BURST_MS);
    expect(t.shown).toEqual([BLOCKED]);
  });

  it('a reload starts from what is waiting then', async () => {
    const first = tab(clock, sent);
    first.n.observe(ready([]));
    first.n.observe(ready([blocked('a')]));
    await clock.advance(BURST_MS);
    const reloaded = tab(clock, sent);
    reloaded.n.observe(ready([blocked('a'), unseen('b')])); // b started while the page reloaded
    await clock.advance(BURST_MS);
    expect(first.shown).toHaveLength(1);
    expect(reloaded.shown).toEqual([]);
  });

  it('keeps at most SENT_MAX sent keys, the newest', async () => {
    sent.keys = Array.from({ length: SENT_MAX }, (_, i) => `old${i}`);
    const t = tab(clock, sent);
    t.n.observe(ready([]));
    t.n.observe(ready([blocked('a')]));
    await clock.advance(BURST_MS);
    expect(sent.keys).toHaveLength(SENT_MAX);
    expect(sent.keys.at(-1)).toBe(`a|blocked|${at(1)}`);
    expect(sent.keys[0]).toBe('old1');
  });

  it('stop drops a burst in progress, all of it', async () => {
    const t = tab(clock, sent);
    t.n.observe(ready([]));
    t.n.observe(ready([blocked('a')]));
    await clock.advance(BURST_MS / 2);
    t.n.observe(ready([blocked('a'), unseen('b')]));
    t.n.stop();
    await clock.advance(BURST_MS);
    expect(t.shown).toEqual([]);
  });
});

describe('Presence', () => {
  // Channels joined like BroadcastChannels of one name: a post reaches every other one.
  function hub() {
    const all: Channel[] = [];
    const make = (): Channel => {
      const ch: Channel = {
        onmessage: null,
        postMessage: (m) => {
          for (const o of all) if (o !== ch) queueMicrotask(() => o.onmessage?.({ data: m } as MessageEvent));
        },
        close: () => {
          all.splice(all.indexOf(ch), 1);
        },
      };
      all.push(ch);
      return ch;
    };
    return make;
  }

  it('is true when this tab is in front, without asking', async () => {
    expect(await new Presence(null, () => true, clock.setTimeout).looking()).toBe(true);
  });

  it('asks the other tabs: true when one is in front, false after ASK_MS when none answers yes', async () => {
    const make = hub();
    let front = false;
    const me = new Presence(make(), () => false, clock.setTimeout);
    new Presence(make(), () => false, clock.setTimeout);
    new Presence(make(), () => front, clock.setTimeout);
    let got: boolean | null = null;
    void me.looking().then((v) => (got = v));
    await clock.advance(0);
    await clock.advance(ASK_MS);
    expect(got).toBe(false);
    front = true;
    got = null;
    void me.looking().then((v) => (got = v));
    await clock.advance(0);
    expect(got).toBe(true);
  });

  it('a tab that closed holds nothing back', async () => {
    const make = hub();
    const me = new Presence(make(), () => false, clock.setTimeout);
    new Presence(make(), () => true, clock.setTimeout).close();
    let got: boolean | null = null;
    void me.looking().then((v) => (got = v));
    await clock.advance(ASK_MS);
    expect(got).toBe(false);
  });
});

describe('the switch', () => {
  const perm = (supported: boolean, current: NotificationPermission): Permission => ({
    supported: () => supported,
    current: () => current,
    request: async () => current,
  });

  it('is on only when chosen and allowed by the browser', () => {
    expect(switchState(true, perm(true, 'granted'))).toBe('on');
    expect(switchState(false, perm(true, 'granted'))).toBe('off');
    expect(switchState(true, perm(true, 'default'))).toBe('off'); // permission taken back
    expect(switchState(true, perm(true, 'denied'))).toBe('denied');
    expect(switchState(false, perm(true, 'denied'))).toBe('denied');
    expect(switchState(true, perm(false, 'granted'))).toBe('none');
  });

  it('is remembered under its key, off by default', () => {
    localStorage.clear();
    expect(loadSwitch()).toBe(false);
    saveSwitch(true);
    expect(localStorage.getItem(SWITCH_KEY)).toBe('on');
    expect(loadSwitch()).toBe(true);
    saveSwitch(false);
    expect(localStorage.getItem(SWITCH_KEY)).toBeNull();
  });

  it('the sent store survives a damaged value', () => {
    localStorage.setItem(SENT_KEY, '{nope');
    expect(sentStore().read()).toEqual([]);
    localStorage.setItem(SENT_KEY, JSON.stringify(['a', 2, 'b']));
    expect(sentStore().read()).toEqual(['a', 'b']);
  });
});

describe('exclusive', () => {
  it('runs under the browser lock of this address, or directly without one', async () => {
    const names: string[] = [];
    const locks = { request: async (name: string, fn: () => Promise<void>) => (names.push(name), fn()) } as unknown as LockManager;
    let ran = 0;
    await exclusive(locks)(async () => void ran++);
    await exclusive(undefined)(async () => void ran++);
    expect(ran).toBe(2);
    expect(names).toEqual(['sitboard.notify']);
  });
});
