// The page's catch-up state: the last list the server sent, whether it is
// being loaded, and what went wrong. Loading marks nothing; only markRead
// does, and only the items of the list shown, so a reload or a second tab
// never loses an item the board learned after the list that was shown.

import type { Catchup, CatchupItem } from '../contract.gen';
import type { CatchupClient } from './catchup';
import { gitlabMount } from './derive';

export type CatchupStatus = 'loading' | 'ready' | 'failed' | 'unauthorized';

/** Why "Caught up" marked nothing when the list shown was not current (409). */
export const STALE = 'the list changed since this tab loaded it (sitboard restarted, or the list is old)';
/** Added to STALE once the list as it is now has loaded. */
export const STALE_RELOADED = '; this is the list as it is now';
/** Why "Caught up" marked nothing at the cap (507). */
export const FULL =
  'marking this list read would keep more than 20,000 items marked read from the last 7 days, the most sitboard keeps; room comes back as older ones pass 7 days';

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

  /** The GitLab hosts of the last snapshot; null before the first. */
  #hosts = $state<ReadonlySet<string> | null>(null);
  /** Unread items, counting those past the server's cap. */
  count = $derived(this.data ? this.data.total : 0);
  /**
   * The list shown can be marked read: it is a list the server sent, with
   * every item it sent, and the first snapshot's GitLab hosts are known (the
   * panel hides a merge request of a host it does not know). A list the page
   * took items out of, or one the server refused as not current, cannot be,
   * until the next list loads.
   */
  canMark = $derived(!!this.data?.list && this.#hosts !== null);

  #client: () => CatchupClient;
  #seq = 0;
  #stopped = false;
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
    const listed = [...hosts];
    // A row stays only on a listed site and under its mount: after a mount
    // change the rows of the old one go at once, before the list is asked for again.
    const keep = (it: CatchupItem) => {
      const pr = it.pullRequest;
      if (pr?.forge !== 'gitlab') return true;
      const mount = gitlabMount(listed, pr.host);
      return mount !== null && (mount === '' || pr.repo.startsWith(mount + '/'));
    };
    if (c.items.every(keep)) return c;
    const items = c.items.filter(keep);
    // Not the list the server sent any more: "Caught up" on its id would mark
    // the rows taken out too, so it has none until the next list loads.
    return { ...c, list: '', total: c.total - (c.items.length - items.length), items };
  }

  /**
   * Marks the shown list's items read, then loads the list again; true when
   * the server took it. What the board learned after that list stays unread.
   * With nothing shown there is nothing to mark. Once the server took it the
   * shown items are read, so they leave the list even if loading it again
   * fails. When the server refused it (the list is not current, or it keeps
   * no more marked items) nothing was marked: the page says why and loads
   * the list again, so the owner sees what is unread now.
   */
  async markRead(): Promise<boolean> {
    const shown = this.data;
    if (this.marking || this.status === 'unauthorized' || !shown || !this.canMark || shown.items.length === 0) return false;
    this.marking = true;
    try {
      const r = await this.#client().markRead(shown.list);
      if (!r.ok) {
        if (r.kind === 'unauthorized') {
          this.status = 'unauthorized';
          return false;
        }
        if (r.kind === 'failed') {
          this.markError = r.message;
          return false;
        }
        if (r.kind === 'full') {
          this.markError = FULL;
          await this.refresh();
          return false;
        }
        // Not current: this list can never be marked; the next one can.
        this.markError = STALE;
        if (this.data === shown) this.data = { ...shown, list: '' };
        await this.refresh();
        if (this.status === 'ready' && this.data?.list) this.markError = STALE + STALE_RELOADED;
        return false;
      }
      this.markError = '';
      this.#seq++; // a load that went out before the mark would answer with read items
      this.data = { ...shown, marker: shown.asOf, total: 0, items: [] }; // what it says of the read (partial, pending, unsaved) stays
      this.afterMark = true;
      await this.refresh();
      return true;
    } finally {
      this.marking = false;
    }
  }
}
