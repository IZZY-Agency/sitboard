// The page's catch-up state: the last list the server sent, whether it is
// being loaded, and what went wrong. Loading never moves the marker; only
// markRead does, up to the list's asOf, so a reload or a second tab never
// loses an item the board learned after the list that was shown.

import type { Catchup } from '../contract.gen';
import type { CatchupClient } from './catchup';

export type CatchupStatus = 'loading' | 'ready' | 'failed' | 'unauthorized';

/** How often the list is loaded again while the page is open. */
export const CATCHUP_REFRESH = 60_000;

export class CatchupState {
  status = $state<CatchupStatus>('loading');
  /** The last list that loaded; kept while a later load fails. */
  data = $state<Catchup | null>(null);
  /** Why the last load failed; '' when it did not. */
  loadError = $state('');
  /** Why the last "Caught up" failed; '' when it did not. */
  markError = $state('');
  /** A mark read is out. */
  marking = $state(false);
  /** The list was marked read and has not loaded again since. */
  afterMark = $state(false);

  /** Unread items, counting those past the server's cap. */
  count = $derived(this.data ? this.data.total : 0);

  #client: () => CatchupClient;
  #seq = 0;
  #timer: ReturnType<typeof setInterval> | null = null;

  constructor(client: () => CatchupClient) {
    this.#client = client;
  }

  /** Loads now and every CATCHUP_REFRESH; returns a stop function. */
  start(every = CATCHUP_REFRESH): () => void {
    void this.refresh();
    this.#timer = setInterval(() => void this.refresh(), every);
    return () => {
      if (this.#timer !== null) clearInterval(this.#timer);
      this.#timer = null;
      this.#seq++; // an answer still out is dropped
    };
  }

  /** Loads the list again. Only the answer to the latest request is kept. */
  async refresh(): Promise<void> {
    if (this.status === 'unauthorized') return;
    const seq = ++this.#seq;
    if (!this.data) this.status = 'loading';
    const r = await this.#client().load();
    if (seq !== this.#seq) return;
    if (r.ok) {
      this.data = r.catchup;
      this.status = 'ready';
      this.loadError = '';
      this.afterMark = false;
    } else if (r.kind === 'unauthorized') {
      this.status = 'unauthorized';
    } else {
      this.status = 'failed';
      this.loadError = r.message;
    }
  }

  /**
   * Moves the marker to the shown list's asOf, then loads the list again;
   * true when the server took it. What the board learned after that list
   * stays unread. With nothing shown there is nothing to mark. Once the
   * server took it the shown items are read, so they leave the list even if
   * loading it again fails.
   */
  async markRead(): Promise<boolean> {
    const shown = this.data;
    if (this.marking || this.status === 'unauthorized' || !shown || shown.items.length === 0) return false;
    this.marking = true;
    try {
      const r = await this.#client().markRead(shown.asOf);
      if (!r.ok) {
        if (r.kind === 'unauthorized') this.status = 'unauthorized';
        else this.markError = r.message;
        return false;
      }
      this.markError = '';
      this.#seq++; // a load that went out before the mark would answer with read items
      this.data = { marker: shown.asOf, asOf: shown.asOf, total: 0, items: [] };
      this.afterMark = true;
      await this.refresh();
      return true;
    } finally {
      this.marking = false;
    }
  }
}
