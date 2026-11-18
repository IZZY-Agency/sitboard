// Dev harness only: placeholder data from the contract's golden snapshot and
// generated placeholders. Never part of the build (scripts/check-dist.mjs).
import goldenJson from '../../internal/contract/golden/snapshot.v1.json';
import type { Card, Snapshot } from '../src/contract.gen';

export const SCENARIOS = [
  'golden',
  'crowd',
  'projects',
  'forty',
  'one',
  'site',
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
  s.projects.push({ id, name, path: `~/projects/${name}`, repo: '', counts: { working: 0, needsYou: 0, inReview: 0, stale: 0 }, area, parentId });
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
    c.pullRequests =
      c.column === 'in_review' || c.column === 'done' || i % 4 === 1
        ? [
            {
              repo: 'placeholder/repo',
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
  // A long-running coordinator with many merged pull requests.
  s.cards[0].pullRequests = Array.from({ length: 26 }, (_, i) => ({
    repo: 'placeholder/repo',
    number: 400 - i,
    title: 'Placeholder',
    url: `https://github.com/placeholder/repo/pull/${400 - i}`,
    state: i === 0 ? ('open' as const) : ('merged' as const),
    checks: 'running' as const,
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
  s.projects = [];
  addProject(s, 'p_site00000001', 'harbor', 'studio');
  addProject(s, 'p_site00000002', 'harbor-web', '', 'p_site00000001');
  addProject(s, 'p_site00000003', 'harbor-api', '', 'p_site00000001');
  addProject(s, 'p_site00000004', 'lumen-docs', 'studio');
  addProject(s, 'p_site00000005', 'pixel-kit', '');
  const repo = 'example-org/harbor';
  const pr = (number: number, state: 'open' | 'merged', checks: 'passing' | 'running' | 'failing' | 'none' = 'passing') => ({
    repo,
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
    lead?: boolean;
    attention?: Card['attention'];
    prs?: Card['pullRequests'];
    issues?: number[];
    arts?: string[];
  };
  let artN = 0;
  const art = (title: string) => ({ title, url: `https://claude.ai/artifact/exampleArtifact${String(++artN).padStart(4, '0')}` });
  const specs: Spec[] = [
    { id: lead, projectId: 'p_site00000001', name: 'harbor-lead', title: 'Checkout rewrite: plan, split and review', column: 'working', status: 'busy', since: 12, model: 'claude-fable-5-2', prs: [pr(214, 'merged')], arts: ['Checkout plan'] },
    { id: 'claude:site-w1', projectId: 'p_site00000002', name: 'web-checkout', title: 'One-page checkout with saved addresses', column: 'working', status: 'busy', since: 4, lead: true, prs: [pr(221, 'open', 'running')], arts: ['Address form layouts'] },
    { id: 'claude:site-w2', projectId: 'p_site00000003', name: 'api-auth', title: 'Session refresh for long-lived tokens', column: 'needs_you', status: 'blocked', since: 2, lead: true, attention: ['blocked'] },
    { id: 'claude:site-w3', projectId: 'p_site00000002', name: 'web-search', title: 'Search suggestions as you type', column: 'with_coordinator', status: 'finished', since: 18, model: 'claude-sonnet-5-5', lead: true },
    { id: 'claude:site-w4', projectId: 'p_site00000003', name: 'api-payments', title: 'Retry failed card payments once', column: 'in_review', status: 'finished', since: 35, model: 'claude-sonnet-5-5', lead: true, prs: [pr(219, 'open', 'passing')], issues: [88], arts: ['Payment retry flow'] },
    { id: 'claude:site-w5', projectId: 'p_site00000002', name: 'web-a11y', title: 'Keyboard paths through the cart', column: 'done', status: 'finished', since: 95, lead: true, prs: [pr(216, 'merged')] },
    { id: 'claude:site-d1', projectId: 'p_site00000004', name: 'docs-refresh', title: 'Rewrite the getting-started guide', column: 'in_review', status: 'finished', since: 50, model: 'claude-haiku-4-5-20251001', attention: ['failing_checks'], prs: [{ ...pr(41, 'open', 'failing'), repo: 'example-org/lumen-docs', url: 'https://github.com/example-org/lumen-docs/pull/41' }] },
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
    c.issues = (x.issues ?? []).map((n) => ({ repo, number: n, url: `https://github.com/${repo}/issues/${n}` }));
    c.agent.name = x.name;
    c.agent.model = x.model ?? 'claude-opus-5-5';
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

function hostile(): Snapshot {
  const s = golden();
  s.cards[0].title = '<img src=x onerror="alert(1)"><script>alert(2)</script> markup as text';
  s.cards[0].titleSource = 'pr';
  s.cards[1].title = 'x'.repeat(10_000);
  s.cards[2].agent.name = 'right\u202eto-left\u0007 override';
  s.cards[3].title = 'A title with a very-long-unbroken-token-' + 'y'.repeat(300);
  s.projects[0].name = '<b>project</b>';
  s.projects[1].name = 'a-project-with-an-extremely-long-folder-name-that-goes-on-and-on-and-on';
  return s;
}

export function initial(sc: Scenario): Snapshot | null {
  switch (sc) {
    case 'golden':
    case 'reconnecting':
      return golden();
    case 'crowd':
      return crowd();
    case 'projects':
      return manyProjects();
    case 'forty':
      return forty();
    case 'one':
      return one();
    case 'site':
      return site();
    case 'hostile':
      return hostile();
    case 'empty': {
      const s = golden();
      s.cards = [];
      s.projects = [];
      s.sources = s.sources.map((x) => (x.kind === 'codex' ? { ...x, state: 'missing', detail: '', ready: true } : { ...x, ready: true }));
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
      for (const c of s.cards) c.agent.terminalId = '';
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
