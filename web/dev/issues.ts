// Dev harness only: placeholder open-issue lists for the sample projects of
// the scenario shown, and the snapshot's counts to match. Never part of the
// build.
import type { Issues, IssuesSummary, OpenIssue, Project, RepoIssues, Snapshot } from '../src/contract.gen';
import type { IssuesClient } from '../src/data/issues';

export const ISSUES = ['few', 'many', 'milestones', 'none', 'loading', 'off', 'down'] as const;
export type IssuesCase = (typeof ISSUES)[number];

const HOUR = 60 * 60_000;
const DAY = 24 * HOUR;
const at = (now: number, ago: number) => new Date(now - ago).toISOString();
// A milestone due in two weeks, at midnight UTC as the forges keep it.
const dueIn = (now: number, days: number) => `${new Date(now + days * DAY).toISOString().slice(0, 10)}T00:00:00Z`;

const TITLES = [
  'Placeholder issue: export misses the last page',
  'Placeholder issue: cache image sizes',
  'Placeholder issue: setup guide',
  'Placeholder issue: a long title that wraps over two lines on a phone to show how the row grows',
  'Placeholder issue: retry after a timeout',
  'Placeholder issue: empty state copy',
];
// The site scenario's sample repositories (web/dev/scenarios.ts, site()) get
// titles that fit their story, for the website's pictures; every other
// repository gets the placeholders above. Same order as LABELS and MILESTONES.
const STORY: Record<string, string[]> = {
  'example-org/harbor': [
    'Checkout loses the saved address after a failed payment',
    'Remember the last delivery slot',
    'Order history loads slowly with many orders',
    'Document the session refresh for the mobile app, with an example for each platform',
    'Retry button stays disabled after a timeout',
    'Empty basket has no way back to the shop',
  ],
  'example-org/lumen-docs': [
    'Broken link in the getting-started guide',
    'A page on API keys and where to find them',
    'Screenshots still show the old theme',
    'Explain the difference between drafts and previews',
    'Search finds nothing for words with accents',
    'Shorter intro on the home page',
  ],
  'example-org/pixel-kit': [
    'Arrow icons look blurry at 16 pixels',
    'Add a calendar icon',
    'Icon names in the README are out of date',
    'Export the set as one SVG sprite',
    'Stroke width differs between 16 and 24 pixels',
    'A dark-background preview page',
  ],
};
const LABELS = [['bug'], ['enhancement'], [], ['docs', 'good first issue'], ['bug', 'needs triage'], []];
const MILESTONES = ['0.5.0', '0.5.0', 'Later', '', '0.5.0', 'Later'];

function issueURL(p: Project, n: number): string {
  return p.repoForge === 'gitlab' ? `https://${p.repoHost}/${p.repo}/-/issues/${n}` : `https://github.com/${p.repo}/issues/${n}`;
}

/** The repositories of the scenario, each once, in the server's order. */
function reposOf(projects: readonly Project[]): Project[] {
  const seen = new Set<string>();
  const out: Project[] = [];
  for (const p of projects) {
    if (!p.repo || !p.repoForge) continue;
    const k = `${p.repoForge}|${p.repoHost}|${p.repo}`;
    if (seen.has(k)) continue;
    seen.add(k);
    out.push(p);
  }
  return out.sort((a, b) => `${a.repoForge}|${a.repoHost}|${a.repo}`.localeCompare(`${b.repoForge}|${b.repoHost}|${b.repo}`));
}

function repoList(c: IssuesCase, p: Project, i: number, now: number): RepoIssues {
  const base: RepoIssues = { forge: p.repoForge, host: p.repoHost, repo: p.repo, state: 'ok', open: 0, issues: [] };
  if (c === 'loading') return { ...base, state: 'pending' };
  if (c === 'none') return base;
  const listed = c === 'many' ? 30 : c === 'milestones' ? 16 : Math.max(2, 4 - i);
  const open = c === 'many' ? 47 - i * 9 : listed + (i === 0 ? 8 : 0);
  const due = dueIn(now, 14);
  const titles = (p.repoForge === 'github' && STORY[p.repo]) || TITLES;
  const issues: OpenIssue[] = [];
  for (let k = 0; k < listed; k++) {
    const n = 148 - k * 3 + i * 200; // above zero for every case: at most 30 listed
    // milestones: a milestone per issue, to see the filter wrap on a phone.
    const m = c === 'milestones' ? `Sample sprint ${k + 1}` : MILESTONES[k % MILESTONES.length];
    const is: OpenIssue = {
      number: n,
      title: titles[k % titles.length],
      url: issueURL(p, n),
      milestone: m,
      labels: LABELS[k % LABELS.length],
      updatedAt: at(now, (k + 1) * 5 * HOUR),
    };
    if (m === '0.5.0') is.milestoneDue = due;
    issues.push(is);
  }
  return { ...base, open, issues };
}

function list(c: IssuesCase, projects: readonly Project[], now: number): Issues {
  if (c === 'off') return { state: 'off', repos: [], rev: 1, freshMs: 0 };
  return {
    state: c === 'loading' ? 'loading' : 'ready',
    ...(c === 'loading' ? {} : { asOf: at(now, 3 * 60_000) }),
    repos: reposOf(projects).map((p, i) => repoList(c, p, i, now)),
    rev: 1, // as the summary's (issuesSnapshot)
    freshMs: 60_000,
  };
}

/** Sets the projects' counts and the header's summary as the server would for this case. */
export function issuesSnapshot(c: IssuesCase, s: Snapshot, now = Date.now()): void {
  const l = list(c, s.projects, now);
  const by = new Map(l.repos.map((r) => [`${r.forge}|${r.host}|${r.repo}`, r]));
  let open = 0;
  for (const r of l.repos) if (r.state === 'ok') open += r.open;
  for (const p of s.projects) {
    const r = p.repo ? by.get(`${p.repoForge}|${p.repoHost}|${p.repo}`) : undefined;
    p.openIssues = r?.state === 'ok' ? { known: true, open: r.open } : { known: false, open: 0 };
  }
  const sum: IssuesSummary = { state: l.state, open, rev: 1 };
  if (l.asOf) sum.asOf = l.asOf;
  s.issues = sum;
}

export function issuesClient(c: IssuesCase, projects: readonly Project[]): IssuesClient {
  return {
    async load() {
      await new Promise((r) => setTimeout(r, 250));
      if (c === 'down') return { ok: false, kind: 'failed', message: 'sitboard did not answer' };
      return { ok: true, issues: list(c, projects, Date.now()) };
    },
  };
}
