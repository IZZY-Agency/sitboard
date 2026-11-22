// Leftovers: merged work still on this machine.
// The server lists, on positive evidence only, the worktrees, branches and
// sessions a merged pull request left behind. The list is for reading:
// sitboard removes nothing and offers no way to. The page loads the list when
// the sheet opens.

import {
  HeldReasonValues,
  LeftoverKindValues,
  LeftoversStateValues,
  Paths,
  type HeldCount,
  type HeldReason,
  type Leftover,
  type LeftoverPart,
  type Leftovers,
  type LeftoversSummary,
} from '../contract.gen';
import { prHref as forgePrHref } from './derive';

export type LeftoversLoad =
  | { ok: true; leftovers: Leftovers }
  | { ok: false; kind: 'unauthorized' } // 401 or 403: this tab lost its key
  | { ok: false; kind: 'failed'; message: string };

export interface LeftoversClient {
  load(): Promise<LeftoversLoad>;
}

const isObj = (v: unknown): v is Record<string, unknown> => !!v && typeof v === 'object' && !Array.isArray(v);
const str = (v: unknown): string => (typeof v === 'string' ? v : '');
const validTime = (s: unknown): s is string => typeof s === 'string' && !Number.isNaN(Date.parse(s));
const count = (v: unknown): number => (typeof v === 'number' && Number.isInteger(v) && v > 0 ? v : 0);

function readPart(raw: unknown): LeftoverPart | null {
  if (!isObj(raw) || !LeftoverKindValues.includes(raw.kind as LeftoverPart['kind'])) return null;
  const p: LeftoverPart = {
    kind: raw.kind as LeftoverPart['kind'],
    path: str(raw.path),
    branch: str(raw.branch),
    cardId: str(raw.cardId),
    name: str(raw.name),
    ignored: count(raw.ignored),
  };
  if (validTime(raw.idleSince)) p.idleSince = raw.idleSince;
  return p;
}

function readItem(raw: unknown): Leftover | null {
  if (!isObj(raw) || typeof raw.projectId !== 'string' || !isObj(raw.pullRequest) || !Array.isArray(raw.parts)) return null;
  const pr = raw.pullRequest;
  if (typeof pr.repo !== 'string' || typeof pr.number !== 'number' || !Number.isInteger(pr.number) || pr.number <= 0) return null;
  // Leftovers has GitHub evidence only: an item of any other forge or host is not one this page can say anything true about.
  if (pr.forge !== 'github' || pr.host !== 'github.com') return null;
  const parts = raw.parts.map(readPart).filter((p): p is LeftoverPart => p !== null);
  // An item whose every part is of a kind this page does not know shows nothing true.
  if (parts.length === 0) return null;
  const it: Leftover = {
    projectId: raw.projectId,
    pullRequest: {
      forge: 'github',
      host: 'github.com',
      repo: pr.repo,
      number: pr.number,
      title: str(pr.title),
      url: str(pr.url),
      state: 'merged',
      checks: 'none',
      review: '',
      draft: false,
    },
    parts,
    waiting: raw.waiting === true,
    ownSession: raw.waiting === true && raw.ownSession === true,
  };
  if (validTime(raw.mergedAt)) it.mergedAt = raw.mergedAt;
  return it;
}

/**
 * Keeps what the page can show truthfully: items with a pull request and at
 * least one part of a known kind, held counts of known reasons. A list in a
 * state this page does not know is refused.
 */
export function readLeftovers(raw: unknown): Leftovers | null {
  if (!isObj(raw) || !LeftoversStateValues.includes(raw.state as Leftovers['state'])) return null;
  const items = Array.isArray(raw.items) ? raw.items.map(readItem).filter((it): it is Leftover => it !== null) : [];
  const held = Array.isArray(raw.held)
    ? raw.held.filter(
        (h): h is HeldCount => isObj(h) && HeldReasonValues.includes(h.reason as HeldReason) && count(h.count) > 0,
      )
    : [];
  const out: Leftovers = { state: raw.state as Leftovers['state'], items, held: held.map((h) => ({ reason: h.reason, count: h.count })), partial: raw.partial === true };
  if (validTime(raw.asOf)) out.asOf = raw.asOf;
  return out;
}

export function createLeftoversClient(fetchFn: typeof fetch): LeftoversClient {
  return {
    async load() {
      let res: Response;
      try {
        res = await fetchFn(Paths.leftovers, { credentials: 'same-origin', headers: { Accept: 'application/json' } });
      } catch {
        return { ok: false, kind: 'failed', message: 'sitboard did not answer' };
      }
      if (res.status === 401 || res.status === 403) return { ok: false, kind: 'unauthorized' };
      if (!res.ok) return { ok: false, kind: 'failed', message: `sitboard answered ${res.status}` };
      let l: Leftovers | null;
      try {
        l = readLeftovers(await res.json());
      } catch {
        l = null;
      }
      return l ? { ok: true, leftovers: l } : { ok: false, kind: 'failed', message: 'sitboard sent something unreadable' };
    },
  };
}

/**
 * The status bar link: "2 leftovers", "0 leftovers · 4 held", "1 leftover ·
 * incomplete"; "" when the last look has nothing to say (or the summary is
 * not one this page can read).
 */
export function leftoversLink(s: LeftoversSummary | undefined): string {
  if (!isObj(s)) return '';
  const items = count(s.items);
  const held = count(s.held);
  const partial = s.partial === true;
  if (!items && !held && !partial) return '';
  let t = `${items} ${items === 1 ? 'leftover' : 'leftovers'}`;
  if (held) t += ` · ${held} held`;
  if (partial) t += ' · incomplete';
  return t;
}

const REASON: Record<HeldReason, [string, string]> = {
  commits: ['has commits after the merge', 'have commits after the merge'],
  changes: ['has uncommitted changes', 'have uncommitted changes'],
  stash: ['has a stash', 'have a stash'],
  inUse: ['is in use', 'are in use'],
  locked: ['is locked', 'are locked'],
  nested: ['holds another checkout', 'hold another checkout'],
  unknown: ['could not be checked', 'could not be checked'],
};

/** "3 not listed: 1 has commits after the merge, 2 are in use." for merged branches and sessions held back; "" when none. */
export function heldLine(held: readonly HeldCount[]): string {
  const total = held.reduce((n, h) => n + h.count, 0);
  if (total === 0) return '';
  const why = held.map((h) => `${h.count} ${REASON[h.reason][h.count === 1 ? 0 : 1]}`).join(', ');
  return `${total} not listed: ${why}.`;
}

/**
 * The row's one plain line: what sitboard checked, never a promise. A
 * worktree: merged, nothing uncommitted, no process in it; or in use only
 * by finished sessions: "its session" when they are this row's, "a
 * finished session" when they are listed under another pull request.
 * Without a worktree, only what was checked: a session's pull requests are
 * merged ("finished"), a local branch is not checked out, a branch is left
 * on GitHub.
 */
export function verdict(it: Leftover): string {
  if (it.waiting) return `merged · nothing uncommitted · in use by ${it.ownSession ? 'its session' : 'a finished session'}`;
  if (it.parts.some((p) => p.kind === 'worktree')) return 'merged · nothing uncommitted · not in use';
  const words = ['merged'];
  if (it.parts.some((p) => p.kind === 'session')) words.push('finished');
  if (it.parts.some((p) => p.kind === 'branch')) words.push('branch not checked out');
  if (it.parts.some((p) => p.kind === 'remoteBranch')) words.push('branch still on GitHub');
  return words.join(' · ');
}

/** The pull request's address when it is the one built for its own repository and number; '' otherwise. */
export function prHref(it: Leftover): string {
  return forgePrHref(it.pullRequest) ?? '';
}
