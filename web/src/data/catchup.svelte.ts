// The page's catch-up state: the last list the server sent, whether it is
// being loaded, and what went wrong. Loading never moves the marker; only
// markRead does, up to the list's asOf, so a reload or a second tab never
// loses an item the board learned after the list that was shown.

import type { Catchup, CatchupItem } from '../contract.gen';
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
  #stopped = false;
  #timer: ReturnType<typeof setInterval> | null = null;
  /** The GitLab hosts of the last snapshot; null before the first. */
  #hosts: ReadonlySet<string> | null = null;

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
      this.#stopped = true;
      this.#seq++; // an answer still out is dropped
    };
  }

  /**
   * Loads the list again. Only the answer to the latest request is kept,
   * except a lost key: that answer to any request counts at once, and stays.
   */
  async refresh(): Promise<void> {
    if (this.status === 'unauthorized') return;
    const seq = ++this.#seq;
    if (!this.data) this.status = 'loading';
    const r = await this.#client().load();
    if (!r.ok && r.kind === 'unauthorized') {
      if (this.#stopped) return;
      this.#seq++; // answers still out are dropped
      this.status = 'unauthorized';
      return;
    }
    if (seq !== this.#seq) return;
    if (r.ok) {
      this.data = this.#listed(r.catchup);
      this.status = 'ready';
      this.loadError = '';
      this.afterMark = false;
    } else {
      this.status = 'failed';
      this.loadError = r.message;
    }
  }

  /**
   * Takes the GitLab hosts of each snapshot (Snapshot.gitlabHosts). The kept
   * list never holds a merge request of a host the snapshot does not list:
   * when a host leaves (glab was logged out of it) its rows are removed, not
   * only hidden, so a later login to the same host, maybe another account,
   * never shows the earlier one's titles. Any change of the hosts asks for
   * the list again, and only that answer counts: an answer to a load that
   * went out before could hold the earlier login's rows.
   */
  setGitlabHosts(hosts: readonly string[]): void {
    const before = this.#hosts;
    const now = new Set(hosts);
    this.#hosts = now;
    if (this.data) this.data = this.#listed(this.data);
    if (before && (before.size !== now.size || [...before].some((h) => !now.has(h)))) void this.refresh();
  }

  /** The list without merge requests of hosts the last snapshot did not list. */
  #listed(c: Catchup): Catchup {
    const hosts = this.#hosts;
    if (!hosts) return c;
    const keep = (it: CatchupItem) => it.pullRequest?.forge !== 'gitlab' || hosts.has(it.pullRequest.host);
    if (c.items.every(keep)) return c;
    const items = c.items.filter(keep);
    return { ...c, total: c.total - (c.items.length - items.length), items };
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
