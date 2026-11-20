// Catch-up: what happened since the owner last marked it read. The server
// lists finished turns, opened and merged pull requests and published
// artifacts it learned since the marker, newest first; the page loads the
// list, groups it, and marks it read only when asked, up to the list's asOf.

import { CatchupKindValues, Paths, type Artifact, type Catchup, type CatchupItem, type CatchupKind, type PullRequest } from '../contract.gen';

export type CatchupLoad =
  | { ok: true; catchup: Catchup }
  | { ok: false; kind: 'unauthorized' } // 401 or 403: this tab lost its key
  | { ok: false; kind: 'failed'; message: string };

export type ReadResult = { ok: true } | { ok: false; kind: 'unauthorized' } | { ok: false; kind: 'failed'; message: string };

export interface CatchupClient {
  load(): Promise<CatchupLoad>;
  /** upTo: the asOf of the list shown; the server moves the marker there. */
  markRead(upTo: string): Promise<ReadResult>;
}

function validTime(s: unknown): s is string {
  return typeof s === 'string' && !Number.isNaN(Date.parse(s));
}

/**
 * Keeps the items the page can show: a known kind and a readable time. An item
 * a newer server added a kind for is left out rather than shown wrong. A list
 * without a readable asOf is refused: it could not be marked read.
 */
export function readCatchup(raw: unknown): Catchup | null {
  if (!raw || typeof raw !== 'object') return null;
  const r = raw as { marker?: unknown; asOf?: unknown; total?: unknown; unsaved?: unknown; pending?: unknown; items?: unknown };
  if (!Array.isArray(r.items) || !validTime(r.asOf)) return null;
  const items = r.items.filter(
    (it): it is CatchupItem =>
      !!it &&
      typeof it === 'object' &&
      CatchupKindValues.includes((it as CatchupItem).kind) &&
      validTime((it as CatchupItem).at) &&
      typeof (it as CatchupItem).projectId === 'string',
  );
  const total = typeof r.total === 'number' && Number.isInteger(r.total) && r.total >= items.length ? r.total : items.length;
  const out: Catchup = { asOf: r.asOf, total, items };
  if (r.unsaved === true) out.unsaved = true;
  if (r.pending === true) out.pending = true;
  return validTime(r.marker) ? { marker: r.marker, ...out } : out;
}

export function createCatchupClient(fetchFn: typeof fetch): CatchupClient {
  return {
    async load() {
      let res: Response;
      try {
        res = await fetchFn(Paths.catchup, { credentials: 'same-origin', headers: { Accept: 'application/json' } });
      } catch {
        return { ok: false, kind: 'failed', message: 'sitboard did not answer' };
      }
      if (res.status === 401 || res.status === 403) return { ok: false, kind: 'unauthorized' };
      if (!res.ok) return { ok: false, kind: 'failed', message: `sitboard answered ${res.status}` };
      let c: Catchup | null;
      try {
        c = readCatchup(await res.json());
      } catch {
        c = null;
      }
      return c ? { ok: true, catchup: c } : { ok: false, kind: 'failed', message: 'sitboard sent something unreadable' };
    },
    async markRead(upTo) {
      let res: Response;
      try {
        res = await fetchFn(Paths.catchupRead, {
          method: 'POST',
          credentials: 'same-origin',
          headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify({ upTo }),
        });
      } catch {
        return { ok: false, kind: 'failed', message: 'sitboard did not answer' };
      }
      if (res.ok) return { ok: true };
      if (res.status === 401 || res.status === 403) return { ok: false, kind: 'unauthorized' };
      if (res.status === 500) return { ok: false, kind: 'failed', message: 'sitboard could not save it' };
      return { ok: false, kind: 'failed', message: `sitboard answered ${res.status}` };
    },
  };
}

/** One line of the list: a pull request, or one agent's finished turns folded together. */
export interface CatchupRow {
  key: string;
  kind: CatchupKind;
  /** The newest time in the row. */
  at: string;
  /** Finished turns folded into this row; 1 for a pull request. */
  count: number;
  agentName: string;
  title: string;
  cardId: string;
  pullRequest?: PullRequest;
  artifact?: Artifact;
}

export interface CatchupGroup {
  projectId: string;
  /** The project's name as the board shows it; '' when the server sent none. */
  name: string;
  /** Items in the group, before folding. */
  total: number;
  rows: CatchupRow[];
}

const kindOrder = (k: CatchupKind) => CatchupKindValues.indexOf(k);

/**
 * Groups items by project, the project with the newest item first. Inside a
 * project, rows are newest first; one agent's finished turns fold into one
 * row with their count and the newest time. Each pull request keeps a row.
 */
export function groupCatchup(items: readonly CatchupItem[]): CatchupGroup[] {
  const groups = new Map<string, { name: string; total: number; rows: Map<string, CatchupRow> }>();
  for (const it of items) {
    let g = groups.get(it.projectId);
    if (!g) {
      g = { name: typeof it.projectName === 'string' ? it.projectName : '', total: 0, rows: new Map() };
      groups.set(it.projectId, g);
    }
    g.total++;
    const who = it.cardId || `name:${it.agentName}`;
    const what = it.pullRequest ? `${it.pullRequest.repo}#${it.pullRequest.number}` : it.artifact ? it.artifact.url : `${who}|${it.at}`;
    const key = it.kind === 'turn_finished' ? `turn|${who}` : `${it.kind}|${what}`;
    const row = g.rows.get(key);
    if (!row) {
      g.rows.set(key, {
        key,
        kind: it.kind,
        at: it.at,
        count: 1,
        agentName: it.agentName,
        title: it.title,
        cardId: it.cardId,
        pullRequest: it.pullRequest,
        artifact: it.artifact,
      });
      continue;
    }
    row.count += it.kind === 'turn_finished' ? 1 : 0;
    if (Date.parse(it.at) > Date.parse(row.at)) {
      row.at = it.at;
      row.title = it.title;
    }
  }
  const newest = (rows: CatchupRow[]) => Math.max(...rows.map((r) => Date.parse(r.at)));
  return [...groups]
    .map(([projectId, g]) => ({
      projectId,
      name: g.name,
      total: g.total,
      rows: [...g.rows.values()].sort(
        (a, b) => Date.parse(b.at) - Date.parse(a.at) || kindOrder(b.kind) - kindOrder(a.kind) || a.key.localeCompare(b.key),
      ),
    }))
    .sort((a, b) => newest(b.rows) - newest(a.rows) || a.projectId.localeCompare(b.projectId));
}

/** The word for a row's kind, never colour alone: "Turn done", "3 turns", "PR opened", "Merged", "Artifact". */
export function rowWord(row: CatchupRow): string {
  switch (row.kind) {
    case 'turn_finished':
      return row.count > 1 ? `${row.count} turns` : 'Turn done';
    case 'pr_opened':
      return 'PR opened';
    case 'pr_merged':
      return 'Merged';
    default:
      return 'Artifact';
  }
}

/** Where a row leads: the pull request, the artifact, the card while it is on the board, or nowhere. */
export type RowLink = { kind: 'url'; href: string; text: string } | { kind: 'card'; cardId: string } | { kind: 'none' };

const SAFE_HREF = /^https:\/\/[^\s/]+\//;

export function rowLink(row: CatchupRow, onBoard: ReadonlySet<string>): RowLink {
  if (row.pullRequest && SAFE_HREF.test(row.pullRequest.url)) return { kind: 'url', href: row.pullRequest.url, text: `#${row.pullRequest.number}` };
  if (row.artifact && SAFE_HREF.test(row.artifact.url)) return { kind: 'url', href: row.artifact.url, text: 'Open' };
  if (row.cardId && onBoard.has(row.cardId)) return { kind: 'card', cardId: row.cardId };
  return { kind: 'none' };
}

/** How many rows a project shows before "Show N more". */
export const FOLD_AT = 4;
