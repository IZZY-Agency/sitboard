// The page's state: the live snapshot, local "seen" marks, arrivals in Needs
// you (for the flash), and the result of the last action.

import { SvelteMap } from 'svelte/reactivity';
import type { Card, Snapshot } from '../contract.gen';
import type { Actions } from './actions';
import type { Conn, Feed } from './client';
import { applySeen, canJump, needsYouArrivals } from './derive';
import { clean } from './text';

export type Notice =
  | { kind: 'copy'; cardId: string; name: string; folder: string; why: 'no_agterm' | 'no_terminal' }
  | { kind: 'failed'; what: string; message: string; retry?: () => void }; // no retry when it would fail the same way

/** How long an arrival in Needs you can still flash. A card that mounts later does not. */
export const FLASH_TTL = 1000;

function uniqueBy<T>(list: T[], key: (t: T) => string): T[] {
  const seen = new Set<string>();
  const out = list.filter((t) => {
    const k = key(t);
    if (seen.has(k)) return false;
    seen.add(k);
    return true;
  });
  return out.length === list.length ? list : out;
}

/** Keeps the first of any duplicate id: a keyed list with a duplicate key throws. */
export function dedupe(s: Snapshot): Snapshot {
  const cards = uniqueBy(s.cards, (c) => c.id);
  const projects = uniqueBy(s.projects, (p) => p.id);
  const sources = uniqueBy(s.sources, (x) => x.kind);
  if (cards === s.cards && projects === s.projects && sources === s.sources) return s;
  return { ...s, cards, projects, sources };
}

export class BoardState {
  conn = $state<Conn>('connecting');
  raw = $state<Snapshot | null>(null);
  retryAt = $state<number | undefined>(undefined);
  #notice = $state<Notice | null>(null);
  /**
   * The last action that succeeded, said to assistive tech; '' when none. Any
   * change of the notice clears it, so it is never read out again later.
   */
  said = $state('');

  get notice(): Notice | null {
    return this.#notice;
  }
  set notice(n: Notice | null) {
    this.#notice = n;
    this.said = '';
  }
  /** When the board stopped being live, while a snapshot is shown; null while live. */
  lostAt = $state<number | null>(null);
  /** card id → flash token, kept for FLASH_TTL so a later remount does not replay it. */
  arrivals = new SvelteMap<string, number>();
  /** card id → the statusSince at which the owner marked it seen here. */
  marked = new SvelteMap<string, string>();

  #token = 0;
  #timers = new Set<ReturnType<typeof setTimeout>>();
  #forgetting = new Map<string, Promise<boolean>>(); // project id -> the forget request that is out
  #now: () => number;
  #actions: () => Actions;
  #stop: (() => void) | null = null;

  /** The snapshot with local seen marks applied. */
  snapshot = $derived.by((): Snapshot | null => {
    const s = this.raw;
    if (!s) return null;
    const cards = applySeen(s.cards, this.marked);
    return cards === s.cards ? s : { ...s, cards };
  });

  constructor(actions: () => Actions, now: () => number = Date.now) {
    this.#actions = actions;
    this.#now = now;
  }

  start(feed: Feed): () => void {
    this.#stop = feed.start((st) => {
      this.conn = st.conn;
      this.retryAt = st.retryAt;
      if (st.snapshot && st.snapshot !== this.raw) this.#accept(dedupe(st.snapshot));
      if (st.conn === 'live') this.lostAt = null;
      else if (this.lostAt === null && this.raw) this.lostAt = this.#now();
    });
    return () => {
      this.#stop?.();
      for (const t of this.#timers) clearTimeout(t);
    };
  }

  #accept(next: Snapshot) {
    const prev = this.raw;
    for (const id of needsYouArrivals(prev?.cards ?? null, next.cards)) {
      const token = ++this.#token;
      this.arrivals.set(id, token);
      const t = setTimeout(() => {
        this.#timers.delete(t);
        if (this.arrivals.get(id) === token) this.arrivals.delete(id);
      }, FLASH_TTL);
      this.#timers.add(t);
    }
    const live = new Set(next.cards.filter((c) => c.column === 'needs_you').map((c) => c.id));
    for (const id of [...this.arrivals.keys()]) if (!live.has(id)) this.arrivals.delete(id);
    // A mark only lasts for the turn it was made on.
    const byId = new Map(next.cards.map((c) => [c.id, c]));
    for (const [id, at] of [...this.marked]) {
      const c = byId.get(id);
      if (!c || (c.agent.statusSince ?? '') !== at || !c.attention.includes('unseen')) this.marked.delete(id);
    }
    this.raw = next;
  }

  #markLocal(card: Card) {
    if (card.attention.includes('unseen')) this.marked.set(card.id, card.agent.statusSince ?? '');
  }

  /** Activating a card: jump to its terminal, or show its folder and name to copy. */
  async jump(card: Card): Promise<void> {
    const name = clean(card.agent.name);
    const folder = clean(card.agent.folder);
    if (!canJump(card, this.raw?.agtermPresent ?? false)) {
      this.notice = { kind: 'copy', cardId: card.id, name, folder, why: this.raw?.agtermPresent ? 'no_terminal' : 'no_agterm' };
      return;
    }
    const r = await this.#actions().jump(card.id);
    if (r.ok) {
      this.#markLocal(card);
      if (this.notice?.kind === 'copy' || (this.notice?.kind === 'failed' && this.notice.what.startsWith('Jump'))) this.notice = null;
      return;
    }
    if (r.kind === 'no_terminal') this.notice = { kind: 'copy', cardId: card.id, name, folder, why: 'no_terminal' };
    else if (r.kind === 'unauthorized') this.#lostKey();
    else this.notice = { kind: 'failed', what: `Jump to ${name || 'this session'}`, message: r.message, retry: () => void this.jump(card) };
  }

  async seen(card: Card): Promise<void> {
    this.#markLocal(card);
    const r = await this.#actions().seen(card.id);
    if (r.ok) return;
    this.marked.delete(card.id);
    if (r.kind === 'unauthorized') this.#lostKey();
    else
      this.notice = {
        kind: 'failed',
        what: `Mark ${clean(card.agent.name) || 'this session'} seen`,
        message: r.kind === 'failed' ? r.message : 'sitboard refused it',
        retry: () => void this.seen(card),
      };
  }

  /**
   * Forgets a project's grouping; true when the server accepted it. One
   * request per project at a time: a second press while one is out (a double
   * click on Retry, the rail's and the tiles' control) gets the same answer
   * instead of sending a second forget, which would fail with "no longer
   * grouped" after the first worked.
   */
  forgetGrouping(project: { id: string; name: string }): Promise<boolean> {
    const out = this.#forgetting.get(project.id);
    if (out) return out;
    const p = this.#forget(project).finally(() => this.#forgetting.delete(project.id));
    this.#forgetting.set(project.id, p);
    return p;
  }

  async #forget(project: { id: string; name: string }): Promise<boolean> {
    const name = clean(project.name) || 'this project';
    const r = await this.#actions().forgetGrouping(project.id);
    if (r.ok) {
      if (this.notice?.kind === 'failed' && this.notice.what.startsWith('Forget')) this.notice = null;
      // Emptied first, so the same words twice are still read out twice.
      this.said = '';
      const t = setTimeout(() => {
        this.#timers.delete(t);
        this.said = `Forgot the grouping of ${name}. Its parts are no longer grouped under it.`;
      }, 0);
      this.#timers.add(t);
      return true;
    }
    if (r.kind === 'unauthorized') this.#lostKey();
    else
      this.notice = {
        kind: 'failed',
        what: `Forget the grouping of ${name}`,
        message: r.kind === 'failed' ? r.message : 'sitboard refused it',
        retry: r.kind === 'failed' && r.final ? undefined : () => void this.forgetGrouping(project),
      };
    return false;
  }

  dismiss() {
    this.notice = null;
  }

  #lostKey() {
    this.#stop?.();
    this.conn = 'unauthorized';
  }
}
