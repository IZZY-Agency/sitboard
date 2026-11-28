// Dev harness only: placeholder open-issue lists for the sample projects of
// the scenario shown, and the snapshot's counts to match. Never part of the
// build.
import type { Card, Issues, IssuesSummary, OpenIssue, Project, RepoIssues, Snapshot } from '../src/contract.gen';
import { readIssues, type IssuesClient, type ProjectNoAgent } from '../src/data/issues';

/**
 * A sample project's issues key, as the dev server serves it: its forge,
 * host and path exactly (the real server folds by its own rule; the page
 * compares the key whole either way).
 */
export function sampleIssuesKey(p: Pick<Project, 'repo' | 'repoForge' | 'repoHost'>): string {
  return p.repo && p.repoForge ? `${p.repoForge}|${p.repoHost}|${p.repo}` : '';
}

export const ISSUES = ['few', 'many', 'milestones', 'unread', 'none', 'loading', 'off', 'down'] as const;
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
// The site and gitlab scenarios' sample repositories (web/dev/scenarios.ts),
// by name on either forge, get titles that fit their story, for the
// website's pictures; every other repository gets the placeholders above.
// Same order as LABELS and MILESTONES.
const STORY: Record<string, string[]> = {
  harbor: [
    'Checkout loses the saved address after a failed payment',
    'Remember the last delivery slot',
    'Order history loads slowly with many orders',
    'Document the session refresh for the mobile app, with an example for each platform',
    'Retry button stays disabled after a timeout',
    'Empty basket has no way back to the shop',
  ],
  'lumen-docs': [
    'Broken link in the getting-started guide',
    'A page on API keys and where to find them',
    'Screenshots still show the old theme',
    'Explain the difference between drafts and previews',
    'Search finds nothing for words with accents',
    'Shorter intro on the home page',
  ],
  'pixel-kit': [
    'Arrow icons look blurry at 16 pixels',
    'Add a calendar icon',
    'Icon names in the README are out of date',
    'Export the set as one SVG sprite',
    'Stroke width differs between 16 and 24 pixels',
    'A dark-background preview page',
  ],
  'harbor-api': [
    'Refresh token expires during a long checkout',
    'Rate limit the address lookup',
    'Payment webhook retries twice on a timeout',
    'Describe the error codes of the orders endpoint',
    'Idempotency key ignored on card retries',
    'Health check reports the old version',
  ],
};
const LABELS = [['bug'], ['enhancement'], [], ['docs', 'good first issue'], ['bug', 'needs triage'], []];
// A release-neutral sample milestone: the website's pictures sit beside its
// version badge, so no sample milestone reads like a sitboard version.
const SOON = 'Soon';
const MILESTONES = [SOON, SOON, 'Later', '', SOON, 'Later'];

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

// unread: the answers that list nothing, in turn after a first repository
// that answered: issues off, not asked yet (or no answer: an error, a rate
// limit, over the cap of one look), not found (or not visible to the login).
// In the gitlab scenario the last two fall on its GitLab repositories.
const UNREAD = ['disabled', 'pending', 'notFound'] as const;

function repoList(c: IssuesCase, p: Project, i: number, now: number): RepoIssues {
  const base: RepoIssues = { forge: p.repoForge, host: p.repoHost, repo: p.repo, key: sampleIssuesKey(p), state: 'ok', open: 0, issues: [], pinned: [] };
  if (c === 'loading') return { ...base, state: 'pending' };
  if (c === 'unread' && i > 0) return { ...base, state: UNREAD[(i - 1) % UNREAD.length] };
  if (c === 'none') return base;
  const listed = c === 'many' ? 30 : c === 'milestones' ? 16 : c === 'unread' ? 3 : Math.max(2, 4 - i);
  const open = c === 'many' ? listed + 17 - i * 4 : listed + (i === 0 ? 8 : 0); // many: more than listed in every repository
  const due = dueIn(now, 14);
  const titles = STORY[p.repo.slice(p.repo.lastIndexOf('/') + 1)] ?? TITLES;
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
      agents: [],
      pinned: false,
    };
    if (m === SOON) is.milestoneDue = due;
    issues.push(is);
  }
  return { ...base, open, issues };
}

function list(c: IssuesCase, projects: readonly Project[], now: number): Issues {
  if (c === 'off') return { state: 'off', repos: [], rev: 1, freshMs: 0, unsaved: false };
  return {
    state: c === 'loading' ? 'loading' : 'ready',
    ...(c === 'loading' ? {} : { asOf: at(now, 3 * 60_000) }),
    repos: reposOf(projects).map((p, i) => repoList(c, p, i, now)),
    rev: 1, // as the summary's (issuesSnapshot)
    freshMs: 60_000,
    unsaved: false,
  };
}

/** Sets the projects' counts and the header's summary as the server would for this case. */
export function issuesSnapshot(c: IssuesCase, s: Snapshot, now = Date.now()): void {
  const l = list(c, s.projects, now);
  const by = new Map(l.repos.map((r) => [`${r.forge}|${r.host}|${r.repo}`, r]));
  let open = 0;
  for (const r of l.repos) if (r.state === 'ok') open += r.open;
  for (const p of s.projects) {
    p.issuesKey = sampleIssuesKey(p);
    const r = p.repo ? by.get(`${p.repoForge}|${p.repoHost}|${p.repo}`) : undefined;
    p.openIssues = r?.state === 'ok' ? { known: true, open: r.open } : { known: false, open: 0 };
  }
  const sum: IssuesSummary = { state: l.state, open, rev: 1, noAgent: 0 };
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

// ---- 0.6.0 stream 4: sample agent links and pins --------------------------
// Kept apart from the cases above so other changes to this file merge
// cleanly. Links come from the scenario's cards as the server builds them:
// every issue a card names ("closes" or "looked") that the list has gets
// that card as an agent; the first repository's third issue also names a
// card that is not on the board (no tag). Pins: the first repository's
// fourth issue (its last when fewer are listed; in the site scenario the one
// a card looked at, so the website's Issues picture shows a "looked at" tag
// under Pinned), plus pinned rows outside the listed ones (numbers no listed
// row has): in the first repository one open (with the board's first card
// on it) and one closed since it was pinned; in the second one known only
// by its number (no title, no state: GitLab's fallback). ?unsaved=1 serves
// them as unsaved; ?agents=unjudged serves every agents field as null (a
// server that has not judged them), and no counts.

type PinRef = { forge: Card['issues'][number]['forge']; host: string; repo: string; number: number };
const pinKeyOf = (r: { forge: string; host: string; repo: string }, n: number) => `${r.forge}|${r.host}|${r.repo.toLowerCase()}#${n}`;

/** The owner's pins in the harness: pinKey -> pinned, starting from the samples. */
const pins = new Map<string, boolean>();
let pinRev = 0;

type Outside = { title: string; state?: OpenIssue['state']; onIt?: boolean };
const OUTSIDE: Record<number, Outside[]> = {
  0: [
    { title: 'Sample pinned issue: gift cards at checkout', state: 'open', onIt: true },
    { title: 'Sample pinned issue: currency on receipts', state: 'closed' },
  ],
  1: [{ title: '' }],
};

/** n numbers no listed row of the repository has, from 23 down. */
function freeNumbers(listed: readonly OpenIssue[], n: number): number[] {
  const taken = new Set(listed.map((i) => i.number));
  const out: number[] = [];
  for (let k = 23; k >= 1 && out.length < n; k--) if (!taken.has(k)) out.push(k);
  return out;
}

/** The case's list before links and pins (for tests). */
export function sampleList(c: IssuesCase, s: Snapshot): Issues {
  return list(c, s.projects, Date.now());
}

export type SampleOpts = { unsaved?: boolean; unjudged?: boolean };

/** The list as the server would send it (JSON), before the page reads it. */
export function sampleRawList(l: Issues, cards: readonly Card[], o: SampleOpts = {}): Issues {
  const out: Issues = JSON.parse(JSON.stringify(l));
  out.unsaved = !!o.unsaved;
  out.rev += pinRev;
  out.repos.forEach((r, i) => {
    if (r.state !== 'ok') return;
    r.issues.forEach((is, k) => {
      for (const c of cards)
        for (const ci of c.issues)
          if (pinKeyOf(ci, ci.number) === pinKeyOf(r, is.number) && !is.agents.some((a) => a.cardId === c.id))
            is.agents.push({ cardId: c.id, kind: ci.kind });
      if (i === 0 && k === 2) is.agents.push({ cardId: 'claude:sample-not-on-board', kind: 'looked' });
      const p = pins.get(pinKeyOf(r, is.number));
      is.pinned = p ?? (i === 0 && k === Math.min(3, r.issues.length - 1));
    });
    const outside = OUTSIDE[i] ?? [];
    const nums = freeNumbers(r.issues, outside.length);
    r.pinned = outside
      .map((o, k) => ({ o, number: nums[k] }))
      .filter(({ number }) => number !== undefined && pins.get(pinKeyOf(r, number)) !== false)
      .map(({ o, number }) => ({
        number,
        title: o.title,
        url: r.forge === 'gitlab' ? `https://${r.host}/${r.repo}/-/issues/${number}` : `https://github.com/${r.repo}/issues/${number}`,
        milestone: '',
        labels: [],
        agents: o.onIt && cards[0] ? [{ cardId: cards[0].id, kind: 'closes' as const }] : [],
        pinned: true,
        ...(o.state ? { state: o.state } : {}),
      }));
    if (o.unjudged) for (const is of [...r.issues, ...r.pinned]) (is as { agents: unknown }).agents = null;
  });
  return out;
}

const withLinks = (l: Issues, cards: readonly Card[], o: SampleOpts = {}): Issues => readIssues(sampleRawList(l, cards, o)) ?? l;

/** The summary's count of listed issues no agent is on or looked at, as the server counts it. */
export function sampleNoAgent(c: IssuesCase, s: Snapshot): number {
  const l = withLinks(list(c, s.projects, Date.now()), s.cards);
  let n = 0;
  const on = new Set(s.cards.map((c) => c.id));
  for (const r of l.repos) if (r.state === 'ok') for (const is of r.issues) if (!is.agents.some((a) => on.has(a.cardId))) n++;
  return n;
}

/**
 * Each project's Project.issuesNoAgent, as the core will serve it: the
 * listed issues of its repository (matched by its issues key) that no card on
 * the board is on or looked at; absent for a repository with no answer.
 */
export function sampleProjectNoAgent(c: IssuesCase, s: Snapshot): void {
  const l = withLinks(list(c, s.projects, Date.now()), s.cards);
  const on = new Set(s.cards.map((x) => x.id));
  const by = new Map(l.repos.map((r) => [r.key, r]));
  for (const p of s.projects as ProjectNoAgent[]) {
    const r = p.issuesKey ? by.get(p.issuesKey) : undefined;
    if (r?.state === 'ok') p.issuesNoAgent = r.issues.filter((is) => !is.agents.some((a) => on.has(a.cardId))).length;
    else delete p.issuesNoAgent;
  }
}

/** issuesClient with the sample links and pins, read as the page reads the server's answer. */
export function linkedIssuesClient(c: IssuesCase, s: () => Snapshot | null, o: SampleOpts = {}): IssuesClient {
  return {
    async load() {
      await new Promise((r) => setTimeout(r, 250));
      if (c === 'down') return { ok: false, kind: 'failed', message: 'sitboard did not answer' };
      const snap = s();
      return { ok: true, issues: withLinks(list(c, snap?.projects ?? [], Date.now()), snap?.cards ?? [], o) };
    },
  };
}

/**
 * The harness's pin route: keeps the pin and bumps the list's rev (so an
 * open panel reads it again); ?fail=pin refuses it, to see the rollback.
 */
export function samplePin(req: PinRef & { pinned: boolean }, fail: boolean): { ok: true } | { ok: false; kind: 'failed'; message: string } {
  if (fail) return { ok: false, kind: 'failed', message: 'sitboard could not save it' };
  pins.set(pinKeyOf(req, req.number), req.pinned);
  pinRev++;
  return { ok: true };
}

/** The rev the snapshot announces after the harness's pins: the list's own plus each pin. */
export function sampleRev(): number {
  return 1 + pinRev;
}
// ---- end of 0.6.0 stream 4 block -------------------------------------------
