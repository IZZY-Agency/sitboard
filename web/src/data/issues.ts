// Open issues: the open issues of the repositories of the projects on the
// board, read only. The server asks GitHub (and GitLab where glab is logged
// in) about every ten minutes; the page loads the list when the panel opens
// and again when the snapshot's issues.rev moves (a newer list, a repository
// that joined or left, a change of state).

import {
  ForgeValues,
  IssuesStateValues,
  Paths,
  RepoIssuesStateValues,
  type Forge,
  type Issues,
  type IssuesSummary,
  type OpenIssue,
  type Project,
  type RepoIssues,
  type Snapshot,
} from '../contract.gen';
import { issueHref, repoKey } from './derive';
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

function readIssue(raw: unknown): OpenIssue | null {
  if (!isObj(raw) || count(raw.number) < 1) return null;
  const out: OpenIssue = {
    number: raw.number as number,
    title: str(raw.title),
    url: str(raw.url),
    milestone: str(raw.milestone),
    labels: Array.isArray(raw.labels) ? raw.labels.filter((l): l is string => typeof l === 'string' && clean(l) !== '').slice(0, 5) : [],
  };
  if (validTime(raw.milestoneDue)) out.milestoneDue = raw.milestoneDue;
  if (validTime(raw.updatedAt)) out.updatedAt = raw.updatedAt;
  return out;
}

function readRepo(raw: unknown): RepoIssues | null {
  if (!isObj(raw) || !ForgeValues.includes(raw.forge as Forge) || raw.forge === '') return null;
  if (!RepoIssuesStateValues.includes(raw.state as RepoIssues['state'])) return null;
  const repo = str(raw.repo);
  if (!repo) return null;
  const issues = Array.isArray(raw.issues) ? raw.issues.map(readIssue).filter((i): i is OpenIssue => i !== null) : [];
  return {
    forge: raw.forge as Forge,
    host: str(raw.host),
    repo,
    state: raw.state as RepoIssues['state'],
    open: Math.max(count(raw.open), issues.length),
    issues,
  };
}

/** The list as the page can show it; null when its state is one this page does not know. */
export function readIssues(raw: unknown): Issues | null {
  if (!isObj(raw) || !IssuesStateValues.includes(raw.state as Issues['state'])) return null;
  const repos = Array.isArray(raw.repos) ? raw.repos.map(readRepo).filter((r): r is RepoIssues => r !== null) : [];
  const count = (v: unknown) => (typeof v === 'number' && Number.isSafeInteger(v) && v >= 0 ? v : 0);
  const out: Issues = { state: raw.state as Issues['state'], repos, rev: count(raw.rev), freshMs: count(raw.freshMs) };
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
 * Open issues of these projects, each repository once; null when none of
 * them has a count (no repository, no answer yet, issues turned off).
 */
export function openIssuesOf(projects: readonly Project[]): number | null {
  const seen = new Set<string>();
  let n = 0;
  let known = false;
  for (const p of projects) {
    if (!p.repo || !p.openIssues?.known) continue;
    const k = repoKey({ forge: p.repoForge, host: p.repoHost, repo: p.repo });
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
 * list; a project whose repository is not in the list is left out.
 */
export function sectionsFor(list: Issues | null, projects: readonly Project[]): IssuesSection[] {
  if (!list) return [];
  const byKey = new Map(list.repos.map((r) => [repoKey(r), r]));
  const out: IssuesSection[] = [];
  const seen = new Set<string>();
  for (const p of projects) {
    if (!p.repo) continue;
    const k = repoKey({ forge: p.repoForge, host: p.repoHost, repo: p.repo });
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
