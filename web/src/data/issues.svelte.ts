// The Issues panel's state: loaded when the panel opens, and again when the
// snapshot's issues.rev moves while it is open (the server bumps it for a new
// list, a repository that joined or left, a change of state). A read is reused
// instead of repeated: one that is out serves a second open, and a finished
// one serves an open at a rev no newer than its own until the server's
// freshness ends (Issues.freshMs, counted from when the read was sent: after
// it a GET makes the server look again); then it is read again so that the server's gate runs. A refresh
// while a read is out asks for one more read after it only when it announces
// a newer rev. A purge (the snapshot's issues.purged, newer than the list
// held) drops the list at once, and a list older than the purge is never
// installed. A lost key (this panel's or any other read's, lose()) clears the
// list at once and for good; a failed load keeps the last list.

// A pin shows at once (pins, laid over the list by withPins) and is taken
// back when the server refuses it. One the server accepted asks for a new
// list and stays laid over until a list from a read sent after the
// acceptance comes in; a newer press stays laid over whatever comes in.

import { SvelteMap } from 'svelte/reactivity';
import type { IssuePinRequest, Issues } from '../contract.gen';
import { pinKey, type IssuesClient } from './issues';

export type IssuesStatus = 'loading' | 'ready' | 'failed' | 'unauthorized';

/** Reads in a row that answered a list older than a purge before the load gives up. */
const STALE_TRIES = 3;

export class IssuesState {
  status = $state<IssuesStatus>('loading');
  data = $state<Issues | null>(null);
  error = $state('');
  /** Pins the page changed that the list held may not show yet: pinKey -> pinned. */
  pins = new SvelteMap<string, boolean>();
  /** pinKey -> the first read that was sent after the server accepted it: its list shows the pin itself. */
  #pinAt = new Map<string, number>();
  /** Reads sent so far (each load counts one). */
  #reads = 0;
  /** pinKey -> the latest press, so an older answer never undoes a newer press. */
  #pinSeq = new Map<string, number>();
  #seq = 0;

  #client: () => IssuesClient;
  #clock: () => number;
  #pending: Promise<void> | null = null;
  /** The newest rev known when the read that is out was asked. */
  #reading = -1;
  /** The newest rev announced while it was out, when it is newer than #reading. */
  #wanted = -1;
  /** Another read follows the one that is out. */
  #again = false;
  /** The newest rev any snapshot announced. */
  #known = -1;
  /** The rev of the list installed (its own, Issues.rev). */
  #loadedRev = -1;
  /** Until when (this clock) the server would answer a GET without a new look. */
  #freshUntil = 0;
  /** The newest purge: a list older than it is never installed. */
  #floor = 0;

  constructor(client: () => IssuesClient, clock: () => number = Date.now) {
    this.#client = client;
    this.#clock = clock;
  }

  /**
   * Pins or unpins an issue of the repository the server keys `repoKey`
   * (RepoIssues.key) at once and asks the server (send); a refusal
   * puts it back as the list has it.
   */
  async pin(repoKey: string, req: IssuePinRequest, send: (req: IssuePinRequest) => Promise<boolean>): Promise<boolean> {
    const k = pinKey({ key: repoKey }, req.number);
    const seq = ++this.#seq;
    this.#pinSeq.set(k, seq);
    this.#pinAt.delete(k);
    this.pins.set(k, req.pinned);
    const ok = await send(req);
    if (this.#pinSeq.get(k) !== seq) return ok; // pressed again meanwhile: that press decides
    if (ok) {
      this.#pinAt.set(k, this.#reads + 1);
      void this.refresh(); // a read sent now (or after the one that is out)
    } else {
      this.pins.delete(k);
      this.#pinSeq.delete(k);
    }
    return ok;
  }

  /** The list of read `read` came in: pins accepted before that read was sent stop being laid over it. */
  #settlePins(read: number): void {
    for (const [k, at] of this.#pinAt) {
      if (read < at) continue;
      this.#pinAt.delete(k);
      this.#pinSeq.delete(k);
      this.pins.delete(k);
    }
  }

  /** The panel closed: a read still out is not followed by another. */
  stop(): void {
    this.#again = false;
  }

  /** Another read lost the tab's key: the list goes, and no read follows. */
  lose(): void {
    this.#again = false;
    this.status = 'unauthorized';
    this.data = null;
    this.pins.clear();
    this.#pinAt.clear();
    this.#pinSeq.clear();
  }

  /**
   * The snapshot says the server dropped rows of its list at rev `purged`:
   * a list held from before goes now, open panel or not, and no answer
   * older than it is installed later.
   */
  purge(purged: number): void {
    this.#known = Math.max(this.#known, purged);
    if (purged <= this.#floor) return;
    this.#floor = purged;
    if (this.data && this.#loadedRev < purged) {
      this.data = null; // and with it any reuse: an open reads again
      if (this.status === 'ready') this.status = 'loading';
    }
  }

  /**
   * The list for a snapshot that announces `rev`. Without a rev the read is
   * asked for outright (Try again): it shares one that is out and never
   * trusts a finished one.
   */
  refresh(rev?: number): Promise<void> {
    if (this.status === 'unauthorized') return Promise.resolve();
    if (rev !== undefined) this.#known = Math.max(this.#known, rev);
    if (this.#pending) {
      if (rev === undefined || rev > this.#reading) {
        this.#again = true;
        this.#wanted = Math.max(this.#wanted, rev ?? this.#known);
      }
      return this.#pending;
    }
    if (rev !== undefined && this.status === 'ready' && this.data && rev <= this.#loadedRev && this.#clock() < this.#freshUntil) {
      return Promise.resolve();
    }
    this.#reading = this.#known;
    this.#pending = this.#run().finally(() => (this.#pending = null));
    return this.#pending;
  }

  async #run(): Promise<void> {
    let stale = 0;
    do {
      this.#again = false;
      const asked = this.#reading;
      if (!this.data) this.status = 'loading';
      // The server counts freshMs from when it answered, which is after this:
      // counted from here, a slow answer can only shorten the reuse.
      const sent = this.#clock();
      const read = ++this.#reads;
      const r = await this.#client().load();
      if (this.status === 'unauthorized') return; // lost meanwhile: nothing comes back
      if (!r.ok && r.kind === 'unauthorized') {
        this.lose();
        return;
      }
      if (r.ok && r.issues.rev < this.#floor) {
        // Asked before a purge it does not know of: its rows may be ones that went.
        if (++stale < STALE_TRIES) {
          this.#again = true;
        } else {
          this.status = 'failed';
          this.error = 'The list changed while it was read.';
        }
      } else if (r.ok) {
        this.data = r.issues;
        this.status = 'ready';
        this.error = '';
        this.#loadedRev = r.issues.rev;
        this.#settlePins(read);
        this.#freshUntil = sent + r.issues.freshMs;
      } else {
        this.status = 'failed';
        this.error = r.message;
      }
      if (this.#again) this.#reading = Math.max(this.#wanted, asked, this.#known);
    } while (this.#again);
  }
}
