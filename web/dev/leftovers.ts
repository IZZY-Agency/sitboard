// Dev harness only: placeholder leftovers lists for the sample projects of
// the scenario shown. Never part of the build.
import type { Leftover, LeftoverPart, Leftovers, LeftoversSummary, Project } from '../src/contract.gen';
import type { LeftoversClient } from '../src/data/leftovers';

export const LEFTOVERS = ['few', 'after', 'held', 'none', 'off', 'looking', 'partial', 'stale', 'down'] as const;
export type LeftoversCase = (typeof LEFTOVERS)[number];

const MIN = 60_000;
const HOUR = 60 * MIN;
const DAY = 24 * HOUR;
const repo = 'example-org/example-repo';
const at = (now: number, ago: number) => new Date(now - ago).toISOString();

const part = (p: Partial<LeftoverPart> & Pick<LeftoverPart, 'kind'>): LeftoverPart => ({ path: '', branch: '', cardId: '', name: '', ignored: 0, ...p });

function item(projectId: string, n: number, title: string, mergedAgo: number, now: number, parts: LeftoverPart[], over: Partial<Leftover> = {}): Leftover {
  return {
    projectId,
    pullRequest: { forge: 'github' as const, host: 'github.com', repo, number: n, title, url: `https://github.com/${repo}/pull/${n}`, state: 'merged', checks: 'passing', review: 'approved', draft: false },
    mergedAt: at(now, mergedAgo),
    parts,
    waiting: false,
    ownSession: false,
    ...over,
  };
}

function list(c: LeftoversCase, projects: readonly Project[], now: number): Leftovers {
  const asOf = new Date(now).toISOString();
  if (c === 'off') return { state: 'off', items: [], held: [], partial: false };
  if (c === 'looking') return { state: 'loading', items: [], held: [], partial: false };
  if (c === 'none') return { state: 'ready', asOf, items: [], held: [], partial: false };
  if (c === 'held')
    // every candidate held back: the link still opens the sheet
    return {
      state: 'ready',
      asOf,
      items: [],
      held: [
        { reason: 'commits', count: 1 },
        { reason: 'inUse', count: 2 },
        { reason: 'nested', count: 1 },
      ],
      partial: false,
    };
  const [a, b] = [projects[0], projects[1] ?? projects[0]];
  const ra = `projects/${a?.name ?? 'sample-app'}`;
  const rb = `projects/${b?.name ?? 'sample-app'}`;
  const pa = a?.id ?? '', pb = b?.id ?? '';
  const wt1 = `${ra}/.worktrees/webhook-retry`;
  const wt2 = `${rb}/.worktrees/card-spacing`;
  const session = part({ kind: 'session', cardId: 'claude:sample-left-1', name: 'sample-agent-5', idleSince: at(now, 2 * DAY - 3 * HOUR) });
  // A branch part names the clone it is in.
  const br = (branch: string, clone: string) => part({ kind: 'branch', branch, path: `~/${clone}` });
  const first =
    c === 'after'
      ? // after the session is closed: its worktree no longer waits
        item(pa, 41, 'Retry the webhook on a timeout', 2 * DAY, now, [part({ kind: 'worktree', path: `~/${wt1}` }), br('webhook-retry', ra)])
      : item(pa, 41, 'Retry the webhook on a timeout', 2 * DAY, now, [session, part({ kind: 'worktree', path: `~/${wt1}` }), br('webhook-retry', ra)], {
          waiting: true,
          ownSession: true,
        });
  const items: Leftover[] = [
    first,
    item(pb, 38, 'Card spacing on phones', 5 * DAY, now, [part({ kind: 'worktree', path: `~/${wt2}`, ignored: 2 }), br('card-spacing', rb), part({ kind: 'remoteBranch', branch: 'card-spacing' })]),
    // a name with a zero-width space and a double space: shown exactly
    item(pb, 36, 'Dark mode toggle', 9 * DAY, now, [br('dark\u200btoggle  v2', rb)]),
    item(pa, 33, 'Export the report as CSV', 11 * DAY, now, [part({ kind: 'remoteBranch', branch: 'export-csv' })]),
    item(pb, 31, 'Sample session that finished its work', 3 * DAY, now, [
      part({ kind: 'session', cardId: 'claude:sample-left-2', name: 'sample-agent-6', idleSince: at(now, 2 * DAY) }),
      part({ kind: 'remoteBranch', branch: 'search-filters' }),
    ]),
  ];
  if (c !== 'after')
    // a worktree that waits for a finished session listed under another pull request
    items.push(
      item(pa, 29, 'Sample worktree a later session sits in', 4 * DAY, now, [part({ kind: 'worktree', path: `~/${ra}/.worktrees/old-fix` }), br('old-fix', ra)], { waiting: true }),
    );
  return {
    state: 'ready',
    asOf,
    items: c === 'after' ? items.slice(0, 4) : items,
    held: [
      { reason: 'commits', count: 1 },
      { reason: 'changes', count: 2 },
      { reason: 'inUse', count: 1 },
      { reason: 'nested', count: 1 },
    ],
    partial: c === 'partial',
  };
}

/**
 * What the snapshot says for this case: the status bar's link. The server
 * says nothing while off or still looking, so the link is hidden then; the
 * harness shows one anyway so those sheet states can be looked at.
 */
export function leftoversSummary(c: LeftoversCase, projects: readonly Project[]): LeftoversSummary {
  if (c === 'down' || c === 'off' || c === 'looking') return { items: 2, held: 0, partial: false };
  const l = list(c, projects, Date.now());
  return { items: l.items.length, held: l.held.reduce((n, h) => n + h.count, 0), partial: l.partial };
}

export function leftoversClient(c: LeftoversCase, projects: readonly Project[]): LeftoversClient {
  let loads = 0;
  return {
    async load() {
      await new Promise((r) => setTimeout(r, 150));
      // stale: the first load works, every refresh after it fails
      if (c === 'down' || (c === 'stale' && loads++ > 0)) return { ok: false, kind: 'failed', message: 'sitboard did not answer' };
      return { ok: true, leftovers: structuredClone(list(c, projects, Date.now())) };
    },
  };
}
