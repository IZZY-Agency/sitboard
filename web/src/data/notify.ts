// A tap on the shoulder: a desktop notification when an agent starts needing
// you. Off until the owner turns it on; the browser shows it from the page
// itself (no service worker, no push service), so nothing leaves the machine.
//
// What fires: a reason ("blocked", "unseen") that appears on a card in Needs
// you, that is, one the card did not have in Needs you on the snapshot before.
// A card that keeps its reason does not fire again, whatever its statusSince
// does (the server moves a time from the future to now on every snapshot).
// Nothing fires for what was waiting when the page loaded, or for what the
// first scan finds while a watcher is still reading; after BASELINE_MS from the
// first snapshot the baseline ends anyway, so a watcher that never finishes its
// first pass cannot keep notifications away for good. Reasons that come within
// BURST_MS make one notification, and a reason that is gone again when the
// burst ends is dropped. A reason fires once per turn (card, reason and
// statusSince; without statusSince, the snapshot it appeared on): each tab
// keeps the keys it saw sent, and the tabs of one address share them, so
// several tabs give one notification. Nothing is shown while a board tab is in
// front and focused: the owner is looking. The text never names an agent, a
// project, a task or a folder: macOS can show it on the lock screen.

import type { Card, Snapshot } from '../contract.gen';
import { sourcesReady } from './derive';

export const BURST_MS = 2000;
/** The longest the baseline at load lasts, from the first snapshot. */
export const BASELINE_MS = 60_000;
/** How many sent keys the tabs keep; the oldest go first. */
export const SENT_MAX = 500;
export const SWITCH_KEY = 'sitboard.notify';
export const SENT_KEY = 'sitboard.notify.sent';
export const LOCK_NAME = 'sitboard.notify';
export const CHANNEL_NAME = 'sitboard.notify';
export const TAG = 'sitboard';

type Reason = 'blocked' | 'unseen';
const REASONS: readonly Reason[] = ['blocked', 'unseen'];

export interface Hit {
  /** Card and reason: what must newly appear to fire. */
  pair: string;
  /** Card, reason and turn: what is sent once across the tabs. */
  key: string;
  cardId: string;
  reason: Reason;
}

/** The reasons the cards in Needs you have on a snapshot. */
export function hits(s: Pick<Snapshot, 'cards' | 'seq'>): Hit[] {
  const out: Hit[] = [];
  for (const c of s.cards as Card[]) {
    if (c.column !== 'needs_you') continue;
    for (const reason of REASONS) {
      if (!c.attention.includes(reason)) continue;
      const pair = `${c.id}|${reason}`;
      out.push({ pair, key: `${pair}|${c.agent.statusSince || `seq ${s.seq}`}`, cardId: c.id, reason });
    }
  }
  return out;
}

/**
 * What a notification says: no names. One agent waiting: why. More: how many
 * wait now, the same number as the tab, so a notification that replaces an
 * unread one (same tag) still tells the whole story.
 */
export function message(batch: readonly Hit[], waiting: number): string {
  if (waiting > 1) return `${waiting} agents need you`;
  const blocked = batch.some((h) => h.reason === 'blocked');
  return `An agent needs you · ${blocked ? 'blocked on a question or a permission' : 'finished a turn'}`;
}

/** The keys the tabs of this address already sent, oldest first. */
export interface SentStore {
  read(): string[];
  write(keys: string[]): void;
}

export interface NotifierDeps {
  /** True when the switch is on and the browser allows notifications. */
  enabled(): boolean;
  /** True while any board tab of this address is visible and focused. */
  looking(): Promise<boolean>;
  show(body: string): void;
  sent: SentStore;
  /** Runs fn alone among the tabs of this address. */
  exclusive(fn: () => Promise<void>): Promise<void>;
  now(): number;
  setTimeout(fn: () => void, ms: number): unknown;
  clearTimeout(id: unknown): void;
}

export class Notifier {
  #deps: NotifierDeps;
  /** Card and reason pairs in Needs you on the last snapshot. */
  #present = new Set<string>();
  /** How many agents are in Needs you on the last snapshot. */
  #waiting = 0;
  /** The keys this tab saw sent: the shared list may be unreadable. */
  #sent = new Set<string>();
  #since: number | null = null;
  #ready = false;
  #pending: Hit[] = [];
  #timer: unknown = null;

  constructor(deps: NotifierDeps) {
    this.#deps = deps;
  }

  /** Every snapshot the page accepts, in order. */
  observe(s: Snapshot): void {
    const now = hits(s);
    const before = this.#present;
    this.#present = new Set(now.map((h) => h.pair));
    this.#waiting = new Set(now.map((h) => h.cardId)).size;
    if (!this.#ready) {
      // At load and through the first scan, everything is what was waiting.
      // Past BASELINE_MS this snapshot already counts as a change.
      this.#since ??= this.#deps.now();
      const late = this.#deps.now() - this.#since >= BASELINE_MS;
      this.#ready = late || sourcesReady(s);
      if (!late) return;
    }
    const fresh = now.filter((h) => !before.has(h.pair));
    if (!fresh.length || !this.#deps.enabled()) return; // off: the change passes, it is not kept for later
    this.#pending.push(...fresh);
    if (this.#timer === null) this.#timer = this.#deps.setTimeout(() => void this.#flush(), BURST_MS);
  }

  async #flush(): Promise<void> {
    this.#timer = null;
    // A reason gone again before the burst ended is not news any more.
    const batch = this.#pending.filter((h) => this.#present.has(h.pair));
    this.#pending = [];
    if (!batch.length || !this.#deps.enabled()) return;
    await this.#deps.exclusive(async () => {
      const shared = this.#deps.sent.read();
      const had = new Set([...shared, ...this.#sent]);
      const fresh = batch.filter((h) => !had.has(h.key));
      if (!fresh.length) return; // sent before, here or by another tab
      // Kept as sent even when nobody is shown it: the owner was looking.
      for (const h of fresh) this.#sent.add(h.key);
      if (this.#sent.size > SENT_MAX) this.#sent = new Set([...this.#sent].slice(-SENT_MAX));
      this.#deps.sent.write([...shared, ...fresh.map((h) => h.key)].slice(-SENT_MAX));
      if (!(await this.#deps.looking()) && this.#deps.enabled()) this.#deps.show(message(fresh, this.#waiting));
    });
  }

  stop(): void {
    if (this.#timer !== null) this.#deps.clearTimeout(this.#timer);
    this.#timer = null;
    this.#pending = [];
  }
}

// ---- The switch --------------------------------------------------------------

/** What the switch shows. */
export type SwitchState = 'off' | 'asking' | 'on' | 'denied' | 'none';

/** The browser's side: what it supports, what it allows, and asking it. */
export interface Permission {
  supported(): boolean;
  current(): NotificationPermission;
  /** Asks the owner; only ever called from his click. */
  request(): Promise<NotificationPermission>;
}

function storage(): Storage | undefined {
  try {
    return globalThis.localStorage;
  } catch {
    return undefined;
  }
}

export function loadSwitch(s: Storage | undefined = storage()): boolean {
  try {
    return s?.getItem(SWITCH_KEY) === 'on';
  } catch {
    return false;
  }
}

export function saveSwitch(on: boolean, s: Storage | undefined = storage()): void {
  try {
    if (on) s?.setItem(SWITCH_KEY, 'on');
    else s?.removeItem(SWITCH_KEY);
  } catch {
    // Storage blocked: the choice lasts for this page only.
  }
}

/** The switch as it stands: the owner's choice, as far as the browser allows it. */
export function switchState(chosen: boolean, p: Permission): SwitchState {
  if (!p.supported()) return 'none';
  const perm = p.current();
  if (perm === 'denied') return 'denied';
  return chosen && perm === 'granted' ? 'on' : 'off';
}

export function sentStore(s: Storage | undefined = storage()): SentStore {
  return {
    read() {
      try {
        const v: unknown = JSON.parse(s?.getItem(SENT_KEY) ?? '[]');
        return Array.isArray(v) ? v.filter((x): x is string => typeof x === 'string') : [];
      } catch {
        return [];
      }
    },
    write(keys) {
      try {
        s?.setItem(SENT_KEY, JSON.stringify(keys));
      } catch {
        // Storage blocked: this tab still remembers its own keys.
      }
    },
  };
}

/** Runs fn under the browser's lock for this address, or directly where there is none. */
export function exclusive(locks: LockManager | undefined = globalThis.navigator?.locks): (fn: () => Promise<void>) => Promise<void> {
  return async (fn) => {
    if (!locks) return fn();
    await locks.request(LOCK_NAME, fn);
  };
}

// ---- Is the owner looking? ---------------------------------------------------

/** The part of a BroadcastChannel this needs. */
export interface Channel {
  postMessage(m: unknown): void;
  onmessage: ((e: MessageEvent) => void) | null;
  close(): void;
}

/** How long a tab waits for the other tabs to say whether they are in front. */
export const ASK_MS = 150;

type Talk = { ask: string } | { answer: string; looking: boolean };

/**
 * Asks the other board tabs of this address, at the moment of need, whether
 * one of them is in front: nothing is kept between asks, so a tab that
 * crashed cannot hold notifications back.
 */
export class Presence {
  #ch: Channel | null;
  #self: () => boolean;
  #setTimeout: (fn: () => void, ms: number) => unknown;
  #waiting = new Map<string, (looking: boolean) => void>();
  #n = 0;

  constructor(ch: Channel | null, self: () => boolean, setTimeout: (fn: () => void, ms: number) => unknown) {
    this.#ch = ch;
    this.#self = self;
    this.#setTimeout = setTimeout;
    if (ch)
      ch.onmessage = (e) => {
        const m = e.data as Talk;
        if (m && typeof m === 'object' && 'ask' in m && typeof m.ask === 'string') ch.postMessage({ answer: m.ask, looking: this.#self() });
        else if (m && typeof m === 'object' && 'answer' in m && m.looking === true) this.#waiting.get(m.answer)?.(true);
      };
  }

  looking(): Promise<boolean> {
    if (this.#self()) return Promise.resolve(true);
    const ch = this.#ch;
    if (!ch) return Promise.resolve(false);
    const id = `${Math.random().toString(36).slice(2)}.${++this.#n}`;
    return new Promise((resolve) => {
      const done = (v: boolean) => {
        if (!this.#waiting.delete(id)) return;
        resolve(v);
      };
      this.#waiting.set(id, done);
      this.#setTimeout(() => done(false), ASK_MS);
      ch.postMessage({ ask: id } satisfies Talk);
    });
  }

  close(): void {
    this.#ch?.close();
    for (const done of [...this.#waiting.values()]) done(false);
  }
}
