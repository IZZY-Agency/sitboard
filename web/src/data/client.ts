// The live connection: load the snapshot, then follow server-sent events.
// On a dropped stream it reconnects with backoff; a 401 or 403 means this tab
// lost its key, and a different contract version means the page is outdated.

import { CONTRACT_VERSION, EVENT_SNAPSHOT, Paths, type Snapshot } from '../contract.gen';
import { readGitlabHosts } from './derive';

export type Conn =
  | 'connecting' // first load, nothing shown yet
  | 'live' // following events
  | 'reconnecting' // had a snapshot, the stream dropped, retrying
  | 'unreachable' // never got a snapshot, retrying
  | 'unauthorized' // 401 or 403: open the link sitboard printed again
  | 'outdated'; // the server speaks another contract version: reload

export interface FeedState {
  conn: Conn;
  snapshot: Snapshot | null;
  /** When the next attempt starts (ms since epoch), while reconnecting or unreachable. */
  retryAt?: number;
}

export interface Feed {
  /** Starts the connection; returns a stop function. */
  start(onState: (s: FeedState) => void): () => void;
}

interface EventSourceLike {
  addEventListener(type: string, fn: (e: MessageEvent<string>) => void): void;
  onerror: ((e: Event) => void) | null;
  onopen: ((e: Event) => void) | null;
  close(): void;
}

export interface FeedDeps {
  fetch: typeof fetch;
  EventSource: new (url: string) => EventSourceLike;
  setTimeout: (fn: () => void, ms: number) => unknown;
  clearTimeout: (id: unknown) => void;
  now: () => number;
}

/** Waits between attempts: 1 s, 2 s, 4 s, 8 s, then 15 s. */
export const BACKOFF = [1000, 2000, 4000, 8000, 15000];

export function createFeed(deps: FeedDeps): Feed {
  return {
    start(onState) {
      let snapshot: Snapshot | null = null;
      let es: EventSourceLike | null = null;
      let timer: unknown = null;
      let attempt = 0;
      let stopped = false;

      const emit = (conn: Conn, retryAt?: number) => {
        if (!stopped) onState({ conn, snapshot, retryAt });
      };

      const halt = (conn: Conn) => {
        es?.close();
        es = null;
        emit(conn);
        stopped = true;
      };

      // accept returns false when the snapshot ended the connection.
      const accept = (s: Snapshot): boolean => {
        if (s.version !== CONTRACT_VERSION) {
          halt('outdated');
          return false;
        }
        // The one list of hosts a GitLab link may point at is checked here, as the snapshot arrives.
        snapshot = { ...s, gitlabHosts: readGitlabHosts(s.gitlabHosts) };
        emit('live');
        return true;
      };

      const retry = () => {
        es?.close();
        es = null;
        const wait = BACKOFF[Math.min(attempt, BACKOFF.length - 1)];
        attempt++;
        emit(snapshot ? 'reconnecting' : 'unreachable', deps.now() + wait);
        timer = deps.setTimeout(connect, wait);
      };

      const follow = () => {
        es = new deps.EventSource(Paths.events);
        es.addEventListener(EVENT_SNAPSHOT, (e) => {
          if (stopped) return;
          let s: Snapshot;
          try {
            s = JSON.parse(e.data) as Snapshot;
          } catch {
            return; // a broken message is skipped; the next one replaces it
          }
          attempt = 0; // the stream works: the next failure starts the backoff again
          accept(s);
        });
        es.onopen = () => {
          if (!stopped) attempt = 0;
        };
        es.onerror = () => {
          if (!stopped) retry();
        };
      };

      async function connect() {
        timer = null;
        if (stopped) return;
        let res: Response;
        try {
          res = await deps.fetch(Paths.snapshot, {
            credentials: 'same-origin',
            headers: { Accept: 'application/json' },
          });
        } catch {
          retry();
          return;
        }
        if (stopped) return;
        if (res.status === 401 || res.status === 403) {
          halt('unauthorized');
          return;
        }
        if (!res.ok) {
          retry();
          return;
        }
        let s: Snapshot;
        try {
          s = (await res.json()) as Snapshot;
        } catch {
          retry();
          return;
        }
        if (stopped) return;
        if (accept(s)) follow();
      }

      emit('connecting');
      void connect();

      return () => {
        stopped = true;
        es?.close();
        if (timer !== null) deps.clearTimeout(timer);
      };
    },
  };
}

/** The feed against the real sitboard, in the browser. */
export function browserFeed(): Feed {
  return createFeed({
    fetch: (...a) => fetch(...a),
    EventSource: EventSource as unknown as FeedDeps['EventSource'],
    setTimeout: (fn, ms) => setTimeout(fn, ms),
    clearTimeout: (id) => clearTimeout(id as number),
    now: () => Date.now(),
  });
}
