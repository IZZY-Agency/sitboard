// Dev harness only: placeholder catch-up lists for the "site" scenario's
// sample projects and agents. Never part of the build.
import type { Catchup, CatchupItem, PullRequest } from '../src/contract.gen';
import type { CatchupClient } from '../src/data/catchup';
import { SAMPLE_GITLAB_HOST, SAMPLE_GITLAB_PATH, SAMPLE_MR_TITLES } from './scenarios';

export const CATCHUPS = ['few', 'gitlab', 'many', 'empty', 'nostate', 'unsaved', 'partial', 'down', 'stale', 'full'] as const;
export type CatchupCase = (typeof CATCHUPS)[number];

const MIN = 60_000;
const HOUR = 60 * MIN;
const repo = 'example-org/example-repo';

type Who = { cardId: string; projectId: string; projectName: string; agentName: string; title: string };
const lead: Who = { cardId: 'claude:site-lead', projectId: 'p_site00000001', projectName: 'harbor', agentName: 'harbor-lead', title: 'Checkout rewrite: plan, split and review' };
const checkout: Who = { cardId: 'claude:site-w1', projectId: 'p_site00000002', projectName: 'harbor-web', agentName: 'web-checkout', title: 'One-page checkout with saved addresses' };
const search: Who = { cardId: 'claude:site-w3', projectId: 'p_site00000002', projectName: 'harbor-web', agentName: 'web-search', title: 'Search suggestions as you type' };
const auth: Who = { cardId: 'claude:site-w2', projectId: 'p_site00000003', projectName: 'harbor-api', agentName: 'api-auth', title: 'Session refresh for long-lived tokens' };
const payments: Who = { cardId: 'claude:site-w4', projectId: 'p_site00000003', projectName: 'harbor-api', agentName: 'api-payments', title: 'Retry failed card payments once' };
const docs: Who = { cardId: 'claude:site-d1', projectId: 'p_site00000004', projectName: 'lumen-docs', agentName: 'docs-refresh', title: 'Rewrite the getting-started guide' };
// A worker that has ended: its card has left the board, and so has its project.
const gone: Who = { cardId: '', projectId: 'p_site00000009', projectName: 'quarry', agentName: 'csv-import', title: 'CSV import with a preview' };

function pr(n: number, title: string, state: PullRequest['state']): PullRequest {
  return { forge: 'github' as const, host: 'github.com', repo, number: n, title, url: `https://github.com/${repo}/pull/${n}`, state, checks: 'passing', review: state === 'merged' ? 'approved' : 'review_required', draft: false };
}
const at = (now: number, ago: number) => new Date(now - ago).toISOString();
const turn = (w: Who, now: number, ago: number): CatchupItem => ({ ...w, at: at(now, ago), kind: 'turn_finished' });
const opened = (w: Who, now: number, ago: number, n: number, title: string): CatchupItem => ({ ...w, at: at(now, ago), kind: 'pr_opened', title, pullRequest: pr(n, title, 'open') });
const merged = (w: Who, now: number, ago: number, n: number, title: string): CatchupItem => ({ ...w, at: at(now, ago), kind: 'pr_merged', title, pullRequest: pr(n, title, 'merged') });
const artifact = (w: Who, now: number, ago: number, n: number, title: string): CatchupItem => ({
  ...w,
  at: at(now, ago),
  kind: 'artifact_published',
  title,
  artifact: { title, url: `https://claude.ai/artifact/exampleArtifact${String(n).padStart(4, '0')}` },
});

function few(now: number): CatchupItem[] {
  return [turn(lead, now, 8 * MIN), opened(search, now, 42 * MIN, 222, 'Search suggestions as you type'), merged(docs, now, 64 * MIN, 41, 'Rewrite the getting-started guide')];
}

/** "few" with the API part's merge requests on the sample GitLab host (the "gitlab" scenario). */
function withGitLab(now: number): CatchupItem[] {
  const mr = (n: number, title: string, state: PullRequest['state']): PullRequest => ({
    forge: 'gitlab' as const, host: SAMPLE_GITLAB_HOST, repo: SAMPLE_GITLAB_PATH,
    number: n,
    title,
    url: `https://${SAMPLE_GITLAB_HOST}/${SAMPLE_GITLAB_PATH}/-/merge_requests/${n}`,
    state,
    checks: 'passing',
    review: state === 'merged' ? 'approved' : 'review_required',
    draft: false,
  });
  // The same merge requests, sessions and titles as the "gitlab" board.
  return [
    turn(lead, now, 8 * MIN),
    { ...payments, at: at(now, 20 * MIN), kind: 'pr_opened', title: SAMPLE_MR_TITLES[31], pullRequest: mr(31, SAMPLE_MR_TITLES[31], 'open') },
    opened(search, now, 42 * MIN, 222, 'Search suggestions as you type'),
    { ...auth, at: at(now, 55 * MIN), kind: 'pr_merged', title: SAMPLE_MR_TITLES[27], pullRequest: mr(27, SAMPLE_MR_TITLES[27], 'merged') },
  ];
}

/** About a weekend: 80 items over three days. */
function many(now: number): CatchupItem[] {
  const out: CatchupItem[] = [];
  const turns: Array<[Who, number]> = [
    [lead, 19],
    [checkout, 14],
    [search, 9],
    [payments, 11],
    [docs, 6],
    [gone, 6],
  ];
  for (const [w, n] of turns) for (let i = 0; i < n; i++) out.push(turn(w, now, (2 + i * 4.3 + w.agentName.length) * HOUR));
  out.push(
    merged(checkout, now, 3 * HOUR, 209, 'One-page checkout with saved addresses'),
    merged(lead, now, 20 * HOUR, 214, 'Checkout plan: split into four parts'),
    merged(gone, now, 30 * HOUR, 57, 'CSV import with a preview'),
    merged(payments, now, 44 * HOUR, 205, 'Idempotency keys on card payments'),
    merged(docs, now, 50 * HOUR, 41, 'Rewrite the getting-started guide'),
    merged(search, now, 60 * HOUR, 199, 'Debounce the search box'),
    merged(checkout, now, 66 * HOUR, 197, 'Address autocomplete'),
    opened(search, now, 6 * HOUR, 222, 'Search suggestions as you type'),
    opened(payments, now, 12 * HOUR, 219, 'Retry failed card payments once'),
    opened(checkout, now, 26 * HOUR, 221, 'Saved addresses for returning buyers'),
    opened(gone, now, 40 * HOUR, 57, 'CSV import with a preview'),
    opened(docs, now, 52 * HOUR, 41, 'Rewrite the getting-started guide'),
    artifact(lead, now, 5 * HOUR, 11, 'Checkout plan'),
    artifact(payments, now, 46 * HOUR, 13, 'Payment retry flow'),
  );
  return out.sort((a, b) => Date.parse(b.at) - Date.parse(a.at)).slice(0, 80);
}

/**
 * A client that answers like the server: "Caught up" on a list marks its
 * items read. "stale" answers the first "Caught up" as the server does for a
 * list made before a restart (409), "full" every one as at the cap (507).
 */
export function catchupClient(c: CatchupCase): CatchupClient {
  const now = Date.now();
  let marker: string | undefined =
    c === 'nostate' || c === 'unsaved' ? undefined : c === 'many' ? at(now, 3 * 24 * HOUR) : c === 'empty' ? at(now, 20 * MIN) : at(now, 100 * MIN);
  let seq = 0;
  let refusedOnce = false;
  let items = c === 'many' ? many(now) : c === 'empty' ? [] : c === 'gitlab' ? withGitLab(now) : c === 'nostate' || c === 'unsaved' ? [...few(now), turn(gone, now, 2 * 24 * HOUR)] : few(now);
  return {
    async load() {
      await new Promise((r) => setTimeout(r, 150));
      if (c === 'down') return { ok: false, kind: 'failed', message: 'sitboard did not answer' };
      const asOf = new Date().toISOString();
      const list = `dev-${++seq}`;
      const out: Catchup = marker ? { marker, list, asOf, total: items.length, items } : { list, asOf, total: items.length, items };
      if (c === 'unsaved') out.unsaved = true;
      if (c === 'partial') out.partial = true;
      return { ok: true, catchup: structuredClone(out) };
    },
    async markRead() {
      await new Promise((r) => setTimeout(r, 150));
      if (c === 'full') return { ok: false, kind: 'full' };
      if (c === 'stale' && !refusedOnce) {
        refusedOnce = true;
        return { ok: false, kind: 'stale' };
      }
      marker = new Date().toISOString();
      // Every item here was in the list shown, so all of them are read now.
      items = [];
      return { ok: true };
    },
  };
}
