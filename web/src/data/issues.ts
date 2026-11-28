// Open issues: the open issues of the repositories of the projects on the
// board, read only. The server asks GitHub (and GitLab where glab is logged
// in) about every ten minutes; the page loads the list when the panel opens
// and again when the snapshot's issues.rev moves (a newer list, a repository
// that joined or left, a change of state).

import {
  ForgeValues,
  IssueKindValues,
  IssuesStateValues,
  OpenIssueStateValues,
  Paths,
  RepoIssuesStateValues,
  type Card,
  type Forge,
  type IssueAgent,
  type IssueKind,
  type Issues,
  type IssuesSummary,
  type OpenIssue,
  type Project,
  type RepoIssues,
  type Snapshot,
} from '../contract.gen';
import { issueHref } from './derive';
import { clean } from './text';

export type IssuesLoad =
  | { ok: true; issues: Issues }
  | { ok: false; kind: 'unauthorized' } // 401 or 403: this tab lost its key
  | { ok: false; kind: 'failed'; message: string };

export interface IssuesClient {
  load(): Promise<IssuesLoad>;
}

const isObj = (v: unknown): v is Record<string, unknown> => !!v && typeof v === 'object' && !Array.isArray(v);
const validTime = (v: unknown): v is string => typeof v === 'string' && !Number.isNaN(Date.parse(v));
const count = (v: unknown): number => (typeof v === 'number' && Number.isInteger(v) && v >= 0 ? v : 0);
const str = (v: unknown): string => (typeof v === 'string' ? v : '');

/** The cards on an issue, each card once; "closes" wins over "looked" for the same card. */
function readAgents(raw: unknown): IssueAgent[] {
  if (!Array.isArray(raw)) return [];
  const out = new Map<string, IssueAgent>();
  for (const a of raw) {
    if (!isObj(a) || typeof a.cardId !== 'string' || a.cardId === '' || !IssueKindValues.includes(a.kind as IssueKind)) continue;
    const had = out.get(a.cardId);
    if (!had || (had.kind === 'looked' && a.kind === 'closes')) out.set(a.cardId, { cardId: a.cardId, kind: a.kind as IssueKind });
  }
  return [...out.values()];
}

function readIssue(raw: unknown): OpenIssue | null {
  if (!isObj(raw) || count(raw.number) < 1) return null;
  const out: OpenIssue = {
    number: raw.number as number,
    title: str(raw.title),
    url: str(raw.url),
    milestone: str(raw.milestone),
    labels: Array.isArray(raw.labels) ? raw.labels.filter((l): l is string => typeof l === 'string' && clean(l) !== '').slice(0, 5) : [],
    agents: readAgents(raw.agents),
    pinned: raw.pinned === true,
  };
  // Only an array (empty too) says which agents were looked for; absent or
  // null is a server that has not judged this issue.
  if (!Array.isArray(raw.agents)) (out as ListedIssue).unjudged = true;
  if (validTime(raw.milestoneDue)) out.milestoneDue = raw.milestoneDue;
  if (validTime(raw.updatedAt)) out.updatedAt = raw.updatedAt;
  return out;
}

/** A pinned issue outside the listed ones: pinned, with its state when the forge gave one. */
function readPinned(raw: unknown): OpenIssue | null {
  const out = readIssue(raw);
  if (!out || !isObj(raw)) return null;
  out.pinned = true;
  if (OpenIssueStateValues.includes(raw.state as OpenIssue['state'] & string)) out.state = raw.state as OpenIssue['state'];
  return out;
}

function readRepo(raw: unknown): RepoIssues | null {
  if (!isObj(raw) || !ForgeValues.includes(raw.forge as Forge) || raw.forge === '') return null;
  if (!RepoIssuesStateValues.includes(raw.state as RepoIssues['state'])) return null;
  const repo = str(raw.repo);
  if (!repo) return null;
  const issues = Array.isArray(raw.issues) ? raw.issues.map(readIssue).filter((i): i is OpenIssue => i !== null) : [];
  // A pinned row the list already has is shown once, as the listed row.
  const listed = new Set(issues.map((i) => i.number));
  const pinned = Array.isArray(raw.pinned)
    ? raw.pinned.map(readPinned).filter((i): i is OpenIssue => i !== null && !listed.has(i.number))
    : [];
  return {
    forge: raw.forge as Forge,
    host: str(raw.host),
    repo,
    key: str(raw.key),
    state: raw.state as RepoIssues['state'],
    open: Math.max(count(raw.open), issues.length),
    issues,
    pinned: pinned.filter((i, n) => pinned.findIndex((j) => j.number === i.number) === n),
  };
}

/** The list as the page can show it; null when its state is one this page does not know. */
export function readIssues(raw: unknown): Issues | null {
  if (!isObj(raw) || !IssuesStateValues.includes(raw.state as Issues['state'])) return null;
  const repos = Array.isArray(raw.repos) ? raw.repos.map(readRepo).filter((r): r is RepoIssues => r !== null) : [];
  const count = (v: unknown) => (typeof v === 'number' && Number.isSafeInteger(v) && v >= 0 ? v : 0);
  const out: Issues = { state: raw.state as Issues['state'], repos, rev: count(raw.rev), freshMs: count(raw.freshMs), unsaved: raw.unsaved === true };
  if (validTime(raw.asOf)) out.asOf = raw.asOf;
  return out;
}

export function createIssuesClient(fetchFn: typeof fetch): IssuesClient {
  return {
    async load() {
      let res: Response;
      try {
        res = await fetchFn(Paths.issues, { credentials: 'same-origin', headers: { Accept: 'application/json' } });
      } catch {
        return { ok: false, kind: 'failed', message: 'sitboard did not answer' };
      }
      if (res.status === 401 || res.status === 403) return { ok: false, kind: 'unauthorized' };
      if (!res.ok) return { ok: false, kind: 'failed', message: `sitboard answered ${res.status}` };
      let l: Issues | null;
      try {
        l = readIssues(await res.json());
      } catch {
        l = null;
      }
      return l ? { ok: true, issues: l } : { ok: false, kind: 'failed', message: 'sitboard sent something unreadable' };
    },
  };
}

/** The header chip is shown: issues are not switched off. */
export function issuesShown(s: IssuesSummary | undefined): boolean {
  return !!s && (s.state === 'loading' || s.state === 'ready');
}

/**
 * Open issues of these projects, each repository once (by the server's
 * issuesKey, compared whole: the page folds nothing itself); null when none
 * of them has a count (no repository, no answer yet, issues turned off).
 */
export function openIssuesOf(projects: readonly Project[]): number | null {
  const seen = new Set<string>();
  let n = 0;
  let known = false;
  for (const p of projects) {
    const k = p.issuesKey;
    if (!p.repo || !k || !p.openIssues?.known) continue;
    if (seen.has(k)) continue;
    seen.add(k);
    known = true;
    n += count(p.openIssues.open);
  }
  return known ? n : null;
}

/** "12 open issues", "1 open issue", "no open issues". */
export function openIssuesText(n: number): string {
  return n === 0 ? 'no open issues' : `${n} open ${n === 1 ? 'issue' : 'issues'}`;
}

/** One repository of the panel, with the name of the project that has it. */
export interface IssuesSection {
  key: string;
  name: string;
  repo: RepoIssues;
}

/**
 * The repositories of these projects (in their order), each once, from the
 * list, matched by the server's key (Project.issuesKey, RepoIssues.key)
 * compared whole; a project whose repository is not in the list is left out.
 */
export function sectionsFor(list: Issues | null, projects: readonly Project[]): IssuesSection[] {
  if (!list) return [];
  const byKey = new Map(list.repos.map((r) => [r.key, r]));
  const out: IssuesSection[] = [];
  const seen = new Set<string>();
  for (const p of projects) {
    const k = p.issuesKey;
    if (!p.repo || !k) continue;
    const r = byKey.get(k);
    if (!r || seen.has(k)) continue;
    seen.add(k);
    out.push({ key: k, name: clean(p.name) || clean(r.repo), repo: r });
  }
  return out;
}

export const NO_MILESTONE = 'No milestone';

export interface MilestoneGroup {
  /** The milestone's title; '' for issues in none. */
  milestone: string;
  issues: OpenIssue[];
}

// Numbers inside titles count as numbers: "Sprint 2" comes before "Sprint 10".
const TITLES = new Intl.Collator(undefined, { numeric: true, sensitivity: 'base' });

/**
 * Issues by milestone: those with a due date first, the soonest first, then
 * the others by title (numbers in a title as numbers), then the issues in no
 * milestone. A milestone several repositories share takes the earliest due
 * date among them. Inside a group the order is the server's (the most
 * recently updated first).
 */
export function byMilestone(issues: readonly OpenIssue[]): MilestoneGroup[] {
  const groups = new Map<string, MilestoneGroup & { due: number }>();
  for (const is of issues) {
    const m = clean(is.milestone);
    let g = groups.get(m);
    if (!g) {
      g = { milestone: m, issues: [], due: Infinity };
      groups.set(m, g);
    }
    const due = is.milestoneDue ? Date.parse(is.milestoneDue) : NaN;
    if (!Number.isNaN(due) && due < g.due) g.due = due;
    g.issues.push(is);
  }
  return [...groups.values()]
    .sort((a, b) => {
      if ((a.milestone === '') !== (b.milestone === '')) return a.milestone === '' ? 1 : -1;
      if (a.due !== b.due) return a.due - b.due;
      return TITLES.compare(a.milestone, b.milestone);
    })
    .map(({ milestone, issues }) => ({ milestone, issues }));
}

/**
 * The snapshot as the page shows it while the tab's key is lost: no project
 * carries an issue count, so no tile, rail row or header chip shows one.
 */
export function withoutIssueCounts(s: Snapshot): Snapshot {
  return { ...s, projects: s.projects.map((p) => ({ ...p, openIssues: { known: false, open: 0 } })) };
}

/** An issue's link: only its own address on its own forge (issueHref). */
export function openIssueHref(r: RepoIssues, is: OpenIssue, gitlabHosts: readonly string[]): string | null {
  return issueHref({ forge: r.forge, host: r.host, repo: r.repo, number: is.number, url: is.url }, gitlabHosts);
}

/**
 * The repository's issue list on its forge, cut from the checked address of
 * one of its issues; null when none of them has one.
 */
export function repoIssuesHref(r: RepoIssues, gitlabHosts: readonly string[]): string | null {
  for (const is of r.issues) {
    const href = openIssueHref(r, is, gitlabHosts);
    if (href) return href.slice(0, href.lastIndexOf('/'));
  }
  return null;
}

/**
 * An issue as the page reads it: unjudged when the server sent no agents
 * array for it (absent or null), so nothing is said about agents on it.
 */
export type ListedIssue = OpenIssue & { unjudged?: true };

/** The server said which agents are on the issue (an array, empty too). */
export function judged(is: OpenIssue): boolean {
  return !(is as ListedIssue).unjudged;
}

/**
 * One issue of one repository, as the page keeps a pin it pressed: the
 * server's key of the repository (RepoIssues.key, compared whole) and the
 * number.
 */
export function pinKey(r: { key: string }, n: number): string {
  return `${r.key}#${n}`;
}

/**
 * The list with the pins the page has changed but the server has not yet
 * served (pinKey -> pinned): a listed row takes the new state, and a pinned
 * row outside the listed ones that was unpinned goes.
 */
export function withPins(list: Issues, pins: ReadonlyMap<string, boolean>): Issues {
  if (pins.size === 0) return list;
  return {
    ...list,
    repos: list.repos.map((r) => ({
      ...r,
      issues: r.issues.map((i) => {
        const p = pins.get(pinKey(r, i.number));
        return p === undefined || p === i.pinned ? i : { ...i, pinned: p };
      }),
      pinned: r.pinned.filter((i) => pins.get(pinKey(r, i.number)) !== false),
    })),
  };
}

/** An agent tag on an issue row: the card's own name, and how its session relates to the issue. */
export interface AgentTag {
  cardId: string;
  name: string;
  kind: IssueKind;
}

/**
 * The tags of an issue: one per agent whose card is on the board, named
 * from that card (never from a second copy), "on it" before "looked at".
 */
export function agentTags(is: OpenIssue, cards: ReadonlyMap<string, Card>): AgentTag[] {
  const out: AgentTag[] = [];
  for (const a of is.agents) {
    const c = cards.get(a.cardId);
    if (!c) continue;
    out.push({ cardId: a.cardId, name: clean(c.agent.name) || 'an agent', kind: a.kind });
  }
  return out.sort((a, b) => (a.kind === b.kind ? 0 : a.kind === 'closes' ? -1 : 1));
}

/** The Agent filter: any issue, those no agent on the board is on or looked at, or those one is. */
export type AgentFilter = 'any' | 'none' | 'with';

/** An agent on the board is on the issue or looked at it. */
export function agentSeen(is: OpenIssue, cards: ReadonlyMap<string, Card>): boolean {
  return is.agents.some((a) => cards.has(a.cardId));
}

/** An unjudged issue passes only Any: it is neither one with no agent seen nor one with an agent. */
export function passesAgent(is: OpenIssue, f: AgentFilter, cards: ReadonlyMap<string, Card>): boolean {
  return f === 'any' || (judged(is) && (f === 'with') === agentSeen(is, cards));
}

/**
 * How many listed issues of these repositories no agent on the board is on
 * or looked at. Only the listed ones are judged; "+N more" stays outside.
 * null when a repository has no answer or an issue was not judged.
 */
export function noAgentSeen(repos: readonly RepoIssues[], cards: ReadonlyMap<string, Card>): number | null {
  let n = 0;
  for (const r of repos) {
    if (r.state !== 'ok') return null;
    for (const i of r.issues) {
      if (!judged(i)) return null;
      if (!agentSeen(i, cards)) n++;
    }
  }
  return n;
}

/**
 * A tile's count judged from the loaded Issues list, for projects the
 * snapshot serves no Project.issuesNoAgent for: only when the list is of the
 * snapshot's current rev and has a usable answer for every repository of
 * these projects with a known open count; null otherwise (no line).
 */
export function noAgentFromList(list: Issues | null, rev: number, projects: readonly Project[], cards: ReadonlyMap<string, Card>): number | null {
  if (!list || list.rev !== rev) return null;
  const byKey = new Map(list.repos.map((r) => [r.key, r]));
  const repos: RepoIssues[] = [];
  const seen = new Set<string>();
  for (const p of projects) {
    const k = p.issuesKey;
    if (!p.repo || !k || !p.openIssues?.known || seen.has(k)) continue;
    seen.add(k);
    const r = byKey.get(k);
    if (!r) return null;
    repos.push(r);
  }
  return repos.length ? noAgentSeen(repos, cards) : null;
}

/**
 * A project as the snapshot may serve it from the 0.6.0 core on: with
 * issuesNoAgent, how many listed open issues of its repository no agent is
 * on or looked at. Absent means not known. Declared here until the core's
 * pull request puts it in the generated contract.
 */
export type ProjectNoAgent = Project & { issuesNoAgent?: number };

/**
 * The issues with no agent seen of these projects, each repository once (by
 * issuesKey, compared whole), from the snapshot's Project.issuesNoAgent;
 * null when any repository with a known count has no such number (the page
 * then judges from the Issues list, if it has one).
 */
export function noAgentOfProjects(projects: readonly ProjectNoAgent[]): number | null {
  const seen = new Set<string>();
  let n = 0;
  let known = false;
  for (const p of projects) {
    const k = p.issuesKey;
    if (!p.repo || !k || !p.openIssues?.known || seen.has(k)) continue;
    seen.add(k);
    const v = p.issuesNoAgent;
    if (typeof v !== 'number' || !Number.isSafeInteger(v) || v < 0) return null;
    n += v;
    known = true;
  }
  return known ? n : null;
}

/**
 * IssuesSummary.noAgent when the server knows it; null when it is absent
 * (not known yet, or issues off; the core serves it optional) or unusable.
 */
export function knownNoAgent(s: IssuesSummary | undefined): number | null {
  const v = (s as { noAgent?: unknown } | undefined)?.noAgent;
  return typeof v === 'number' && Number.isSafeInteger(v) && v >= 0 ? v : null;
}
