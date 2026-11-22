// Dev harness only: placeholder data from the contract's golden snapshot and
// generated placeholders. Never part of the build (scripts/check-dist.mjs).
import goldenJson from '../../internal/contract/golden/snapshot.v1.json';
import type { Card, Snapshot, Spend } from '../src/contract.gen';
import { addSpend, addTokens, localDay, noTokens } from '../src/data/spend';

export const SCENARIOS = [
  'golden',
  'spend',
  'spend-unpriced',
  'spend-pending',
  'settled',
  'crowd',
  'effort',
  'codex',
  'projects',
  'forty',
  'one',
  'site',
  'gitlab',
  'grouping',
  'settings200',
  'hostile',
  'empty',
  'scanning',
  'noagterm',
  'loading',
  'unreachable',
  'reconnecting',
  'unauthorized',
  'outdated',
] as const;
export type Scenario = (typeof SCENARIOS)[number];

const clone = <T>(v: T): T => JSON.parse(JSON.stringify(v)) as T;

/** Moves every time in the golden snapshot so it reads as "now". */
function fresh(s: Snapshot): Snapshot {
  const delta = Date.now() - Date.parse(s.generatedAt);
  const shift = (t?: string) => (t ? new Date(Date.parse(t) + delta).toISOString() : t);
  // Spend days move with it: the golden's last day becomes today.
  const days = Math.round((Date.parse(`${localDay(Date.now())}T12:00:00`) - Date.parse(`${s.generatedAt.slice(0, 10)}T12:00:00`)) / 86_400_000);
  for (const d of s.spend.days) d.day = localDay(Date.parse(`${d.day}T12:00:00`) + days * 86_400_000);
  s.generatedAt = new Date().toISOString();
  for (const c of s.cards) {
    c.agent.statusSince = shift(c.agent.statusSince);
    c.agent.startedAt = shift(c.agent.startedAt);
    c.agent.lastActivity = shift(c.agent.lastActivity);
  }
  return s;
}

// Project grouping: a project can be a part of another (parentId) and sits in
// an area (the folder above it).
function group(s: Snapshot, id: string, area: string, parentId = ''): void {
  const p = s.projects.find((x) => x.id === id);
  if (p) Object.assign(p, { area, parentId });
}
function addProject(s: Snapshot, id: string, name: string, area = '', parentId = ''): void {
  s.projects.push({ id, name, path: `~/projects/${name}`, repo: '', repoForge: '', repoHost: '', counts: { working: 0, needsYou: 0, inReview: 0, stale: 0 }, area, parentId });
}

export function golden(): Snapshot {
  const s = fresh(clone(goldenJson as Snapshot));
  // The first project has a part, and both projects share an area.
  const [a, b] = s.projects;
  group(s, a.id, 'area-one');
  if (b) group(s, b.id, 'area-one');
  addProject(s, 'p_0000000000a1', 'alpha-web', '', a.id);
  for (const c of s.cards.filter((x) => x.projectId === a.id).slice(1, 3)) c.projectId = 'p_0000000000a1';
  return s;
}

/**
 * Seven days of spend for the panel's pictures: three projects, one day with
 * use that has no price next to use that has ("mixed"), or none priced at all
 * ("unpriced"); "pending" is the mixed week while older sessions are read.
 */
function spendWeek(kind: 'mixed' | 'unpriced' | 'pending'): Snapshot {
  const s = golden();
  const ids = [s.projects[0], s.projects[1], s.projects.find((p) => p.id === 'p_0000000000a1')!];
  const one = (out: number, usd: number, priced: boolean): Spend => ({
    tokens: { input: Math.round(out / 20), output: out, cacheRead: out * 90, cacheWrite: out * 8 },
    usd: priced ? usd : kind === 'unpriced' ? 0 : usd,
    priced,
    recorded: false,
  });
  const zero = (): Spend => ({ tokens: { input: 0, output: 0, cacheRead: 0, cacheWrite: 0 }, usd: 0, priced: true, recorded: false });
  s.spend.days = [];
  for (let back = 6; back >= 0; back--) {
    const day = localDay(Date.now() - back * 86_400_000);
    const per = ids.map((p, i) => {
      if (back > 3 && i === 1) return null; // no use that day
      const out = 40_000 + ((back * 37 + i * 91) % 9) * 17_000;
      const unpriced = kind === 'unpriced' || (i === 1 && back % 2 === 0) || (i === 0 && back === 0);
      const partsPriced = kind !== 'unpriced' && i === 0 && back === 0; // some of it priced: a "+ unpriced" total
      // Codex use (tokens only) in the first project on the last three days.
      const cx = i === 0 && back <= 2 ? 12_000 + back * 9_000 : 0;
      const codex = { input: Math.round(cx / 10), output: cx, cacheRead: cx * 60, cacheWrite: 0 };
      return {
        projectId: p.id,
        name: p.name,
        path: p.path,
        spend: unpriced ? (partsPriced ? one(out, 8.75, false) : one(out, 0, false)) : one(out, out / 11_000, true),
        codex,
      };
    });
    const projects = per.filter((x) => x !== null);
    s.spend.days.push({
      day,
      spend: projects.reduce((t, x) => addSpend(t, x.spend), zero()),
      codex: projects.reduce((t, x) => addTokens(t, x.codex), noTokens()),
      projects,
    });
  }
  s.spend.pending = kind === 'pending';
  // The cards agree with the panel: some use unpriced everywhere, or in one card only.
  for (const [i, c] of s.cards.entries()) {
    if (kind === 'unpriced') c.spend = { ...c.spend, usd: 0, priced: false };
    else if (i === 0) c.spend = { ...c.spend, priced: false };
  }
  return s;
}

function ago(min: number): string {
  return new Date(Date.now() - min * 60_000).toISOString();
}

function crowd(): Snapshot {
  const s = golden();
  const base = s.cards[1];
  const projects = ['gamma-monorepo', 'delta-dashboard', 'epsilon-tools'].map((name, i) => ({
    id: `p_00000000000${i + 3}`,
    name,
    path: `~/projects/${name}`,
    repo: '',
    repoForge: '' as const,
    repoHost: '',
    parentId: '',
    area: 'projects',
    counts: { working: 0, needsYou: 0, inReview: 0, stale: 0 },
  }));
  s.projects.push(...projects);
  group(s, projects[0].id, 'area-one');
  group(s, projects[1].id, 'area-two');
  const cols: Card['column'][] = ['working', 'working', 'idle', 'with_coordinator', 'in_review', 'idle', 'working', 'done'];
  for (let i = 0; i < 14; i++) {
    const c = clone(base);
    c.id = `claude:placeholder-${i}`;
    c.projectId = i < 6 ? s.projects[0].id : projects[i % 3].id;
    c.column = cols[i % cols.length];
    c.attention = i === 3 ? ['stale'] : [];
    c.agent.name = `worker-${i + 1}`;
    c.title = i % 2 ? `Placeholder task number ${i + 1} with a longer title that wraps onto two lines` : c.agent.name;
    c.titleSource = i % 2 ? 'pr' : 'session';
    c.agent.status = c.column === 'working' ? 'busy' : 'finished';
    c.agent.statusSince = ago(3 + i * 47);
    c.agent.lastActivity = ago(1 + i);
    c.agent.parent = i < 6 ? 'known' : 'none';
    c.agent.parentCardId = i < 6 ? s.cards[0].id : '';
    // Every model family, a model that is not Claude's and an unknown one, for the chip colours.
    c.agent.model = ['claude-fable-5-2', 'claude-opus-5-5', 'claude-sonnet-5-5', 'claude-haiku-4-5-20251001', 'gpt-5.4-codex', ''][i % 6];
    // Every effort level, one set at launch (on the unknown-model chip, as before a first reply) and one unknown.
    const effort = (['xhigh', 'max', 'medium', 'low', '', 'high', 'high'] as const)[i % 7];
    const claude = c.agent.model === '' || c.agent.model.startsWith('claude-');
    c.agent.effort = claude ? effort : '';
    c.agent.effortSource = !claude || effort === '' ? 'none' : c.agent.model === '' ? 'launch' : 'recorded';
    // A Codex model with an effort word that has no place on the ramp.
    c.agent.effortWord = claude ? '' : 'ultra';
    c.pullRequests =
      c.column === 'in_review' || c.column === 'done' || i % 4 === 1
        ? [
            {
              forge: 'github' as const, host: 'github.com', repo: 'placeholder/repo',
              number: 200 + i,
              title: 'Placeholder',
              url: `https://github.com/placeholder/repo/pull/${200 + i}`,
              state: c.column === 'done' ? 'merged' : 'open',
              checks: i % 3 === 0 ? 'running' : 'passing',
              review: '',
              draft: false,
            },
          ]
        : [];
    s.cards.push(c);
  }
  // A long-running coordinator with many merged pull requests. All merged:
  // the card is an unseen turn (rule 5), which an open pull request would
  // have sent to In review (rule 4) instead.
  s.cards[0].pullRequests = Array.from({ length: 26 }, (_, i) => ({
    forge: 'github' as const, host: 'github.com', repo: 'placeholder/repo',
    number: 400 - i,
    title: 'Placeholder',
    url: `https://github.com/placeholder/repo/pull/${400 - i}`,
    state: 'merged' as const,
    checks: 'passing' as const,
    review: '' as const,
    draft: false,
  }));
  // One worker lives in another repo, so the team spans two projects.
  s.cards[2].projectId = s.projects[1].id;
  s.counts.working = s.cards.filter((c) => c.column === 'working').length;
  return s;
}

/** Twelve projects, so the project filter wraps. */
function manyProjects(): Snapshot {
  const s = crowd();
  const names = ['zeta-platform', 'eta-service-api', 'theta-website', 'iota-web-client', 'kappa-mobile', 'lambda-docs-site', 'mu-infrastructure'];
  for (const [i, name] of names.entries()) {
    const id = `p_0000000001${String(i).padStart(2, '0')}`;
    addProject(s, id, name, i < 4 ? 'area-two' : i < 6 ? 'area-three' : '', i === 6 ? s.projects[0].id : '');
    const c = clone(s.cards[1]);
    c.id = `claude:placeholder-p${i}`;
    c.projectId = id;
    c.agent.name = `${name}-agent`;
    c.title = c.agent.name;
    c.agent.parent = 'none';
    c.agent.parentCardId = '';
    c.attention = [];
    s.cards.push(c);
  }
  return s;
}

/** Forty projects: the tiles band stops wrapping after two rows and the rail scrolls. */
function forty(): Snapshot {
  const s = golden();
  const base = s.cards[1];
  for (let i = 0; i < 40; i++) {
    const id = `p_0000000002${String(i).padStart(2, '0')}`;
    const name = `placeholder-project-${String(i + 1).padStart(2, '0')}`;
    addProject(s, id, name, `area-${(i % 5) + 1}`);
    const c = clone(base);
    c.id = `claude:placeholder-f${i}`;
    c.projectId = id;
    c.agent.name = `${name}-agent`;
    c.title = c.agent.name;
    c.agent.parent = 'none';
    c.agent.parentCardId = '';
    c.column = i % 3 === 0 ? 'idle' : i % 4 === 0 ? 'in_review' : 'working';
    c.attention = [];
    s.cards.push(c);
  }
  return s;
}

/** One project: the band still shows "all projects", the overview. */
function one(): Snapshot {
  const s = golden();
  const keep = new Set([s.projects[0].id, 'p_0000000000a1']);
  s.cards = s.cards.filter((c) => keep.has(c.projectId));
  s.projects = s.projects.filter((p) => keep.has(p.id));
  return s;
}

/**
 * For the website's pictures: invented names only. A coordinator and its
 * workers in a project with two parts, every column in use, pull request
 * lines, artifact links, and one card that needs you.
 */
function site(): Snapshot {
  const s = golden();
  const base = s.cards[1];
  s.groupingSettings = []; // the website's pictures show no grouping settings
  // GitHub only, as the server sends it: no merge request, so no GitLab host,
  // and no GitLab source line (the server does not report one yet).
  s.gitlabHosts = [];
  s.sources = s.sources.filter((x) => x.kind !== 'gitlab');
  s.projects = [];
  addProject(s, 'p_site00000001', 'harbor', 'studio');
  addProject(s, 'p_site00000002', 'harbor-web', '', 'p_site00000001');
  addProject(s, 'p_site00000003', 'harbor-api', '', 'p_site00000001');
  addProject(s, 'p_site00000004', 'lumen-docs', 'studio');
  addProject(s, 'p_site00000005', 'pixel-kit', '');
  const repo = 'example-org/harbor';
  const pr = (number: number, state: 'open' | 'merged', checks: 'passing' | 'running' | 'failing' | 'none' = 'passing') => ({
    forge: 'github' as const, host: 'github.com', repo,
    number,
    title: 'Placeholder',
    url: `https://github.com/${repo}/pull/${number}`,
    state,
    checks,
    review: '' as const,
    draft: false,
  });
  const lead = 'claude:site-lead';
  type Spec = {
    id: string;
    projectId: string;
    name: string;
    title: string;
    column: Card['column'];
    status: Card['agent']['status'];
    since: number;
    model?: string;
    effort?: Card['agent']['effort'];
    lead?: boolean;
    attention?: Card['attention'];
    prs?: Card['pullRequests'];
    issues?: number[];
    arts?: string[];
  };
  let artN = 0;
  const art = (title: string) => ({ title, url: `https://claude.ai/artifact/exampleArtifact${String(++artN).padStart(4, '0')}` });
  const specs: Spec[] = [
    { id: lead, projectId: 'p_site00000001', name: 'harbor-lead', title: 'Checkout rewrite: plan, split and review', column: 'working', status: 'busy', since: 12, model: 'claude-fable-5-2', effort: 'xhigh', prs: [pr(214, 'merged')], arts: ['Checkout plan'] },
    { id: 'claude:site-w1', projectId: 'p_site00000002', name: 'web-checkout', title: 'One-page checkout with saved addresses', column: 'working', status: 'busy', since: 4, effort: 'high', lead: true, prs: [pr(221, 'open', 'running')] },
    { id: 'claude:site-w2', projectId: 'p_site00000003', name: 'api-auth', title: 'Session refresh for long-lived tokens', column: 'needs_you', status: 'blocked', since: 2, effort: 'max', lead: true, attention: ['blocked'] },
    { id: 'claude:site-w3', projectId: 'p_site00000002', name: 'web-search', title: 'Search suggestions as you type', column: 'with_coordinator', status: 'finished', since: 18, model: 'claude-sonnet-5-5', effort: 'medium', lead: true },
    { id: 'claude:site-w4', projectId: 'p_site00000003', name: 'api-payments', title: 'Retry failed card payments once', column: 'in_review', status: 'finished', since: 35, model: 'claude-sonnet-5-5', effort: 'high', lead: true, prs: [pr(219, 'open', 'passing')], issues: [88], arts: ['Payment retry flow'] },
    { id: 'claude:site-w5', projectId: 'p_site00000002', name: 'web-a11y', title: 'Keyboard paths through the cart', column: 'done', status: 'finished', since: 95, effort: 'medium', lead: true, prs: [pr(216, 'merged')] },
    { id: 'claude:site-d1', projectId: 'p_site00000004', name: 'docs-refresh', title: 'Rewrite the getting-started guide', column: 'in_review', status: 'finished', since: 50, model: 'claude-haiku-4-5-20251001', effort: 'low', attention: ['failing_checks'], prs: [{ ...pr(41, 'open', 'failing'), repo: 'example-org/lumen-docs', url: 'https://github.com/example-org/lumen-docs/pull/41' }] },
    { id: 'claude:site-k1', projectId: 'p_site00000005', name: 'pixel-kit-icons', title: 'Icon set at 16 and 24 pixels', column: 'idle', status: 'finished', since: 300 },
  ];
  s.cards = specs.map((x) => {
    const c = clone(base);
    c.id = x.id;
    c.projectId = x.projectId;
    c.title = x.title;
    c.titleSource = x.prs?.length ? 'pr' : 'session';
    c.column = x.column;
    c.attention = x.attention ?? [];
    c.seen = true;
    c.subagents = x.id === lead ? 3 : 0;
    c.pullRequests = x.prs ?? [];
    c.artifacts = (x.arts ?? []).map(art);
    c.issues = (x.issues ?? []).map((n) => ({ forge: 'github' as const, host: 'github.com', repo, number: n, url: `https://github.com/${repo}/issues/${n}` }));
    c.agent.name = x.name;
    c.agent.model = x.model ?? 'claude-opus-5-5';
    c.agent.effort = x.effort ?? '';
    c.agent.effortSource = x.effort ? 'recorded' : 'none';
    c.agent.status = x.status;
    c.agent.statusSince = ago(x.since);
    c.agent.startedAt = ago(x.since + 120);
    c.agent.lastActivity = ago(Math.max(1, x.since - 1));
    c.agent.parent = x.lead ? 'known' : 'none';
    c.agent.parentCardId = x.lead ? lead : '';
    c.agent.folder = `~/studio/${x.name}`;
    c.agent.workspace = 'studio';
    return c;
  });
  // The project counts the way the server counts them.
  for (const p of s.projects) {
    const mine = s.cards.filter((c) => c.projectId === p.id);
    p.counts = {
      working: mine.filter((c) => c.column === 'working').length,
      needsYou: mine.filter((c) => c.column === 'needs_you').length,
      inReview: mine.filter((c) => c.column === 'in_review').length,
      stale: mine.filter((c) => c.attention.includes('stale')).length,
    };
  }
  s.counts = {
    working: s.cards.filter((c) => c.column === 'working').length,
    needsYou: s.cards.filter((c) => c.column === 'needs_you').length,
    inReview: s.cards.filter((c) => c.column === 'in_review').length,
    stale: 0,
  };
  return s;
}

/** The sample GitLab host and group of the "gitlab" scenario (invented). */
export const SAMPLE_GITLAB_HOST = 'gitlab.example';
export const SAMPLE_GITLAB_PATH = 'example-group/harbor-api';
/** Its merge requests' titles, shared with the "gitlab" catch-up list. */
export const SAMPLE_MR_TITLES: Record<number, string> = {
  27: 'Refresh tokens without a second login',
  29: 'Session refresh for long-lived tokens',
  31: 'Retry failed card payments once',
};

/**
 * The "site" board with its API part on a GitLab host: merge requests beside
 * pull requests, one waiting for an approval, one merged, an issue it closes,
 * and the GitLab source read through glab. Invented names only.
 */
function gitlabScenario(): Snapshot {
  const s = site();
  const host = SAMPLE_GITLAB_HOST;
  const path = SAMPLE_GITLAB_PATH;
  const api = s.projects.find((p) => p.id === 'p_site00000003')!;
  Object.assign(api, { repo: path, repoForge: 'gitlab', repoHost: host });
  const mr = (number: number, state: 'open' | 'merged', review: Card['pullRequests'][number]['review'], checks: Card['pullRequests'][number]['checks'] = 'passing') => ({
    forge: 'gitlab' as const, host, repo: path,
    number,
    title: SAMPLE_MR_TITLES[number] ?? 'Placeholder',
    url: `https://${host}/${path}/-/merge_requests/${number}`,
    state,
    checks,
    review,
    draft: false,
  });
  const byId = (id: string) => s.cards.find((c) => c.id === id)!;
  const payments = byId('claude:site-w4');
  payments.pullRequests = [mr(31, 'open', 'review_required')];
  payments.issues = [{ forge: 'gitlab', host, repo: path, number: 12, url: `https://${host}/${path}/-/issues/12` }];
  const auth = byId('claude:site-w2');
  auth.pullRequests = [mr(29, 'open', 'approved', 'running'), mr(27, 'merged', 'approved')];
  auth.titleSource = 'pr';
  s.gitlabHosts = [host];
  s.sources = [...s.sources, { kind: 'gitlab', state: 'ok', version: '1.46.0', detail: '', tested: true, ready: true }];
  return s;
}

/**
 * Every model family at every effort level, one column per family, and the
 * unknown-model chip with a launch level and with none, beside a needs-you
 * card at max, for the effort ramp.
 */
function effort(): Snapshot {
  const s = golden();
  const base = s.cards[1];
  const levels = ['low', 'medium', 'high', 'xhigh', 'max'] as const;
  const families: Array<[string, Card['column']]> = [
    ['claude-fable-5-2', 'working'],
    ['claude-opus-5-5', 'with_coordinator'],
    ['claude-sonnet-5-5', 'in_review'],
    ['claude-haiku-5-5', 'idle'],
  ];
  const lead = s.cards[0];
  s.cards = [lead];
  lead.agent.effort = 'xhigh';
  lead.agent.effortSource = 'recorded';
  const add = (name: string, model: string, column: Card['column'], effort: Card['agent']['effort'], src: Card['agent']['effortSource'], i: number) => {
    const c = clone(base);
    c.id = `claude:effort-${name}`;
    c.projectId = s.projects[0].id;
    c.column = column;
    c.attention = [];
    c.pullRequests = [];
    c.artifacts = [];
    c.issues = [];
    c.subagents = 0;
    c.title = `Sample task at ${effort || 'no'} effort`;
    c.titleSource = 'session';
    c.agent.name = name;
    c.agent.model = model;
    c.agent.effort = effort;
    c.agent.effortSource = src;
    c.agent.status = column === 'working' ? 'busy' : column === 'needs_you' ? 'blocked' : 'finished';
    c.agent.statusSince = ago(2 + i);
    c.agent.lastActivity = ago(1 + i);
    c.agent.parent = 'known';
    c.agent.parentCardId = lead.id;
    s.cards.push(c);
  };
  for (const [f, [model, column]] of families.entries()) {
    for (const [l, level] of levels.entries()) add(`${model.split('-')[1]}-${level}`, model, column, level, 'recorded', f * 5 + l);
  }
  add('new-session', '', 'needs_you', 'high', 'launch', 30);
  add('no-level', '', 'needs_you', '', 'none', 31);
  add('needs-max', 'claude-opus-5-5', 'needs_you', 'max', 'recorded', 32);
  s.cards[s.cards.length - 1].attention = ['blocked'];
  for (const p of s.projects) {
    const mine = s.cards.filter((c) => c.projectId === p.id);
    p.counts = {
      working: mine.filter((c) => c.column === 'working').length,
      needsYou: mine.filter((c) => c.column === 'needs_you').length,
      inReview: mine.filter((c) => c.column === 'in_review').length,
      stale: 0,
    };
  }
  s.counts = {
    working: s.cards.filter((c) => c.column === 'working').length,
    needsYou: s.cards.filter((c) => c.column === 'needs_you').length,
    inReview: s.cards.filter((c) => c.column === 'in_review').length,
    stale: 0,
  };
  return s;
}

/**
 * Codex sessions among Claude Code ones, sample names only: a terminal
 * session at work with an effort word off the ramp ("ultra"), one that
 * finished with a title the model wrote, one blocked (known through agterm),
 * one with no title (the card takes the folder's name), a `codex exec` run
 * with a pull request, and a known level. The Codex source line says what the
 * adapter says.
 */
function codexScenario(): Snapshot {
  const s = golden();
  const claude = s.cards[1];
  s.sources = s.sources.map((x) => (x.kind === 'codex' ? { ...x, version: '0.171.0', detail: 'approval waits show only through agterm', ready: true } : { ...x, ready: true }));
  const project = s.projects[0].id;
  type Spec = {
    id: string;
    name: string;
    title: string;
    titleSource: Card['titleSource'];
    column: Card['column'];
    status: Card['agent']['status'];
    since: number;
    effort: string;
    folder: string;
    terminal?: string;
    attention?: Card['attention'];
    seen?: boolean;
    headless?: boolean;
    prs?: Card['pullRequests'];
    subagents?: number;
  };
  const specs: Spec[] = [
    { id: 'codex:aaaaaaaa-0000-4000-8000-000000000001', name: 'sample-refactor', title: 'sample-refactor', titleSource: 'session', column: 'working', status: 'busy', since: 6, effort: 'ultra', folder: '~/projects/alpha', terminal: 'aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa', subagents: 2 },
    { id: 'codex:aaaaaaaa-0000-4000-8000-000000000002', name: 'Sample title the model wrote for this thread', title: 'Sample title the model wrote for this thread', titleSource: 'session', column: 'needs_you', status: 'finished', since: 14, effort: 'high', folder: '~/projects/alpha', terminal: 'bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb', attention: ['unseen'], seen: false },
    { id: 'codex:aaaaaaaa-0000-4000-8000-000000000003', name: 'sample-approval', title: 'sample-approval', titleSource: 'session', column: 'needs_you', status: 'blocked', since: 3, effort: 'minimal', folder: '~/projects/alpha', terminal: 'cccccccc-cccc-4ccc-8ccc-cccccccccccc', attention: ['blocked'] },
    { id: 'codex:aaaaaaaa-0000-4000-8000-000000000004', name: '', title: 'beta', titleSource: 'folder', column: 'idle', status: 'finished', since: 95, effort: 'ultra', folder: '~/projects/beta', terminal: 'dddddddd-dddd-4ddd-8ddd-dddddddddddd', seen: true },
    {
      id: 'codex:aaaaaaaa-0000-4000-8000-000000000005',
      name: 'sample-exec-run',
      title: 'Placeholder pull request title',
      titleSource: 'pr',
      column: 'in_review',
      status: 'finished',
      since: 40,
      effort: 'medium',
      folder: '~/projects/alpha',
      headless: true,
      seen: true,
      prs: [{ forge: 'github' as const, host: 'github.com', repo: 'placeholder/repo', number: 301, title: 'Placeholder pull request title', url: 'https://github.com/placeholder/repo/pull/301', state: 'open', checks: 'passing', review: '', draft: false }],
    },
  ];
  const codex = specs.map((x, i) => {
    const c = clone(claude);
    // Sample token counts; Codex keeps no price, so never an amount.
    const out = [84_000, 21_000, 9_500, 3_200, 46_000][i % 5];
    c.spend = { tokens: { input: Math.round(out / 10), output: out, cacheRead: out * 60, cacheWrite: 0 }, usd: 0, priced: false, recorded: false };
    c.id = x.id;
    c.projectId = project;
    c.title = x.title;
    c.titleSource = x.titleSource;
    c.column = x.column;
    c.attention = x.attention ?? [];
    c.seen = x.seen ?? true;
    c.subagents = x.subagents ?? 0;
    c.pullRequests = x.prs ?? [];
    c.issues = [];
    c.artifacts = [];
    c.agent.tool = 'codex';
    c.agent.name = x.name;
    c.agent.model = 'gpt-5.4-codex';
    const onRamp = (['low', 'medium', 'high', 'xhigh', 'max'] as string[]).includes(x.effort);
    c.agent.effort = (onRamp ? x.effort : '') as Card['agent']['effort'];
    c.agent.effortSource = onRamp ? 'recorded' : 'none';
    c.agent.effortWord = onRamp ? '' : x.effort;
    c.agent.status = x.status;
    c.agent.statusSince = ago(x.since);
    c.agent.startedAt = ago(x.since + 90);
    c.agent.lastActivity = ago(Math.max(1, x.since - 1));
    c.agent.live = 'yes';
    c.agent.parent = 'none';
    c.agent.parentCardId = '';
    c.agent.folder = x.folder;
    c.agent.terminalId = x.terminal ?? '';
    c.agent.workspace = x.terminal ? 'sample' : '';
    return c;
  });
  // Claude Code sessions beside them: the lead and one worker.
  s.cards = [...s.cards.slice(0, 2), ...codex];
  s.cards[1].projectId = project;
  for (const p of s.projects) {
    const mine = s.cards.filter((c) => c.projectId === p.id);
    p.counts = {
      working: mine.filter((c) => c.column === 'working').length,
      needsYou: mine.filter((c) => c.column === 'needs_you').length,
      inReview: mine.filter((c) => c.column === 'in_review').length,
      stale: 0,
    };
  }
  s.counts = {
    working: s.cards.filter((c) => c.column === 'working').length,
    needsYou: s.cards.filter((c) => c.column === 'needs_you').length,
    inReview: s.cards.filter((c) => c.column === 'in_review').length,
    stale: 0,
  };
  return s;
}

/**
 * Grouping settings, on the website's sample board: the umbrella project is
 * set to always, and two sample folders are set to never.
 */
function groupingScenario(): Snapshot {
  const s = site();
  s.groupingSettings = [
    { projectId: 'p_site00000001', name: 'harbor', path: '~/projects/harbor', mode: 'always' },
    { projectId: 'p_site0000000a', name: 'scratch', path: '~/scratch', mode: 'never' },
    { projectId: 'p_site0000000b', name: 'downloads-sorting', path: '~/Downloads/downloads-sorting', mode: 'never' },
  ];
  return s;
}

/** The most folders that can have a setting (state.MaxGroupingSettings). */
function settings200(): Snapshot {
  const s = groupingScenario();
  s.groupingSettings = Array.from({ length: 200 }, (_, i) => {
    const n = String(i + 1).padStart(3, '0');
    return { projectId: `p_set${n.padStart(9, '0')}`, name: `folder-${n}`, path: `~/projects/folder-${n}`, mode: i % 7 === 0 ? 'always' : 'never' };
  });
  return s;
}

function hostile(): Snapshot {
  const s = golden();
  s.cards[0].title = '<img src=x onerror="alert(1)"><script>alert(2)</script> markup as text';
  s.cards[0].titleSource = 'pr';
  s.cards[1].title = 'x'.repeat(10_000);
  s.cards[2].agent.name = 'right\u202eto-left\u0007 override';
  s.cards[3].title = 'A title with a very-long-unbroken-token-' + 'y'.repeat(300);
  // Addresses from a tampered snapshot: the page shows these as text, never as links.
  const pr = s.cards[3].pullRequests[0];
  s.cards[3].pullRequests.push({ ...pr, number: pr.number + 1, url: 'javascript:alert(3)' });
  s.cards[3].issues.push({ ...s.cards[3].issues[0], number: 8, url: 'https://evil.example/example-org/example-repo/issues/8' });
  // Well-formed github.com addresses that are not this card's own: text as well.
  s.cards[3].pullRequests.push({ ...pr, number: pr.number + 2, url: 'https://github.com/attacker-org/phish/pull/1' });
  s.cards[3].issues.push({ ...s.cards[3].issues[0], number: 9, url: 'https://github.com/attacker-org/phish/issues/9' });
  s.projects[0].name = '<b>project</b>';
  s.projects[1].name = 'a-project-with-an-extremely-long-folder-name-that-goes-on-and-on-and-on';
  s.groupingSettings.push({ projectId: 'p_0000000000ee', name: '<img src=x onerror="alert(3)">\u202e' + 'z'.repeat(200), path: '~/' + 'deep/'.repeat(60), mode: 'never' });
  return s;
}

export function initial(sc: Scenario): Snapshot | null {
  switch (sc) {
    case 'golden':
    case 'reconnecting':
      return golden();
    case 'spend':
      return spendWeek('mixed');
    case 'spend-unpriced':
      return spendWeek('unpriced');
    case 'spend-pending':
      return spendWeek('pending');
    case 'crowd':
      return crowd();
    case 'effort':
      return effort();
    case 'codex':
      return codexScenario();
    case 'projects':
      return manyProjects();
    case 'forty':
      return forty();
    case 'one':
      return one();
    case 'site':
      return site();
    case 'gitlab':
      return gitlabScenario();
    case 'grouping':
      return groupingScenario();
    case 'settings200':
      return settings200();
    case 'hostile':
      return hostile();
    case 'empty': {
      const s = golden();
      s.cards = [];
      s.projects = [];
      s.sources = s.sources.map((x) => (x.kind === 'codex' ? { ...x, state: 'missing', detail: '', ready: true } : { ...x, ready: true }));
      return s;
    }
    // Golden with every watcher past its first pass: what fires a notification.
    case 'settled': {
      const s = golden();
      s.sources = s.sources.map((x) => ({ ...x, ready: true }));
      return s;
    }
    case 'scanning': {
      const s = golden();
      s.cards = [];
      s.projects = [];
      s.sources = s.sources.map((x) => ({ ...x, ready: x.kind !== 'claude' }));
      return s;
    }
    case 'noagterm': {
      const s = golden();
      s.agtermPresent = false;
      s.sources = s.sources.map((x) => (x.kind === 'agterm' ? { ...x, state: 'missing', version: '', detail: '' } : x));
      for (const c of s.cards) {
        c.agent.terminalId = '';
        if (c.agent.jump === 'agterm') c.agent.jump = 'none'; // a tmux card still jumps
      }
      return s;
    }
    default:
      return null;
  }
}

/** One step of the live mode: a card changes column so the motion shows. */
const CYCLE: Array<[Card['column'], Card['agent']['status'], Card['attention']]> = [
  ['working', 'busy', []],
  ['needs_you', 'blocked', ['blocked']],
  ['idle', 'finished', []],
  ['with_coordinator', 'finished', []],
];

export function step(s: Snapshot, n: number): Snapshot {
  const next = clone(s);
  next.seq++;
  next.generatedAt = new Date().toISOString();
  const movable = next.cards.filter((c) => c.agent.tool === 'claude' && c.agent.status !== 'ended');
  const c = movable[n % movable.length];
  if (!c) return next;
  const i = CYCLE.findIndex((x) => x[0] === c.column);
  const [column, status, attention] = CYCLE[(i + 1) % CYCLE.length];
  c.column = column;
  c.agent.status = status;
  c.attention = attention;
  c.agent.statusSince = new Date().toISOString();
  return next;
}
