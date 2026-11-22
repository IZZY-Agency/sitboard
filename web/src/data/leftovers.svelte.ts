// The sheet's leftovers state: loaded when the sheet opens and again every
// minute while it is open. A lost key clears the list at once. The last list
// that loaded is kept while a later load fails.

import type { Leftovers } from '../contract.gen';
import type { LeftoversClient } from './leftovers';

export type LeftoversStatus = 'loading' | 'ready' | 'failed' | 'unauthorized';

/** How often the list is loaded again while the sheet is open. */
export const LEFTOVERS_REFRESH = 60_000;

export class LeftoversState {
  status = $state<LeftoversStatus>('loading');
  data = $state<Leftovers | null>(null);
  error = $state('');

  #client: () => LeftoversClient;
  #seq = 0;
  #stopped = false;

  constructor(client: () => LeftoversClient) {
    this.#client = client;
  }

  /** Loads now and every LEFTOVERS_REFRESH; returns a stop function. */
  start(every = LEFTOVERS_REFRESH): () => void {
    void this.refresh();
    const timer = setInterval(() => void this.refresh(), every);
    return () => {
      clearInterval(timer);
      this.#stopped = true;
      this.#seq++; // an answer still out is dropped
    };
  }

  /**
   * Only the answer to the latest request is kept, except a lost key: that
   * answer to any request clears the list and drops the others still out.
   */
  async refresh(): Promise<void> {
    if (this.status === 'unauthorized') return;
    const seq = ++this.#seq;
    if (!this.data) this.status = 'loading';
    const r = await this.#client().load();
    if (!r.ok && r.kind === 'unauthorized') {
      if (this.#stopped) return;
      this.#seq++;
      this.status = 'unauthorized';
      this.data = null; // private paths go with the key
      return;
    }
    if (seq !== this.#seq) return;
    if (r.ok) {
      this.data = r.leftovers;
      this.status = 'ready';
      this.error = '';
    } else {
      this.status = 'failed';
      this.error = r.message;
    }
  }
}
