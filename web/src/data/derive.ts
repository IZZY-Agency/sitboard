// Pure derivations from a snapshot to what the views show. Every word a state
// needs lives here, so each view says the same thing the same way.

import {
  ARTIFACT_URL_PATTERN,
  ColumnValues,
  type Artifact,
  type Attention,
  type Card,
  type Checks,
  type Column,
  type Project,
  type Issue,
  type PullRequest,
  type Snapshot,
  type SourceStatus,
} from '../contract.gen';
import { since } from './time';
import { clean } from './text';
import { countsOf, grouping, partParent, projectsById } from './groups';

export type Tone = 'needs' | 'working' | 'coordinator' | 'review' | 'done' | 'idle' | 'failing' | 'muted' | 'body';

export interface ColumnMeta {
  label: string; // column head
  word: string; // state word on a row
  tone: Tone;
  hint: string;
  empty: string;
}

export const COLUMNS: readonly Column[] = ColumnValues;

export const COLUMN_META: Record<Column, ColumnMeta> = {
  needs_you: {
    label: 'Needs you',
    word: 'Needs you',
    tone: 'needs',
    hint: 'Blocked, or finished a turn you have not seen.',
    empty: 'Nobody is waiting for you. An agent lands here when it asks something or finishes a turn you have not seen.',
  },
  working: {
    label: 'Working',
    word: 'Working',
    tone: 'working',
    hint: 'Busy right now.',
    empty: 'No agent is busy right now.',
  },
  with_coordinator: {
    label: 'With coordinator',
    word: 'With coordinator',
    tone: 'coordinator',
    hint: 'Finished a turn, waiting for its coordinator.',
    empty: 'Empty right now. A worker lands here when it finishes a turn and waits for its coordinator.',
  },
  in_review: {
    label: 'In review',
    word: 'In review',
    tone: 'review',
    hint: 'Stopped with an open pull request.',
    empty: 'Empty right now. An agent lands here when it stops while one of its pull requests is still open.',
  },
  done: {
    label: 'Done · 24h',
    word: 'Done',
    tone: 'done',
    hint: 'Every pull request it opened is merged or closed.',
    empty: 'Nothing finished in the last 24 hours.',
  },
  idle: {
    label: 'Idle',
    word: 'Idle',
    tone: 'idle',
    hint: 'Finished a turn, waiting for your next message.',
    empty: 'No agent is waiting for your next message.',
  },
};

/** The state word for a card. Unknown status says so in every view. */
export function stateWord(card: Card): string {
  if (card.agent.status === 'unknown') return 'Status unknown';
  return COLUMN_META[card.column].word;
}

export function columnTone(c: Column): Tone {
  return COLUMN_META[c].tone;
}

/** Time in the current state; falls back to the last activity. "" when unknown. */
export function timeInState(card: Card, now: number): string {
  return since(card.agent.statusSince ?? card.agent.lastActivity, now);
}

function stamp(t: string | undefined): number {
  const v = t ? Date.parse(t) : Number.NaN;
  return Number.isNaN(v) ? Number.NEGATIVE_INFINITY : v;
}

function stateStamp(c: Card): number {
  return stamp(c.agent.statusSince ?? c.agent.lastActivity);
}

/** Most recent change first, so the longest-waiting card is last. Unknown times go last. */
export function byRecent(a: Card, b: Card): number {
  return stateStamp(b) - stateStamp(a) || a.id.localeCompare(b.id);
}

/** Longest-waiting first. Unknown times go last. */
export function byLongest(a: Card, b: Card): number {
  const sa = stateStamp(a);
  const sb = stateStamp(b);
  if (sa === sb) return a.id.localeCompare(b.id);
  if (sa === Number.NEGATIVE_INFINITY) return 1;
  if (sb === Number.NEGATIVE_INFINITY) return -1;
  return sa - sb;
}

export function byColumn(cards: readonly Card[]): Record<Column, Card[]> {
  const out = Object.fromEntries(COLUMNS.map((c) => [c, [] as Card[]])) as Record<Column, Card[]>;
  for (const card of cards) (out[card.column] ?? out.idle).push(card);
  for (const c of COLUMNS) out[c].sort(byRecent);
  return out;
}

/** "Opus 5.5" from "claude-opus-5-5"; the raw id when it does not look like one; "model unknown" when empty. */
export function modelLabel(model: string): string {
  const m = clean(model);
  if (!m) return 'model unknown';
  const parts = m.replace(/^claude-/, '').split('-');
  const family = parts[0];
  const nums = parts.slice(1).filter((p) => /^\d{1,2}$/.test(p));
  if (/^[a-z]+$/.test(family) && nums.length > 0 && model.startsWith('claude-')) {
    return `${family[0].toUpperCase()}${family.slice(1)} ${nums.join('.')}`;
  }
  return m;
}

export const MODEL_FAMILIES = ['fable', 'opus', 'sonnet', 'haiku'] as const;
export type ModelFamily = (typeof MODEL_FAMILIES)[number];

const isFamily = (w: string | undefined): w is ModelFamily => (MODEL_FAMILIES as readonly string[]).includes(w ?? '');

/**
 * The family of an Anthropic Claude model id, so a new version, a date or a
 * suffix keeps its hue: "claude-opus-5-5", "claude-haiku-4-5-20251001",
 * "claude-sonnet-4-5@20250929", "claude-opus-4-6[1m]". The family word comes
 * right after "claude", or after its version in the old form
 * ("claude-3-5-sonnet-20241022"). Before "claude" there is nothing, or a
 * provider prefix ending in "anthropic" ("us.anthropic.claude-…",
 * "anthropic/claude-…"), so another maker's model named after Claude
 * ("deepseek-claude-sonnet-distill") is not one. '' when empty, not Claude's,
 * or a family the page does not know: those keep a neutral chip.
 */
export function modelFamily(model: string): ModelFamily | '' {
  const words = clean(model).toLowerCase().split(/[^a-z0-9]+/).filter(Boolean);
  const at = words.indexOf('claude');
  if (at < 0 || (at > 0 && words[at - 1] !== 'anthropic')) return '';
  let i = at + 1;
  while (/^\d+$/.test(words[i] ?? '')) i++;
  const family = words[i];
  return isFamily(family) ? family : '';
}

/** No model id at all, as opposed to one the page shows without a family hue. */
export function modelUnknown(model: string): boolean {
  return !clean(model);
}

export function agentName(card: Card): string {
  return clean(card.agent.name) || 'unnamed session';
}

export function cardTitle(card: Card): string {
  return clean(card.title) || agentName(card);
}

export interface Index {
  byId: Map<string, Card>;
  workers: Map<string, Card[]>; // coordinator card id → its workers
  projects: Map<string, Project>;
}

export function indexOf(s: Pick<Snapshot, 'cards' | 'projects'>): Index {
  const byId = new Map(s.cards.map((c) => [c.id, c]));
  const workers = new Map<string, Card[]>();
  for (const c of s.cards) {
    if (c.agent.parent === 'known' && c.agent.parentCardId && byId.has(c.agent.parentCardId)) {
      const list = workers.get(c.agent.parentCardId) ?? [];
      list.push(c);
      workers.set(c.agent.parentCardId, list);
    }
  }
  return { byId, workers, projects: projectsById(s.projects) };
}

const plural = (n: number, one: string, many = one + 's') => `${n} ${n === 1 ? one : many}`;

/** The coordinator line: "coordinator · 3 workers", "for <name>", "coordinator: unknown", or "". */
export function roleLine(card: Card, ix: Index): string {
  const n = ix.workers.get(card.id)?.length ?? 0;
  if (n > 0) return `coordinator · ${plural(n, 'worker')}`;
  if (card.agent.parent === 'known') {
    const parent = card.agent.parentCardId ? ix.byId.get(card.agent.parentCardId) : undefined;
    return parent ? `for ${agentName(parent)}` : 'for a coordinator not on the board';
  }
  if (card.agent.parent === 'unknown') return 'coordinator: unknown';
  return '';
}

/** A card's project: the part under its parent ("mainapp" + "web"), or just the name. */
export function projectLabel(card: Card, ix: Index): { name: string; part: string } {
  const p = ix.projects.get(card.projectId);
  if (!p) return { name: 'unknown project', part: '' };
  const parent = partParent(p, ix.projects);
  return parent ? { name: clean(parent.name), part: clean(p.name) } : { name: clean(p.name), part: '' };
}

/** The project label as one string: "mainapp / web" or "tool". */
export function projectName(card: Card, ix: Index): string {
  const l = projectLabel(card, ix);
  return l.part ? `${l.name} / ${l.part}` : l.name;
}

/** True when jump can focus the card's terminal. */
export function canJump(card: Card, agtermPresent: boolean): boolean {
  return agtermPresent && card.agent.terminalId !== '';
}

const CHECK_RANK: Record<Checks, number> = { failing: 4, running: 3, unknown: 2, passing: 1, none: 0 };

export interface Line {
  text: string;
  tone: Tone;
}

export function prLine(pr: PullRequest): Line {
  const n = `#${pr.number}`;
  if (pr.state === 'merged') return { text: `${n} merged`, tone: 'done' };
  if (pr.state === 'closed') return { text: `${n} closed`, tone: 'muted' };
  const parts = [n];
  if (pr.draft) parts.push('draft');
  const tone: Tone = pr.checks === 'failing' ? 'failing' : pr.checks === 'running' ? 'coordinator' : 'body';
  parts.push(pr.checks === 'none' ? 'open' : `checks ${pr.checks}`);
  if (pr.review === 'changes_requested') parts.push('· changes requested');
  if (pr.review === 'approved') parts.push('· approved');
  return { text: parts.join(' '), tone };
}

/** At most this many pull request lines on a card, the last one possibly "+N more merged". */
export const CARD_PR_LINES = 3;

/**
 * The pull requests a card shows, in the order the core sends them (open
 * first, newest first): all of them when they fit in CARD_PR_LINES lines,
 * otherwise the first ones and a last line for the rest.
 */
export function cardPrs(prs: readonly PullRequest[]): { shown: PullRequest[]; more: Line | null } {
  if (prs.length <= CARD_PR_LINES) return { shown: [...prs], more: null };
  const shown = prs.slice(0, CARD_PR_LINES - 1);
  const rest = prs.slice(CARD_PR_LINES - 1);
  const count = (s: PullRequest['state']) => rest.filter((p) => p.state === s).length;
  const kinds = (['open', 'merged', 'closed'] as const).filter((s) => count(s) > 0);
  let text =
    kinds.length === 1
      ? `+${rest.length} more ${kinds[0]}`
      : `+${rest.length} more: ${kinds.map((s) => `${count(s)} ${s}`).join(' · ')}`;
  // A failing check is never hidden by the cut.
  const failing = rest.some((p) => p.state === 'open' && p.checks === 'failing');
  if (failing) text += ' · checks failing';
  return { shown, more: { text, tone: failing ? 'failing' : 'muted' } };
}

/** A claude.ai artifact link, or null for anything else: the page links nothing but that form. */
export function artifactHref(url: string): string | null {
  return ARTIFACT_URL_PATTERN.test(url) ? url : null;
}

/** An artifact's title as the card shows it; a blank one says so. */
export function artifactTitle(a: Artifact): string {
  return clean(a.title) || 'Untitled artifact';
}

/**
 * The artifacts a card shows, in the order the core sends them (most recently
 * published first), with the pull request rule: all of them when they fit in
 * CARD_PR_LINES lines, otherwise the first ones and "+N more". A link that is
 * not a claude.ai artifact address is left out.
 */
export function cardArtifacts(list: readonly Artifact[]): { shown: Artifact[]; more: string | null } {
  const ok = list.filter((a) => artifactHref(a.url) !== null);
  if (ok.length <= CARD_PR_LINES) return { shown: ok, more: null };
  return { shown: ok.slice(0, CARD_PR_LINES - 1), more: `+${ok.length - (CARD_PR_LINES - 1)} more` };
}

const NUMS_IN_LINE = 2;

/**
 * One line for a row: the open ones with their worst check, then "+N merged".
 * Never a long run of numbers: "#9 #8 open · checks failing · +12 merged",
 * "#194 #193 +28 merged" when nothing is open.
 */
export function prSummary(prs: readonly PullRequest[]): Line | null {
  if (prs.length === 0) return null;
  const open = prs.filter((p) => p.state === 'open');
  const merged = prs.filter((p) => p.state === 'merged');
  const closed = prs.filter((p) => p.state === 'closed');
  const lead = (l: readonly PullRequest[], word: string) => {
    const nums = l.slice(0, NUMS_IN_LINE + 1 === l.length ? l.length : NUMS_IN_LINE).map((p) => `#${p.number}`);
    const extra = l.length - nums.length;
    return `${nums.join(' ')}${extra > 0 ? ` +${extra}` : ''} ${word}`;
  };
  const parts: string[] = [];
  let tone: Tone = 'muted';
  if (open.length) {
    const worst = open.map((p) => p.checks).sort((a, b) => CHECK_RANK[b] - CHECK_RANK[a])[0];
    parts.push(lead(open, 'open') + (worst === 'none' ? '' : ` · checks ${worst}`));
    tone = worst === 'failing' ? 'failing' : worst === 'running' ? 'coordinator' : 'body';
  }
  if (merged.length) {
    parts.push(parts.length ? `+${merged.length} merged` : lead(merged, 'merged'));
    if (!open.length) tone = 'done';
  }
  if (closed.length) parts.push(parts.length ? `+${closed.length} closed` : lead(closed, 'closed'));
  return { text: parts.join(' · '), tone };
}

/**
 * The issues a card's pull requests close: "closes #7 #8 +3". An issue in the
 * card's own repository (`home`, "owner/name") is a bare number; one elsewhere
 * carries its repository's short name, "other#7", or the full "owner/name#7"
 * when another repository on the line, the home one included (a fork), shares
 * that short name. With no home, names appear only when the issues span
 * repositories. GitHub repository names ignore case, so every comparison does;
 * a name prints as first seen. The contract says which issues the card's pull
 * requests close, not which pull request closes which, so this belongs to the
 * card, never to one line.
 */
export function closesText(issues: readonly Issue[], home: string): string {
  const key = (repo: string) => repo.toLowerCase();
  const short = (repo: string) => repo.slice(repo.lastIndexOf('/') + 1);
  const seen = new Set<string>();
  const list: Issue[] = [];
  const shown = new Map<string, string>(); // repository key → its name as first seen
  for (const i of issues) {
    const k = `${key(i.repo)}#${i.number}`;
    if (seen.has(k)) continue;
    seen.add(k);
    list.push(i);
    if (!shown.has(key(i.repo))) shown.set(key(i.repo), i.repo);
  }
  if (list.length === 0) return '';
  const own = key(home) || (shown.size === 1 ? [...shown.keys()][0] : '');
  const owners = new Map<string, Set<string>>(); // short name key → the repositories using it
  for (const r of own ? [...shown.keys(), own] : shown.keys()) {
    const set = owners.get(short(r)) ?? new Set<string>();
    set.add(r);
    owners.set(short(r), set);
  }
  const label = (i: Issue) => {
    const r = key(i.repo);
    if (r === own) return `#${i.number}`;
    const name = shown.get(r) as string;
    return `${clean((owners.get(short(r))?.size ?? 0) > 1 ? name : short(name))}#${i.number}`;
  };
  const extra = list.length - NUMS_IN_LINE;
  return `closes ${list.slice(0, NUMS_IN_LINE).map(label).join(' ')}${extra > 0 ? ` +${extra}` : ''}`;
}

/** The closes text for a card, measured against its own project's repository. */
export function cardCloses(card: Card, ix: Index): string {
  return closesText(card.issues, ix.projects.get(card.projectId)?.repo ?? '');
}

/** The one-line pull request form plus the issues they close; null when the card has no pull request. */
export function cardPrSummary(card: Card, ix: Index): Line | null {
  const s = prSummary(card.pullRequests);
  if (!s) return null;
  const closes = cardCloses(card, ix);
  return closes ? { ...s, text: `${s.text} · ${closes}` } : s;
}

const ATTENTION_ORDER: Record<Attention, number> = { blocked: 0, unseen: 1, failing_checks: 2, stale: 3 };

/** The attention reasons of a card as words, most urgent first. */
export function attentionLines(card: Card, now: number): Line[] {
  return [...card.attention]
    .sort((a, b) => ATTENTION_ORDER[a] - ATTENTION_ORDER[b])
    .map((a): Line => {
      switch (a) {
        case 'blocked':
          return { text: 'Blocked on a question or a permission', tone: 'needs' };
        case 'unseen':
          return { text: 'Finished a turn you have not seen', tone: 'needs' };
        case 'failing_checks': {
          const nums = card.pullRequests
            .filter((p) => p.state === 'open' && p.checks === 'failing')
            .map((p) => `#${p.number}`);
          return { text: nums.length ? `Checks failing on ${nums.join(' ')}` : 'Checks failing', tone: 'failing' };
        }
        case 'stale': {
          const quiet = since(card.agent.lastActivity, now);
          return { text: quiet ? `Stale · no activity for ${quiet}` : 'Stale · no recent activity', tone: 'muted' };
        }
        default:
          return { text: String(a), tone: 'muted' };
      }
    });
}

/** The needs-you inbox: every card with at least one attention reason. */
export function inbox(cards: readonly Card[]): Card[] {
  const rank = (c: Card) => Math.min(...c.attention.map((a) => ATTENTION_ORDER[a] ?? 9));
  return cards.filter((c) => c.attention.length > 0).sort((a, b) => rank(a) - rank(b) || byRecent(a, b));
}

/** Hides "unseen" for cards the owner marked seen here, until the agent finishes another turn. */
export function applySeen(cards: readonly Card[], marked: ReadonlyMap<string, string>): Card[] {
  if (marked.size === 0) return cards as Card[];
  return cards.map((c) => {
    const at = marked.get(c.id);
    if (at === undefined || at !== (c.agent.statusSince ?? '') || !c.attention.includes('unseen')) return c;
    return { ...c, seen: true, attention: c.attention.filter((a) => a !== 'unseen') };
  });
}

/** Cards that arrived in Needs you between two snapshots. Nothing on the first one. */
export function needsYouArrivals(prev: readonly Card[] | null, next: readonly Card[]): string[] {
  if (!prev) return [];
  const before = new Map(prev.map((c) => [c.id, c.column]));
  return next.filter((c) => c.column === 'needs_you' && before.get(c.id) !== 'needs_you').map((c) => c.id);
}

export interface ProjectTab {
  id: string;
  name: string;
  count: number;
}

/** Projects with at least one card, most cards first. */
export function projectTabs(s: Pick<Snapshot, 'cards' | 'projects'>): ProjectTab[] {
  const counts = new Map<string, number>();
  for (const c of s.cards) counts.set(c.projectId, (counts.get(c.projectId) ?? 0) + 1);
  return s.projects
    .filter((p) => counts.has(p.id))
    .map((p) => ({ id: p.id, name: clean(p.name), count: counts.get(p.id) ?? 0 }))
    .sort((a, b) => b.count - a.count || a.name.localeCompare(b.name));
}

// ---- Lanes -----------------------------------------------------------------

export interface LaneRow {
  project: Project;
  name: string; // the project, or "parent / part" when only one part is shown
  meta: string; // "1 coordinator · 7 agents"
  overview: string[]; // "1 working", "1 needs you", ...
  cells: Record<Column, Card[]>;
}

/** Columns Lanes shows: a column with no card in any row is hidden, except Needs you and Working. */
export function laneColumns(cards: readonly Card[]): Column[] {
  const used = new Set(cards.map((c) => c.column));
  return COLUMNS.filter((c) => c === 'needs_you' || c === 'working' || used.has(c));
}

export function overviewWords(counts: Project['counts']): string[] {
  const out: string[] = [];
  if (counts.working) out.push(`${counts.working} working`);
  if (counts.needsYou) out.push(`${counts.needsYou} needs you`);
  if (counts.inReview) out.push(`${counts.inReview} in review`);
  if (counts.stale) out.push(`${counts.stale} stale`);
  return out;
}

export function lanes(s: Pick<Snapshot, 'cards' | 'projects'>): LaneRow[] {
  const ix = indexOf(s);
  return grouping(s).tops.map((t) => {
    const coords = t.cards.filter((c) => (ix.workers.get(c.id)?.length ?? 0) > 0).length;
    // A lane counts what it shows: with one part selected, that part alone.
    const shown = new Set(t.cards.map((c) => c.projectId));
    const only = shown.size === 1 ? ix.projects.get([...shown][0]) : undefined;
    const part = only && only.id !== t.id ? only : undefined;
    const parts = part ? 0 : t.entries.filter((e) => !e.own).length;
    const meta = [parts ? plural(parts, 'part') : '', coords ? plural(coords, 'coordinator') : '', plural(t.cards.length, 'agent')]
      .filter(Boolean)
      .join(' · ');
    return {
      project: part ?? t.project,
      name: part ? `${t.name} / ${clean(part.name)}` : t.name,
      meta,
      overview: overviewWords(countsOf(t.cards)),
      cells: byColumn(t.cards),
    };
  });
}

/**
 * A lane note, shown as `head · model · tail` (an empty end left out), then
 * `closes`. `closes` stays muted whatever the note's tone. The model takes its
 * family's hue (`hue`) only in a muted note: in a needs-you or failing note
 * the state stays the strongest signal, so the model takes the note's tone.
 */
export interface LaneNote {
  tone: Tone;
  head: string;
  model: string;
  hue: ModelFamily | '';
  tail: string;
  closes: string;
}

/** The short note under an agent in a lane cell. */
export function laneNote(card: Card, ix: Index): LaneNote {
  const front: string[] = [];
  const back: string[] = [];
  const n = ix.workers.get(card.id)?.length ?? 0;
  if (n) front.push('coordinator');
  if (card.agent.status === 'unknown') front.push('status unknown');
  if (n) back.push(plural(n, 'worker'));
  else if (card.agent.parent === 'known') {
    const parent = card.agent.parentCardId ? ix.byId.get(card.agent.parentCardId) : undefined;
    if (parent) back.push(`for ${agentName(parent)}`);
  } else if (card.agent.parent === 'unknown') back.push('coordinator: unknown');
  const prs = prSummary(card.pullRequests);
  const title = cardTitle(card);
  if (prs) back.push(prs.text);
  else if (title !== agentName(card)) back.push(title);
  let tone: Tone = 'muted';
  if (card.column === 'needs_you') tone = 'needs';
  else if (card.attention.includes('failing_checks')) tone = 'failing';
  const model = modelLabel(card.agent.model);
  return {
    tone,
    head: front.join(' · '),
    model,
    hue: tone === 'muted' ? modelFamily(card.agent.model) : '',
    tail: back.join(' · '),
    closes: prs ? cardCloses(card, ix) : '',
  };
}

// ---- Teams -----------------------------------------------------------------

export interface TeamRow {
  card: Card;
  depth: number; // 0 for a coordinator or an agent with no known coordinator
}

export interface Team {
  key: string;
  label: string; // the coordinator's session name, or "Solo agents"
  path: string; // the project path of the coordinator's folder
  projects: string[]; // distinct project names of the members, coordinator's first
  meta: string;
  rows: TeamRow[];
  solo: boolean;
}

export function teams(s: Pick<Snapshot, 'cards' | 'projects'>): Team[] {
  const ix = indexOf(s);
  const isCoord = (c: Card) => (ix.workers.get(c.id)?.length ?? 0) > 0;
  const hasCardParent = (c: Card) =>
    c.agent.parent === 'known' && c.agent.parentCardId !== '' && ix.byId.has(c.agent.parentCardId);

  // Top coordinators: they have workers and no coordinator of their own on the board.
  const tops = s.cards.filter((c) => isCoord(c) && !hasCardParent(c)).sort(byRecent);
  const placed = new Set<string>();
  const out: Team[] = [];

  const walk = (c: Card, depth: number, rows: TeamRow[]) => {
    if (placed.has(c.id)) return;
    placed.add(c.id);
    rows.push({ card: c, depth });
    for (const w of [...(ix.workers.get(c.id) ?? [])].sort(byRecent)) walk(w, depth + 1, rows);
  };

  const lastTeamOfProject = new Map<string, Team>();
  for (const top of tops) {
    const rows: TeamRow[] = [];
    walk(top, 0, rows);
    // A team can work across several repos: it is named after its coordinator.
    const project = ix.projects.get(top.projectId);
    const team: Team = { key: top.id, label: agentName(top), path: project ? clean(project.path) : '', projects: [], meta: '', rows, solo: false };
    out.push(team);
    lastTeamOfProject.set(top.projectId, team);
  }

  // A worker with no known coordinator is listed last in its project's team, not indented.
  const orphans = s.cards
    .filter((c) => !placed.has(c.id) && (c.agent.parent === 'unknown' || (c.agent.parent === 'known' && !hasCardParent(c))))
    .sort(byRecent);
  const orphanCount = new Map<Team, number>();
  for (const c of orphans) {
    const team = lastTeamOfProject.get(c.projectId);
    if (!team) continue;
    placed.add(c.id);
    team.rows.push({ card: c, depth: 0 });
    orphanCount.set(team, (orphanCount.get(team) ?? 0) + 1);
  }

  for (const team of out) {
    team.projects = [...new Set(team.rows.map((r) => projectName(r.card, ix)))];
    const workers = team.rows.filter((r) => r.depth > 0).length;
    const coords = team.rows.filter((r) => isCoord(r.card)).length;
    const o = orphanCount.get(team) ?? 0;
    team.meta = [
      plural(coords, 'coordinator'),
      plural(workers, 'worker'),
      o ? plural(o, 'agent with no known coordinator', 'agents with no known coordinator') : '',
    ]
      .filter(Boolean)
      .join(' · ');
  }

  const solo = s.cards.filter((c) => !placed.has(c.id)).sort(byRecent);
  if (solo.length) {
    const projects = new Set(solo.map((c) => c.projectId)).size;
    out.push({
      key: 'solo',
      label: 'Solo agents',
      path: plural(projects, 'project'),
      projects: [],
      meta: 'no coordinator on the board',
      rows: solo.map((card) => ({ card, depth: 0 })),
      solo: true,
    });
  }
  return out;
}

// ---- Sources ---------------------------------------------------------------

// `ready` is false while a present watcher is still on its first pass.
const scanning = (src: SourceStatus) => src.state === 'ok' && !src.ready;

/** Session sources (Claude Code, Codex) that are failing: the board cannot say there are none. */
export function failingSessionSources(s: Pick<Snapshot, 'sources'>): SourceStatus[] {
  return s.sources.filter((src) => (src.kind === 'claude' || src.kind === 'codex') && src.state === 'failing');
}

/** Says what is uncertain about a card's coordinator: "coordinator: unknown", "coordinator not on the board", or "". */
export function uncertainRole(card: Card, ix: Index): string {
  if (card.agent.parent === 'unknown') return 'coordinator: unknown';
  if (card.agent.parent === 'known' && !(card.agent.parentCardId && ix.byId.has(card.agent.parentCardId)))
    return 'coordinator not on the board';
  return '';
}

/**
 * False while any source that is ok is still on its first pass: an empty board
 * is not yet the truth. A missing, off or failing source never holds the board,
 * whatever its ready value.
 */
export function sourcesReady(s: Pick<Snapshot, 'sources'>): boolean {
  return !s.sources.some(scanning);
}

export const SOURCE_LABEL: Record<SourceStatus['kind'], string> = {
  claude: 'claude code',
  codex: 'codex',
  agterm: 'agterm',
  git: 'git',
  github: 'github',
};

/** One status-bar entry per source: "agterm not found", "github off", "claude code 3 live". */
export function sourceLine(src: SourceStatus, s: Pick<Snapshot, 'cards'>): Line {
  const detail = clean(src.detail);
  const withDetail = (w: string) => (detail ? `${w} · ${detail}` : w);
  switch (src.state) {
    case 'ok': {
      if (scanning(src)) return { text: withDetail('reading…'), tone: 'muted' };
      if (src.kind === 'claude' || src.kind === 'codex') {
        const live = s.cards.filter((c) => c.agent.tool === src.kind && c.agent.live === 'yes').length;
        return { text: withDetail(live ? `${live} live` : 'found'), tone: 'body' };
      }
      if (src.kind === 'github') return { text: withDetail('through gh'), tone: 'body' };
      return { text: withDetail('found'), tone: 'body' };
    }
    case 'missing':
      return { text: withDetail('not found'), tone: 'muted' };
    case 'off':
      return { text: withDetail('off'), tone: 'muted' };
    case 'failing':
      return { text: withDetail('failing'), tone: 'failing' };
    default:
      return { text: withDetail('unknown'), tone: 'muted' };
  }
}
